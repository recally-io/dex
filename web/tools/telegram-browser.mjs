// Real local HTTP, SQLite, native services and Moli layout. No real Telegram token is used.
/* eslint-disable no-await-in-loop */
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createWriteStream } from "node:fs";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { setTimeout as delay } from "node:timers/promises";

const [binary, output] = process.argv.slice(2);

assert.ok(binary);

const evidence = output ? resolve(output) : await mkdtemp(join(tmpdir(), "dex-telegram-evidence-"));
const root = await mkdtemp(join(tmpdir(), "dex-telegram-browser-"));

await mkdir(evidence, { recursive: true });

await mkdir(join(root, "home"));

const appPort = await port();
const browserPort = await port();
const base = `http://127.0.0.1:${appPort}`;
const cdp = `http://127.0.0.1:${browserPort}`;

const app = spawn(
  resolve(binary),
  ["serve", "--host", "127.0.0.1", "--port", String(appPort), "--data-dir", join(root, "data")],
  {
    env: { PATH: process.env.PATH, HOME: join(root, "home"), LANG: "en_US.UTF-8" },
    stdio: ["ignore", "pipe", "pipe"],
  },
);

const browser = spawn("moli", ["serve", "--host", "127.0.0.1", "--port", String(browserPort), "--layout"], {
  env: { PATH: process.env.PATH, HOME: join(root, "home"), LANG: "en_US.UTF-8" },
  stdio: ["ignore", "pipe", "pipe"],
});

const logs = [createWriteStream(join(evidence, "app.log")), createWriteStream(join(evidence, "browser.log"))];

app.stdout.pipe(logs[0]);

app.stderr.pipe(logs[0]);

browser.stdout.pipe(logs[1]);

browser.stderr.pipe(logs[1]);

let socket;
let serial = 0;
const pending = new Map();
const events = [];

try {
  await poll("local app", async () => (await fetch(base + "/api/health")).ok);
  await poll("CDP discovery", async () => (await fetch(cdp + "/json/version")).ok);
  const pages = await (await fetch(cdp + "/json/list")).json();
  assert.ok(
    pages.some((page) => page.type === "page"),
    "CDP has no page target",
  );
  socket = new WebSocket(pages.find((page) => page.type === "page").webSocketDebuggerUrl);
  await new Promise((done, fail) => {
    socket.addEventListener("open", done, { once: true });
    socket.addEventListener("error", fail, { once: true });
  });
  socket.addEventListener("message", ({ data }) => {
    const message = JSON.parse(String(data));

    if (!message.id) {
      if (message.method === "Runtime.exceptionThrown") events.push(message);

      return;
    }

    const waiting = pending.get(message.id);
    pending.delete(message.id);

    if (message.error) waiting.fail(new Error(JSON.stringify(message.error)));
    else waiting.done(message.result);
  });
  await call("Page.enable");
  await call("Runtime.enable");
  await call("Emulation.setDeviceMetricsOverride", {
    width: 1280,
    height: 960,
    deviceScaleFactor: 1,
    mobile: false,
  });
  await call("Page.navigate", { url: base + "/a/default/channels/telegram" });
  await poll("Telegram setup", () => evaluate("document.body.textContent.includes('Connect bot')"));
  await evaluate(
    "[...document.querySelectorAll('summary')].find(el => el.textContent === 'Commands and limits').parentElement.open = true",
  );
  assert.equal(
    await evaluate(
      "document.body.textContent.includes('/workflows') && document.body.textContent.includes('15 minutes')",
    ),
    true,
  );
  await evaluate(
    "[...document.querySelectorAll('summary')].find(el => el.textContent === 'Commands and limits').scrollIntoView({ block: 'center' })",
  );
  await capture("telegram-workflow-help.png");
  await evaluate(
    "[...document.querySelectorAll('button')].find(el => el.textContent.trim() === 'Connect bot').click()",
  );
  await poll("group settings", () =>
    evaluate("[...document.querySelectorAll('summary')].some(el => el.textContent === 'Group input')"),
  );
  await evaluate(
    "[...document.querySelectorAll('summary')].find(el => el.textContent === 'Group input').parentElement.open = true",
  );

  const selector =
    "document.getElementById([...document.querySelectorAll('label')].find(el => el.textContent === 'Require group mention').htmlFor)";

  assert.equal(await evaluate(selector + ".value"), "off");
  await capture("require-mention-default-off.png");
  await evaluate(
    `(() => { const el = ${selector}; el.value = 'on'; el.dispatchEvent(new Event('change', { bubbles: true })); })()`,
  );
  await poll("explicit mention mode", () => evaluate(selector + ".value === 'on'"));
  await capture("require-mention-explicit-on.png");
  await writeFile(
    join(evidence, "results.json"),
    JSON.stringify({ default: "off", explicit: "on", realTokenUsed: false, base }, null, 2),
  );
  console.log(`PASS group requireMention default off and explicit on; screenshots: ${evidence}`);
} catch (error) {
  const page =
    socket?.readyState === WebSocket.OPEN
      ? await evaluate(
          "JSON.stringify({url:location.href,text:document.body.textContent,html:document.body.innerHTML.slice(0,2000)})",
        ).catch(String)
      : "no page";

  await writeFile(join(evidence, "failure.txt"), `${error.stack}\n${page}\n${JSON.stringify(events)}`);
  throw error;
} finally {
  socket?.close();

  for (const child of [browser, app]) if (child.exitCode === null) child.kill("SIGTERM");
  await Promise.all(
    [browser, app].map(async (child) => {
      if (child.exitCode !== null) return;
      await Promise.race([new Promise((done) => child.once("exit", done)), delay(5000)]);

      if (child.exitCode === null) child.kill("SIGKILL");
    }),
  );

  for (const log of logs) log.end();
  await rm(root, { recursive: true, force: true });
}

async function port() {
  const listener = createServer();
  await new Promise((done) => listener.listen(0, "127.0.0.1", done));
  const value = listener.address().port;
  await new Promise((done) => listener.close(done));

  return value;
}

async function poll(label, test) {
  const end = Date.now() + 20000;
  let last;

  while (Date.now() < end) {
    if (app.exitCode !== null || browser.exitCode !== null) throw new Error(`process exited before ${label}`);

    try {
      last = await test();

      if (last) return last;
    } catch (error) {
      last = String(error);
    }

    await delay(50);
  }

  throw new Error(`timeout: ${label}: ${last}`);
}

async function call(method, params = {}) {
  const id = ++serial;
  const promise = new Promise((done, fail) => pending.set(id, { done, fail }));
  socket.send(JSON.stringify({ id, method, params }));

  return Promise.race([
    promise,
    delay(20000, undefined, { ref: false }).then(() => {
      throw new Error(`CDP timeout: ${method}`);
    }),
  ]);
}

async function evaluate(expression) {
  const response = await call("Runtime.evaluate", { expression, returnByValue: true, awaitPromise: true });

  if (response.exceptionDetails) throw new Error(JSON.stringify(response.exceptionDetails));

  return response.result.value;
}

async function capture(name) {
  const image = await call("Page.captureScreenshot", { format: "png", captureBeyondViewport: false });
  await writeFile(join(evidence, name), Buffer.from(image.data, "base64"));
}
