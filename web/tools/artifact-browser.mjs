// Real HTTP, native engine, SQLite, and Chrome. Only the model provider is faux.
/* eslint-disable no-await-in-loop */
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { randomUUID } from "node:crypto";
import { createWriteStream } from "node:fs";
import { mkdir, mkdtemp, readFile, unlink, writeFile } from "node:fs/promises";
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
const project = join(root, "project");
const workspace = join(data, "agents", "default", "workspace");
const profile = join(root, "chrome");

await Promise.all([
  mkdir(evidence, { recursive: true }),
  mkdir(home),
  mkdir(project),
  mkdir(workspace, { recursive: true }),
]);

for (const name of ["main.markdown", "chat.md", "task.md", "routine.md", "routine-task.md", "lifecycle.md"]) {
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
  await send(main.id, 'deliver_file {"path":"main.markdown"}');
  const mainArtifact = await artifact(main.id, "main.markdown");
  const chat = await api("/api/agents/default/chats", {}, "POST");
  await api(`/api/agents/default/chats/${chat.id}/project`, { cwd: project }, "PUT");
  await send(chat.id, 'deliver_file {"path":"chat.md"}');
  const chatArtifact = await artifact(chat.id, "chat.md");
  await model("spawn");
  await send(
    main.id,
    JSON.stringify({
      title: "Artifact task",
      message: 'deliver_file {"path":"task.md"}',
      provider: "faux",
      modelId: "tool",
    }),
  );
  const task = await childNamed("Artifact task");
  const taskArtifact = await artifact(task.id, "task.md");
  await model("tool");
  const routine = await makeRoutine("Routine delivery", 'deliver_file {"path":"routine.md"}');
  const run = await api(`/api/agents/default/automations/${routine.id}/run`, {}, "POST");
  const routineArtifact = await artifact(run.chatId, "routine.md");
  await model("spawn");

  const routine2 = await makeRoutine(
    "Routine task",
    JSON.stringify({
      title: "Routine child",
      message: 'deliver_file {"path":"routine-task.md"}',
      provider: "faux",
      modelId: "tool",
    }),
  );

  const run2 = await api(`/api/agents/default/automations/${routine2.id}/run`, {}, "POST");
  const child = await childNamed("Routine child");
  const childArtifact = await artifact(child.id, "routine-task.md");
  assert.equal(childArtifact.displayConversationId, run2.chatId);
  await model("echo");
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
  await visit(`/a/default`);
  await verifyArtifactEntry("desktop");
  await preview("main.markdown", mainArtifact.id);
  await click("Continue editing");
  await hasDraft(mainArtifact.id);
  assert.equal(await evaluate("location.pathname"), "/a/default");
  await shot("main");
  results.push("main .markdown preview and writable draft");
  await visit(`/a/default/c/${chat.id}`);
  await preview("chat.md", chatArtifact.id);
  await click("Continue editing");
  await hasDraft(chatArtifact.id);
  assert.equal(await evaluate("location.pathname"), `/a/default/c/${chat.id}`);
  await shot("chat");
  results.push("project Chat preview and writable draft");
  await visit(`/a/default`);
  await preview("main.markdown", mainArtifact.id);
  await click("Continue editing");
  await hasDraft(mainArtifact.id);
  await openChildPage(task.id);
  await preview("task.md", taskArtifact.id);
  await click("Continue editing");
  await hasDraft(taskArtifact.id);
  assert.equal(await evaluate("location.pathname"), `/a/default/c/${task.id}`);
  assert.equal((await api(`/api/agents/default/chats/${task.id}`)).readOnly, false);
  await shot("task");
  await evaluate(`document.querySelector('aside a[href="/a/default"]').click()`);
  await poll("main restored", async () => (await evaluate("location.pathname")) === "/a/default");
  await hasDraft(mainArtifact.id);
  results.push("a child session continues in its own conversation and preserves the main draft");
  await visit(`/a/default/c/${run.chatId}`);
  await preview("routine.md", routineArtifact.id);
  await click("Continue editing");
  await poll("routine opens writable Chat", async () => {
    const path = await evaluate("location.pathname");

    return path.startsWith("/a/default/c/") && path !== `/a/default/c/${run.chatId}`;
  });
  await hasDraft(routineArtifact.id);
  const routineChat = Number((await evaluate("location.pathname")).split("/").at(-1));
  assert.equal((await api(`/api/agents/default/chats/${routineChat}`)).cwd, project);
  await shot("routine");
  results.push("routine opens writable Chat with project");
  await visit(`/a/default/c/${run2.chatId}`);
  await openChildPage(child.id);
  await preview("routine-task.md", childArtifact.id);
  await click("Continue editing");
  await hasDraft(childArtifact.id);
  const childView = await api(`/api/agents/default/chats/${child.id}`);
  assert.equal(childView.readOnly, false);
  assert.equal(childView.cwd, project);
  await shot("routine-task");
  results.push("a routine child session is a writable Chat with project");

  // Two real pages share a child delivery. SSE updates the source and parent views.
  const secondTarget = await call("Target.createTarget", { url: "about:blank" });

  const { sessionId: second } = await call("Target.attachToTarget", {
    targetId: secondTarget.targetId,
    flatten: true,
  });

  const other = async (expression) => {
    const result = await call(
      "Runtime.evaluate",
      { expression, awaitPromise: true, returnByValue: true },
      second,
    );

    if (result.exceptionDetails) throw new Error(JSON.stringify(result.exceptionDetails));

    return result.result.value;
  };

  await call("Network.enable", {}, second);
  await call("Page.navigate", { url: base + "/a/default" }, second);
  await poll("second page header", () =>
    other(
      "!![...document.querySelectorAll('header button')].find(b => b.getAttribute('aria-label') === 'Artifacts')",
    ),
  );
  await other(
    "[...document.querySelectorAll('header button')].find(b => b.getAttribute('aria-label') === 'Artifacts').click()",
  );
  await poll("second page child delivery", () => other("document.body.innerText.includes('task.md')"));

  await visit(`/a/default/c/${task.id}`);
  assert.equal(await evaluate('document.body.innerText.includes("Delivered files")'), false);
  await preview("task.md", taskArtifact.id);
  await click("Rename");
  await setName("../bad.md");
  await click("Save");
  await poll("invalid rename is shown", () =>
    evaluate('document.body.innerText.includes("Could not rename artifact")'),
  );
  await setName("renamed-task.md");
  await click("Save");
  await poll("source renamed", () =>
    evaluate(
      '!document.body.innerText.includes("Rename artifact") && document.body.innerText.includes("renamed-task.md")',
    ),
  );
  await poll("parent renamed without reload", () =>
    other('document.body.innerText.includes("renamed-task.md")'),
  );
  assert.ok(
    network.some(
      (e) => e.method === "Network.eventSourceMessageReceived" && e.params.eventName === "artifacts",
    ),
    "delivery mutations emit SSE invalidations",
  );
  await shot("artifacts-renamed");
  await click("Delete");
  await shot("artifacts-delete-confirm");
  await click("Cancel");
  assert.ok(
    (await api(`/api/agents/default/chats/${task.id}/artifacts`)).artifacts.some(
      (a) => a.id === taskArtifact.id,
    ),
  );
  await click("Delete");
  await click("Delete artifact");
  await poll("source deletion clears selection", () =>
    evaluate(
      'document.body.innerText.includes("No artifacts yet.") && !document.body.innerText.includes("Continue editing")',
    ),
  );
  await poll("parent deletion without reload", () =>
    other('!document.body.innerText.includes("renamed-task.md")'),
  );
  assert.equal(await readFile(join(workspace, "task.md"), "utf8"), "# Preview task.md\n");
  await visit("/a/default");
  await click("Artifacts");
  await model("tool");
  await send(main.id, 'deliver_file {"path":"lifecycle.md"}');
  const fresh = await artifact(main.id, "lifecycle.md");
  await poll("new delivery in open panel", () =>
    evaluate('document.body.innerText.includes("lifecycle.md")'),
  );
  await poll("new delivery in other page", () => other('document.body.innerText.includes("lifecycle.md")'));
  await click("lifecycle.md");
  await poll("fresh preview", () => evaluate('document.body.innerText.includes("Preview lifecycle.md")'));
  const download = await evaluate('document.querySelector("a[download=\\"lifecycle.md\\"]").href');
  assert.equal(await (await fetch(download)).text(), "# Preview lifecycle.md\n");
  await shot("artifacts-preview");
  await unlink(join(data, "agents", "default", "artifacts", fresh.id));
  await poll("external removal clears list and preview", () =>
    evaluate(`!document.querySelector('[role=dialog]')?.innerText.includes("lifecycle.md")`),
  );
  await poll("external removal reaches other page", () =>
    other(`!document.querySelector('[role=dialog]')?.innerText.includes("lifecycle.md")`),
  );
  assert.equal(await readFile(join(workspace, "lifecycle.md"), "utf8"), "# Preview lifecycle.md\n");
  await click("Close");
  await shot("chat-artifacts-entry");
  await call("Emulation.setDeviceMetricsOverride", {
    width: 320,
    height: 844,
    deviceScaleFactor: 1,
    mobile: true,
  });
  await verifyArtifactEntry("mobile");
  await click("Artifacts");
  await poll("mobile artifact dialog", () =>
    evaluate(`(() => {
    const dialog = document.querySelector('[role=dialog]');
    if (!dialog) return false;
    const rect = dialog.getBoundingClientRect();
    return rect.left >= 0 && rect.right <= innerWidth;
  })()`),
  );
  await shot("artifacts-mobile");
  await click("Close");
  await call("Target.closeTarget", { targetId: secondTarget.targetId });
  results.push("desktop and 320px mobile icon-only entry, tooltip, keyboard access, and dialog state");
  results.push(
    "snapshot rename validation, download, delete confirmation/cancel, source preservation, parent/source SSE, two-page new delivery, and external removal",
  );
  await writeFile(
    join(evidence, "results.json"),
    JSON.stringify(
      { results, mainArtifact, chatArtifact, taskArtifact, routineArtifact, childArtifact, project },
      null,
      2,
    ),
  );
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

async function artifact(id, name) {
  return poll(`artifact ${name}`, async () =>
    (await api(`/api/agents/default/chats/${id}/artifacts`)).artifacts.find((a) => a.name === name),
  );
}

async function childNamed(title) {
  return poll(title, async () =>
    (await api("/api/agents/default/chats")).chats.find(
      (c) => c.parentId !== undefined && c.title === title && !c.busy,
    ),
  );
}

async function makeRoutine(name, prompt) {
  return api("/api/agents/default/automations", {
    name,
    prompt,
    cwd: project,
    schedule: { kind: "daily", time: "09:00" },
    timeZone: "UTC",
    session: "new",
    enabled: false,
  });
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

async function preview(name, id) {
  await click("Artifacts");
  await click(name);
  await poll(`preview ${id}`, () =>
    evaluate(`document.body.innerText.includes(${JSON.stringify(`Preview ${name}`)})`),
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

async function openChildPage(id) {
  await click("View thread");
  await poll("thread session link", () =>
    evaluate("!!document.querySelector('a[aria-label=\"Open as a session page\"]')"),
  );
  await evaluate("document.querySelector('a[aria-label=\"Open as a session page\"]').click()");
  await poll(
    "child session page",
    async () => (await evaluate("location.pathname")) === "/a/default/c/" + id,
  );
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
