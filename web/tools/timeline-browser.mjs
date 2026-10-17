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
import { DatabaseSync } from "node:sqlite";
import { setTimeout as delay } from "node:timers/promises";

const activityReads = [];

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
const aborts = [];
const exceptions = [];
const taskReads = new Map();
const noticeLayouts = [];
let heldAgentSave;
let heldUpload;
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
      // Each visit must close the previous document's SSE connection.
      "--disable-features=BackForwardCache",
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
    } else if (event.method === "Fetch.requestPaused") {
      if (event.params.request.method === "PUT") heldAgentSave = event.params.requestId;
      else if (event.params.request.url.endsWith("/uploads")) heldUpload = event.params.requestId;
      else void call("Fetch.continueRequest", { requestId: event.params.requestId });
    } else if (event.method === "Runtime.exceptionThrown") exceptions.push(event.params.exceptionDetails);
    else if (event.method === "Network.requestWillBeSent") {
      const { request } = event.params;

      if (request.method === "GET" && request.url.startsWith(`${base}/api/`)) activityReads.push(request.url);

      if (request.url.startsWith(`${base}/api/agents/default/tasks/`))
        taskReads.set(request.url, (taskReads.get(request.url) ?? 0) + 1);

      if (request.url.endsWith("/clear"))
        clears.push({ request: JSON.parse(request.postData), requestId: event.params.requestId });

      if (request.url.endsWith("/abort"))
        aborts.push({ url: request.url, requestId: event.params.requestId });
    } else if (event.method === "Network.responseReceived") {
      const clear = clears.find((entry) => entry.requestId === event.params.requestId);

      if (clear) clear.status = event.params.response.status;

      const abort = aborts.find((entry) => entry.requestId === event.params.requestId);

      if (abort) abort.status = event.params.response.status;
    }
  });
  await call("Page.enable");
  await call("Runtime.enable");
  await call("Network.enable");
  await viewport(1280, 900);

  // Given a new installation, the root is the bot's empty timeline and composer.
  await visit("/a/default");
  await ui("empty timeline", "document.body.innerText.includes('What should Dex do?')");
  await check(
    "shell lists the agent and the global settings, with no chat list",
    `(() => {
    const nav = document.querySelector('aside[aria-label="Navigation"]');
    const links = [...nav.querySelectorAll('a')];
    return links.some(a => a.getAttribute('href') === '/a/default' && a.querySelector('span span').textContent === 'Dex' && a.getAttribute('aria-current') === 'page') &&
      nav.innerText.includes('Agents') && nav.innerText.includes('Global settings') &&
      links.map(a => a.getAttribute('href')).slice(1).join(',') === '/providers,/secrets,/about' &&
      !/New chat|Search chats|Timeline|Configure/.test(nav.innerText) &&
      !links.some(a => /^\\/c\\//.test(a.getAttribute('href')));
  })()`,
  );
  await check(
    "empty timeline has a composer and no Clear context",
    "!!document.querySelector('textarea[aria-label=\"Message Dex\"]') && ![...document.querySelectorAll('button:not([inert] *)')].some(b => b.textContent.trim() === 'Clear context')",
  );
  await shot("01-empty");
  await check(
    "main opens Sessions without a New session action",
    "!!document.querySelector('a[href=\"/a/default/sessions\"]') && ![...document.querySelectorAll('button:not([inert] *)')].some(b => b.textContent.trim() === 'New session')",
  );
  const main = await api("/api/agents/default/chats/main");
  await visit("/a/default/sessions");
  await ui("empty Sessions", "document.body.innerText.includes('No sessions yet')");
  await check(
    "empty Sessions hides the filter and activity caption",
    "!document.querySelector('select[aria-label=\"Kind\"]') && !document.body.innerText.includes('active · newest first')",
  );
  await click("New session");
  await ui(
    "new Chat session",
    "location.pathname.startsWith('/a/default/c/') && document.body.innerText.includes('This session has its own context. It does not see the main timeline.')",
  );
  const emptySession = Number((await evaluate("location.pathname")).split("/").at(-1));
  assert.notEqual(emptySession, main.id);
  await check(
    "new Chat has its crumb, title, kind and composer before choosing a model",
    `(() => {
      const header = [...document.querySelectorAll('header')].at(-1);
      return !!header.querySelector('a[href="/a/default/sessions"]') && document.querySelector('#page-title').textContent === 'New session' &&
        header.innerText.includes('Chat') && document.querySelector('nav a[aria-current="page"]').dataset.tab === 'Sessions';
    })()`,
  );
  await visit("/a/default/sessions");
  await ui("abandoned empty Chat stays hidden", "document.body.innerText.includes('No sessions yet')");
  await click("New session");
  await ui("empty Chat reused", `location.pathname === '/a/default/c/${emptySession}'`);
  results.push("New session opens and reuses an empty Chat without showing an empty list row");
  await visit("/a/default");
  await model("echo");
  await reload();
  await ui("composer ready", "!!document.querySelector('textarea:not(:disabled):not([inert] *)')");

  // Given durable input waiting for admission, a reload must keep Stop available.
  await poll("maintenance holds admission", async () => {
    const response = await fetch(`${base}/api/maintenance`, { method: "POST" });

    return response.ok;
  });
  await sendFromComposer("queued before Stop");
  await poll("input is queued", async () => (await api(`/api/agents/default/chats/${main.id}`)).busy);
  await reload();
  await ui("queued input after reload", "document.body.innerText.includes('queued before Stop')");
  await check(
    "queued input keeps Stop after a reload",
    "!!document.querySelector('button[aria-label=\"Stop run\"]:not([inert] *)')",
  );
  await evaluate("document.querySelector('button[aria-label=\"Stop run\"]:not([inert] *)').click()");
  await poll("queued input stopped", async () => !(await api(`/api/agents/default/chats/${main.id}`)).busy);
  await ui(
    "queued input withdrawn",
    "document.body.innerText.includes('Stop requested. Earlier actions are not undone. Processes and remote work may continue.')",
  );
  await api("/api/maintenance", undefined, "DELETE");

  await sendFromComposer("first input");
  await ui("first answer", "document.body.innerText.includes('echo: first input')");
  assert.equal(
    await evaluate("document.body.innerText.includes('echo: queued before Stop')"),
    false,
    "withdrawn input must not run after release",
  );
  await ui(
    "clear visible",
    "!![...document.querySelectorAll('button:not([inert] *)')].find(b => b.textContent.trim() === 'Clear context')",
  );

  // An existing worker conversation waits at the maintenance gate when its stream reconnects.
  await poll("maintenance before reconnect", async () => {
    const response = await fetch(`${base}/api/maintenance`, { method: "POST" });

    return response.ok;
  });
  await sendFromComposer("queued during reconnect");
  await poll("input awaits reconnect", async () => (await api(`/api/agents/default/chats/${main.id}`)).busy);
  await reload();
  await ui("composer awaits stream", "!!document.querySelector('textarea:disabled')");
  await check(
    "Stop stays available while the stream awaits admission",
    "!!document.querySelector('button[aria-label=\"Stop run\"]:not([inert] *)')",
  );
  await evaluate("document.querySelector('button[aria-label=\"Stop run\"]:not([inert] *)').click()");
  await poll(
    "input stopped before reconnect",
    async () => !(await api(`/api/agents/default/chats/${main.id}`)).busy,
  );
  await api("/api/maintenance", undefined, "DELETE");
  await ui("stream restored", "!!document.querySelector('textarea:not(:disabled):not([inert] *)')");
  assert.equal(
    await evaluate("document.body.innerText.includes('echo: queued during reconnect')"),
    false,
    "reconnect must not revive withdrawn input",
  );

  // When a real tool holds the run, only the server refuses Clear context.
  await model("bash");
  const busyGate = join(isolated, "busy.gate");
  await send(`while ! test -f '${busyGate}'; do sleep 0.05; done; echo gate-released`);
  await ui("run is active", "!!document.querySelector('button[aria-label=\"Stop run\"]:not([inert] *)')");
  await check(
    "Clear context remains enabled while busy",
    "![...document.querySelectorAll('button:not([inert] *)')].find(b => b.textContent.trim() === 'Clear context').disabled",
  );
  await click("Clear context");
  await poll("real 409", () => clears.some((entry) => entry.status === 409));
  await ui(
    "409 warn note",
    "document.body.innerText.includes('Context not cleared.') && document.body.innerText.includes('Dex is working. Wait for the reply or stop it, then try again.')",
  );
  await shot("02-refused");

  for (const [label, width, height, theme] of [
    ["desktop-light", 1280, 900, "light"],
    ["desktop-dark", 1280, 900, "dark"],
    ["mobile-light", 390, 844, "light"],
    ["mobile-dark", 390, 844, "dark"],
  ]) {
    await viewport(width, height);
    await call("Emulation.setEmulatedMedia", {
      features: [{ name: "prefers-color-scheme", value: theme }],
    });
    await ui(
      "notice theme applied",
      `document.documentElement.classList.contains('dark') === ${theme === "dark"}`,
    );
    await evaluate("new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))");

    const layout = await evaluate(`(() => {
      const area = document.querySelector('textarea:not([inert] *)');
      const pane = area.closest('section');
      const scroll = pane.querySelector('.overflow-y-auto').getBoundingClientRect();
      const stop = [...pane.querySelectorAll('[role=status]')].find(node => node.textContent.trim().startsWith('Stop requested.')).getBoundingClientRect();
      const warning = [...pane.querySelectorAll('[role=alert]')].find(node => node.textContent.includes('Context not cleared.')).getBoundingClientRect();
      const composer = area.closest('form').getBoundingClientRect();
      return { viewport: innerWidth, pageWidth: document.documentElement.scrollWidth,
        scrollBottom: scroll.bottom, stopTop: stop.top, stopBottom: stop.bottom,
        warningTop: warning.top, warningBottom: warning.bottom, composerTop: composer.top,
        left: Math.min(stop.left, warning.left, composer.left),
        right: Math.max(stop.right, warning.right, composer.right) };
    })()`);

    noticeLayouts.push({ label, ...layout });
    await shot(`02-notices-${label}`);
    assert.ok(
      layout.scrollBottom <= layout.stopTop + 1 &&
        layout.stopBottom <= layout.warningTop + 1 &&
        layout.warningBottom <= layout.composerTop + 1 &&
        layout.left >= 0 &&
        layout.right <= layout.viewport + 1 &&
        layout.pageWidth <= layout.viewport + 1,
      `composer notices stay in layout (${label}): ${JSON.stringify(layout)}`,
    );
    results.push(`composer notices stay in layout (${label})`);
  }

  await viewport(1280, 900);
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
    "!!document.querySelector('[role=separator]') && ![...document.querySelectorAll('button:not([inert] *)')].some(b => b.textContent.trim() === 'Clear context')",
  );
  results.push("server 409 note, stable reset ID, idle clear, divider, hidden Clear after reset");
  await reload();
  await ui(
    "fresh history boundary",
    "document.body.innerText.includes('Show earlier messages') && !document.body.innerText.includes('first input')",
  );
  await click("Show earlier messages");
  await ui("earlier messages loaded", "document.body.innerText.includes('echo: first input')");
  await check(
    "history merges once and does not make cleared context writable again",
    "document.querySelectorAll('[role=separator]').length === 1 && document.body.innerText.split('echo: first input').length === 2 && ![...document.querySelectorAll('button:not([inert] *)')].some(b => b.textContent.trim() === 'Clear context')",
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
    (await sessionTasks()).find((task) => task.status === "done"),
  );

  await ui(
    "result and dispatch links",
    `document.querySelectorAll('a[href="/a/default/tasks/${done.id}"]').length === 2`,
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
    (await sessionTasks()).find((task) => task.status === "running"),
  );

  assert.notEqual(done.id, running.id);
  await ui("active task badge", "!!document.querySelector('[aria-label=\"1 active\"]')");
  await check(
    "main Stop remains available after dispatch reply",
    "!!document.querySelector('button[title=\"Stop this run and its background work\"]:not([inert] *)')",
  );
  await visit("/tasks");
  await ui("tasks list", `!!document.querySelector('a[href="/a/default/tasks/${running.id}"]')`);
  await check(
    "session rows keep newest activity first for same-title tasks",
    `(() => { const links = [...document.querySelectorAll('main a[href^="/a/default/tasks/"]:not([inert] *)')]; return links.length === 2 && links[0].getAttribute('href') === '/a/default/tasks/${running.id}' && links[1].getAttribute('href') === '/a/default/tasks/${done.id}'; })()`,
  );
  await shot("04-tasks");
  await visit(`/a/default/tasks/${running.id}`);
  await ui(
    "live task work",
    "document.body.innerText.includes('No report yet.') && document.body.innerText.includes('Running bash')",
  );
  await check(
    "task work is read-only",
    "!document.querySelector('textarea:not([inert] *)') && ![...document.querySelectorAll('button:not([inert] *)')].some(b => /^(Cancel|Retry|Send)$/.test(b.textContent.trim()))",
  );
  const runningURL = `${base}/api/agents/default/tasks/${running.id}`;
  const beforePoll = taskReads.get(runningURL) ?? 0;
  await poll("running task detail polls", () => (taskReads.get(runningURL) ?? 0) > beforePoll);
  results.push("running task detail keeps polling");
  await shot("05-task-work");
  await visit("/a/default");
  await ui(
    "stop background work",
    "!!document.querySelector('button[aria-label=\"Stop run\"]:not([inert] *):not(:disabled)')",
  );
  await tap('button[aria-label="Stop run"]');
  await poll(
    "background cancellation",
    async () => (await api(`/api/agents/default/tasks/${running.id}`)).status === "cancelled",
  );
  await visit(`/a/default/tasks/${done.id}`);
  await ui("saved report", "document.body.innerText.includes('echo: saved report')");
  const doneURL = `${base}/api/agents/default/tasks/${done.id}`;
  const terminalReads = taskReads.get(doneURL);
  const observedUntil = Date.now() + 6500;
  await poll(
    "terminal task stops polling",
    () => {
      assert.equal(taskReads.get(doneURL), terminalReads, "terminal detail sent another request");

      return Date.now() >= observedUntil;
    },
    10_000,
  );
  results.push("terminal task detail sends no requests over two polling periods");
  await visit(`/a/default/c/${done.conversationId}`);
  await ui(
    "read-only conversation",
    "document.body.innerText.includes('You can read this conversation here.') && document.body.innerText.includes('echo: saved report')",
  );
  await check(
    "read-only chat has no composer",
    "!document.querySelector('textarea:not([inert] *)') && !document.querySelector('button[aria-label=\"Send message\"]:not([inert] *)')",
  );
  await shot("06-read-only");

  // Given an idle main timeline, admit real channel work through control's SQLite queue.
  const channelGate = join(isolated, "channel.gate");
  const channelDB = new DatabaseSync(join(data, "control.sqlite"));
  channelDB.exec("PRAGMA busy_timeout = 5000");

  try {
    const inserted = channelDB
      .prepare(
        "INSERT INTO conversations (agent_id, channel_key, created_at) VALUES ('default', 'telegram:7', ?) RETURNING id",
      )
      .get(Date.now());

    const content = JSON.stringify({
      title: "Channel work",
      goal: `while ! test -f '${channelGate}'; do sleep 0.05; done; echo channel-released`,
      provider: "faux",
      modelId: "bash",
    });

    channelDB
      .prepare(
        "INSERT INTO submissions (id, conversation_id, content, status, created_at, origin) VALUES (?, ?, ?, 'pending', ?, 'telegram:7')",
      )
      .run(randomUUID(), inserted.id, content, Date.now());
  } finally {
    channelDB.close();
  }

  const channelTask = await poll("channel task running", async () =>
    (await sessionTasks()).find((task) => task.status === "running"),
  );

  await visit("/a/default");
  await ui("channel work in bot badge", "!!document.querySelector('[aria-label=\"1 active\"]')");
  await ui("main stream ready", "!!document.querySelector('textarea:not(:disabled):not([inert] *)')");
  await check(
    "channel work counts in Sessions but cannot enable main Stop",
    "!document.querySelector('button[aria-label=\"Stop run\"]:not([inert] *)')",
  );
  await writeFile(channelGate, "release");
  await poll(
    "channel work completed",
    async () => (await api(`/api/agents/default/tasks/${channelTask.id}`)).status === "done",
  );

  // Sessions shares one list for real automation, channel, task and Chat contexts.
  await visit("/a/default/sessions");
  await ui("Sessions kind filter", "!!document.querySelector('select[aria-label=\"Kind\"]')");
  await selectKind("Chat");
  await ui(
    "empty kind filter",
    "document.body.innerText.includes('No sessions of this kind') && document.body.innerText.includes('Choose All kinds to see every session.') && document.body.innerText.includes('0 active · newest first')",
  );
  results.push("an empty kind filter keeps its selector and shows the accepted empty copy");
  await model("echo");

  const automation = await api("/api/agents/default/automations", {
    name: "Daily review",
    prompt: "review the session checks",
    session: "new",
    enabled: true,
    timeZone: "Asia/Shanghai",
    schedule: { kind: "every", minutes: 60 },
  });

  const automationRun = await api(`/api/agents/default/automations/${automation.id}/run`, undefined, "POST");
  await poll(
    "automation session settled",
    async () => !(await api(`/api/agents/default/chats/${automationRun.chatId}`)).busy,
  );
  await model("context");
  await visit(`/a/default/c/${emptySession}`);
  await ui("Chat composer ready", "!!document.querySelector('textarea:not(:disabled):not([inert] *)')");
  await sendFromComposer("Plan a separate session");
  await ui(
    "Chat gets its own model context",
    "document.body.innerText.includes('context: [\"Plan a separate session\"]')",
  );
  await ui(
    "first input names the Chat",
    "document.querySelector('#page-title').textContent === 'Plan a separate session'",
  );
  await check(
    "a Chat session belongs to the agent and its Sessions tab",
    `!!document.querySelector('aside a[href="/a/default"][aria-current="page"]') &&
      document.querySelector('header nav a[aria-current="page"]').dataset.tab === 'Sessions'`,
  );
  await click("Clear context");
  await ui("Chat reset divider", "!!document.querySelector('[role=separator]')");
  await reload();
  await ui("Chat history is available", "document.body.innerText.includes('Show earlier messages')");
  await click("Show earlier messages");
  await ui(
    "Chat history loaded",
    "document.body.innerText.includes('context: [\"Plan a separate session\"]')",
  );
  await sendFromComposer("Fresh Chat context");
  await ui(
    "Chat clear resets only its model context",
    "document.body.innerText.includes('context: [\"Fresh Chat context\"]')",
  );
  await visit("/a/default");
  await ui(
    "main stream ready after Chat clear",
    "!!document.querySelector('textarea:not(:disabled):not([inert] *)')",
  );
  await check(
    "Chat input and reset do not enter main",
    "!document.body.innerText.includes('Plan a separate session') && !document.body.innerText.includes('Fresh Chat context') && document.body.innerText.includes('after clear')",
  );

  await model("spawn");
  await visit(`/a/default/c/${emptySession}`);
  const sessionTaskGate = join(isolated, "session-task.gate");
  await sendFromComposer(
    JSON.stringify({
      title: "Session audit",
      goal: `while ! test -f '${sessionTaskGate}'; do sleep 0.05; done; echo session-report`,
      provider: "faux",
      modelId: "bash",
    }),
  );

  const sessionTask = await poll("Chat background work", async () =>
    (await sessionTasks()).find((task) => task.title === "Session audit" && task.status === "running"),
  );

  assert.equal(sessionTask.parentConversationId, emptySession);
  await poll(
    "Chat dispatch reply settled",
    async () => !(await api(`/api/agents/default/chats/${emptySession}`)).busy,
  );
  await visit("/a/default");
  await ui("Chat task in main badge", "!!document.querySelector('[aria-label=\"1 active\"]')");
  await ui("main composer ready", "!!document.querySelector('textarea:not(:disabled):not([inert] *)')");
  await check(
    "Chat background work cannot enable main Stop",
    "!document.querySelector('button[aria-label=\"Stop run\"]:not([inert] *)')",
  );
  await model("bash");
  await visit(`/a/default/c/${emptySession}`);
  await ui(
    "Chat Stop covers its own background work",
    "!!document.querySelector('button[aria-label=\"Stop run\"]:not([inert] *)')",
  );
  const sessionTurnGate = join(isolated, "session-turn.gate");
  await sendFromComposer(
    `while ! test -f '${sessionTurnGate}'; do sleep 0.05; done; echo session-turn-released`,
  );
  await poll("Chat turn is busy", async () => (await api(`/api/agents/default/chats/${emptySession}`)).busy);

  const allSessions = (await api("/api/agents/default/chats")).chats;
  assert.ok(
    allSessions.every((chat) => !chat.main && !("report" in (chat.task ?? {})) && !("items" in chat)),
  );
  await visit("/tasks");
  await ui(
    "old Tasks address redirects to Sessions",
    "location.pathname === '/a/default/sessions' && !!document.querySelector('select[aria-label=\"Kind\"]')",
  );
  await ui(
    "busy Chat and task count once each",
    "document.body.innerText.includes('2 active · newest first')",
  );

  const targets = allSessions.map((chat) =>
    chat.task ? `/a/default/tasks/${chat.task.id}` : `/a/default/c/${chat.id}`,
  );

  assert.deepEqual(
    await evaluate(
      "[...document.querySelectorAll('main li:not([inert] *) a')].map(a => a.getAttribute('href'))",
    ),
    targets,
    "Sessions keeps the server's activity order",
  );

  for (const [kind, active, count] of [
    ["Chat", 1, 1],
    ["Task", 1, 4],
    ["Automation", 0, 1],
    ["All kinds", 2, allSessions.length],
  ]) {
    await selectKind(kind);
    await ui(
      `filter ${kind}`,
      `document.body.innerText.includes('${active} active · newest first') && document.querySelectorAll('main li:not([inert] *)').length === ${count}`,
    );
    results.push(`${kind} filter counts only its visible active rows`);
  }

  const nextEmpty = await api("/api/agents/default/chats", undefined, "POST");

  for (const [label, width, height, theme] of [
    ["desktop-light", 1280, 900, "light"],
    ["desktop-dark", 1280, 900, "dark"],
    ["mobile-light", 390, 844, "light"],
    ["mobile-dark", 390, 844, "dark"],
  ]) {
    await viewport(width, height);
    await call("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: theme }] });

    for (const [view, path, ready] of [
      ["sessions", "/a/default/sessions", "document.body.innerText.includes('2 active · newest first')"],
      [
        "chat",
        `/a/default/c/${emptySession}`,
        "!!document.querySelector('textarea:not(:disabled):not([inert] *)') && !!document.querySelector('button[aria-label=\"Stop run\"]:not([inert] *)')",
      ],
      [
        "task",
        `/a/default/tasks/${sessionTask.id}`,
        "document.body.innerText.includes('Running bash') && !!document.querySelector('a[href=\"/a/default/sessions\"]')",
      ],
      [
        "new-session",
        `/a/default/c/${nextEmpty.id}`,
        "document.body.innerText.includes('This session has its own context. It does not see the main timeline.')",
      ],
    ]) {
      await visit(path);
      await ui(`${view} ready (${label})`, ready);
      await ui(
        "session theme applied",
        `document.documentElement.classList.contains('dark') === ${theme === "dark"}`,
      );
      await check(
        `${view} fits the viewport (${label})`,
        "document.documentElement.scrollWidth <= innerWidth + 1 && [...document.querySelectorAll('main :is(header a:not(nav a), header nav, header button, textarea, select):not([inert] *)')].every(node => { const r = node.getBoundingClientRect(); return r.left >= 0 && r.right <= innerWidth + 1; })",
      );
      await shot(`10-${view}-${label}`);
    }
  }

  await writeFile(sessionTurnGate, "release");
  await poll(
    "Chat turn finished",
    async () => !(await api(`/api/agents/default/chats/${emptySession}`)).busy,
  );
  await model("spawn");
  await writeFile(sessionTaskGate, "release");
  await poll(
    "Chat report saved",
    async () => (await api(`/api/agents/default/tasks/${sessionTask.id}`)).status === "done",
  );
  await visit(`/a/default/c/${emptySession}`);
  await ui(
    "Chat receives its task result",
    "document.body.innerText.includes('Background result') && document.body.innerText.includes('task-result:') && document.body.innerText.includes('session-report')",
  );
  await visit("/a/default");
  await ui(
    "main snapshot after Chat report",
    "!!document.querySelector('textarea:not(:disabled):not([inert] *)')",
  );
  await check(
    "Chat report does not enter main",
    "!document.body.innerText.includes('Session audit') && !document.body.innerText.includes('session-report')",
  );

  await visit(`/a/default/c/${emptySession}`);
  const stopGate = join(isolated, "session-stop.gate");
  await sendFromComposer(
    JSON.stringify({
      title: "Session Stop check",
      goal: `while ! test -f '${stopGate}'; do sleep 0.05; done`,
      provider: "faux",
      modelId: "bash",
    }),
  );

  const stopTask = await poll("Chat task before Stop", async () =>
    (await sessionTasks()).find((task) => task.title === "Session Stop check" && task.status === "running"),
  );

  await poll(
    "Chat dispatch finished before Stop",
    async () => !(await api(`/api/agents/default/chats/${emptySession}`)).busy,
  );
  await ui(
    "Chat Stop remains after dispatch",
    "!!document.querySelector('button[aria-label=\"Stop run\"]:not([inert] *)')",
  );
  await tap('button[aria-label="Stop run"]');
  await poll(
    "Chat Stop cancels its task",
    async () => (await api(`/api/agents/default/tasks/${stopTask.id}`)).status === "cancelled",
  );
  results.push(
    "Chat context, title, Clear, history, task return, Stop and main Stop scope use the selected session",
  );

  // Channel inputs use the real main queue. The Go tests cover each platform boundary.
  await model("echo");
  const entries = ["telegram:7", "wechat:owner", "feishu:owner"];
  const entryDB = new DatabaseSync(join(data, "control.sqlite"));
  entryDB.exec("PRAGMA busy_timeout = 5000");

  try {
    const insert = entryDB.prepare(
      "INSERT INTO submissions (id, conversation_id, content, origin, status, created_at) VALUES (?, ?, ?, ?, 'pending', ?)",
    );

    for (const origin of entries)
      insert.run(randomUUID(), main.id, `Input from ${origin}`, origin, Date.now());
  } finally {
    entryDB.close();
  }

  for (const [label, width, height, theme] of [
    ["desktop-light", 1280, 900, "light"],
    ["desktop-dark", 1280, 900, "dark"],
    ["mobile-light", 390, 844, "light"],
    ["mobile-dark", 390, 844, "dark"],
  ]) {
    await viewport(width, height);
    await call("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: theme }] });
    await visit("/a/default");
    await ui("channel answers in main", "document.body.innerText.includes('echo: Input from feishu:owner')");
    await check(
      `each channel input has its entry mark (${label})`,
      `[...document.querySelectorAll('[data-origin]')].filter(node => ['telegram:7', 'wechat:owner', 'feishu:owner'].includes(node.dataset.origin)).map(node => node.textContent.trim()).join(',') === 'Telegram,WeChat,Feishu'`,
    );
    await ui(
      "channel theme applied",
      `document.documentElement.classList.contains('dark') === ${theme === "dark"}`,
    );
    await evaluate(
      "Promise.all(document.getAnimations().filter(a => a.effect.getTiming().iterations !== Infinity).map(a => a.finished))",
    );
    await evaluate(`(() => {
      const scroll = document.querySelector('textarea:not([inert] *)').closest('section').querySelector('.overflow-y-auto');
      scroll.scrollTo({ top: scroll.scrollHeight, behavior: 'instant' });
    })()`);
    await check(
      `channel timeline fits (${label})`,
      `(() => {
        const scroll = document.querySelector('textarea:not([inert] *)').closest('section').querySelector('.overflow-y-auto').getBoundingClientRect();
        return document.documentElement.scrollWidth <= innerWidth + 1 &&
          [...document.querySelectorAll('[data-origin]')].every(node => {
            const rect = node.getBoundingClientRect();
            return rect.top >= scroll.top && rect.bottom <= scroll.bottom;
          });
      })()`,
    );
    await shot(`11-channel-main-${label}`);
    await visit("/a/default/sessions");
    await ui("Sessions after channel input", "!!document.querySelector('select[aria-label=\"Kind\"]')");
    await check(
      `Sessions has only Chat, Task and Automation (${label})`,
      `[...document.querySelector('select[aria-label="Kind"]').options].map(option => option.textContent).join(',') === 'All kinds,Chat,Task,Automation' && ![...document.querySelectorAll('main li:not([inert] *)')].some(node => /Telegram|WeChat|Feishu/.test(node.textContent))`,
    );
    await shot(`12-channel-sessions-${label}`);
  }

  // Everything of the agent is a tab of its page; the sidebar keeps one row for the agent.
  const tabs = "Chat,Instructions,Channels,Automations,Sessions,Extensions,Settings";
  const tab = (label) => `nav a[data-tab="${label}"]`;
  // The channel widths above leave a phone viewport behind; all tabs fit on a desktop one.
  await viewport(1280, 900);
  await visit("/a/default");
  await check(
    "the agent header has one tab for each part of the agent",
    `[...document.querySelectorAll('header nav a')].map(a => a.dataset.tab).join(',') === '${tabs}' &&
      document.querySelector('header nav a[aria-current="page"]').dataset.tab === 'Chat'`,
  );

  const automationsOn = (await api("/api/agents/default/automations")).automations.filter(
    (a) => a.enabled,
  ).length;

  await ui(
    "the Automations tab counts the automations that are on, and the agent row shows its status",
    `document.querySelector('${tab("Automations")}').textContent === 'Automations${automationsOn || ""}' &&
      /^(\\d+ active|\\d+ channels? on|Idle)$/.test(document.querySelector('aside[aria-label="Navigation"] a[href="/a/default"] span span:last-child').textContent)`,
  );
  await evaluate("document.querySelector('textarea[aria-label^=\"Message\"]').dataset.probe = 'kept'");
  await evaluate("document.querySelector('textarea[aria-label^=\"Message\"]').focus()");
  await call("Input.insertText", { text: "unsent words" });

  // Each tab opens its page, and the agent row in the sidebar stays current.
  for (const [label, path, ready] of [
    [
      "Channels",
      "/a/default/channels",
      "document.querySelector('#page-title')?.textContent === 'Channels' && document.body.innerText.includes('All channels')",
    ],
    [
      "Automations",
      "/a/default/automations",
      "document.querySelector('#page-title')?.textContent === 'Automations' && document.body.innerText.includes('All automations')",
    ],
    ["Sessions", "/a/default/sessions", "!!document.querySelector('select[aria-label=\"Kind\"]')"],
    [
      "Instructions",
      "/a/default/instructions",
      "!!document.querySelector('textarea[aria-label=\"Agent instructions\"]')",
    ],
    ["Settings", "/a/default/agent", "!!document.querySelector('#bot-name')"],
  ]) {
    await tap(tab(label));
    await ui(`the ${label} tab opens ${path}`, `location.pathname === '${path}' && (${ready})`);
    await check(
      `the ${label} tab is current, and so is the agent row`,
      `document.querySelector('header nav a[aria-current="page"]').dataset.tab === '${label}' &&
        document.querySelector('aside[aria-label="Navigation"] a[href="/a/default"]').getAttribute('aria-current') === 'page'`,
    );
  }

  await check(
    "settings hold only the profile",
    "[...document.querySelectorAll('h2')].map(h => h.textContent).join(',') === 'Profile'",
  );
  await shot("07-agent-settings");
  await tap(tab("Chat"));
  await ui(
    "the main timeline kept its composer and the unsent message",
    `location.pathname === '/a/default' && document.querySelector('textarea[aria-label^="Message"]').dataset.probe === 'kept' &&
      document.querySelector('textarea[aria-label^="Message"]').value === 'unsent words'`,
  );
  await evaluate(
    "(() => { const t = document.querySelector('textarea[aria-label^=\"Message\"]'); t.select(); document.execCommand('delete'); })()",
  );
  await ui("composer cleared", "document.querySelector('textarea[aria-label^=\"Message\"]').value === ''");
  await visit("/providers");
  await check(
    "a global settings page is not part of the agent",
    "!document.querySelector('header nav a[data-tab]') && document.querySelector('aside[aria-label=\"Navigation\"] a[href=\"/a/default\"]').getAttribute('aria-current') === null",
  );

  // The Settings tab renames the agent; the header, the sidebar and the composer follow.
  await visit("/a/default/agent");
  await ui("agent settings", "!!document.querySelector('#bot-name')");
  await evaluate("document.querySelector('#bot-name').focus(); document.querySelector('#bot-name').select()");
  await call("Input.insertText", { text: "  Browser Bot  " });
  await call("Input.dispatchKeyEvent", { type: "keyDown", key: "Enter", code: "Enter" });
  await poll("name saved", async () => (await api("/api/agents/default")).name === "Browser Bot");
  await ui(
    "the header and the sidebar follow the new name",
    `!!document.querySelector('header nav[aria-label="Browser Bot sections"]') &&
      document.querySelector('aside[aria-label="Navigation"] a[href="/a/default"]').textContent.includes('Browser Bot')`,
  );
  await check(
    "channel settings have no links to old peer contexts",
    "![...document.querySelectorAll('a')].some(a => a.textContent.trim() === 'Open conversation')",
  );
  await visit("/settings");
  await ui("old settings address redirects", "location.pathname === '/a/default/agent'");

  await visit("/a/default");
  await ui("renamed composer", "!!document.querySelector('textarea[aria-label=\"Message Browser Bot\"]')");

  // The header of every tab carries the model and the thinking level.
  const chosen = (await api("/api/agents/default")).modelId;
  await check(
    "the agent header shows the model",
    `document.querySelector('header button[aria-label="Change model"]').innerText.includes(${JSON.stringify(chosen)})`,
  );
  await model("think");
  await visit("/a/default/channels");
  await ui(
    "a reasoning model adds the thinking level to the header of every tab",
    `document.querySelector('header [aria-label="Change thinking level"]')?.innerText.trim().length > 0`,
  );
  await model(chosen);
  await visit("/a/default");
  await ui(
    "a model without reasoning leaves the thinking level out",
    `!document.querySelector('header [aria-label="Change thinking level"]')`,
  );

  // The Instructions tab holds the draft; the tab and the sidebar mark it until it is saved.
  await tap(tab("Instructions"));
  await ui("instructions tab", "!!document.querySelector('textarea[aria-label=\"Agent instructions\"]')");
  await evaluate("document.querySelector('textarea[aria-label=\"Agent instructions\"]').focus()");
  await call("Input.insertText", { text: "Answer in the language I write in." });
  await ui(
    "the draft marks the tab, the name and the sidebar",
    `!!document.querySelector('${tab("Instructions")} [aria-label="Unsaved draft"]') &&
      [...document.querySelector('header').querySelectorAll('span')].some(n => n.textContent === 'Instructions draft') &&
      document.querySelector('aside[aria-label="Navigation"]').innerText.includes('Draft')`,
  );
  await shot("07a-instructions");
  await tap(tab("Chat"));
  await tap(tab("Instructions"));
  await ui(
    "the draft survives a tab change",
    "document.querySelector('textarea[aria-label=\"Agent instructions\"]').value === 'Answer in the language I write in.'",
  );
  await tap('button[aria-label="Save instructions"]');
  await poll(
    "instructions saved",
    async () => (await api("/api/agents/default")).instructions === "Answer in the language I write in.",
  );
  await ui(
    "saving clears the draft marks",
    `!document.querySelector('${tab("Instructions")} [aria-label="Unsaved draft"]') &&
      !document.querySelector('aside[aria-label="Navigation"]').innerText.includes('Draft')`,
  );
  await evaluate(
    "(() => { const t = document.querySelector('textarea[aria-label=\"Agent instructions\"]'); t.focus(); t.select(); document.execCommand('delete'); })()",
  );
  await tap('button[aria-label="Save instructions"]');
  await poll("instructions restored", async () => (await api("/api/agents/default")).instructions === "");

  // A phone keeps the tabs in one row that scrolls to the current tab.
  await viewport(390, 844);
  await visit("/a/default/agent");
  await ui(
    "the current tab is in view on a phone",
    `(() => {
      const nav = document.querySelector('header nav').getBoundingClientRect();
      const current = document.querySelector('header nav a[aria-current="page"]').getBoundingClientRect();
      return document.querySelector('header nav').scrollWidth > nav.width && current.left >= nav.left - 1 && current.right <= nav.right + 1 &&
        document.documentElement.scrollWidth <= innerWidth + 1;
    })()`,
  );
  await visit("/a/default");
  await shot("08-mobile");
  await evaluate("document.querySelector('button[aria-label=\"Open navigation\"]').click()");
  await ui("mobile agent row", "!!document.querySelector('[role=dialog] a[href=\"/a/default\"]')");
  await shot("09-mobile-nav");
  await tap('[role="dialog"] a[href="/providers"]');
  await ui("mobile global settings opened", "location.pathname === '/providers'");
  await evaluate("document.querySelector('button[aria-label=\"Open navigation\"]').click()");
  await ui("mobile drawer reopened", "!!document.querySelector('[role=dialog]')");
  await tap('[role="dialog"] button[aria-label="Close navigation"]');
  await ui("mobile drawer closed", "!document.querySelector('[role=dialog]')");
  results.push("mobile drawer links and close button accept pointer clicks");
  // Given two agents, UI navigation keeps their messages and unsaved work separate.
  await viewport(1280, 900);
  await model("echo");
  await visit("/a/default");
  const visibleComposer = 'textarea[aria-label^="Message"]:not([inert] *)';
  const visibleInstructions = 'textarea[aria-label="Agent instructions"]:not([inert] *)';
  const agentTab = (name) => `nav a[data-tab="${name}"]:not([inert] *)`;
  await ui("default composer ready", `!!document.querySelector('${visibleComposer}:not(:disabled)')`);
  await evaluate(`document.querySelector('${visibleComposer}').focus()`);
  await call("Input.insertText", { text: "default unsent text" });
  await evaluate(`(() => {
    const input = document.querySelector('input[type=file]:not([inert] *)');
    const transfer = new DataTransfer();
    transfer.items.add(new File(['default attachment'], 'default-draft.txt', {type: 'text/plain'}));
    input.files = transfer.files;
    input.dispatchEvent(new Event('change', {bubbles: true}));
  })()`);
  await ui(
    "default draft upload ready",
    "document.body.innerText.includes('default-draft.txt') && !document.body.innerText.includes('Uploading')",
  );
  await tap(agentTab("Instructions"));
  await ui("default instructions ready", `!!document.querySelector('${visibleInstructions}')`);
  await evaluate(`document.querySelector('${visibleInstructions}').focus()`);
  await call("Input.insertText", { text: "Default instructions draft" });
  await tap('aside button[aria-label="Create agent"]');
  await ui("create agent dialog", "!!document.querySelector('form#create-agent input')");
  await evaluate("document.querySelector('form#create-agent input').focus()");
  await call("Input.insertText", { text: "  Second Bot  " });
  await tap("form#create-agent button");
  await ui("create dialog model picker", "!!document.querySelector('[cmdk-input]')");
  await evaluate("document.querySelector('[cmdk-input]').focus()");
  await call("Input.insertText", { text: "echo" });
  await tap('[cmdk-item][data-value="faux/echo"]');
  await ui("create picker closed", "!document.querySelector('[cmdk-input]')");
  await tap('button[form="create-agent"]');

  const second = await poll("second agent created", async () =>
    (await api("/api/agents")).agents.find((agent) => agent.name === "Second Bot"),
  );

  const secondPath = `/a/${second.id}`;
  const secondLink = `aside a[href="${secondPath}"]`;
  const defaultLink = 'aside a[href="/a/default"]';
  await ui(
    "created agent opens empty timeline",
    `location.pathname === '${secondPath}' && document.body.innerText.includes('What should Second Bot do?')`,
  );
  await sendFromComposer("only the second agent sees this");
  await ui(
    "second agent echo reply",
    "document.body.innerText.includes('echo: only the second agent sees this')",
  );
  await evaluate(`document.querySelector('${visibleComposer}').focus()`);
  await call("Input.insertText", { text: "second unsent text" });
  await tap(agentTab("Instructions"));
  await ui("second instructions ready", `!!document.querySelector('${visibleInstructions}')`);
  await check(
    "new agent does not inherit the instructions draft",
    `document.querySelector('${visibleInstructions}').value === ''`,
  );
  await evaluate(`document.querySelector('${visibleInstructions}').focus()`);
  await call("Input.insertText", { text: "Second instructions draft" });
  await tap(defaultLink);
  await ui(
    "default composer restored",
    `document.querySelector('${visibleComposer}')?.value === 'default unsent text'`,
  );
  await check(
    "default timeline excludes second agent messages and keeps its upload",
    "!document.body.innerText.includes('echo: only the second agent sees this') && document.body.innerText.includes('default-draft.txt')",
  );
  await tap(agentTab("Instructions"));
  await ui(
    "default instructions draft restored",
    `document.querySelector('${visibleInstructions}')?.value === 'Default instructions draft'`,
  );
  await call("Fetch.enable", {
    patterns: [{ urlPattern: `${base}/api/agents/default`, requestStage: "Response" }],
  });
  await tap('button[aria-label="Save instructions"]:not([inert] *)');
  await poll("default save response held", async () => heldAgentSave);
  await tap(secondLink);
  await ui(
    "second composer restored",
    `document.querySelector('${visibleComposer}')?.value === 'second unsent text'`,
  );
  await check(
    "second composer excludes default attachments",
    "!document.body.innerText.includes('default-draft.txt')",
  );
  await tap(agentTab("Instructions"));
  await ui(
    "second instructions draft restored",
    `document.querySelector('${visibleInstructions}')?.value === 'Second instructions draft'`,
  );
  await call("Fetch.continueRequest", { requestId: heldAgentSave });
  await call("Fetch.disable");
  await poll(
    "default instructions saved after switch",
    async () => (await api("/api/agents/default")).instructions === "Default instructions draft",
  );
  await ui(
    "late save preserves second draft",
    `document.querySelector('${visibleInstructions}')?.value === 'Second instructions draft' && !document.querySelector('${defaultLink}').textContent.includes('Draft') && document.querySelector('${secondLink}').textContent.includes('Draft')`,
  );
  await tap(defaultLink);
  await ui("default composer before upload", `!!document.querySelector('${visibleComposer}')`);
  await call("Fetch.enable", {
    patterns: [{ urlPattern: `${base}/api/agents/default/uploads`, requestStage: "Response" }],
  });
  await evaluate(`(() => {
    const input = document.querySelector('input[type=file]:not([inert] *)');
    const transfer = new DataTransfer();
    transfer.items.add(new File(['late attachment'], 'late-draft.txt', {type: 'text/plain'}));
    input.files = transfer.files;
    input.dispatchEvent(new Event('change', {bubbles: true}));
  })()`);
  await poll("upload response held", async () => heldUpload);
  await tap(secondLink);
  await ui(
    "second composer while default uploads",
    `document.querySelector('${visibleComposer}')?.value === 'second unsent text'`,
  );
  await call("Fetch.continueRequest", { requestId: heldUpload });
  await call("Fetch.disable");
  await check(
    "late upload stays out of second agent draft",
    "!document.body.innerText.includes('late-draft.txt')",
  );
  await tap(defaultLink);
  await ui(
    "late upload restores to its own agent",
    "document.body.innerText.includes('late-draft.txt') && !document.body.innerText.includes('Uploading')",
  );
  await tap(secondLink);
  await ui(
    "second draft after late upload",
    `document.querySelector('${visibleComposer}')?.value === 'second unsent text'`,
  );
  await tap(agentTab("Channels"));
  await ui("second agent channel controls", "document.body.innerText.includes('All channels')");
  await check(
    "second agent channel links",
    `['telegram', 'wechat', 'feishu'].every(name => !!document.querySelector('main a[href="/a/${second.id}/channels/' + name + '"]:not([inert] *)'))`,
  );
  await shot("13-multi-agent-desktop");
  await viewport(390, 844);
  await check("second agent channels fit phone", "document.documentElement.scrollWidth <= innerWidth + 1");
  await shot("13-channels-phone");
  await viewport(1440, 1000);

  for (const root of [home, data, join(data, "agents", second.id)]) {
    await mkdir(join(root, ".agents"), { recursive: true });
    await writeFile(join(root, ".agents", ".env"), "EXTENSION_DEMO_KEY=never-show-this-value\n");
  }

  await tap(agentTab("Extensions"));
  await ui(
    "second agent extensions",
    `document.body.innerText.includes('/agents/${second.id}/.agents/skills') && !!document.querySelector('main button:not([inert] *)')`,
  );
  await check(
    "extensions belong to the selected agent",
    `!document.querySelector('aside a[href="/extensions"]') && document.body.innerText.includes('restarts worker for all agents')`,
  );
  await ui("extension env levels", "document.body.innerText.includes('EXTENSION_DEMO_KEY')");
  await check(
    "extension levels and secrecy",
    `['home', 'dex', 'agent', 'Shadowed', 'The model can', 'Tool output becomes part of the transcript', 'MCP servers on disk', 'MCP servers now'].every(text => document.body.innerText.includes(text)) && !document.body.innerText.includes('never-show-this-value')`,
  );
  await shot("13a-agent-extensions-desktop");
  await viewport(390, 844);
  await check("extensions fit phone", "document.documentElement.scrollWidth <= innerWidth + 1");
  await shot("13b-agent-extensions-phone");
  await evaluate(
    "Array.from(document.querySelectorAll('h2')).find(node => node.textContent === 'Environment keys on disk').scrollIntoView({block: 'center', behavior: 'instant'})",
  );
  await shot("13c-extension-env-phone");
  await viewport(1440, 1000);
  await shot("13d-extension-env-desktop");
  await tap('aside a[href="/secrets"]');
  await ui(
    "secrets uses current agent",
    `!!document.querySelector('main a[href="/a/${second.id}/extensions"]') && !document.body.innerText.includes('Not yet available')`,
  );
  await tap(`main a[href="/a/${second.id}/extensions"]`);
  await ui(
    "returned to current agent extensions",
    `location.pathname === '/a/${second.id}/extensions' && !!document.querySelector('nav a[data-tab="Extensions"][aria-current="page"]')`,
  );
  await viewport(390, 844);
  await tap(agentTab("Chat"));
  await ui(
    "second timeline restored on phone",
    `location.pathname === '${secondPath}' && document.querySelector('${visibleComposer}')?.value === 'second unsent text' && document.body.innerText.includes('echo: only the second agent sees this')`,
  );
  await ui("second timeline fits phone", "document.documentElement.scrollWidth <= innerWidth + 1");
  await shot("14-multi-agent-phone");
  await tap('button[aria-label="Open navigation"]:not([inert] *)');
  await ui(
    "phone lists both agents",
    `!!document.querySelector('[role="dialog"] a[href="${secondPath}"]') && !!document.querySelector('[role="dialog"] a[href="/a/default"]')`,
  );
  await shot("15-multi-agent-phone-nav");
  await tap('[role="dialog"] button[aria-label="Close navigation"]');
  await visit("/sessions");
  await ui("legacy sessions redirect", "location.pathname === '/a/default/sessions'");
  await visit("/a/nope");
  await ui(
    "unknown agent page",
    `document.body.innerText.includes('Agent not found') && !!document.querySelector('main a[href="/"]')`,
  );
  results.push(
    "create second agent, echo isolation, separate composer/instructions/upload drafts, channels boundary, legacy redirect and unknown agent",
  );

  // Seven client-side agent switches must leave a connection for ordinary requests.
  await viewport(1280, 900);
  const connectionAgents = [];

  for (let index = 0; index < 7; index++) {
    connectionAgents.push(
      await api("/api/agents", {
        name: `Connection ${index + 1}`,
        provider: "faux",
        modelId: "echo",
        thinkingLevel: "off",
        instructions: "",
      }),
    );
  }

  await visit("/a/default");

  for (const agent of connectionAgents) {
    await ui("agent appears in sidebar", `!!document.querySelector('aside a[href="/a/${agent.id}"]')`);
    await tap(`aside a[href="/a/${agent.id}"]`);
    await ui(
      `Chat works after switching to ${agent.name}`,
      `!!document.querySelector('textarea[aria-label="Message ${agent.name}"]:not(:disabled)')`,
    );
  }

  await tap('nav a[data-tab="Sessions"]');
  await ui(
    "Sessions loads after seven agent switches",
    "document.body.innerText.includes('No sessions yet')",
  );
  results.push("seven agent Chat visits leave ordinary Sessions requests working");
  await shot("16-seven-agents");

  await visit("/providers");
  await ui(
    "global provider page ready",
    "document.querySelector('#page-title')?.textContent === 'Providers'",
  );
  const activityStart = activityReads.length;
  await poll(
    "two shared agent activity polls",
    async () => activityReads.slice(activityStart).filter((url) => url === `${base}/api/agents`).length >= 2,
  );
  const activityWindow = activityReads.slice(activityStart);
  assert.deepEqual(
    activityWindow.filter((url) => /\/api\/agents\/[^/]+\/(channels|automations|chats)(?:[/?]|$)/.test(url)),
    [],
    "sidebar activity polls only the shared agent list",
  );
  results.push("two agents share one activity list poll with no per-agent channel, automation or chat reads");
  assert.deepEqual(exceptions, [], "no browser exceptions");
  assert.equal((await api("/api/agents/default/chats/main")).id, main.id);
  results.push(
    "same-title task IDs, active task badge, list order, saved report, live read-only work, idle Stop",
  );
  results.push(
    "agent tabs, kept composer, instructions draft, trimmed name, phone tabs and drawer, stable main ID",
  );
  await writeFile(
    join(artifacts, "browser-results.json"),
    JSON.stringify(
      {
        base,
        data,
        profile,
        results,
        clears,
        aborts,
        exceptions,
        taskReads: Object.fromEntries(taskReads),
        noticeLayouts,
        activityWindow,
      },
      null,
      2,
    ),
  );
  console.log(`PASS: ${results.length} timeline browser checks. Evidence: ${artifacts}`);
} catch (error) {
  await writeFile(
    join(artifacts, "browser-network.json"),
    JSON.stringify({ clears, aborts, exceptions }, null, 2),
  );

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
  const previous = await evaluate("performance.timeOrigin");

  await call("Page.navigate", { url: base + path });
  await ui(
    "page ready",
    `performance.timeOrigin !== ${previous} && document.readyState === 'complete' && !!document.querySelector('main')`,
  );
}

async function reload() {
  const previous = await evaluate("performance.timeOrigin");
  await call("Page.reload");
  await ui(
    "reload complete",
    `performance.timeOrigin !== ${previous} && document.readyState === 'complete' && !!document.querySelector('main')`,
  );
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
  const agent = await api("/api/agents/default");
  await api("/api/agents/default", { ...agent, provider: "faux", modelId }, "PUT");
}

async function sessionTasks() {
  return (await api("/api/agents/default/chats")).chats.flatMap((chat) => (chat.task ? [chat.task] : []));
}

async function selectKind(kind) {
  await evaluate(
    `(() => { const select = document.querySelector('select[aria-label="Kind"]'); select.value = ${JSON.stringify(kind)}; select.dispatchEvent(new Event('change', { bubbles: true })); })()`,
  );
}

async function send(text) {
  const { id } = await api("/api/agents/default/chats/main");
  await api(`/api/agents/default/chats/${id}/messages`, { id: randomUUID(), text });
}

async function sendFromComposer(text) {
  await ui("composer enabled", "!!document.querySelector('textarea:not(:disabled):not([inert] *)')");
  await evaluate("document.querySelector('textarea:not([inert] *)').focus()");
  await call("Input.insertText", { text });
  await call("Input.dispatchKeyEvent", { type: "keyDown", key: "Enter", code: "Enter" });
  await call("Input.dispatchKeyEvent", { type: "keyUp", key: "Enter", code: "Enter" });
}

async function click(text) {
  await evaluate(
    `(() => { const button = [...document.querySelectorAll('button:not([inert] *)')].find(b => b.textContent.trim() === ${JSON.stringify(text)}); if (!button || button.disabled) throw new Error('Button is unavailable'); button.click(); })()`,
  );
}

async function tap(selector) {
  await evaluate(
    `document.querySelector(${JSON.stringify(selector)}).scrollIntoView({ block: 'nearest', inline: 'nearest', behavior: 'instant' })`,
  );

  const point = await evaluate(`(() => {
    const rect = document.querySelector(${JSON.stringify(selector)}).getBoundingClientRect();
    return { x: rect.x + rect.width / 2, y: rect.y + rect.height / 2 };
  })()`);

  await call("Input.dispatchMouseEvent", { type: "mousePressed", ...point, button: "left", clickCount: 1 });
  await call("Input.dispatchMouseEvent", { type: "mouseReleased", ...point, button: "left", clickCount: 1 });
}

async function stop(child) {
  if (!child || child.exitCode !== null) return;
  child.kill("SIGTERM");
  await Promise.race([
    new Promise((accept) => child.once("exit", accept)),
    delay(12_000).then(() => child.kill("SIGKILL")),
  ]);
}
