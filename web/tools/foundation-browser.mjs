/* oxlint-disable */
// Real-browser foundation evidence. The app and Chrome use isolated temporary state.
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdir, writeFile } from "node:fs/promises";
import { mkdtemp } from "node:fs/promises";
import { join, resolve } from "node:path";
import { tmpdir } from "node:os";
import { setTimeout as delay } from "node:timers/promises";

const [binary, outputArg] = process.argv.slice(2);
assert.ok(binary, "Pass a dex binary");
const output = resolve(outputArg ?? join(tmpdir(), "dex-foundation-evidence"));
await mkdir(output, { recursive: true });
const isolated = await mkdtemp(join(tmpdir(), "dex-foundation-browser-"));
const port = 18787 + Math.floor(Math.random() * 1000);
const app = spawn(
  resolve(binary),
  ["serve", "--host", "127.0.0.1", "--port", String(port), "--data-dir", join(isolated, "data")],
  {
    env: { PATH: process.env.PATH, HOME: join(isolated, "home"), LANG: "en_US.UTF-8", DEX_FAKE_MODEL: "1" },
    stdio: "ignore",
  },
);
const base = `http://127.0.0.1:${port}`;
const results = [];
async function request(path, options) {
  const response = await fetch(`${base}${path}`, {
    headers: { "content-type": "application/json" },
    ...options,
  });
  const body = await response.text();
  assert.ok(response.ok, `${options?.method ?? "GET"} ${path}: ${response.status} ${body}`);
  return body ? JSON.parse(body) : null;
}
async function seed() {
  await request("/api/settings/providers/foundation", {
    method: "PUT",
    body: JSON.stringify({
      api: "openai-completions",
      name: "Foundation Demo",
      baseUrl: "http://127.0.0.1:9",
      apiKey: "demo",
      models: ["demo/model"],
    }),
  });
  const agent = await request("/api/agents/default");
  await request("/api/agents/default", {
    method: "PUT",
    body: JSON.stringify({
      name: agent.name,
      provider: "faux",
      modelId: "echo",
      thinkingLevel: agent.thinkingLevel,
      instructions: agent.instructions,
    }),
  });
  const main = await request("/api/agents/default/chats/main");
  await request(`/api/agents/default/chats/${main.id}/messages`, {
    method: "POST",
    body: JSON.stringify({ id: crypto.randomUUID(), text: "foundation screenshot seed" }),
  });
  const session = await request("/api/agents/default/chats", { method: "POST" });
  await request(`/api/agents/default/chats/${session.id}/messages`, {
    method: "POST",
    body: JSON.stringify({ id: crypto.randomUUID(), text: "session screenshot seed" }),
  });
}
let chrome;
let socket;
let nextId = 1;
const pending = new Map();
function call(method, params = {}) {
  const id = nextId++;
  socket.send(JSON.stringify({ id, method, params }));
  return new Promise((resolveCall, reject) => pending.set(id, { resolve: resolveCall, reject }));
}
async function waitFor(name, fn, timeout = 30000) {
  const end = Date.now() + timeout;
  while (Date.now() < end) {
    try {
      const value = await fn();
      if (value) return value;
    } catch {}
    await delay(100);
  }
  throw new Error(`${name} timed out`);
}
async function evaluate(expression) {
  return (await call("Runtime.evaluate", { expression, returnByValue: true })).result?.value;
}
// Chrome activates a button on Enter only when the key event carries its text.
async function press(key, code, keyCode, text) {
  await call("Input.dispatchKeyEvent", { type: "keyDown", key, code, windowsVirtualKeyCode: keyCode, text });
  await call("Input.dispatchKeyEvent", { type: "keyUp", key, code, windowsVirtualKeyCode: keyCode });
}
// Opens an overlay from the keyboard, captures it, closes it with Escape, and checks that focus
// returns to the button that opened it.
async function overlayByKeyboard(name, opener, file) {
  assert.ok(
    await evaluate(`(${opener})?.focus() ?? true, document.activeElement === (${opener})`),
    `${name}: focus opener`,
  );
  await press("Enter", "Enter", 13, "\r");
  await waitFor(`${name} open`, () => evaluate("!!document.querySelector('[role=dialog]')"));
  await capture(file);
  await press("Escape", "Escape", 27);
  await waitFor(`${name} closed`, () => evaluate("!document.querySelector('[role=dialog]')"));
  await waitFor(`${name} focus returns`, () => evaluate(`document.activeElement === (${opener})`), 3000);
  return true;
}
// Pages and overlays animate in, and a theme switch runs transitions. A screenshot taken during
// one shows half-faded surfaces, so it waits until every finite animation has finished.
async function capture(file) {
  await waitFor(
    `settled ${file}`,
    async () =>
      (
        await call("Runtime.evaluate", {
          expression:
            "document.getAnimations().every((a) => a.playState !== 'running' || a.effect?.getTiming().iterations === Infinity)",
          returnByValue: true,
        })
      ).result?.value,
  );
  const shot = await call("Page.captureScreenshot", { format: "png" });
  await writeFile(join(output, file), Buffer.from(shot.data, "base64"));
}
try {
  await waitFor("app", async () => (await fetch(`${base}/api/health`)).ok);
  const profile = join(isolated, "chrome");
  chrome = spawn(
    process.env.CHROME_PATH ?? "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
    [
      "--headless=new",
      "--no-first-run",
      "--no-default-browser-check",
      "--disable-background-networking",
      "--remote-debugging-port=0",
      `--user-data-dir=${profile}`,
      "about:blank",
    ],
    { stdio: "ignore" },
  );
  const debug = await waitFor("chrome", async () => {
    const text = await (
      await import("node:fs/promises")
    )
      .readFile(join(profile, "DevToolsActivePort"), "utf8")
      .catch(() => "");
    return text.split("\n")[0];
  });
  const targets = await (await fetch(`http://127.0.0.1:${debug}/json/list`)).json();
  const page = targets.find((target) => target.type === "page");
  socket = new WebSocket(page.webSocketDebuggerUrl);
  socket.addEventListener("message", ({ data }) => {
    const event = JSON.parse(String(data));
    if (event.id && pending.has(event.id)) {
      const task = pending.get(event.id);
      pending.delete(event.id);
      event.error ? task.reject(new Error(JSON.stringify(event.error))) : task.resolve(event.result);
    }
  });
  await new Promise((resolveOpen, reject) => {
    socket.addEventListener("open", resolveOpen, { once: true });
    socket.addEventListener("error", reject, { once: true });
  });
  await call("Page.enable");
  await call("Runtime.enable");
  await call("Emulation.setDeviceMetricsOverride", {
    width: 1280,
    height: 900,
    deviceScaleFactor: 1,
    mobile: false,
  });
  await seed();
  const pages = [
    ["chat", "/a/default"],
    ["sessions", "/a/default/sessions"],
    ["bot-settings", "/a/default/agent"],
    ["providers", "/providers"],
  ];
  for (const theme of ["light", "dark"])
    for (const [name, path] of pages)
      for (const [size, width, height, mobile] of [
        ["desktop", 1280, 900, false],
        ["mobile", 390, 844, true],
      ]) {
        await call("Emulation.setDeviceMetricsOverride", { width, height, deviceScaleFactor: 1, mobile });
        await call("Page.navigate", { url: `${base}${path}` });
        await waitFor(
          `${theme} ${name}`,
          async () =>
            (
              await call("Runtime.evaluate", {
                expression: "document.readyState === 'complete' && document.body.innerText.length > 20",
                returnByValue: true,
              })
            ).result?.value,
        );
        await call("Runtime.evaluate", {
          expression: `localStorage.setItem('dex-theme', '${theme}'); document.documentElement.classList.toggle('dark', '${theme}' === 'dark');`,
          returnByValue: true,
        });
        await capture(`${theme}-${name}-${size}.png`);
        const text =
          (await call("Runtime.evaluate", { expression: "document.body.innerText", returnByValue: true }))
            .result?.value ?? "";
        results.push({ theme, page: name, size, path, textLength: text.length });
        if (name === "providers" && size === "mobile") {
          const keyboard = await overlayByKeyboard(
            `${theme} drawer`,
            "document.querySelector('button[aria-label=\"Open navigation\"]')",
            `${theme}-providers-drawer-mobile.png`,
          );
          results.push({
            theme,
            page: "providers-drawer",
            size: "mobile",
            path,
            textLength: text.length,
            keyboard,
          });
        }
        if (name === "providers" && size === "desktop") {
          const keyboard = await overlayByKeyboard(
            `${theme} provider dialog`,
            "[...document.querySelectorAll('button')].find((button) => button.textContent.trim() === 'Add provider')",
            `${theme}-providers-dialog-desktop.png`,
          );
          results.push({
            keyboard,
            theme,
            page: "providers-dialog",
            size: "desktop",
            path,
            textLength:
              (await call("Runtime.evaluate", { expression: "document.body.innerText", returnByValue: true }))
                .result?.value?.length ?? 0,
          });
        }
      }
  await writeFile(join(output, "result.json"), JSON.stringify({ base, results }, null, 2));
} catch (error) {
  results.push({ error: String(error) });
  await writeFile(join(output, "result.json"), JSON.stringify({ base, results }, null, 2));
  throw error;
} finally {
  socket?.close();
  chrome?.kill();
  app.kill();
}
