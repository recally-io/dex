// Real HTTP, native engine, SQLite and Chrome. Only the model provider is faux.
/* eslint-disable no-await-in-loop */
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createWriteStream } from "node:fs";
import { mkdir, mkdtemp, readFile, writeFile } from "node:fs/promises";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { setTimeout as delay } from "node:timers/promises";

const [binary, output] = process.argv.slice(2);

assert.ok(binary);

const evidence = output ? resolve(output) : await mkdtemp(join(tmpdir(), "dex-archive-evidence-"));
const root = await mkdtemp(join(tmpdir(), "dex-archive-browser-"));
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
const pending = new Map();
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

  // The default bot runs the faux tool model, which turns "bot_delete {json}" into a tool call.
  const initial = await api("/api/agents/default");
  await api("/api/agents/default", { ...initial, provider: "faux", modelId: "tool" }, "PUT");
  await api(
    "/api/agents",
    { name: "Coder", provider: "faux", modelId: "echo", thinkingLevel: "off", instructions: "" },
    "POST",
  );

  const bots = async (path) => (await api(path)).agents.map((a) => a.name);

  async function ask() {
    const chat = await api("/api/agents/default/chats", {}, "POST");
    await api(
      `/api/agents/default/chats/${chat.id}/messages`,
      {
        id: crypto.randomUUID(),
        text: `bot_delete ${JSON.stringify({ bot: "Coder", reason: "Not used any more" })}`,
      },
      "POST",
    );
    await visit(`/a/default/c/${chat.id}`);
    await poll("approve card", () =>
      evaluate(
        "document.querySelector('section[aria-label=\"Question from the agent\"]')?.innerText.includes('ARCHIVE BOT')",
      ),
    );

    return chat;
  }

  const answer = (label) =>
    evaluate(
      `[...document.querySelectorAll('section[aria-label="Question from the agent"] button')].find((b) => b.textContent.includes(${JSON.stringify(label)})).click()`,
    );

  // 1. Any other answer keeps the bot.
  await ask();
  await shot("1-approve-card");
  await api("/api/agents/default", { ...initial, provider: "faux", modelId: "echo" }, "PUT");
  await answer("Cancel");
  await poll("refusal shown", () =>
    evaluate("document.body.innerText.includes('Not approved. Bot \"Coder\" is not archived.')"),
  );
  assert.deepEqual(await bots("/api/agents"), ["Dex", "Coder"]);
  await shot("2-cancelled");

  // 2. The approve answer archives it.
  await api("/api/agents/default", { ...initial, provider: "faux", modelId: "tool" }, "PUT");
  await ask();
  await api("/api/agents/default", { ...initial, provider: "faux", modelId: "echo" }, "PUT");
  await answer("Archive Coder");
  await poll("approval shown", () =>
    evaluate("document.body.innerText.includes('Approved. Bot \"Coder\" is archived.')"),
  );
  assert.deepEqual(await bots("/api/agents"), ["Dex"]);
  assert.deepEqual(await bots("/api/archived-agents"), ["Coder"]);
  await poll("sidebar drops the bot", () =>
    // A bot row names its bot first. Chat titles in the sidebar may name the bot too.
    evaluate('!document.querySelector(\'nav[aria-label="Bots"] a[aria-label^="Coder"]\')'),
  );
  await shot("3-archived");

  // 3. The archived list restores it.
  await visit("/archived");
  await poll("archived list", () => evaluate("document.querySelector('main').innerText.includes('Coder')"));
  await shot("4-archived-list");
  await click("Restore");
  await poll("restored", async () => (await bots("/api/agents")).includes("Coder"));
  await poll("empty list", () =>
    evaluate("document.querySelector('main').innerText.includes('No archived bots')"),
  );
  assert.deepEqual(await bots("/api/archived-agents"), []);
  await poll("sidebar shows the bot", () =>
    evaluate('!!document.querySelector(\'nav[aria-label="Bots"] a[aria-label^="Coder"]\')'),
  );
  await shot("5-restored");
  results.push("cancel keeps the bot; approve archives it; the archived list restores it");

  await writeFile(join(evidence, "results.json"), JSON.stringify(results, null, 2));
  console.log(`PASS ${JSON.stringify(results)}; evidence: ${evidence}`);
} catch (error) {
  await writeFile(
    join(evidence, "failure.txt"),
    `${error.stack}\n${socket?.readyState === WebSocket.OPEN ? await evaluate("document.body.innerText").catch(() => "") : ""}\n`,
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

async function shot(name) {
  const image = await call("Page.captureScreenshot", { format: "png" });
  await writeFile(join(evidence, `${name}.png`), Buffer.from(image.data, "base64"));
}
