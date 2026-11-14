// Real isolated app and native execution. Only the model provider is faux.
/* eslint-disable no-await-in-loop */
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { randomUUID } from "node:crypto";
import { createWriteStream } from "node:fs";
import { mkdtemp, mkdir, writeFile } from "node:fs/promises";
import { createServer } from "node:net";
import { createServer as createHttpServer, request as httpRequest } from "node:http";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { setTimeout as delay } from "node:timers/promises";

const [binary, evidenceArg] = process.argv.slice(2);

assert.ok(binary, "Pass a dex_e2e binary");

const evidence = evidenceArg ? resolve(evidenceArg) : await mkdtemp(join(tmpdir(), "dex-status-evidence-"));

await mkdir(evidence, { recursive: true });

const isolated = await mkdtemp(join(tmpdir(), "dex-status-browser-"));
const home = join(isolated, "home");

await mkdir(home);

const port = await freePort();
const cdpPort = await freePort();
const appPort = await freePort();
const base = `http://127.0.0.1:${port}`;
const appLog = createWriteStream(join(evidence, "app.log"));
const browserLog = createWriteStream(join(evidence, "moli.log"));
let app;
let browser;
let socket;
let sessionId;
let serial = 0;
const pending = new Map();
const reads = [];
const results = [];
let streamOffline = false;
const streams = new Set();

// Drop only real SSE connections. Ordinary API reads must keep working during the outage.
const proxy = createHttpServer((incoming, outgoing) => {
  const stream = incoming.url.endsWith("/events");

  if (stream && streamOffline) {
    outgoing.writeHead(503);
    outgoing.end();

    return;
  }

  if (stream) {
    streams.add(outgoing);
    outgoing.on("close", () => streams.delete(outgoing));
  }

  const upstream = httpRequest(
    {
      hostname: "127.0.0.1",
      port: appPort,
      path: incoming.url,
      method: incoming.method,
      headers: incoming.headers,
    },
    (response) => {
      outgoing.writeHead(response.statusCode, response.headers);
      response.on("error", () => outgoing.destroy());
      response.pipe(outgoing);
    },
  );

  upstream.on("error", () => outgoing.destroy());
  outgoing.on("close", () => upstream.destroy());
  incoming.pipe(upstream);
});

await new Promise((accept) => proxy.listen(port, "127.0.0.1", accept));

function startApp() {
  app = spawn(
    resolve(binary),
    ["serve", "--host", "127.0.0.1", "--port", String(appPort), "--data-dir", join(isolated, "data")],
    {
      env: { PATH: process.env.PATH, HOME: home, LANG: "en_US.UTF-8" },
      stdio: ["ignore", "pipe", "pipe"],
    },
  );
  app.stdout.pipe(appLog, { end: false });
  app.stderr.pipe(appLog, { end: false });
}

try {
  startApp();
  await poll("app ready", async () => (await fetch(`${base}/api/health`)).ok);
  browser = spawn("moli", ["serve", "--host", "127.0.0.1", "--port", String(cdpPort), "--layout", "--font"], {
    env: { PATH: process.env.PATH, HOME: home, LANG: "en_US.UTF-8" },
    stdio: ["ignore", "pipe", "pipe"],
  });
  browser.stdout.pipe(browserLog);
  browser.stderr.pipe(browserLog);

  const version = await poll("CDP discovery", async () =>
    (await fetch(`http://127.0.0.1:${cdpPort}/json/version`)).json(),
  );

  socket = new WebSocket(version.webSocketDebuggerUrl);
  await new Promise((accept, reject) => {
    socket.addEventListener("open", accept, { once: true });
    socket.addEventListener("error", reject, { once: true });
  });
  socket.addEventListener("message", ({ data }) => {
    const event = JSON.parse(String(data));

    if (event.id) {
      const waiter = pending.get(event.id);
      pending.delete(event.id);

      if (event.error) waiter?.reject(new Error(JSON.stringify(event.error)));
      else waiter?.accept(event.result);
    } else if (event.method === "Network.requestWillBeSent" && event.params.request.method === "GET") {
      reads.push(event.params.request.url);
    }
  });
  const target = await call("Target.createTarget", { url: "about:blank" });
  const attached = await call("Target.attachToTarget", { targetId: target.targetId, flatten: true });
  sessionId = attached.sessionId;
  await call("Page.enable");
  await call("Runtime.enable");
  await call("Network.enable");
  await call("Emulation.setDeviceMetricsOverride", {
    width: 1280,
    height: 900,
    deviceScaleFactor: 1,
    mobile: false,
  });
  await api(
    "/api/agents/default",
    { name: "Status demo", provider: "faux", modelId: "spawn", thinkingLevel: "off", instructions: "" },
    "PUT",
  );
  const main = await api("/api/agents/default/chats/main");
  await call("Page.navigate", { url: base + "/a/default" });
  await ui("parent ready", "!!document.querySelector('textarea:not(:disabled):not([inert] *)')");
  const release = join(isolated, "release");
  await send(
    main.id,
    JSON.stringify({
      title: "Status demo child",
      provider: "faux",
      modelId: "bash",
      cwd: isolated,
      message: `while [ ! -f '${release}' ]; do sleep 0.05; done; printf proof-finished`,
    }),
  );

  // The selector is kept as an expression so every read uses the current DOM.
  const childCard =
    "[...document.querySelectorAll('section[aria-label]')].find(e=>e.getAttribute('aria-label')==='Session thread: Status demo child' && !e.closest('[inert]'))";

  await ui(
    "parent sees running tool before child text",
    `${childCard}?.querySelector('[data-state="tool"]') !== null && !!${childCard}`,
  );
  const child = (await api("/api/agents/default/chats")).chats.find((item) => item.parentId === main.id);
  assert.ok(child);
  assert.ok(
    !reads.some((url) => url.includes(`/chats/${child.id}/history`)),
    "the dispatch card must not fetch child history",
  );
  assert.ok(
    !reads.some((url) => new URL(url).pathname === `/api/agents/default/chats/${child.id}`),
    "the dispatch card must not poll child details",
  );
  assert.equal(
    await evaluate(`${childCard}.innerText.includes('proof-finished')`),
    false,
    "the parent card has no copied child text",
  );
  await shot("01-parent-running");
  await evaluate(`${childCard}.querySelector('button').click()`);

  const childPanel =
    "[...document.querySelectorAll('aside[aria-label]')].find(e=>e.getAttribute('aria-label')==='Session thread' && !e.closest('[inert]'))";

  await ui(
    "child panel shows native tool phase without a reply",
    `!!${childPanel}?.querySelector('[data-state="tool"]')`,
  );
  await shot("02-child-running");
  results.push(
    "Parent card receives native tool status before text, without child history or detail polling; child panel uses the same state.",
  );
  await writeFile(release, "release");
  await ui(
    "parent updates to done without navigation",
    `!!${childCard}?.querySelector('[data-state="done"]')`,
  );
  await ui("child panel receives completion", `!!${childPanel}?.querySelector('[data-state="done"]')`);
  await shot("03-completed");
  await evaluate("document.querySelector('button[aria-label=\"Close thread\"]').click()");
  const again = join(isolated, "again");
  await send(child.id, `while [ ! -f '${again}' ]; do sleep 0.05; done; printf reused`);
  await poll("reused child busy", async () => (await api(`/api/agents/default/chats/${child.id}`)).busy);
  await ui(
    "the first dispatch stays done during child reuse",
    `!!${childCard}?.querySelector('[data-state="done"]')`,
  );
  await writeFile(again, "release");
  await poll("reused child complete", async () => !(await api(`/api/agents/default/chats/${child.id}`)).busy);
  results.push("The first card stays done while the same child runs another input.");
  await send(
    main.id,
    JSON.stringify({ title: "Failed child", provider: "faux", modelId: "tool", message: "read not-json" }),
  );
  await ui(
    "failed child outcome reaches parent",
    "!![...document.querySelectorAll('section[aria-label]')].find(e=>e.getAttribute('aria-label')==='Session thread: Failed child')?.querySelector('[data-state=\"failed\"]')",
  );
  await shot("04-failed");
  results.push("A native provider failure shows failed without a child text reply.");
  streamOffline = true;

  for (const stream of streams) stream.destroy();
  await ui(
    "disconnected cards do not claim live status",
    `!!${childCard}?.querySelector('[data-state="unavailable"]')`,
  );
  await shot("05-disconnected");
  streamOffline = false;
  await ui(
    "SSE reconnect restores submission outcomes",
    `!!${childCard}?.querySelector('[data-state="done"]')`,
  );
  await shot("06-reconnected");
  results.push("Disconnect shows status unavailable; automatic SSE reconnect restores durable completion.");
  await writeFile(
    join(evidence, "results.json"),
    JSON.stringify({ results, base, isolated, browser: version.Browser }, null, 2),
  );
  console.log(JSON.stringify({ results, evidence }));
} catch (error) {
  if (socket?.readyState === WebSocket.OPEN && sessionId) {
    try {
      await shot("failure");
      await writeFile(join(evidence, "failure.txt"), String(await evaluate("document.body.innerText")));
      await writeFile(
        join(evidence, "failure-state.json"),
        JSON.stringify(
          await evaluate(
            "[...document.querySelectorAll('aside[aria-label]')].map(e=>({label:e.getAttribute('aria-label'),states:[...e.querySelectorAll('[data-state]')].map(b=>[b.getAttribute('data-state'),b.textContent]),text:e.innerText}))",
          ),
          null,
          2,
        ),
      );
    } catch (captureError) {
      console.error("Failure capture:", captureError.message);
    }
  }

  throw error;
} finally {
  socket?.close();
  await stop(browser);
  await stop(app);
  proxy.closeAllConnections();
  await new Promise((accept) => proxy.close(accept));
  appLog.end();
  browserLog.end();
}

async function call(method, params = {}) {
  const id = ++serial;

  return new Promise((accept, reject) => {
    const timer = setTimeout(() => {
      pending.delete(id);
      reject(new Error(`CDP timeout: ${method}`));
    }, 15000);

    pending.set(id, {
      accept: (value) => {
        clearTimeout(timer);
        accept(value);
      },
      reject: (error) => {
        clearTimeout(timer);
        reject(error);
      },
    });
    const message = { id, method, params };

    if (sessionId) message.sessionId = sessionId;
    socket.send(JSON.stringify(message));
  });
}

async function evaluate(expression) {
  const value = await call("Runtime.evaluate", { expression, returnByValue: true, awaitPromise: true });

  if (value.exceptionDetails) throw new Error(JSON.stringify(value.exceptionDetails));

  return value.result.value;
}

async function ui(label, expression) {
  await poll(label, () => evaluate(expression));
}

async function shot(name) {
  const value = await call("Page.captureScreenshot", { format: "png", captureBeyondViewport: false });
  await writeFile(join(evidence, `${name}.png`), Buffer.from(value.data, "base64"));
}

async function api(path, body, method = body ? "POST" : "GET") {
  const response = await fetch(base + path, {
    method,
    headers: body ? { "Content-Type": "application/json" } : {},
    body: body ? JSON.stringify(body) : undefined,
  });

  assert.ok(response.ok, `${method} ${path}: ${response.status}`);

  return response.json();
}

async function send(chatId, text) {
  return api(`/api/agents/default/chats/${chatId}/messages`, { id: randomUUID(), text });
}

async function freePort() {
  const server = createServer();
  await new Promise((accept) => server.listen(0, "127.0.0.1", accept));
  const selectedPort = server.address().port;
  await new Promise((accept) => server.close(accept));
  assert.notEqual(selectedPort, 8787);

  return selectedPort;
}

async function poll(label, read) {
  const until = Date.now() + 25000;
  let last;

  while (Date.now() < until) {
    try {
      const value = await read();

      if (value) return value;
      last = value;
    } catch (error) {
      last = error.message;
    }

    if (app?.exitCode !== null && app?.exitCode !== undefined)
      throw new Error(`App exited during ${label}: ${app.exitCode}`);

    if (browser?.exitCode !== null && browser?.exitCode !== undefined)
      throw new Error(`Browser exited during ${label}: ${browser.exitCode}`);
    await delay(50);
  }

  throw new Error(`Timeout: ${label}; last=${JSON.stringify(last)}`);
}

async function stop(child) {
  if (!child || child.exitCode !== null || child.signalCode !== null) return;
  await new Promise((accept) => {
    const timer = setTimeout(() => child.kill("SIGKILL"), 5000);
    child.once("exit", () => {
      clearTimeout(timer);
      accept();
    });
    child.kill("SIGTERM");
  });
}
