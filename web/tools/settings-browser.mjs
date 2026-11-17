// Real HTTP, native engine, SQLite and Chrome. Only the model provider is faux.
/* eslint-disable no-await-in-loop */
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createWriteStream } from "node:fs";
import { mkdir, mkdtemp, readFile, writeFile } from "node:fs/promises";
import { createServer as createHttpServer } from "node:http";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { setTimeout as delay } from "node:timers/promises";

const [binary, output, scenario = "all"] = process.argv.slice(2);

assert.ok(binary);

const evidence = output ? resolve(output) : await mkdtemp(join(tmpdir(), "dex-settings-evidence-"));
const root = await mkdtemp(join(tmpdir(), "dex-settings-browser-"));
const home = join(root, "home");
const data = join(root, "data");
const profile = join(root, "chrome");

await mkdir(evidence, { recursive: true });

await mkdir(home);

const listener = createServer();

await new Promise((done) => listener.listen(0, "127.0.0.1", done));

const port = listener.address().port;

await new Promise((done) => listener.close(done));

const base = `http://127.0.0.1:${port}`;
const appLog = createWriteStream(join(evidence, "app.log"));
const chromeLog = createWriteStream(join(evidence, "chrome.log"));

const app = spawn(
  resolve(binary),
  ["serve", "--host", "127.0.0.1", "--port", String(port), "--data-dir", data],
  {
    env: { PATH: process.env.PATH, HOME: home, LANG: "en_US.UTF-8" },
    stdio: ["ignore", "pipe", "pipe"],
  },
);

app.stdout.pipe(appLog);

app.stderr.pipe(appLog);

let chrome;
let socket;
let serial = 0;
let held;
let heldRefresh;
const pending = new Map();
const writes = [];
const results = [];

try {
  await poll("app", async () => (await fetch(base + "/api/health")).ok);
  chrome = spawn(
    process.env.CHROME_PATH ?? "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
    [
      "--headless=new",
      "--no-first-run",
      "--no-default-browser-check",
      "--disable-background-networking",
      "--disable-features=BackForwardCache",
      "--remote-debugging-port=0",
      `--user-data-dir=${profile}`,
      "about:blank",
    ],
    { stdio: ["ignore", "pipe", "pipe"] },
  );
  chrome.stdout.pipe(chromeLog);
  chrome.stderr.pipe(chromeLog);

  const debug = await poll(
    "chrome",
    async () => (await readFile(join(profile, "DevToolsActivePort"), "utf8")).split("\n")[0],
  );

  const pages = await (await fetch(`http://127.0.0.1:${debug}/json/list`)).json();
  socket = new WebSocket(pages.find((page) => page.type === "page").webSocketDebuggerUrl);
  await new Promise((done, fail) => {
    socket.addEventListener("open", done, { once: true });
    socket.addEventListener("error", fail, { once: true });
  });
  socket.addEventListener("message", ({ data: raw }) => {
    const msg = JSON.parse(String(raw));

    if (msg.id) {
      const promise = pending.get(msg.id);
      pending.delete(msg.id);

      if (msg.error) promise.fail(new Error(JSON.stringify(msg.error)));
      else promise.done(msg.result);
    } else if (msg.method === "Fetch.requestPaused") {
      if (msg.params.request.method === "PUT") held = msg.params.requestId;
      else heldRefresh = msg.params.requestId;
    } else if (
      msg.method === "Network.requestWillBeSent" &&
      msg.params.request.method === "PUT" &&
      msg.params.request.url.endsWith("/settings")
    ) {
      writes.push(JSON.parse(msg.params.request.postData));
    }
  });
  await call("Page.enable");
  await call("Runtime.enable");
  await call("Network.enable");
  await call("Emulation.setDeviceMetricsOverride", {
    width: 1280,
    height: 900,
    deviceScaleFactor: 1,
    mobile: false,
  });

  if (scenario === "all" || scenario === "provider") {
    await api(
      "/api/settings/providers/audit-provider",
      {
        name: "Audit provider",
        api: "openai-completions",
        baseUrl: "http://127.0.0.1:1",
        apiKey: "",
        models: ["audit-model"],
      },
      "PUT",
    );
    const initial = await api("/api/agents/default");
    await api("/api/agents/default", { ...initial, provider: "faux", modelId: "echo" }, "PUT");
    const original = await api("/api/agents/default");

    const bots = [];

    for (const name of ["Audit Bot", "Second Audit Bot"]) {
      bots.push(
        await api("/api/agents", {
          name,
          provider: "audit-provider",
          modelId: "audit-model",
          thinkingLevel: "off",
        }),
      );
    }

    // Opened from the default bot, Providers still lists every bot that uses the provider.
    await visit("/a/default");
    await tap('aside[aria-label="Sidebar"] a[href="/providers"]');
    await poll("client navigation to providers", () => evaluate("location.pathname === '/providers'"));
    await poll("provider users shown", () =>
      evaluate("document.body.innerText.includes('Used by Audit Bot, Second Audit Bot')"),
    );
    await tap('button[aria-label="Remove Audit provider"]');
    await poll("provider used by bots", () =>
      evaluate("document.body.innerText.includes('Audit Bot, Second Audit Bot use this provider now.')"),
    );
    await click("Remove provider");
    await poll(
      "provider removed",
      async () => !(await api("/api/settings")).providers.some((p) => p.id === "audit-provider"),
    );

    for (const bot of bots) {
      await poll(`${bot.name} model cleared`, async () => !(await api(`/api/agents/${bot.id}`)).modelId);
    }

    assert.deepEqual(await api("/api/agents/default"), original, "default bot must remain unchanged");
    results.push("Providers from the default bot removes the models of both users and preserves default");
  }

  if (scenario === "all" || scenario === "provider-models") {
    // A custom endpoint is a system boundary, so a local server stands in for it.
    const endpoint = createHttpServer((request, response) => {
      response.writeHead(request.url === "/v1/models" ? 200 : 404, { "Content-Type": "application/json" });
      response.end(JSON.stringify({ data: [{ id: "model-a" }, { id: "model-b" }, { id: "model-c" }] }));
    });

    await new Promise((done) => endpoint.listen(0, "127.0.0.1", done));

    try {
      await visit("/providers");
      await click("Add provider");
      await tap('[role="dialog"] button[aria-label="Custom endpoint"]');
      await type('[role="dialog"] input[placeholder="LM Studio"]', "Lan");
      // The default API is the first by name, Anthropic Messages, which asks <base>/v1/models.
      await evaluate(`(() => {
        const select = document.querySelector('[role="dialog"] select');
        Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value").set.call(select, "openai-completions");
        select.dispatchEvent(new Event("change", { bubbles: true }));
      })()`);
      await type('[role="dialog"] input[type="url"]', `http://127.0.0.1:${endpoint.address().port}/v1`);
      await poll("models listed without a save", () =>
        evaluate("document.querySelector('[role=\"dialog\"]').innerText.includes('3 listed')"),
      );
      assert.equal(
        (await api("/api/settings")).providers.some((p) => p.id === "lan"),
        false,
        "a listing must not save the provider",
      );
      // A click on one model while all models are enabled keeps the other models.
      await evaluate(
        `[...document.querySelectorAll('[role="dialog"] input[type="checkbox"]')].find(i => i.parentElement.textContent.includes('model-b')).click()`,
      );
      await click("Save provider");

      const saved = await poll("subset saved", async () =>
        (await api("/api/settings")).providers.find((p) => p.id === "lan"),
      );

      assert.deepEqual(saved.models, ["model-a", "model-c"]);
      results.push({
        scenario: "listed provider models before save and enabled a subset",
        models: saved.models,
      });
    } finally {
      endpoint.close();
    }
  }

  if (scenario === "all" || scenario === "session") {
    const agent = await api("/api/agents/default");
    await api("/api/agents/default", { ...agent, provider: "faux", modelId: "think" }, "PUT");
    const chat = await api("/api/agents/default/chats", {}, "POST");
    await visit(`/a/default/c/${chat.id}`);
    // One input control opens both the model list and thinking choices.
    await poll("session controls", () =>
      evaluate("!!document.querySelector('form button[aria-label=\"Set model\"]:not([inert] *)')"),
    );
    await tap('form button[aria-label="Set model"]:not([inert] *)');
    await poll("thinking menu", () =>
      evaluate('!!document.querySelector(\'[aria-label="Thinking level"] button[data-value="high"]\')'),
    );
    await call("Fetch.enable", {
      patterns: [
        { urlPattern: `${base}/api/agents/default/chats/${chat.id}/settings`, requestStage: "Response" },
        { urlPattern: `${base}/api/agents/default/chats/${chat.id}`, requestStage: "Response" },
      ],
    });
    await tap('[aria-label="Thinking level"] button[data-value="high"]');
    await poll("first response held", () => held);

    const disabled = await evaluate(
      "document.querySelector('button[aria-label=\"Set model\"]:not([inert] *)').disabled",
    );

    assert.equal(disabled, true, "model picker must be disabled while thinking save awaits refresh");
    await evaluate("document.querySelector('button[aria-label=\"Set model\"]:not([inert] *)').click()");
    await evaluate("new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))");
    assert.equal(writes.length, 1, "the second action must not send a conflicting write");
    await call("Fetch.continueRequest", { requestId: held });
    await poll("chat refresh held", () => heldRefresh);
    assert.equal(
      await evaluate("document.querySelector('button[aria-label=\"Set model\"]:not([inert] *)').disabled"),
      true,
      "model picker must stay disabled until refresh finishes",
    );
    await call("Fetch.continueRequest", { requestId: heldRefresh });
    await call("Fetch.disable");
    await poll("first save refreshed", () =>
      evaluate("!document.querySelector('button[aria-label=\"Set model\"]:not([inert] *)').disabled"),
    );
    await tap('[cmdk-item][data-value="faux/echo"]');

    const saved = await poll("both overrides saved", async () => {
      const current = await api(`/api/agents/default/chats/${chat.id}`);

      return (
        current.settings?.modelId === "echo" && current.settings?.thinkingLevel === "high" && current.settings
      );
    });

    await poll("non-reasoning model hides thinking choices", () =>
      evaluate(`!document.querySelector('[aria-label="Chat model"] [aria-label="Thinking level"]')`),
    );
    assert.equal(writes.length, 2);
    results.push({ scenario: "serialized session settings", writes, saved });
  }

  {
    await mkdir(join(home, ".ssh"), { recursive: true });
    await writeFile(
      join(home, ".ssh", "config"),
      "Host browser-ssh-box\n  HostName 10.0.0.10\n  User ops\n  Port 2222\n",
    );

    await visit("/environment");
    await poll("ssh config import button visible", () =>
      evaluate(
        "!![...document.querySelectorAll('button')].find(b => b.textContent.trim() === 'Import SSH config')",
      ),
    );
    await click("Import SSH config");
    await poll("ssh config import dialog", () =>
      evaluate("document.querySelector('[role=\"dialog\"]')?.innerText.includes('browser-ssh-box')"),
    );
    await tap('[role="dialog"] input[type="checkbox"]');
    await click("Import 1 host");

    const importedSsh = await poll("ssh environment imported", async () =>
      (await api("/api/settings/environments")).environments.find((env) => env.name === "browser-ssh-box"),
    );

    assert.equal(importedSsh.kind, "pi-env");
    assert.equal(importedSsh.config.host, "browser-ssh-box");

    await poll("no internal pi-env jargon shown in environment cards", () =>
      evaluate("!document.body.innerText.includes('pi-env') && document.body.innerText.includes('SSH')"),
    );

    await click("Add environment");
    await poll("add environment dialog", () => evaluate("!!document.querySelector('[role=\"dialog\"]')"));
    await click("Advanced options");
    await poll("login shell defaults to enabled", () =>
      evaluate(
        "document.querySelector('[role=\"dialog\"] [aria-label=\"Use login shell\"]')?.getAttribute('aria-checked') === 'true'",
      ),
    );
    await tap('[role="dialog"] button[aria-label="Sandbox"]');
    await type('[role="dialog"] input[placeholder="E2B Sandbox"]', "E2B Browser Env");
    await type('[role="dialog"] input[placeholder="e2b_..."]', "e2b_browser_secret");
    await evaluate(
      `[...document.querySelectorAll('[role="dialog"] button')].find(b => b.textContent.trim() === 'Add environment').click()`,
    );

    const createdEnv = await poll("e2b environment created", async () =>
      (await api("/api/settings/environments")).environments.find((env) => env.name === "E2B Browser Env"),
    );

    assert.equal(createdEnv.kind, "e2b");
    assert.equal(createdEnv.secretSet, true);
    await poll("e2b card visible without secret leak", () =>
      evaluate(
        "document.body.innerText.includes('E2B Browser Env') && document.body.innerText.includes('API key saved') && !document.body.innerText.includes('e2b_browser_secret')",
      ),
    );

    await tap('button[aria-label="Edit E2B Browser Env"]');
    await poll("saved e2b secret hidden", () =>
      evaluate("document.querySelector('[role=\"dialog\"]').innerText.includes('Saved API key')"),
    );
    await click("Save changes");
    await poll("edit dialog closed", () => evaluate("!document.querySelector('[role=\"dialog\"]')"));

    await click("Add environment");
    await poll("add custom e2b dialog", () => evaluate("!!document.querySelector('[role=\"dialog\"]')"));
    await tap('[role="dialog"] button[aria-label="Sandbox"]');
    await tap('[role="dialog"] button[aria-label="Local Docker / VM"]');
    await type('[role="dialog"] input[placeholder="Local Docker sandbox"]', "Docker E2B Gateway");
    await type('[role="dialog"] input[placeholder="http://localhost:3000"]', "http://localhost:3000");
    await evaluate(
      `[...document.querySelectorAll('[role="dialog"] button')].find(b => b.textContent.trim() === 'Add environment').click()`,
    );

    const customEnv = await poll("custom e2b created without secret", async () =>
      (await api("/api/settings/environments")).environments.find((env) => env.name === "Docker E2B Gateway"),
    );

    assert.equal(customEnv.secretSet, false);
    assert.equal(customEnv.config.apiUrl, "http://localhost:3000");
    assert.equal(customEnv.config.autoPause, false);
    assert.equal(customEnv.config.timeoutSeconds, 3600);
    await poll("local sandbox card shows Local sandbox badge and No API key", () =>
      evaluate(
        "document.body.innerText.includes('Docker E2B Gateway') && document.body.innerText.includes('Local sandbox') && document.body.innerText.includes('No API key')",
      ),
    );

    await visit("/a/default/agent");
    await poll("bot environment selector loaded", () =>
      evaluate(`!!document.querySelector('#bot-environment option[value="${createdEnv.id}"]')`),
    );
    await evaluate(`(() => {
      const select = document.querySelector('#bot-environment');
      Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value").set.call(select, ${JSON.stringify(createdEnv.id)});
      select.dispatchEvent(new Event("change", { bubbles: true }));
    })()`);
    await poll("stateless reset sandbox button visible", () =>
      evaluate(
        "!![...document.querySelectorAll('button')].find(b => b.textContent.trim() === 'Reset sandbox' && !b.disabled)",
      ),
    );
    await click("Reset sandbox");
    await poll("reset notification visible", () =>
      evaluate("document.body.innerText.includes('Sandbox reset')"),
    );

    await visit("/environment");
    await poll("used by badge visible on bound environment", () =>
      evaluate("document.body.innerText.includes('Used by')"),
    );
    await tap('button[aria-label="Remove E2B Browser Env"]');
    await poll("guided reassign dialog shown", () =>
      evaluate("document.querySelector('[role=\"dialog\"]')?.innerText.includes('Before removal')"),
    );
    await click("Reassign bots and remove");
    await poll(
      "e2b environment removed with bot reassignment",
      async () =>
        !(await api("/api/settings/environments")).environments.some((env) => env.id === createdEnv.id) &&
        (await api("/api/agents/default")).environmentId === "",
    );
    await poll("reassign dialog closed", () => evaluate("!document.querySelector('[role=\"dialog\"]')"));

    for (const name of ["Docker E2B Gateway", "browser-ssh-box"]) {
      await tap(`button[aria-label="Remove ${name}"]`);
      await click("Remove environment");
      await poll(`${name} remove dialog closed`, () =>
        evaluate("!document.querySelector('[role=\"dialog\"]')"),
      );
    }

    await poll(
      "all test environments removed",
      async () => (await api("/api/settings/environments")).environments.length === 0,
    );

    results.push({
      scenario: "ssh config import, custom keyless e2b, stateless reset, and guided removal",
      envId: createdEnv.id,
      customEnvId: customEnv.id,
      importedSshId: importedSsh.id,
    });
  }

  await writeFile(join(evidence, "results.json"), JSON.stringify(results, null, 2));
  console.log(`PASS ${JSON.stringify(results)}; evidence: ${evidence}`);
} catch (error) {
  await writeFile(
    join(evidence, "failure.txt"),
    `${error.stack}\n${socket?.readyState === WebSocket.OPEN ? await evaluate("document.body.innerText").catch(() => "") : ""}\nwrites=${JSON.stringify(writes)}\n`,
  );
  throw error;
} finally {
  socket?.close();

  for (const child of [chrome, app]) if (child && child.exitCode === null) child.kill("SIGTERM");
  appLog.end();
  chromeLog.end();
}

async function poll(label, fn) {
  const end = Date.now() + 30000;
  let last;

  while (Date.now() < end) {
    try {
      last = await fn();

      if (last) return last;
    } catch (error) {
      last = String(error);
    }

    await delay(50);
  }

  throw new Error(`${label}: ${JSON.stringify(last)}`);
}

async function api(path, body, method = body ? "POST" : "GET") {
  const response = await fetch(base + path, {
    method,
    headers: body ? { "Content-Type": "application/json" } : {},
    body: body ? JSON.stringify(body) : undefined,
  });

  const result = await response.json();
  assert.ok(response.ok, `${path}: ${response.status} ${JSON.stringify(result)}`);

  return result;
}

function call(method, params = {}) {
  const id = ++serial;
  const promise = new Promise((done, fail) => pending.set(id, { done, fail }));
  socket.send(JSON.stringify({ id, method, params }));

  return promise;
}

async function evaluate(expression) {
  const result = await call("Runtime.evaluate", { expression, awaitPromise: true, returnByValue: true });

  if (result.exceptionDetails) throw new Error(JSON.stringify(result.exceptionDetails));

  return result.result.value;
}

async function visit(path) {
  await call("Page.navigate", { url: base + path });
  await poll("page", () =>
    evaluate("document.readyState === 'complete' && !!document.querySelector('main')"),
  );
}

async function tap(selector) {
  await poll(`target ${selector}`, () => evaluate(`!!document.querySelector(${JSON.stringify(selector)})`));
  await evaluate(`document.querySelector(${JSON.stringify(selector)}).click()`);
}

async function type(selector, text) {
  await poll(`target ${selector}`, () => evaluate(`!!document.querySelector(${JSON.stringify(selector)})`));
  await evaluate(`document.querySelector(${JSON.stringify(selector)}).focus()`);
  await call("Input.insertText", { text });
}

async function click(text) {
  await poll(`button ${text}`, () =>
    evaluate(
      `!![...document.querySelectorAll('button:not([inert] *)')].find(b => b.textContent.trim() === ${JSON.stringify(text)} && !b.disabled)`,
    ),
  );
  await evaluate(
    `[...document.querySelectorAll('button:not([inert] *)')].find(b => b.textContent.trim() === ${JSON.stringify(text)} && !b.disabled).click()`,
  );
}
