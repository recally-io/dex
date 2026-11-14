// Real app, native engine, and Chrome with isolated data and the faux provider.
/* eslint-disable no-await-in-loop */
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdtemp, mkdir, readFile, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { setTimeout as delay } from "node:timers/promises";

const [binary, outputArg, mode] = process.argv.slice(2);

assert.ok(binary, "Pass a dex binary");

const output = resolve(outputArg ?? ".local/navigation-evidence/after");

await mkdir(output, { recursive: true });

const isolated = await mkdtemp(join(tmpdir(), "dex-navigation-"));
const home = join(isolated, "home");
const project = join(home, "dex");

await mkdir(project, { recursive: true });

const port = 19787 + Math.floor(Math.random() * 1000);
const base = `http://127.0.0.1:${port}`;

const app = spawn(
  resolve(binary),
  ["serve", "--host", "127.0.0.1", "--port", String(port), "--data-dir", join(isolated, "data")],
  {
    env: { PATH: process.env.PATH, HOME: home, LANG: "en_US.UTF-8" },
    stdio: "ignore",
  },
);

let chrome;
let socket;
let serial = 0;
const pending = new Map();
const results = [];

async function poll(name, fn) {
  const deadline = Date.now() + 30000;

  while (Date.now() < deadline) {
    try {
      if (await fn()) return;
    } catch {}

    await delay(100);
  }

  throw new Error(`${name} timed out`);
}

function call(method, params = {}) {
  const id = ++serial;
  socket.send(JSON.stringify({ id, method, params }));

  return new Promise((accept, reject) => pending.set(id, { accept, reject }));
}

async function evaluate(expression) {
  const response = await call("Runtime.evaluate", { expression, returnByValue: true, awaitPromise: true });

  if (response.exceptionDetails) throw new Error(JSON.stringify(response.exceptionDetails));

  return response.result?.value;
}

async function api(path, body, method = "POST") {
  const response = await fetch(
    `${base}/api${path}`,
    body === undefined
      ? undefined
      : {
          method,
          headers: { "content-type": "application/json" },
          body: JSON.stringify(body),
        },
  );

  assert.ok(response.ok, `${path}: ${response.status}`);

  return response.json();
}

async function visit(path) {
  await call("Page.navigate", { url: `${base}${path}` });
  await poll(path, () =>
    evaluate(`location.pathname === ${JSON.stringify(path)} && !!document.querySelector('#page-title')`),
  );
}

async function click(selector) {
  await poll(selector, () => evaluate(`!!document.querySelector(${JSON.stringify(selector)})`));
  await evaluate(`document.querySelector(${JSON.stringify(selector)}).click()`);
}

async function capture(name) {
  await poll("animations settled", () =>
    evaluate(
      "document.getAnimations().every(a => a.playState !== 'running' || a.effect?.getTiming().iterations === Infinity)",
    ),
  );
  const screenshot = await call("Page.captureScreenshot", { format: "png" });
  await writeFile(join(output, `${name}.png`), Buffer.from(screenshot.data, "base64"));
}

async function check(name, expression) {
  assert.ok(await evaluate(expression), name);
  results.push(name);
}

try {
  await poll("app ready", async () => (await fetch(`${base}/api/health`)).ok);
  const agent = await api("/agents/default");
  await api("/agents/default", { ...agent, name: "Coder", provider: "faux", modelId: "think" }, "PUT");
  await api("/agents", { name: "Anna", provider: "faux", modelId: "echo", thinkingLevel: "off" });
  const main = await api("/agents/default/chats/main");
  await api(`/agents/default/chats/${main.id}/project`, { cwd: project }, "PUT");
  const task = await api("/agents/default/chats", {});
  await api(`/agents/default/chats/${task.id}/messages`, {
    id: crypto.randomUUID(),
    text: "Review project navigation",
  });
  await api(`/agents/default/chats/${task.id}/project`, { cwd: project }, "PUT");
  chrome = spawn(
    process.env.CHROME_PATH ?? "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
    [
      "--headless=new",
      "--no-first-run",
      "--no-default-browser-check",
      "--disable-background-networking",
      "--disable-features=BackForwardCache",
      "--remote-debugging-port=0",
      `--user-data-dir=${join(isolated, "chrome")}`,
      "about:blank",
    ],
    { stdio: "ignore" },
  );
  let debug;
  await poll("Chrome ready", async () => {
    debug = (await readFile(join(isolated, "chrome", "DevToolsActivePort"), "utf8")).split("\n")[0];

    return debug;
  });
  const pages = await (await fetch(`http://127.0.0.1:${debug}/json/list`)).json();
  socket = new WebSocket(pages.find((p) => p.type === "page").webSocketDebuggerUrl);
  await new Promise((accept) => socket.addEventListener("open", accept, { once: true }));
  socket.addEventListener("message", ({ data }) => {
    const event = JSON.parse(String(data));
    const promise = pending.get(event.id);

    if (!promise) return;
    pending.delete(event.id);

    if (event.error) promise.reject(new Error(JSON.stringify(event.error)));
    else promise.accept(event.result);
  });
  await call("Page.enable");
  await call("Runtime.enable");

  for (const [size, width, height] of [
    ["desktop", 1440, 900],
    ["mobile", 390, 844],
  ]) {
    await call("Emulation.setDeviceMetricsOverride", { width, height, deviceScaleFactor: 1, mobile: false });
    await visit("/a/default");
    await poll("main composer", () => evaluate("!!document.querySelector('textarea:not([inert] *)')"));
    await capture(`${size}-main`);

    if (mode === "--baseline") {
      if (size === "mobile") await click('button[aria-label^="Open navigation"]');
      await click('aside button[aria-label^="Bot menu"]');
      await poll("old bot menu", () => evaluate("!!document.querySelector('[role=menu]')"));
    } else {
      // Given Main, desktop shows bot navigation and mobile puts the same actions in one menu.
      await check(
        `${size}: Main has no task settings`,
        "!document.querySelector('header button[aria-label=\"Task settings\"]') && !document.querySelector('header button[aria-label=\"Chat more\"]')",
      );

      if (size === "desktop") {
        await check(
          "desktop: sidebar only switches bots",
          "!document.querySelector('aside button[aria-label^=\"Bot menu\"]')",
        );
        await check(
          "desktop: bot navigation is visible",
          '!!document.querySelector(\'header nav[aria-label="Bot navigation"] a[href="/a/default/sessions"]\')',
        );
      } else await click('header button[aria-label="Bot menu: Coder"]');
    }

    await capture(`${size}-navigation`);
    await call("Input.dispatchKeyEvent", { type: "keyDown", key: "Escape", code: "Escape" });
    await visit(`/a/default/c/${task.id}`);
    await poll("task composer", () => evaluate("!!document.querySelector('textarea:not([inert] *)')"));

    if (mode !== "--baseline") {
      await check(
        `${size}: project visible without task settings`,
        "!!document.querySelector('button[aria-label=\"Set project directory\"]:not([inert] *)') && !document.querySelector('[aria-label=\"Task settings\"][role=region]')",
      );
      await click('header button[aria-label="Task settings"]');
      await poll("task settings", () =>
        evaluate("!!document.querySelector('[aria-label=\"Task settings\"][role=region]')"),
      );
    }

    await capture(`${size}-task`);

    if (mode !== "--baseline") {
      await click('button[aria-label="Set project directory"]:not([inert] *)');
      await poll("project picker", () =>
        evaluate("!!document.querySelector('[aria-label=\"Directory path\"]')"),
      );
      await poll("project listing ready", () =>
        evaluate(
          `document.querySelector('[aria-label="Subdirectories"]')?.getAttribute('aria-busy') === 'false' && !document.querySelector('[aria-label="Recent projects"]').textContent.includes('Loading')`,
        ),
      );
      await capture(`${size}-project`);
      await evaluate(
        `[...document.querySelectorAll('[role=dialog] button')].find(b => b.textContent.trim() === 'Clear project').click()`,
      );
      await poll("project cleared", async () => (await api(`/agents/default/chats/${task.id}`)).cwd === "");
      await poll("default project label", () =>
        evaluate(
          `document.querySelector('button[aria-label="Set project directory"]:not([inert] *)')?.textContent.includes('Bot working directory')`,
        ),
      );
      await click('button[aria-label="Set project directory"]:not([inert] *)');
      await click(`[aria-label="Recent projects"] button[title="${project}"]`);
      await poll(
        "project restored",
        async () => (await api(`/agents/default/chats/${task.id}`)).cwd === project,
      );
      results.push(`${size}: clear and select a project through the picker`);
      await check(`${size}: no horizontal overflow`, "document.documentElement.scrollWidth <= innerWidth");
    }
  }

  if (mode !== "--baseline") {
    await call("Emulation.setDeviceMetricsOverride", {
      width: 1440,
      height: 900,
      deviceScaleFactor: 1,
      mobile: false,
    });
    await visit("/a/default");
    await click('header button[aria-label="New task"]');
    await poll("new task project picker", () =>
      evaluate("!!document.querySelector('[role=dialog] [aria-label=\"Directory path\"]')"),
    );
    await check(
      "new task starts with the current project",
      `document.querySelector('[aria-label="Directory path"]').value === ${JSON.stringify(project)}`,
    );
    await capture("desktop-new-task-project");
    await click('[role=dialog] button[data-slot="dialog-close"]');
    await poll("new task opens", () => evaluate("location.pathname.includes('/c/')"));
    const id = await evaluate("location.pathname.split('/').at(-1)");
    assert.equal((await api(`/agents/default/chats/${id}`)).cwd, project);
    results.push("new task keeps the current project");
    await api(`/agents/default/chats/${main.id}/settings`, { provider: "faux", modelId: "echo" }, "PUT");
    await visit("/a/default/agent");
    await click('button[aria-label="Use Bot defaults for Main"]');
    await poll(
      "Main follows Bot defaults",
      async () => !(await api(`/agents/default/chats/${main.id}`)).settings.modelId,
    );
    results.push("Bot settings can clear a saved Main override");
  }

  await writeFile(join(output, "results.json"), JSON.stringify(results, null, 2));
  console.log(`PASS navigation; evidence: ${output}`);
} catch (error) {
  if (socket?.readyState === WebSocket.OPEN) {
    await capture("failure").catch(() => {});
    await writeFile(
      join(output, "failure.txt"),
      `${error.stack}\n${await evaluate("document.body.innerText").catch(() => "")}`,
    );
  }

  throw error;
} finally {
  socket?.close();

  for (const child of [chrome, app]) {
    if (!child || child.exitCode !== null) continue;
    child.kill("SIGTERM");
    await Promise.race([
      new Promise((accept) => child.once("exit", accept)),
      delay(5000).then(() => child.kill("SIGKILL")),
    ]);
  }
}
