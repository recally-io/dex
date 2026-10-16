// Real control, worker, HTTP, and Chrome. The provider is the app's faux boundary.
/* eslint-disable no-await-in-loop */
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { randomUUID } from "node:crypto";
import { createWriteStream } from "node:fs";
import { mkdtemp, mkdir, readFile, writeFile } from "node:fs/promises";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { setTimeout as delay } from "node:timers/promises";

const [binary, artifactArg] = process.argv.slice(2);

assert.ok(binary, "Pass a dex_e2e binary");

const artifacts = artifactArg
  ? resolve(artifactArg)
  : await mkdtemp(join(tmpdir(), "dex-timeline-evidence-"));

await mkdir(artifacts, { recursive: true });

const isolated = await mkdtemp(join(tmpdir(), "dex-timeline-browser-"));
const home = join(isolated, "home");
const data = join(isolated, "data");
const profile = join(isolated, "chrome");

await mkdir(home);

const port = await freePort();

assert.notEqual(port, 8787);

const base = `http://127.0.0.1:${port}`;
const appLog = createWriteStream(join(artifacts, "browser-app.log"));

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

const chromeLog = createWriteStream(join(artifacts, "chrome.log"));
let chrome;
let socket;
const results = [];
const clears = [];
const exceptions = [];
let serial = 0;
const pending = new Map();

try {
  await poll("app ready", async () => (await fetch(`${base}/api/health`)).ok);
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
    { stdio: ["ignore", "pipe", "pipe"] },
  );
  chrome.stdout.pipe(chromeLog);
  chrome.stderr.pipe(chromeLog);
  chrome.on("error", (error) => exceptions.push(error.message));

  const debug = await poll("Chrome ready", async () => {
    const value = await readFile(join(profile, "DevToolsActivePort"), "utf8");

    return value.split("\n")[0];
  });

  const pages = await (await fetch(`http://127.0.0.1:${debug}/json/list`)).json();
  const page = pages.find((target) => target.type === "page" && target.url === "about:blank");
  assert.ok(page, "Chrome opened an isolated page");
  socket = new WebSocket(page.webSocketDebuggerUrl);
  await new Promise((accept, reject) => {
    socket.addEventListener("open", accept, { once: true });
    socket.addEventListener("error", reject, { once: true });
  });
  socket.addEventListener("message", ({ data: message }) => {
    const event = JSON.parse(String(message));

    if (event.id) {
      const promise = pending.get(event.id);
      pending.delete(event.id);

      if (event.error) promise.reject(new Error(JSON.stringify(event.error)));
      else promise.accept(event.result);
    } else if (event.method === "Runtime.exceptionThrown") exceptions.push(event.params.exceptionDetails);
    else if (event.method === "Network.requestWillBeSent" && event.params.request.url.endsWith("/clear"))
      clears.push({ request: JSON.parse(event.params.request.postData), requestId: event.params.requestId });
    else if (event.method === "Network.responseReceived") {
      const clear = clears.find((entry) => entry.requestId === event.params.requestId);

      if (clear) clear.status = event.params.response.status;
    }
  });
  await call("Page.enable");
  await call("Runtime.enable");
  await call("Network.enable");
  await viewport(1280, 900);

  // Given a new installation, the root is the bot's empty timeline and composer.
  await visit("/");
  await ui("empty timeline", "document.body.innerText.includes('What should Dex do?')");
  await check(
    "shell has the bot row and gear, with no chat list",
    `(() => {
    const nav = document.querySelector('aside[aria-label="Navigation"]');
    const links = [...nav.querySelectorAll('a')];
    return links.some(a => a.getAttribute('href') === '/' && a.textContent.trim().endsWith('Dex')) &&
      links.some(a => a.getAttribute('aria-label') === 'Dex settings' && a.getAttribute('href') === '/agent') &&
      !/New chat|Search chats|Timeline|Configure/.test(nav.innerText) &&
      !links.some(a => /^\\/c\\//.test(a.getAttribute('href')));
  })()`,
  );
  await check(
    "empty timeline has a composer and no Clear context",
    "!!document.querySelector('textarea[aria-label=\"Message Dex\"]') && ![...document.querySelectorAll('button')].some(b => b.textContent.trim() === 'Clear context')",
  );
  await shot("01-empty");
  const main = await api("/api/chats/main");
  await model("echo");
  await call("Page.reload");
  await ui("composer ready", "!!document.querySelector('textarea:not(:disabled)')");
  await sendFromComposer("first input");
  await ui("first answer", "document.body.innerText.includes('echo: first input')");
  await ui(
    "clear visible",
    "!![...document.querySelectorAll('button')].find(b => b.textContent.trim() === 'Clear context')",
  );

  // When a real tool holds the run, only the server refuses Clear context.
  await model("bash");
  const busyGate = join(isolated, "busy.gate");
  await send(`while ! test -f '${busyGate}'; do sleep 0.05; done; echo gate-released`);
  await ui("run is active", "!!document.querySelector('button[aria-label=\"Stop run\"]')");
  await check(
    "Clear context remains enabled while busy",
    "![...document.querySelectorAll('button')].find(b => b.textContent.trim() === 'Clear context').disabled",
  );
  await click("Clear context");
  await poll("real 409", () => clears.some((entry) => entry.status === 409));
  await ui(
    "409 warn note",
    "document.body.innerText.includes('Context not cleared.') && document.body.innerText.includes('Dex is working. Wait for the reply or stop it, then try again.')",
  );
  await shot("02-refused");
  await click("Clear context");
  await poll("second refusal", () => clears.filter((entry) => entry.status === 409).length === 2);
  assert.equal(clears[0].request.id, clears[1].request.id, "transport retry keeps its reset ID");
  await writeFile(busyGate, "release");
  await ui(
    "reply completed and note hidden",
    "document.body.innerText.includes('bash: gate-released') && !document.body.innerText.includes('Context not cleared.')",
  );
  await click("Clear context");
  await poll("accepted clear", () => clears.some((entry) => entry.status === 202));
  assert.equal(clears[0].request.id, clears.at(-1).request.id, "accepted retry keeps the refused ID");
  await ui(
    "reset divider",
    "!!document.querySelector('[role=separator]') && ![...document.querySelectorAll('button')].some(b => b.textContent.trim() === 'Clear context')",
  );
  results.push("server 409 note, stable reset ID, idle clear, divider, hidden Clear after reset");
  await call("Page.reload");
  await ui(
    "fresh history boundary",
    "document.body.innerText.includes('Show earlier messages') && !document.body.innerText.includes('first input')",
  );
  await click("Show earlier messages");
  await ui("earlier messages loaded", "document.body.innerText.includes('echo: first input')");
  await check(
    "history merges once and does not make cleared context writable again",
    "document.querySelectorAll('[role=separator]').length === 1 && document.body.innerText.split('echo: first input').length === 2 && ![...document.querySelectorAll('button')].some(b => b.textContent.trim() === 'Clear context')",
  );
  await model("context");
  await sendFromComposer("after clear");
  await ui(
    "old context stays out of the model",
    "document.body.innerText.includes('context: [\"after clear\"]')",
  );
  await shot("03-earlier");

  // Given two same-title tasks, dispatch and result links use their saved IDs.
  await model("spawn");
  await send(JSON.stringify({ title: "Check", goal: "saved report", provider: "faux", modelId: "echo" }));

  const done = await poll("completed task", async () =>
    (await api("/api/tasks")).tasks.find((task) => task.status === "done"),
  );

  await ui(
    "result and dispatch links",
    `document.querySelectorAll('a[href="/tasks/${done.id}"]').length === 2`,
  );
  const workGate = join(isolated, "work.gate");
  await send(
    JSON.stringify({
      title: "Check",
      goal: `while ! test -f '${workGate}'; do sleep 0.05; done; echo work-released`,
      provider: "faux",
      modelId: "bash",
    }),
  );

  const running = await poll("running task", async () =>
    (await api("/api/tasks")).tasks.find((task) => task.status === "running"),
  );

  assert.notEqual(done.id, running.id);
  await ui("active task badge", "!!document.querySelector('[aria-label=\"1 active\"]')");
  await check(
    "main Stop remains available after dispatch reply",
    "!!document.querySelector('button[title=\"Stop this run and its background work\"]')",
  );
  await visit("/tasks");
  await ui("tasks list", `!!document.querySelector('a[href="/tasks/${running.id}"]')`);
  await check(
    "running task comes before completed task with the same title",
    `(() => { const links = [...document.querySelectorAll('main a[href^="/tasks/"]')]; return links.length === 2 && links[0].getAttribute('href') === '/tasks/${running.id}' && links[1].getAttribute('href') === '/tasks/${done.id}'; })()`,
  );
  await shot("04-tasks");
  await visit(`/tasks/${running.id}`);
  await ui(
    "live task work",
    "document.body.innerText.includes('No report yet.') && document.body.innerText.includes('Running bash')",
  );
  await check(
    "task work is read-only",
    "!document.querySelector('textarea') && ![...document.querySelectorAll('button')].some(b => /^(Cancel|Retry|Send)$/.test(b.textContent.trim()))",
  );
  await shot("05-task-work");
  await visit("/");
  await ui("stop background work", "!!document.querySelector('button[aria-label=\"Stop run\"]')");
  await evaluate("document.querySelector('button[aria-label=\"Stop run\"]').click()");
  await poll(
    "background cancellation",
    async () => (await api(`/api/tasks/${running.id}`)).status === "cancelled",
  );
  await visit(`/tasks/${done.id}`);
  await ui("saved report", "document.body.innerText.includes('echo: saved report')");
  await visit(`/c/${done.conversationId}`);
  await ui(
    "read-only conversation",
    "document.body.innerText.includes('You can read this conversation here.') && document.body.innerText.includes('echo: saved report')",
  );
  await check(
    "read-only chat has no composer",
    "!document.querySelector('textarea') && !document.querySelector('button[aria-label=\"Send message\"]')",
  );
  await shot("06-read-only");

  // Bot settings hold the editable name, channels and automations.
  await visit("/agent");
  await ui(
    "bot settings",
    "!!document.querySelector('#bot-name') && document.body.innerText.includes('Automations')",
  );
  await evaluate("document.querySelector('#bot-name').focus(); document.querySelector('#bot-name').select()");
  await call("Input.insertText", { text: "  Browser Bot  " });
  await call("Input.dispatchKeyEvent", { type: "keyDown", key: "Enter", code: "Enter" });
  await poll("name saved", async () => (await api("/api/settings")).agent.name === "Browser Bot");
  await check(
    "settings sections match the bot scope",
    "[...document.querySelectorAll('h2')].map(h => h.textContent).join(',') === 'Profile,Instructions,Channels,Automations'",
  );
  await shot("07-bot-settings");

  for (const route of ["/channels", "/automations"]) {
    await visit(route);
    await ui("list redirect", "location.pathname === '/agent'");
  }

  await visit("/");
  await ui("renamed composer", "!!document.querySelector('textarea[aria-label=\"Message Browser Bot\"]')");
  await viewport(390, 844);
  await shot("08-mobile");
  await evaluate("document.querySelector('button[aria-label=\"Open navigation\"]').click()");
  await ui(
    "mobile bot gear",
    "!!document.querySelector('[role=dialog] a[aria-label=\"Browser Bot settings\"]')",
  );
  await shot("09-mobile-nav");
  assert.deepEqual(exceptions, [], "no browser exceptions");
  assert.equal((await api("/api/chats/main")).id, main.id);
  results.push(
    "same-title task IDs, active task badge, list order, saved report, live read-only work, idle Stop",
  );
  results.push("bot settings, trimmed name, redirects, mobile header and drawer, stable main ID");
  await writeFile(
    join(artifacts, "browser-results.json"),
    JSON.stringify({ base, data, profile, results, clears, exceptions }, null, 2),
  );
  console.log(`PASS: ${results.length} timeline browser checks. Evidence: ${artifacts}`);
} catch (error) {
  if (socket?.readyState === WebSocket.OPEN) {
    await shot("failure").catch(() => {});
    await writeFile(
      join(artifacts, "failure.txt"),
      String(error) + "\n" + (await evaluate("document.body.innerText").catch(() => "")),
    );
  }

  throw error;
} finally {
  socket?.close();
  await Promise.all([stop(chrome), stop(app)]);
  appLog.end();
  chromeLog.end();
}

async function freePort() {
  const server = createServer();
  await new Promise((accept, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", accept);
  });
  const assigned = server.address();
  await new Promise((accept) => server.close(accept));

  return assigned.port;
}

async function poll(label, read, timeout = 25_000) {
  const deadline = Date.now() + timeout;
  let last;

  while (Date.now() < deadline) {
    try {
      last = await read();

      if (last) return last;
    } catch (error) {
      last = String(error);
    }

    await delay(50);
  }

  throw new Error(`${label}: deadline exceeded; last=${JSON.stringify(last)}`);
}

function call(method, params = {}) {
  const id = ++serial;
  const result = new Promise((accept, reject) => pending.set(id, { accept, reject }));
  socket.send(JSON.stringify({ id, method, params }));

  return result;
}

async function evaluate(expression) {
  const response = await call("Runtime.evaluate", { expression, awaitPromise: true, returnByValue: true });

  if (response.exceptionDetails) throw new Error(JSON.stringify(response.exceptionDetails));

  return response.result.value;
}

async function ui(label, expression) {
  await poll(label, () => evaluate(expression));
}

async function check(label, expression) {
  assert.equal(await evaluate(expression), true, label);
  results.push(label);
}

async function visit(path) {
  await call("Page.navigate", { url: base + path });
  await ui("page ready", "document.readyState === 'complete' && !!document.querySelector('main')");
}

async function viewport(width, height) {
  await call("Emulation.setDeviceMetricsOverride", {
    width,
    height,
    deviceScaleFactor: 1,
    mobile: width < 600,
  });
}

async function shot(name) {
  const screenshot = await call("Page.captureScreenshot", { format: "png", captureBeyondViewport: false });
  await writeFile(join(artifacts, `${name}.png`), Buffer.from(screenshot.data, "base64"));
}

async function api(path, body, method = body ? "POST" : "GET") {
  const response = await fetch(base + path, {
    method,
    headers: body ? { "Content-Type": "application/json" } : {},
    body: body ? JSON.stringify(body) : undefined,
  });

  const value = await response.json();
  assert.ok(response.ok, `${method} ${path}: ${response.status} ${JSON.stringify(value)}`);

  return value;
}

async function model(modelId) {
  const { agent } = await api("/api/settings");
  await api("/api/settings/agent", { ...agent, provider: "faux", modelId }, "PUT");
}

async function send(text) {
  const { id } = await api("/api/chats/main");
  await api(`/api/chats/${id}/messages`, { id: randomUUID(), text });
}

async function sendFromComposer(text) {
  await ui("composer enabled", "!!document.querySelector('textarea:not(:disabled)')");
  await evaluate("document.querySelector('textarea').focus()");
  await call("Input.insertText", { text });
  await call("Input.dispatchKeyEvent", { type: "keyDown", key: "Enter", code: "Enter" });
  await call("Input.dispatchKeyEvent", { type: "keyUp", key: "Enter", code: "Enter" });
}

async function click(text) {
  await evaluate(
    `(() => { const button = [...document.querySelectorAll('button')].find(b => b.textContent.trim() === ${JSON.stringify(text)}); if (!button || button.disabled) throw new Error('Button is unavailable'); button.click(); })()`,
  );
}

async function stop(child) {
  if (!child || child.exitCode !== null) return;
  child.kill("SIGTERM");
  await Promise.race([
    new Promise((accept) => child.once("exit", accept)),
    delay(12_000).then(() => child.kill("SIGKILL")),
  ]);
}
