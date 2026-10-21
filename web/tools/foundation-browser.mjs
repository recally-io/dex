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
    env: { PATH: process.env.PATH, HOME: join(isolated, "home"), LANG: "en_US.UTF-8" },
    stdio: "ignore",
  },
);
const base = `http://127.0.0.1:${port}`;
const results = [];
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
        await call("Page.captureScreenshot", { format: "png" }).then((shot) =>
          writeFile(join(output, `${theme}-${name}-${size}.png`), Buffer.from(shot.data, "base64")),
        );
        const text =
          (await call("Runtime.evaluate", { expression: "document.body.innerText", returnByValue: true }))
            .result?.value ?? "";
        results.push({ theme, page: name, size, path, textLength: text.length });
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
