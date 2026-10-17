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
    } else if (event.method === "Runtime.exceptionThrown") exceptions.push(event.params.exceptionDetails);
    else if (event.method === "Network.requestWillBeSent") {
      const { request } = event.params;

      if (request.url.startsWith(`${base}/api/tasks/`))
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
  await check(
    "main opens Sessions without a New session action",
    "!!document.querySelector('a[href=\"/sessions\"]') && ![...document.querySelectorAll('button')].some(b => b.textContent.trim() === 'New session')",
  );
  const main = await api("/api/chats/main");
  await visit("/sessions");
  await ui("empty Sessions", "document.body.innerText.includes('No sessions yet')");
  await check(
    "empty Sessions hides the filter and activity caption",
    "!document.querySelector('select[aria-label=\"Kind\"]') && !document.body.innerText.includes('active · newest first')",
  );
  await click("New session");
  await ui(
    "new Chat session",
    "location.pathname.startsWith('/c/') && document.body.innerText.includes('This session has its own context. It does not see the main timeline.')",
  );
  const emptySession = Number((await evaluate("location.pathname")).split("/").at(-1));
  assert.notEqual(emptySession, main.id);
  await check(
    "new Chat has its crumb, title, kind and composer before choosing a model",
    "!!document.querySelector('header a[href=\"/sessions\"]') && document.querySelector('#page-title').textContent === 'New session' && document.querySelector('header').innerText.includes('Chat') && !!document.querySelector('textarea')",
  );
  await visit("/sessions");
  await ui("abandoned empty Chat stays hidden", "document.body.innerText.includes('No sessions yet')");
  await click("New session");
  await ui("empty Chat reused", `location.pathname === '/c/${emptySession}'`);
  results.push("New session opens and reuses an empty Chat without showing an empty list row");
  await visit("/");
  await model("echo");
  await reload();
  await ui("composer ready", "!!document.querySelector('textarea:not(:disabled)')");

  // Given durable input waiting for admission, a reload must keep Stop available.
  await poll("maintenance holds admission", async () => {
    const response = await fetch(`${base}/api/maintenance`, { method: "POST" });

    return response.ok;
  });
  await sendFromComposer("queued before Stop");
  await poll("input is queued", async () => (await api(`/api/chats/${main.id}`)).busy);
  await reload();
  await ui("queued input after reload", "document.body.innerText.includes('queued before Stop')");
  await check(
    "queued input keeps Stop after a reload",
    "!!document.querySelector('button[aria-label=\"Stop run\"]')",
  );
  await evaluate("document.querySelector('button[aria-label=\"Stop run\"]').click()");
  await poll("queued input stopped", async () => !(await api(`/api/chats/${main.id}`)).busy);
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
    "!![...document.querySelectorAll('button')].find(b => b.textContent.trim() === 'Clear context')",
  );

  // An existing worker conversation waits at the maintenance gate when its stream reconnects.
  await poll("maintenance before reconnect", async () => {
    const response = await fetch(`${base}/api/maintenance`, { method: "POST" });

    return response.ok;
  });
  await sendFromComposer("queued during reconnect");
  await poll("input awaits reconnect", async () => (await api(`/api/chats/${main.id}`)).busy);
  await reload();
  await ui("composer awaits stream", "!!document.querySelector('textarea:disabled')");
  await check(
    "Stop stays available while the stream awaits admission",
    "!!document.querySelector('button[aria-label=\"Stop run\"]')",
  );
  await evaluate("document.querySelector('button[aria-label=\"Stop run\"]').click()");
  await poll("input stopped before reconnect", async () => !(await api(`/api/chats/${main.id}`)).busy);
  await api("/api/maintenance", undefined, "DELETE");
  await ui("stream restored", "!!document.querySelector('textarea:not(:disabled)')");
  assert.equal(
    await evaluate("document.body.innerText.includes('echo: queued during reconnect')"),
    false,
    "reconnect must not revive withdrawn input",
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
      const area = document.querySelector('textarea');
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
    "!!document.querySelector('[role=separator]') && ![...document.querySelectorAll('button')].some(b => b.textContent.trim() === 'Clear context')",
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
    (await sessionTasks()).find((task) => task.status === "done"),
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
    (await sessionTasks()).find((task) => task.status === "running"),
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
    "session rows keep newest activity first for same-title tasks",
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
  const runningURL = `${base}/api/tasks/${running.id}`;
  const beforePoll = taskReads.get(runningURL) ?? 0;
  await poll("running task detail polls", () => (taskReads.get(runningURL) ?? 0) > beforePoll);
  results.push("running task detail keeps polling");
  await shot("05-task-work");
  await visit("/");
  await ui(
    "stop background work",
    "!!document.querySelector('button[aria-label=\"Stop run\"]:not(:disabled)')",
  );
  await tap('button[aria-label="Stop run"]');
  await poll(
    "background cancellation",
    async () => (await api(`/api/tasks/${running.id}`)).status === "cancelled",
  );
  await visit(`/tasks/${done.id}`);
  await ui("saved report", "document.body.innerText.includes('echo: saved report')");
  const doneURL = `${base}/api/tasks/${done.id}`;
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

  await visit("/");
  await ui("channel work in bot badge", "!!document.querySelector('[aria-label=\"1 active\"]')");
  await ui("main stream ready", "!!document.querySelector('textarea:not(:disabled)')");
  await check(
    "channel work counts in Sessions but cannot enable main Stop",
    "!document.querySelector('button[aria-label=\"Stop run\"]')",
  );
  await writeFile(channelGate, "release");
  await poll(
    "channel work completed",
    async () => (await api(`/api/tasks/${channelTask.id}`)).status === "done",
  );

  // Sessions shares one list for real automation, channel, task and Chat contexts.
  await visit("/sessions");
  await ui("Sessions kind filter", "!!document.querySelector('select[aria-label=\"Kind\"]')");
  await selectKind("Chat");
  await ui(
    "empty kind filter",
    "document.body.innerText.includes('No sessions of this kind') && document.body.innerText.includes('Choose All kinds to see every session.') && document.body.innerText.includes('0 active · newest first')",
  );
  results.push("an empty kind filter keeps its selector and shows the accepted empty copy");
  await model("echo");

  const automation = await api("/api/automations", {
    name: "Daily review",
    prompt: "review the session checks",
    session: "new",
    enabled: true,
    timeZone: "Asia/Shanghai",
    schedule: { kind: "every", minutes: 60 },
  });

  const automationRun = await api(`/api/automations/${automation.id}/run`, undefined, "POST");
  await poll(
    "automation session settled",
    async () => !(await api(`/api/chats/${automationRun.chatId}`)).busy,
  );
  await model("context");
  await visit(`/c/${emptySession}`);
  await ui("Chat composer ready", "!!document.querySelector('textarea:not(:disabled)')");
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
    "Chat does not highlight main in the sidebar",
    '!document.querySelector(\'aside a[href="/"][aria-current="page"]\')',
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
  await visit("/");
  await ui("main stream ready after Chat clear", "!!document.querySelector('textarea:not(:disabled)')");
  await check(
    "Chat input and reset do not enter main",
    "!document.body.innerText.includes('Plan a separate session') && !document.body.innerText.includes('Fresh Chat context') && document.body.innerText.includes('after clear')",
  );

  await model("spawn");
  await visit(`/c/${emptySession}`);
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
  await poll("Chat dispatch reply settled", async () => !(await api(`/api/chats/${emptySession}`)).busy);
  await visit("/");
  await ui("Chat task in main badge", "!!document.querySelector('[aria-label=\"1 active\"]')");
  await ui("main composer ready", "!!document.querySelector('textarea:not(:disabled)')");
  await check(
    "Chat background work cannot enable main Stop",
    "!document.querySelector('button[aria-label=\"Stop run\"]')",
  );
  await model("bash");
  await visit(`/c/${emptySession}`);
  await ui(
    "Chat Stop covers its own background work",
    "!!document.querySelector('button[aria-label=\"Stop run\"]')",
  );
  const sessionTurnGate = join(isolated, "session-turn.gate");
  await sendFromComposer(
    `while ! test -f '${sessionTurnGate}'; do sleep 0.05; done; echo session-turn-released`,
  );
  await poll("Chat turn is busy", async () => (await api(`/api/chats/${emptySession}`)).busy);

  const allSessions = (await api("/api/chats")).chats;
  assert.ok(
    allSessions.every((chat) => !chat.main && !("report" in (chat.task ?? {})) && !("items" in chat)),
  );
  await visit("/tasks");
  await ui(
    "old Tasks address redirects to Sessions",
    "location.pathname === '/sessions' && !!document.querySelector('select[aria-label=\"Kind\"]')",
  );
  await ui(
    "busy Chat and task count once each",
    "document.body.innerText.includes('2 active · newest first')",
  );
  const targets = allSessions.map((chat) => (chat.task ? `/tasks/${chat.task.id}` : `/c/${chat.id}`));
  assert.deepEqual(
    await evaluate("[...document.querySelectorAll('main li a')].map(a => a.getAttribute('href'))"),
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
      `document.body.innerText.includes('${active} active · newest first') && document.querySelectorAll('main li').length === ${count}`,
    );
    results.push(`${kind} filter counts only its visible active rows`);
  }

  const nextEmpty = await api("/api/chats", undefined, "POST");

  for (const [label, width, height, theme] of [
    ["desktop-light", 1280, 900, "light"],
    ["desktop-dark", 1280, 900, "dark"],
    ["mobile-light", 390, 844, "light"],
    ["mobile-dark", 390, 844, "dark"],
  ]) {
    await viewport(width, height);
    await call("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: theme }] });

    for (const [view, path, ready] of [
      ["sessions", "/sessions", "document.body.innerText.includes('2 active · newest first')"],
      [
        "chat",
        `/c/${emptySession}`,
        "!!document.querySelector('textarea:not(:disabled)') && !!document.querySelector('button[aria-label=\"Stop run\"]')",
      ],
      [
        "task",
        `/tasks/${sessionTask.id}`,
        "document.body.innerText.includes('Running bash') && !!document.querySelector('a[href=\"/sessions\"]')",
      ],
      [
        "new-session",
        `/c/${nextEmpty.id}`,
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
        "document.documentElement.scrollWidth <= innerWidth + 1 && [...document.querySelectorAll('main header a, main header button, main textarea, main select')].every(node => { const r = node.getBoundingClientRect(); return r.left >= 0 && r.right <= innerWidth + 1; })",
      );
      await shot(`10-${view}-${label}`);
    }
  }

  await writeFile(sessionTurnGate, "release");
  await poll("Chat turn finished", async () => !(await api(`/api/chats/${emptySession}`)).busy);
  await model("spawn");
  await writeFile(sessionTaskGate, "release");
  await poll("Chat report saved", async () => (await api(`/api/tasks/${sessionTask.id}`)).status === "done");
  await visit(`/c/${emptySession}`);
  await ui(
    "Chat receives its task result",
    "document.body.innerText.includes('Background result') && document.body.innerText.includes('task-result:') && document.body.innerText.includes('session-report')",
  );
  await visit("/");
  await ui("main snapshot after Chat report", "!!document.querySelector('textarea:not(:disabled)')");
  await check(
    "Chat report does not enter main",
    "!document.body.innerText.includes('Session audit') && !document.body.innerText.includes('session-report')",
  );

  await visit(`/c/${emptySession}`);
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
    async () => !(await api(`/api/chats/${emptySession}`)).busy,
  );
  await ui("Chat Stop remains after dispatch", "!!document.querySelector('button[aria-label=\"Stop run\"]')");
  await tap('button[aria-label="Stop run"]');
  await poll(
    "Chat Stop cancels its task",
    async () => (await api(`/api/tasks/${stopTask.id}`)).status === "cancelled",
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
    await visit("/");
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
      const scroll = document.querySelector('textarea').closest('section').querySelector('.overflow-y-auto');
      scroll.scrollTo({ top: scroll.scrollHeight, behavior: 'instant' });
    })()`);
    await check(
      `channel timeline fits (${label})`,
      `(() => {
        const scroll = document.querySelector('textarea').closest('section').querySelector('.overflow-y-auto').getBoundingClientRect();
        return document.documentElement.scrollWidth <= innerWidth + 1 &&
          [...document.querySelectorAll('[data-origin]')].every(node => {
            const rect = node.getBoundingClientRect();
            return rect.top >= scroll.top && rect.bottom <= scroll.bottom;
          });
      })()`,
    );
    await shot(`11-channel-main-${label}`);
    await visit("/sessions");
    await ui("Sessions after channel input", "!!document.querySelector('select[aria-label=\"Kind\"]')");
    await check(
      `Sessions has only Chat, Task and Automation (${label})`,
      `[...document.querySelector('select[aria-label="Kind"]').options].map(option => option.textContent).join(',') === 'All kinds,Chat,Task,Automation' && ![...document.querySelectorAll('main li')].some(node => /Telegram|WeChat|Feishu/.test(node.textContent))`,
    );
    await shot(`12-channel-sessions-${label}`);
  }

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
  await check(
    "channel settings have no links to old peer contexts",
    "![...document.querySelectorAll('a')].some(a => a.textContent.trim() === 'Open conversation')",
  );
  await shot("07-bot-settings");

  for (const route of ["/channels", "/automations"]) {
    await visit(route);
    await ui("list redirect", "location.pathname === '/agent'");
  }

  await visit("/");
  await ui("renamed composer", "!!document.querySelector('textarea[aria-label=\"Message Browser Bot\"]')");

  // The channel widths above leave a phone viewport behind; the panel needs a desktop one.
  await viewport(1280, 900);

  // The header of the main timeline carries the model, the thinking level and the instructions.
  const chosen = (await api("/api/settings")).agent.modelId;
  await check(
    "the timeline header shows the model and the instructions entry",
    `document.querySelector('header button[aria-label="Change model"]').innerText.includes(${JSON.stringify(chosen)}) &&
      !!document.querySelector('header [aria-label="Edit instructions"]')`,
  );
  await model("think");
  await visit("/");
  await ui(
    "a reasoning model adds the thinking level to the header",
    `document.querySelector('header [aria-label="Change thinking level"]')?.innerText.trim().length > 0`,
  );
  await model(chosen);
  await visit("/");
  await ui(
    "a model without reasoning leaves the thinking level out",
    `!document.querySelector('header [aria-label="Change thinking level"]')`,
  );
  await tap('header [aria-label="Edit instructions"]');
  await ui(
    "instructions panel opens beside the transcript",
    `!!document.querySelector('aside[aria-label="Agent instructions"] textarea') &&
      document.querySelector('header [aria-label="Edit instructions"]').getAttribute('aria-expanded') === 'true' &&
      !!document.querySelector('textarea[aria-label="Message Browser Bot"]')`,
  );
  await evaluate("document.querySelector('aside[aria-label=\"Agent instructions\"] textarea').focus()");
  await call("Input.insertText", { text: "Answer in the language I write in." });
  await ui(
    "the draft marks the header and the sidebar",
    `document.querySelector('header [aria-label="Edit instructions"]').innerText.includes('Draft') &&
      document.querySelector('nav, aside[aria-label="Navigation"]').innerText.includes('Draft')`,
  );
  await shot("07a-instructions-panel");
  await tap('aside[aria-label="Agent instructions"] button[aria-label="Close instructions"]');
  await ui(
    "instructions panel closes",
    "!document.querySelector('aside[aria-label=\"Agent instructions\"]')",
  );
  await tap('header [aria-label="Edit instructions"]');
  await ui(
    "closing the panel keeps the draft",
    `document.querySelector('aside[aria-label="Agent instructions"] textarea').value === 'Answer in the language I write in.'`,
  );

  // One draft, two surfaces: the settings page shows what the panel holds. The draft lives in
  // app state, so it follows a route change; a reload starts from the saved instructions.
  await tap('aside[aria-label="Agent instructions"] a[href="/agent"]');
  await ui("the panel link opens the settings page", "location.pathname === '/agent'");
  await ui(
    "the settings page shows the same draft",
    `document.querySelector('textarea[aria-label="Agent instructions"]').value === 'Answer in the language I write in.' &&
      document.body.innerText.includes('Unsaved draft')`,
  );
  await tap('button[aria-label="Discard draft"]');
  await ui(
    "discard returns the saved instructions",
    "document.querySelector('textarea[aria-label=\"Agent instructions\"]').value === ''",
  );

  // The dashboard opens between the header and the transcript, and keeps its state per device.
  await visit("/");
  const toggle = 'header button[aria-controls], header button[aria-label$="dashboard"]';
  await ui(
    "dashboard starts closed",
    `!document.querySelector('#agent-dashboard') && !!document.querySelector(${JSON.stringify(toggle)})`,
  );
  await tap(toggle);
  await ui(
    "dashboard opens above the transcript with five tiles",
    `[...document.querySelectorAll('#agent-dashboard section')].map(n => n.getAttribute('aria-label')).join(',') === 'Model,Name,Channels,Automations,Instructions' &&
      document.querySelector(${JSON.stringify(toggle)}).getAttribute('aria-expanded') === 'true' &&
      !!document.querySelector('textarea[aria-label="Message Browser Bot"]')`,
  );
  await check(
    "dashboard costs at most 140px of the transcript",
    "document.querySelector('#agent-dashboard').getBoundingClientRect().height <= 140",
  );
  await check(
    "channel tiles link to their settings instead of switching them",
    `[...document.querySelectorAll('#agent-dashboard section[aria-label="Channels"] a')].map(a => a.getAttribute('href')).join(',') === '/channels/telegram,/channels/wechat,/channels/feishu,/agent' &&
      !document.querySelector('#agent-dashboard section[aria-label="Channels"] [role=switch]')`,
  );
  await shot("07b-dashboard");

  const switchSelector = '#agent-dashboard section[aria-label="Automations"] [role=switch]';

  const before = await evaluate(
    `document.querySelector(${JSON.stringify(switchSelector)})?.getAttribute('aria-label') ?? ''`,
  );

  if (before) {
    const name = before.replace(/^Enable /, "");
    const was = (await api("/api/automations")).automations.find((a) => a.name === name).enabled;
    await tap(switchSelector);
    await poll(
      "automation switch saves",
      async () => (await api("/api/automations")).automations.find((a) => a.name === name).enabled === !was,
    );
    await ui(
      "switch follows the saved state",
      `document.querySelector(${JSON.stringify(switchSelector)}).getAttribute('aria-checked') === '${!was}'`,
    );
    await tap(switchSelector);
    await poll(
      "automation switch restores",
      async () => (await api("/api/automations")).automations.find((a) => a.name === name).enabled === was,
    );
  }

  await evaluate(
    "document.querySelector('#agent-dashboard input[aria-label=\"Bot name\"]').focus(); document.querySelector('#agent-dashboard input[aria-label=\"Bot name\"]').select()",
  );
  await call("Input.insertText", { text: "Tile Bot" });
  await call("Input.dispatchKeyEvent", { type: "keyDown", key: "Enter", code: "Enter" });
  await poll("tile rename saves", async () => (await api("/api/settings")).agent.name === "Tile Bot");
  await ui(
    "the header follows the new name",
    "document.querySelector('#page-title').textContent === 'Tile Bot'",
  );
  await evaluate(
    "document.querySelector('#agent-dashboard input[aria-label=\"Bot name\"]').focus(); document.querySelector('#agent-dashboard input[aria-label=\"Bot name\"]').select()",
  );
  await call("Input.insertText", { text: "Browser Bot" });
  await call("Input.dispatchKeyEvent", { type: "keyDown", key: "Enter", code: "Enter" });
  await poll("tile rename restores", async () => (await api("/api/settings")).agent.name === "Browser Bot");

  await tap('#agent-dashboard section[aria-label="Instructions"] button');
  await ui(
    "the instructions tile opens the side panel",
    "!!document.querySelector('aside[aria-label=\"Agent instructions\"] textarea')",
  );
  await visit("/");
  await ui("an open dashboard stays open after a reload", "!!document.querySelector('#agent-dashboard')");
  await tap(toggle);
  await ui("dashboard closes", "!document.querySelector('#agent-dashboard')");
  await visit("/");
  await ui(
    "a closed dashboard stays closed after a reload",
    `!document.querySelector('#agent-dashboard') && !!document.querySelector(${JSON.stringify(toggle)})`,
  );

  await visit("/");
  await viewport(390, 844);
  await shot("08-mobile");
  await evaluate("document.querySelector('button[aria-label=\"Open navigation\"]').click()");
  await ui(
    "mobile bot settings row",
    "!!document.querySelector('[role=dialog] a[aria-label=\"Browser Bot settings\"]')",
  );
  await shot("09-mobile-nav");
  await tap('[role="dialog"] a[aria-label="Browser Bot settings"]');
  await ui(
    "mobile settings opened",
    "location.pathname === '/agent' && !!document.querySelector('#bot-name')",
  );
  await visit("/");
  await ui(
    "the narrow header sends instructions to the settings page",
    `document.querySelector('header [aria-label="Edit instructions"]').getAttribute('href') === '/agent'`,
  );
  // A phone has no room above the transcript, so the dashboard opens as a sheet each time.
  await ui(
    "a phone does not reopen the saved dashboard inline",
    "!document.querySelector('#agent-dashboard')",
  );
  await tap('header button[aria-label$="dashboard"]');
  await ui(
    "the phone dashboard is a sheet with the same tiles",
    "document.querySelector('#agent-dashboard')?.getAttribute('role') === 'dialog' && document.querySelectorAll('#agent-dashboard section').length === 5",
  );
  await ui(
    "the sheet finishes opening",
    `document.getAnimations().every(a => a.playState !== 'running') && !!document.querySelector('#agent-dashboard section[aria-label="Automations"] [role=switch]')`,
  );
  await shot("09a-mobile-dashboard");
  await tap('#agent-dashboard button[aria-label="Close dashboard"]');
  await ui("the phone dashboard closes", "!document.querySelector('#agent-dashboard')");
  await evaluate("document.querySelector('button[aria-label=\"Open navigation\"]').click()");
  await ui("mobile drawer reopened", "!!document.querySelector('[role=dialog]')");
  await tap('[role="dialog"] button[aria-label="Close navigation"]');
  await ui("mobile drawer closed", "!document.querySelector('[role=dialog]')");
  results.push("mobile settings gear and close button both accept pointer clicks");
  assert.deepEqual(exceptions, [], "no browser exceptions");
  assert.equal((await api("/api/chats/main")).id, main.id);
  results.push(
    "same-title task IDs, active task badge, list order, saved report, live read-only work, idle Stop",
  );
  results.push("bot settings, trimmed name, redirects, mobile header and drawer, stable main ID");
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
  const { agent } = await api("/api/settings");
  await api("/api/settings/agent", { ...agent, provider: "faux", modelId }, "PUT");
}

async function sessionTasks() {
  return (await api("/api/chats")).chats.flatMap((chat) => (chat.task ? [chat.task] : []));
}

async function selectKind(kind) {
  await evaluate(
    `(() => { const select = document.querySelector('select[aria-label="Kind"]'); select.value = ${JSON.stringify(kind)}; select.dispatchEvent(new Event('change', { bubbles: true })); })()`,
  );
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

async function tap(selector) {
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
