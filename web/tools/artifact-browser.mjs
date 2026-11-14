// Real HTTP, native engine, SQLite, and Chrome. Only the model provider is faux.
/* eslint-disable no-await-in-loop */
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { randomUUID } from "node:crypto";
import { createWriteStream } from "node:fs";
import { mkdir, mkdtemp, readFile, rename, unlink, utimes, writeFile } from "node:fs/promises";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { setTimeout as delay } from "node:timers/promises";

const [binary, output] = process.argv.slice(2);

assert.ok(binary);

const evidence = output ? resolve(output) : await mkdtemp(join(tmpdir(), "dex-artifact-evidence-"));
const root = await mkdtemp(join(tmpdir(), "dex-artifact-browser-"));
const data = join(root, "data");
const home = join(root, "home");
const workspace = join(data, "agents", "default", "workspace");
const profile = join(root, "chrome");
const files = join(data, "agents", "default", "artifacts");

await Promise.all([mkdir(evidence, { recursive: true }), mkdir(home), mkdir(workspace, { recursive: true })]);

for (const name of ["report.md", "second.md"]) {
  await writeFile(join(workspace, name), `# Preview ${name}\n`);
}

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
const network = [];

try {
  await poll("app", async () => (await fetch(base + "/api/health")).ok);
  const main = await api("/api/agents/default/chats/main");
  await model("tool");
  await send(main.id, 'deliver_file {"path":"report.md"}');
  const chat = await api("/api/agents/default/chats", {}, "POST");
  await send(chat.id, 'deliver_file {"path":"second.md"}');

  const bot = await api("/api/agents", {
    name: "Other bot",
    provider: "faux",
    modelId: "echo",
    thinkingLevel: "off",
  });

  const botFiles = join(data, "agents", bot.id, "artifacts");
  await mkdir(botFiles, { recursive: true });
  await writeFile(join(botFiles, "b-only.md"), "# Only in the other bot\n");
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
  socket = new WebSocket(pages.find((p) => p.type === "page").webSocketDebuggerUrl);
  await new Promise((done, fail) => {
    socket.addEventListener("open", done, { once: true });
    socket.addEventListener("error", fail, { once: true });
  });
  socket.addEventListener("message", ({ data: raw }) => {
    const msg = JSON.parse(String(raw));

    if (msg.id) {
      const p = pending.get(msg.id);
      pending.delete(msg.id);

      if (msg.error) p.fail(new Error(JSON.stringify(msg.error)));
      else p.done(msg.result);
    } else if (msg.method?.startsWith("Network.")) network.push(msg);
  });
  await call("Network.enable");
  await call("Page.enable");
  await call("Runtime.enable");
  await call("Emulation.setDeviceMetricsOverride", {
    width: 1280,
    height: 900,
    deviceScaleFactor: 1,
    mobile: false,
  });
  await visit("/a/default");
  await verifyArtifactEntry("desktop");
  await click("Artifacts");
  await click("report.md");
  await panelHas("Preview report.md");
  await click("Continue editing");
  await hasDraft("/api/agents/default/artifacts/report.md");
  await shot("artifacts-entry");
  results.push("desktop icon, tooltip, keyboard open, preview, and continuation");

  await visit(`/a/default/c/${chat.id}`);
  await click("Artifacts");
  await click("report.md");
  await panelHas("Preview report.md");
  await click("Rename");
  await setName("second.md");
  await click("Save");
  await panelHas("Could not rename artifact");
  await click("Cancel");
  assert.equal(await readFile(join(files, "second.md"), "utf8"), "# Preview second.md\n");
  await click("Rename");
  await setName("renamed.md");
  await click("Save");
  await panelHas("renamed.md");
  await poll("renamed download", () => evaluate(`!!document.querySelector('a[download="renamed.md"]')`));
  assert.equal(
    await (await fetch(base + "/api/agents/default/artifacts/renamed.md")).text(),
    "# Preview report.md\n",
  );
  results.push("the same bot files in another conversation, native rename, conflict, and download");

  const { targetId } = await call("Target.createTarget", { url: base + "/a/default" });
  const { sessionId } = await call("Target.attachToTarget", { targetId, flatten: true });

  const other = async (expression) => {
    const response = await call("Runtime.evaluate", { expression, returnByValue: true }, sessionId);

    if (response.exceptionDetails) throw new Error(JSON.stringify(response.exceptionDetails));

    return response.result.value;
  };

  await poll("second page", () => other(`!!document.querySelector('button[aria-label="Artifacts"]')`));
  await other(`document.querySelector('button[aria-label="Artifacts"]').click()`);
  await rename(join(files, "renamed.md"), join(files, "external.md"));
  await panelHas("external.md");
  await poll("old selection clears after external rename", () =>
    evaluate(`!document.querySelector('[role=dialog] strong')`),
  );
  await poll("second page sees rename", () =>
    other(`document.querySelector('[role=dialog]')?.innerText.includes('external.md')`),
  );
  await click("external.md");
  await writeFile(join(files, "external.md"), "# Disk version A\n");
  await panelHas("Disk version A");
  await writeFile(join(files, "external.md"), "# Disk version B\n");
  const changed = new Date(Date.now() + 1000);
  await utimes(join(files, "external.md"), changed, changed);
  await panelHas("Disk version B");
  await shot("artifacts-desktop");
  await click("Delete");
  await shot("artifacts-confirm");
  await click("Cancel");
  await readFile(join(files, "external.md"));
  await click("Delete");
  await click("Delete artifact");
  await poll("delete clears file and selection", () =>
    evaluate(
      `!document.querySelector('[role=dialog]').innerText.includes('external.md') && !document.querySelector('[role=dialog] strong')`,
    ),
  );
  await poll("other page sees deletion", () =>
    other(`!document.querySelector('[role=dialog]').innerText.includes('external.md')`),
  );
  await writeFile(join(files, "added.md"), "# Added on disk\n");
  await panelHas("added.md");
  await poll("other page sees addition", () =>
    other(`document.querySelector('[role=dialog]').innerText.includes('added.md')`),
  );
  await click("added.md");
  await panelHas("Added on disk");
  await unlink(join(files, "added.md"));
  await poll("external delete clears selection", () =>
    evaluate(
      `!document.querySelector('[role=dialog]').innerText.includes('added.md') && !document.querySelector('[role=dialog] strong')`,
    ),
  );
  await call("Target.closeTarget", { targetId });
  results.push("two-page disk rename, edits, addition, delete confirmation, and external deletion");

  await visit(`/a/${bot.id}`);
  await click("Artifacts");
  await panelHas("b-only.md");
  assert.equal(
    await evaluate(`document.querySelector('[role=dialog]').innerText.includes('second.md')`),
    false,
  );
  await shot("artifacts-other-bot");
  results.push("different bots show different physical directories");
  await visit("/a/default");
  await call("Emulation.setDeviceMetricsOverride", {
    width: 320,
    height: 844,
    deviceScaleFactor: 1,
    mobile: true,
  });
  await verifyArtifactEntry("mobile");
  await click("Artifacts");
  await panelHas("second.md");
  await shot("artifacts-mobile");
  await click("Close");
  results.push("320px icon entry, tooltip, keyboard activation, and file panel");
  await writeFile(join(evidence, "results.json"), JSON.stringify({ results }, null, 2));
  console.log(`PASS: ${results.length} artifact browser checks. Evidence: ${evidence}`);
} catch (error) {
  await writeFile(
    join(evidence, "failure.txt"),
    String(error) +
      "\n" +
      (socket?.readyState === WebSocket.OPEN
        ? await evaluate("document.body.innerText").catch(() => "")
        : ""),
  );
  await writeFile(join(evidence, "network.json"), JSON.stringify(network, null, 2));
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
    } catch (err) {
      last = String(err);
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

async function model(id) {
  const agent = await api("/api/agents/default");
  await api("/api/agents/default", { ...agent, provider: "faux", modelId: id }, "PUT");
}

async function send(id, text) {
  await api(`/api/agents/default/chats/${id}/messages`, { id: randomUUID(), text });
  await poll("chat settled", async () => !(await api(`/api/agents/default/chats/${id}`)).busy);
}

function call(method, params = {}, sessionId) {
  const id = ++serial;
  const promise = new Promise((done, fail) => pending.set(id, { done, fail }));
  socket.send(JSON.stringify({ id, method, params, sessionId }));

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
      `!![...document.querySelectorAll('button:not([inert] *)')].find(b => (b.getAttribute("aria-label") === ${JSON.stringify(text)} || b.textContent.trim() === ${JSON.stringify(text)}) && !b.disabled)`,
    ),
  );
  await evaluate(
    `[...document.querySelectorAll('button:not([inert] *)')].find(b => (b.getAttribute("aria-label") === ${JSON.stringify(text)} || b.textContent.trim() === ${JSON.stringify(text)})).click()`,
  );
}

async function hasDraft(id) {
  await poll(`draft ${id}`, () =>
    evaluate(
      `!![...document.querySelectorAll('textarea:not([inert] *)')].find(t => t.value.includes(${JSON.stringify(id)}) && !t.disabled)`,
    ),
  );
}

async function shot(name) {
  const image = await call("Page.captureScreenshot", { format: "png" });
  await writeFile(join(evidence, `${name}.png`), Buffer.from(image.data, "base64"));
}

async function setName(value) {
  await evaluate(`(() => {
    const input = document.querySelector('#artifact-name');
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(input, ${JSON.stringify(value)});
    input.dispatchEvent(new Event('input', { bubbles: true }));
  })()`);
}

async function verifyArtifactEntry(mode) {
  const selector = 'header button[aria-label="Artifacts"]';
  await poll(`${mode} icon-only entry`, () =>
    evaluate(`(() => {
    const button = document.querySelector(${JSON.stringify(selector)});
    if (!button) return false;
    const rect = button.getBoundingClientRect();
    const neighbor = document.querySelector('header button[aria-label="New task"]');
    const size = neighbor?.getBoundingClientRect();
    return button.textContent.trim() === '' && !!button.querySelector('svg[aria-hidden="true"]') &&
      button.getAttribute('aria-haspopup') === 'dialog' && button.getAttribute('aria-expanded') === 'false' &&
      rect.width === 28 && rect.height === 28 && rect.left >= 0 && rect.right <= innerWidth &&
      (${JSON.stringify(mode)} !== 'desktop' || (rect.width === size?.width && rect.height === size?.height));
  })()`),
  );

  const center = await evaluate(`(() => {
    const rect = document.querySelector(${JSON.stringify(selector)}).getBoundingClientRect();
    return { x: rect.x + rect.width / 2, y: rect.y + rect.height / 2 };
  })()`);

  await call("Input.dispatchMouseEvent", { type: "mouseMoved", ...center });
  await poll(`${mode} tooltip`, () =>
    evaluate(
      `!![...document.querySelectorAll('[data-slot="tooltip-content"]')].find(t => t.textContent.trim() === 'Artifacts' && t.getBoundingClientRect().width > 0)`,
    ),
  );
  await shot(`artifacts-entry-${mode}`);
  await evaluate(`document.querySelector(${JSON.stringify(selector)}).focus()`);
  assert.equal(
    await evaluate(`document.activeElement === document.querySelector(${JSON.stringify(selector)})`),
    true,
  );
  await call("Page.bringToFront");
  await call("Input.dispatchKeyEvent", {
    type: "keyDown",
    key: "Enter",
    code: "Enter",
    text: "\r",
    unmodifiedText: "\r",
    windowsVirtualKeyCode: 13,
  });
  await call("Input.dispatchKeyEvent", {
    type: "keyUp",
    key: "Enter",
    code: "Enter",
    windowsVirtualKeyCode: 13,
  });
  await poll(`${mode} keyboard opens dialog`, () =>
    evaluate(
      `!!document.querySelector('[role="dialog"]') && document.querySelector(${JSON.stringify(selector)}).getAttribute('aria-expanded') === 'true'`,
    ),
  );
  await click("Close");
  await call("Input.dispatchMouseEvent", { type: "mouseMoved", x: 0, y: 0 });
}

async function panelHas(text) {
  await poll("panel " + text, () =>
    evaluate(
      `[...document.querySelectorAll('[role=dialog]')].some(dialog => dialog.innerText.includes(${JSON.stringify(text)}))`,
    ),
  );
}
