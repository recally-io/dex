// Real native engine, business services, HTTP, and Chrome. The provider is the app's faux boundary.
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

// In-page expressions for the "View thread" buttons of the result lines and the thread panel.
const viewThreadButtons =
  "[...document.querySelectorAll('h3 button:not([inert] *)')].filter(b => b.textContent.trim() === 'View thread')";

const threadPanel = "document.querySelector('aside[aria-label=\"Session thread\"]:not([inert] *)')";

const clears = [];
const aborts = [];
const messages = [];
const exceptions = [];
const turnReads = new Map();
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

      if (request.url.startsWith(`${base}/api/agents/default/turns/`))
        turnReads.set(request.url, (turnReads.get(request.url) ?? 0) + 1);

      if (request.url.endsWith("/messages"))
        messages.push({ request: JSON.parse(request.postData), requestId: event.params.requestId });

      if (request.url.endsWith("/clear"))
        clears.push({ request: JSON.parse(request.postData), requestId: event.params.requestId });

      if (request.url.endsWith("/abort"))
        aborts.push({ url: request.url, requestId: event.params.requestId });
    } else if (event.method === "Network.responseReceived") {
      const messageResponse = messages.find((entry) => entry.requestId === event.params.requestId);

      if (messageResponse) messageResponse.status = event.params.response.status;

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
    "shell lists only the bots and the global settings",
    `(() => {
    const nav = document.querySelector('aside[aria-label="Sidebar"]');
    return !!nav.querySelector('nav[aria-label="Bots"] a[href="/a/default"][aria-current="page"]') &&
      !!nav.querySelector('a[href="/providers"]') && !nav.querySelector('a[href="/about"]') &&
      !nav.querySelector('button[aria-label^="More"]') && !nav.querySelector('a[data-tab="Main"]') &&
      !!nav.querySelector('button[aria-label="New bot"]') &&
      !nav.querySelector('a[href="/work"], a[href="/needs-you"], a[href="/automations"], input');
  })()`,
  );
  await check(
    "empty timeline has a composer",
    "!!document.querySelector('textarea[aria-label=\"Message Dex\"]')",
  );
  await shot("01-empty");
  await check(
    "main has bot navigation and no task settings",
    "!!document.querySelector('header nav[aria-label=\"Bot navigation\"]') && !document.querySelector('aside button[aria-label^=\"Bot menu\"]') && !document.querySelector('header button[aria-label=\"Task settings\"]') && !document.querySelector('[role=region][aria-label=\"Task settings\"]')",
  );
  const main = await api("/api/agents/default/chats/main");
  await visit("/a/default/sessions");
  await ui("empty Sessions", "document.body.innerText.includes('No sessions yet')");
  await check(
    "empty Sessions hides the filter and activity caption",
    "!document.querySelector('select[aria-label=\"Kind\"]') && !document.body.innerText.includes('active · newest first')",
  );
  await clickLabel("New task");
  await ui(
    "new task project picker",
    `!!document.querySelector('[role="dialog"] [aria-label="Directory path"]')`,
  );
  await tap('[role="dialog"] button[data-slot="dialog-close"]');
  await ui(
    "new Chat session",
    "location.pathname.startsWith('/a/default/c/') && document.body.innerText.includes('This session has its own context. It does not see the main timeline.')",
  );
  const emptySession = Number((await evaluate("location.pathname")).split("/").at(-1));
  assert.notEqual(emptySession, main.id);
  await check(
    "new Chat has its title and composer before choosing a model",
    `document.querySelector('#page-title').textContent === 'New task' &&
      !!document.querySelector('textarea:not([inert] *)') && !document.querySelector('aside[aria-label="Sidebar"] a[data-tab][aria-current="page"]')`,
  );
  await visit("/a/default/sessions");
  await ui("abandoned empty Chat stays hidden", "document.body.innerText.includes('No sessions yet')");
  await clickLabel("New task");
  await ui(
    "reused task project picker",
    `!!document.querySelector('[role="dialog"] [aria-label="Directory path"]')`,
  );
  await tap('[role="dialog"] button[data-slot="dialog-close"]');
  await ui("empty Chat reused", `location.pathname === '/a/default/c/${emptySession}'`);
  results.push("New session opens and reuses an empty Chat without showing an empty list row");
  await visit("/a/default");
  await model("echo");
  await reload();
  await ui("composer ready", "!!document.querySelector('textarea:not(:disabled):not([inert] *)')");
  await typeDraft("/");
  await ui(
    "empty context greys out /clear and /compact",
    `${commandOption("clear")}?.getAttribute('aria-disabled') === 'true' && ${commandOption("clear")}.textContent.includes('already empty') && ${commandOption("compact")}?.getAttribute('aria-disabled') === 'true'`,
  );
  await typeDraft("");

  // Maintenance refuses input before admission. Release must not create a ghost turn.
  await holdMaintenance();
  await sendFromComposer("refused during maintenance");
  await poll("maintenance refuses with HTTP 409", () =>
    messages.some((entry) => entry.request.text === "refused during maintenance" && entry.status === 409),
  );
  await ui(
    "maintenance refusal visible",
    "document.body.innerText.includes('Could not complete this action')",
  );
  assert.equal((await api(`/api/agents/default/chats/${main.id}`)).busy, false);
  await shot("01a-maintenance-refused");
  await api("/api/maintenance", undefined, "DELETE");
  await reload();
  await ui(
    "composer ready after release",
    "!!document.querySelector('textarea:not(:disabled):not([inert] *)')",
  );
  await check(
    "maintenance refusal leaves no ghost input after release and reload",
    "!document.body.innerText.includes('refused during maintenance') && !document.querySelector('button[aria-label=\"Stop run\"]:not([inert] *)')",
  );
  await sendFromComposer("first input");
  await ui("first answer", "document.body.innerText.includes('echo: first input')");
  await typeDraft("/");
  await ui(
    "command menu offers /clear and /compact",
    `${commandOption("clear")}?.getAttribute('aria-disabled') === 'false' && ${commandOption("compact")}?.getAttribute('aria-disabled') === 'false'`,
  );
  await shot("01a-command-menu");
  // An unknown command shows a hint and is never sent.
  await command("/nope");
  await ui("unknown command hint", "document.body.innerText.includes('Unknown command /nope.')");
  assert.ok(!messages.some((entry) => entry.request.text === "/nope"), "an unknown command is not sent");
  await typeDraft("");

  // Reload during maintenance closes the stream. Release permits a fresh native subscription.
  await holdMaintenance();
  await reload();
  await ui("composer awaits stream", "!!document.querySelector('textarea:disabled')");
  await check(
    "maintenance does not invent active work while the stream reconnects",
    "!document.querySelector('button[aria-label=\"Stop run\"]:not([inert] *)')",
  );
  await shot("01b-maintenance-reconnect");
  await api("/api/maintenance", undefined, "DELETE");
  await ui("stream restored", "!!document.querySelector('textarea:not(:disabled):not([inert] *)')");
  await sendFromComposer("after maintenance reconnect");
  await ui(
    "reconnected stream answer",
    "document.body.innerText.includes('echo: after maintenance reconnect')",
  );
  await check(
    "reconnected stream keeps history without the refused input",
    "document.body.innerText.includes('echo: first input') && !document.body.innerText.includes('refused during maintenance')",
  );
  await shot("01c-maintenance-restored");

  // Stop a real tool run and keep its notice for the layout checks below.
  await model("bash");
  const maintenanceStopGate = join(isolated, "stop.gate");
  await send(`while ! test -f '${maintenanceStopGate}'; do sleep 0.05; done; echo stopped-tool`);
  await ui(
    "tool can be stopped",
    "!!document.querySelector('button[aria-label=\"Stop run\"]:not([inert] *)')",
  );
  await evaluate("document.querySelector('button[aria-label=\"Stop run\"]:not([inert] *)').click()");
  await poll("real tool stopped", async () => !(await api(`/api/agents/default/chats/${main.id}`)).busy);
  await writeFile(maintenanceStopGate, "release");
  await ui(
    "Stop notice visible",
    "document.body.innerText.includes('Stop requested. Earlier actions are not undone. Processes and remote work may continue.')",
  );

  // When a real tool holds the run, only the server refuses /clear.
  await model("bash");
  const busyGate = join(isolated, "busy.gate");
  await send(`while ! test -f '${busyGate}'; do sleep 0.05; done; echo gate-released`);
  await ui("run is active", "!!document.querySelector('button[aria-label=\"Stop run\"]:not([inert] *)')");
  await typeDraft("/");
  await ui(
    "/clear remains available while busy",
    `${commandOption("clear")}?.getAttribute('aria-disabled') === 'false'`,
  );
  await command("/clear");
  await poll("real 409", () => clears.some((entry) => entry.status === 409));
  // The refused command stays in the box; Escape closes its menu.
  await key("Escape");
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
  await key("Enter");
  await poll("second refusal", () => clears.filter((entry) => entry.status === 409).length === 2);
  assert.equal(clears[0].request.id, clears[1].request.id, "transport retry keeps its reset ID");
  await writeFile(busyGate, "release");
  await ui(
    "reply completed and note hidden",
    "document.body.innerText.includes('bash: gate-released') && !document.body.innerText.includes('Context not cleared.')",
  );
  // The streamed answer can arrive before the business receipt settles.
  await poll(
    "reply settled before Clear",
    async () => !(await api(`/api/agents/default/chats/${main.id}`)).busy,
  );
  await key("Enter");
  await poll("accepted clear", () => clears.some((entry) => entry.status === 202));
  assert.equal(clears[0].request.id, clears.at(-1).request.id, "accepted retry keeps the refused ID");
  await ui(
    "reset divider and empty box",
    "!!document.querySelector('[role=separator]') && document.querySelector('textarea:not([inert] *)').value === ''",
  );
  results.push("server 409 note, stable reset ID, idle /clear, divider, empty box after reset");
  await reload();
  // The history button is in view above a short context, so the earlier page loads by itself.
  await ui("earlier messages load in view", "document.body.innerText.includes('echo: first input')");
  await check(
    "history merges once",
    "document.querySelectorAll('[role=separator]').length === 1 && document.body.innerText.split('echo: first input').length === 2",
  );
  await typeDraft("/");
  await ui(
    "loaded history does not make the cleared context clearable again",
    `${commandOption("clear")}?.getAttribute('aria-disabled') === 'true'`,
  );
  await typeDraft("");
  await model("context");
  await sendFromComposer("after clear");
  await ui(
    "old context stays out of the model",
    "document.body.innerText.includes('context: [\"after clear\"]')",
  );
  await shot("03-earlier");

  // While a real tool runs, a draft turns Stop into a split button and Enter steers the run.
  await model("bash");
  const steerGate = join(isolated, "steer.gate");
  await send(`while ! test -f '${steerGate}'; do sleep 0.05; done; echo steer-gate`);
  await ui(
    "steer run is active",
    "!!document.querySelector('button[aria-label=\"Stop run\"]:not([inert] *)')",
  );
  await evaluate("document.querySelector('textarea:not([inert] *)').focus()");
  await call("Input.insertText", { text: "echo steered-in" });
  await ui(
    "draft shows the steer split button",
    "!!document.querySelector('button[aria-label=\"Steer this run\"]:not([inert] *)') && !!document.querySelector('[aria-label=\"Choose when to send\"]:not([inert] *)') && !document.querySelector('button[aria-label=\"Stop run\"]:not([inert] *)')",
  );
  await shot("03b-steer-draft");
  await call("Input.dispatchKeyEvent", { type: "keyDown", key: "Enter", code: "Enter" });
  await call("Input.dispatchKeyEvent", { type: "keyUp", key: "Enter", code: "Enter" });
  await ui(
    "steer item queued",
    "document.body.innerText.includes('Steer · joins after the current tool step')",
  );
  await writeFile(steerGate, "release");
  await ui(
    "steered run answers once",
    "document.body.innerText.includes('bash: steered-in') && !document.body.innerText.includes('bash: steer-gate')",
  );
  results.push("Enter steers a running chat and the run answers once");

  // A held tool keeps the first reply busy while the user queues and removes messages.
  await poll(
    "steer settled before queue checks",
    async () => !(await api(`/api/agents/default/chats/${main.id}`)).busy,
  );
  const queueGate = join(isolated, "queue.gate");
  const followGate = join(isolated, "follow.gate");
  const firstCommand = `while ! test -f '${queueGate}'; do sleep 0.05; done; echo queue-first`;
  const followCommand = `while ! test -f '${followGate}'; do sleep 0.05; done; echo queue-follow`;
  const removedCommand = "echo queue-removed";
  await sendFromComposer(firstCommand);
  await ui("queue run active", "!!document.querySelector('button[aria-label=\"Stop run\"]:not([inert] *)')");
  await typeDraft(followCommand);
  await tap('[aria-label="Choose when to send"]');
  await ui(
    "queue mode offered",
    "!![...document.querySelectorAll('[role=menuitemradio]')].find(e => e.innerText.includes('Queue'))",
  );
  await evaluate(
    "[...document.querySelectorAll('[role=menuitemradio]')].find(e => e.innerText.includes('Queue')).click()",
  );
  await key("Enter");
  await ui(
    "follow-up queued",
    "document.body.innerText.includes('Queued · runs after the current reply') && document.body.innerText.includes('echo queue-follow')",
  );

  const followMessage = await poll("follow-up accepted", () =>
    messages.find((entry) => entry.request.text === followCommand && entry.status === 202),
  );

  assert.equal(followMessage.request.mode, "followUp");
  await check(
    "follow-up waits while the first tool runs",
    "!document.body.innerText.includes('bash: queue-first') && !document.body.innerText.includes('bash: queue-follow')",
  );

  await sendFromComposer(removedCommand);

  const removedMessage = await poll("removable message accepted", () =>
    messages.find((entry) => entry.request.text === removedCommand && entry.status === 202),
  );

  assert.equal(removedMessage.request.mode, "followUp");
  await ui(
    "queued message has Remove",
    `(() => { const item = [...document.querySelectorAll('main div')].find(e => e.textContent === ${JSON.stringify(removedCommand)}); return !!item?.parentElement.querySelector('button') && item.parentElement.innerText.includes('Queued · runs after the current reply'); })()`,
  );
  await evaluate(
    `(() => { const item = [...document.querySelectorAll('main div')].find(e => e.textContent === ${JSON.stringify(removedCommand)}); [...item.parentElement.querySelectorAll('button')].find(b => b.textContent.trim() === 'Remove').click(); })()`,
  );
  await ui(
    "removed message leaves queue",
    `!document.body.innerText.includes(${JSON.stringify(removedCommand)})`,
  );

  const withdrawn = await api(
    `/api/agents/default/chats/${main.id}/messages/${removedMessage.request.id}/withdraw`,
    undefined,
    "POST",
  );

  assert.equal(withdrawn.text, removedCommand, "repeated withdrawal returns the removed message");

  // Reject is an API-only mode: the composer offers Steer and Queue, not Reject.
  const rejectedText = "echo queue-rejected";

  const rejected = await fetch(`${base}/api/agents/default/chats/${main.id}/messages`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ id: randomUUID(), text: rejectedText, mode: "reject" }),
  });

  const rejection = await rejected.json();
  assert.equal(rejected.status, 409, JSON.stringify(rejection));
  assert.match(JSON.stringify(rejection), /busy|working/i);
  await check(
    "rejected input does not appear in the queue",
    `!document.body.innerText.includes(${JSON.stringify(rejectedText)})`,
  );

  await writeFile(queueGate, "release");
  await ui(
    "first answer precedes follow-up",
    "document.body.innerText.includes('bash: queue-first') && document.body.innerText.includes('echo queue-follow') && !document.body.innerText.includes('bash: queue-follow')",
  );
  await ui(
    "started follow-up leaves queue",
    "document.body.innerText.includes('bash: queue-first') && !document.body.innerText.includes('Queued · runs after the current reply')",
  );

  const startedWithdrawal = await fetch(
    `${base}/api/agents/default/chats/${main.id}/messages/${followMessage.request.id}/withdraw`,
    { method: "POST" },
  );

  assert.equal(startedWithdrawal.status, 409, "a started message cannot be withdrawn");
  await writeFile(followGate, "release");
  await ui("follow-up completes", "document.body.innerText.includes('bash: queue-follow')");
  await poll("queue run settled", async () => !(await api(`/api/agents/default/chats/${main.id}`)).busy);
  await reload();
  await ui(
    "queue history restored",
    "document.body.innerText.includes('bash: queue-first') && document.body.innerText.includes('bash: queue-follow')",
  );
  await check(
    "removed and rejected messages never run",
    "!document.body.innerText.includes('bash: queue-removed') && !document.body.innerText.includes('queue-rejected') && document.body.innerText.indexOf('bash: queue-first') < document.body.innerText.indexOf('bash: queue-follow')",
  );
  const queueHistory = await api(`/api/agents/default/chats/${main.id}/history?before=2147483647`);
  assert.ok(!JSON.stringify(queueHistory).includes("bash: queue-removed"));
  assert.ok(!JSON.stringify(queueHistory).includes(rejectedText));
  results.push("follow-up waits, runs after the first reply, and cannot be withdrawn after it starts");
  results.push("Remove withdraws a queued web message before it runs");
  results.push("reject returns 409 while busy and never queues or runs the message");

  // Retry on a stopped run continues the same run and does not send the message again.
  const retryGate = join(isolated, "retry.gate");
  const retryCommand = `while ! test -f '${retryGate}'; do sleep 0.05; done; echo retry-gate`;
  await send(retryCommand);
  await ui(
    "retry run is active",
    "!!document.querySelector('button[aria-label=\"Stop run\"]:not([inert] *)')",
  );
  await tap('button[aria-label="Stop run"]');
  await poll("retry run stopped", async () => !(await api(`/api/agents/default/chats/${main.id}`)).busy);

  const retries =
    "[...document.querySelectorAll('button:not([inert] *)')].filter(b => b.textContent.trim() === 'Retry').length";

  await ui("stopped card offers Retry", `${retries} > 0`);

  const before = await evaluate(retries);

  await evaluate(
    "[...document.querySelectorAll('button:not([inert] *)')].filter(b => b.textContent.trim() === 'Retry').at(-1).click()",
  );
  await ui(
    "continued run answers without a second message",
    `${retries} === ${before - 1} && document.body.innerText.split(${JSON.stringify(retryCommand)}).length === 2`,
  );
  // The stop can land before the tool starts, so the continued run may run the command again.
  await writeFile(retryGate, "release");
  await ui(
    "continued run answered",
    "!document.querySelector('button[aria-label=\"Stop run\"]:not([inert] *)') && !document.body.innerText.includes('Nothing to continue')",
  );
  results.push("Retry continues a stopped run without a second message");

  // /compact on a short context says there is nothing to compact.
  await poll("retry run settled", async () => !(await api(`/api/agents/default/chats/${main.id}`)).busy);
  await command("/compact keep the plan");
  await ui(
    "nothing to compact",
    "document.body.innerText.includes('Nothing to compact. The context is still short.')",
  );
  await shot("03c-nothing-to-compact");
  results.push("/compact reports a short context");

  // Given a parent that starts a child session, the result line offers its thread.
  await model("spawn");
  await send(JSON.stringify({ title: "Check", message: "saved report", provider: "faux", modelId: "echo" }));

  const done = await poll("completed child", async () =>
    (await childSessions()).find((child) => !child.busy),
  );

  const doneTurn = childTurn(done.id);

  await ui("result line offers the thread", `${viewThreadButtons}.length === 1`);
  // When the user opens the thread from the parent.
  await evaluate(`${viewThreadButtons}.at(-1).click()`);
  // Then the panel beside the parent shows the answer of the child.
  await ui(
    "thread panel shows the child answer",
    `${threadPanel}?.innerText.includes('echo: saved report') && !location.pathname.includes('/c/${done.id}')`,
  );
  await shot("06-thread-panel");
  // When the user opens the panel link.
  await evaluate(`${threadPanel}.querySelector('a[aria-label="Open as a session page"]').click()`);
  // Then the child session opens as a normal chat that names its parent.
  await ui(
    "thread panel link opens the child session",
    `location.pathname === '/a/default/c/${done.id}' && document.body.innerText.includes('echo: saved report') && !!document.querySelector('header a[aria-label="Started by Main"]') && !!document.querySelector('textarea:not(:disabled):not([inert] *)')`,
  );
  assert.ok(doneTurn);
  assert.equal((await api(`/api/agents/default/chats/${done.id}`)).readOnly, false);
  await shot("06-child-session");

  // An old task link names a task from before child sessions. It opens the session of that task.
  const old = seedOldTask(main.id);

  await visit(`/a/default/tasks/${old.taskId}`);
  await ui(
    "old task link opens the child session",
    `location.pathname === '/a/default/c/${old.conversationId}' && !!document.querySelector('header a[aria-label="Started by Main"]') && !!document.querySelector('textarea:not(:disabled):not([inert] *)')`,
  );
  await shot("06a-old-task-link");

  // The same link without the bot prefix goes through the redirect of the old address.
  await visit(`/tasks/${old.taskId}`);
  await ui(
    "the old address of a task link opens the child session",
    `location.pathname === '/a/default/c/${old.conversationId}' && !!document.querySelector('header a[aria-label="Started by Main"]')`,
  );
  assert.equal((await api(`/api/agents/default/chats/${old.conversationId}`)).readOnly, false);
  // The user can archive it, so it leaves the lists below.
  await api(`/api/agents/default/chats/${old.conversationId}/archive`, undefined, "PUT");
  results.push("a thread panel link and an old task link open the session of their turn");

  await visit("/a/default");
  const workGate = join(isolated, "work.gate");
  await send(
    JSON.stringify({
      title: "Check",
      message: `while ! test -f '${workGate}'; do sleep 0.05; done; echo work-released`,
      provider: "faux",
      modelId: "bash",
    }),
  );

  const running = await poll("running child", async () =>
    (await childSessions()).find((child) => child.busy),
  );

  assert.notEqual(done.id, running.id);
  await ui(
    "a working bot shows the working dot",
    '!!document.querySelector(\'aside a[href="/a/default"] [role="img"][aria-label="Working"]\')',
  );
  await check(
    "main Stop remains available after dispatch reply",
    "!!document.querySelector('button[title=\"Stop this run and its background work\"]:not([inert] *)')",
  );
  await check(
    "the sidebar does not expand the running child",
    `!document.querySelector('aside[aria-label="Sidebar"] a[href="/a/default/c/${running.id}"]')`,
  );
  await check(
    "the bot column has no task rows, Stop, Mark handled or Completed group",
    `(() => { const panel = document.querySelector('aside[aria-label="Sidebar"]'); return !/Mark handled|Mark all handled|Completed|Open full record|step/i.test(panel.innerText) && ![...panel.querySelectorAll('button')].some((b) => b.textContent.trim() === 'Stop'); })()`,
  );

  await shot("04-sessions-panel");
  await visit("/tasks");
  await ui(
    "both child rows loaded",
    `!!document.querySelector('main a[href="/a/default/c/${running.id}"]:not([inert] *)') && !!document.querySelector('main a[href="/a/default/c/${done.id}"]:not([inert] *)')`,
  );
  await check(
    "session rows keep newest activity first, and show the running state and the parent",
    `(() => { const links = [...document.querySelectorAll('main li:not([inert] *) a')].map((a) => a.getAttribute('href')); const row = document.querySelector('main a[href="/a/default/c/${running.id}"]:not([inert] *)').closest('li'); return links.indexOf('/a/default/c/${running.id}') >= 0 && links.indexOf('/a/default/c/${running.id}') < links.indexOf('/a/default/c/${done.id}') && row.innerText.includes('Started by Main') && row.innerText.includes('Running'); })()`,
  );
  await advanceClock(31 * 60 * 1000);
  await ui(
    "the Sessions page turns the running child to Stalled when only the clock moves",
    `document.querySelector('main a[href="/a/default/c/${running.id}"]:not([inert] *)').closest('li').innerText.includes('Stalled')`,
  );
  await restoreClock();
  await shot("04-sessions");
  await visit("/a/default");
  await ui(
    "stop background work",
    "!!document.querySelector('button[aria-label=\"Stop run\"]:not([inert] *):not(:disabled)')",
  );
  await tap('button[aria-label="Stop run"]');
  await poll("child stop", async () => !(await api(`/api/agents/default/chats/${running.id}`)).busy);

  // A running child takes a steer from its own composer, and its run answers once.
  const childGate = join(isolated, "child-steer.gate");
  await send(
    JSON.stringify({
      title: "Steer check",
      message: `while ! test -f '${childGate}'; do sleep 0.05; done; echo child-gate`,
      provider: "faux",
      modelId: "bash",
    }),
  );

  const steered = await poll("child to steer", async () =>
    (await childSessions()).find((child) => child.title === "Steer check" && child.busy),
  );

  await visit(`/a/default/tasks/${childTurn(steered.id)}`);
  await ui(
    "a running child is a normal chat with Stop",
    `location.pathname === '/a/default/c/${steered.id}' && !!document.querySelector('header a[aria-label="Started by Main"]') && !!document.querySelector('textarea:not(:disabled):not([inert] *)') && !!document.querySelector('button[aria-label="Stop run"]:not([inert] *)')`,
  );
  // After a redirect, the shell moves focus to the page title in an animation frame. A headless page
  // draws that frame late, so draw one with a screenshot and wait for the title to take focus.
  await shot("05-child-opened");

  await ui("the page title takes focus after the redirect", "document.activeElement?.id === 'page-title'");

  // Then the user clicks the composer and types. The focus must stay there, also across a screenshot.
  await tap("textarea:not([inert] *)");
  await check("a click puts the focus in the composer", "document.activeElement?.tagName === 'TEXTAREA'");
  await call("Input.insertText", { text: "echo steered-child" });
  await ui(
    "the draft shows the steer button",
    "!!document.querySelector('button[aria-label=\"Steer this run\"]:not([inert] *)')",
  );
  await check(
    "the composer keeps the focus before the screenshot",
    "document.activeElement?.tagName === 'TEXTAREA'",
  );
  await shot("05-child-steer-draft");
  await check(
    "the composer keeps the focus after the screenshot",
    "document.activeElement?.tagName === 'TEXTAREA'",
  );
  await call("Input.dispatchKeyEvent", { type: "keyDown", key: "Enter", code: "Enter" });
  await call("Input.dispatchKeyEvent", { type: "keyUp", key: "Enter", code: "Enter" });
  await ui(
    "the child steer is queued",
    "document.body.innerText.includes('Steer · joins after the current tool step')",
  );
  await writeFile(childGate, "release");
  await ui(
    "the steered child answers once",
    "document.body.innerText.includes('bash: steered-child') && !document.body.innerText.includes('bash: child-gate')",
  );
  await poll("steered child idle", async () => !(await api(`/api/agents/default/chats/${steered.id}`)).busy);
  // One Enter sends one input: one stored row, and one answer on the page.
  assert.equal(
    withControlDB(
      (db) =>
        db
          .prepare("SELECT count(*) AS n FROM submissions WHERE conversation_id = ? AND content = ?")
          .get(steered.id, "echo steered-child").n,
    ),
    1,
  );
  await check(
    "the steer was sent once and answered once",
    "document.body.innerText.split('bash: steered-child').length === 2",
  );
  await shot("06b-child-steered");
  results.push("a running child takes a steer from its composer");

  // A run that the user started in a child is not work of the parent. Stop on the parent leaves it,
  // so the parent page offers no Stop for it.
  const ownGate = join(isolated, "child-own.gate");
  await api(`/api/agents/default/chats/${steered.id}/messages`, {
    id: randomUUID(),
    text: `while ! test -f '${ownGate}'; do sleep 0.05; done; echo own-run`,
  });
  await poll(
    "the child runs the user turn",
    async () => (await api(`/api/agents/default/chats/${steered.id}`)).busy,
  );
  // The result of the steered child started a short run of the parent. Its Stop is not the check here.
  await poll(
    "parent idle after the steered child answered",
    async () => !(await api(`/api/agents/default/chats/${main.id}`)).busy,
  );
  await visit("/a/default");

  const listedOwn = await poll("the list shows the child busy", async () =>
    (await childSessions()).find((child) => child.id === steered.id && child.busy),
  );

  assert.equal(listedOwn.stoppedWithParent, undefined);
  await ui(
    "the bot shows work in progress",
    `!!document.querySelector('aside a[href="/a/default"] [aria-label="Working"]')`,
  );
  await check(
    "the parent page has no Stop for a child run that the user started",
    "!document.querySelector('button[aria-label=\"Stop run\"]:not([inert] *)')",
  );
  await writeFile(ownGate, "release");
  await poll("the user turn ends", async () => !(await api(`/api/agents/default/chats/${steered.id}`)).busy);

  // A child session in another bot shows in the lists of its parent with its bot, and the result
  // mark of the parent opens it in that bot.
  const coder = await api("/api/agents", {
    name: "Coder",
    provider: "faux",
    modelId: "echo",
    thinkingLevel: "off",
    instructions: "",
  });

  const parentBot = (await api("/api/agents/default")).name;
  await visit("/a/default");
  await send(
    JSON.stringify({
      bot: "Coder",
      title: "Cross-bot check",
      message: "hello coder",
      provider: "faux",
      modelId: "echo",
    }),
  );

  const crossChild = await poll("child in another bot", async () =>
    (await childSessions()).find((child) => child.agentId === coder.id && !child.busy),
  );

  assert.equal(crossChild.agentName, "Coder");
  await check(
    "the sidebar does not expand cross-bot children",
    `!document.querySelector('aside[aria-label="Sidebar"] a[href="/a/${coder.id}/c/${crossChild.id}"]')`,
  );
  await shot("06c-cross-bot-panel");
  await ui("result line of the child in another bot offers its thread", `${viewThreadButtons}.length > 0`);
  await evaluate(`${viewThreadButtons}.at(-1).click()`);
  await ui(
    "thread panel shows the answer of the child in another bot",
    `${threadPanel}?.innerText.includes('echo: hello coder') && !!${threadPanel}.querySelector('a[href="/a/${coder.id}/c/${crossChild.id}"]')`,
  );
  await evaluate(`${threadPanel}.querySelector('a[aria-label="Open as a session page"]').click()`);
  await ui(
    "the thread panel link opens the child in its own bot, and the child names the bot of its parent",
    `location.pathname === '/a/${coder.id}/c/${crossChild.id}' && document.body.innerText.includes('echo: hello coder') && !!document.querySelector('header a[aria-label="Started by Main in ${parentBot}"]') && !!document.querySelector('textarea:not(:disabled):not([inert] *)')`,
  );
  await shot("06c-cross-bot-child");
  // Open All sessions from the parent bot, then archive the cross-bot child in its owning bot.
  await tap('aside a[href="/a/default"]');
  await tap('header nav a[href="/a/default/sessions"]');
  const crossRow = `main a[href="/a/${coder.id}/c/${crossChild.id}"]`;
  await ui("cross-bot child in parent sessions", `!!document.querySelector('${crossRow}')`);
  await tap(`main li:has(a[href="/a/${coder.id}/c/${crossChild.id}"]) button`);
  await poll(
    "cross-bot archive uses owner",
    async () => !!(await api(`/api/agents/${coder.id}/chats/${crossChild.id}`)).archivedAt,
  );
  await ui("archived child leaves active sessions", `!document.querySelector('${crossRow}')`);
  await tap('main button[aria-expanded="false"]:not([inert] *):not(header *)');
  await ui(
    "archived child is listed",
    `!!document.querySelector('${crossRow}') && document.querySelector('${crossRow}').closest('li').innerText.includes('Restore')`,
  );
  await tap(`main li:has(a[href="/a/${coder.id}/c/${crossChild.id}"]) button`);
  await poll(
    "cross-bot restore uses owner",
    async () => !(await api(`/api/agents/${coder.id}/chats/${crossChild.id}`)).archivedAt,
  );
  await ui(
    "restored child returns to active sessions",
    `!!document.querySelector('${crossRow}') && document.querySelector('${crossRow}').closest('li').innerText.includes('Archive')`,
  );
  // Leave the child archived so later list counts stay unchanged.
  await tap(`main li:has(a[href="/a/${coder.id}/c/${crossChild.id}"]) button`);
  await poll(
    "cross-bot child archived again",
    async () => !!(await api(`/api/agents/${coder.id}/chats/${crossChild.id}`)).archivedAt,
  );
  results.push("a child in another bot shows with its bot, and its thread panel link opens it there");

  // A parent-started run with user input has no parent Stop until another parent input is queued.
  // The check above ends on the page of another bot.
  await visit("/a/default");
  const mixedGate = join(isolated, "mixed-stop.gate");
  await send(
    JSON.stringify({
      title: "Mixed Stop",
      message: `while ! test -f '${mixedGate}'; do sleep 0.05; done; echo mixed-run`,
      provider: "faux",
      modelId: "bash",
    }),
  );

  const mixed = await poll("mixed child running", async () =>
    (await childSessions()).find((child) => child.title === "Mixed Stop" && child.busy),
  );

  await poll(
    "parent idle after mixed child start",
    async () => !(await api(`/api/agents/default/chats/${main.id}`)).busy,
  );
  await api(`/api/agents/default/chats/${mixed.id}/messages`, { id: randomUUID(), text: "echo mixed-user" });
  assert.equal((await childSessions()).find((child) => child.id === mixed.id).stoppedWithParent, undefined);
  await ui(
    "no Stop for a placed parent input",
    "!document.querySelector('button[aria-label=\"Stop run\"]:not([inert] *)')",
  );
  await send(
    JSON.stringify({ tool: "session_send", id: String(mixed.id), message: "echo mixed-parent-queued" }),
  );
  await poll(
    "parent idle after queueing",
    async () => !(await api(`/api/agents/default/chats/${main.id}`)).busy,
  );
  await poll("queued parent input enables Stop", async () =>
    (await childSessions()).find((child) => child.id === mixed.id && child.stoppedWithParent),
  );
  await ui(
    "Stop for queued parent input",
    "!!document.querySelector('button[aria-label=\"Stop run\"]:not([inert] *)')",
  );
  await tap('button[aria-label="Stop run"]');
  await poll(
    "queued parent input withdrawn",
    async () =>
      withControlDB((db) =>
        db
          .prepare("SELECT status, reason FROM submissions WHERE conversation_id = ? AND content = ?")
          .get(mixed.id, "echo mixed-parent-queued"),
      )?.reason === "stopped",
  );

  const mixedRows = withControlDB((db) =>
    db
      .prepare(
        "SELECT status, reason FROM submissions WHERE conversation_id = ? AND kind = 'message' ORDER BY rowid",
      )
      .all(mixed.id),
  );

  assert.deepEqual(
    mixedRows.map((row) => row.status),
    ["pending", "pending", "unanswered"],
  );
  assert.equal((await childSessions()).find((child) => child.id === mixed.id).stoppedWithParent, undefined);
  await ui(
    "Stop disappears after withdrawal",
    "!document.querySelector('button[aria-label=\"Stop run\"]:not([inert] *)')",
  );
  await writeFile(mixedGate, "release");
  await poll("mixed child ends", async () => !(await api(`/api/agents/default/chats/${mixed.id}`)).busy);
  await poll("mixed result ends", async () => !(await api(`/api/agents/default/chats/${main.id}`)).busy);
  results.push("parent Stop tracks withdrawable mixed child input");

  // Release reconciles a saved channel input through the real native admission path.
  await holdMaintenance();
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
      message: `while ! test -f '${channelGate}'; do sleep 0.05; done; echo channel-released`,
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

  await api("/api/maintenance", undefined, "DELETE");

  const channelChild = await poll("channel child running", async () =>
    (await childSessions()).find((child) => child.busy),
  );

  await visit("/a/default");
  await ui(
    "channel work in bot status",
    '!!document.querySelector(\'aside a[href="/a/default"] [role="img"][aria-label="Working"]\')',
  );
  await ui("main stream ready", "!!document.querySelector('textarea:not(:disabled):not([inert] *)')");
  await check(
    "channel work counts in Sessions but cannot enable main Stop",
    "!document.querySelector('button[aria-label=\"Stop run\"]:not([inert] *)')",
  );
  await writeFile(channelGate, "release");
  await poll(
    "channel work completed",
    async () => !(await api(`/api/agents/default/chats/${channelChild.id}`)).busy,
  );

  // Sessions shares one list for real automation, child session and Chat contexts.
  await visit("/a/default/sessions");
  await ui("Sessions kind filter", "!!document.querySelector('select[aria-label=\"Kind\"]')");
  await selectKind("Automation");
  await ui(
    "empty kind filter",
    "document.body.innerText.includes('No sessions of this kind') && document.body.innerText.includes('Choose All kinds to see every session.') && document.body.innerText.includes('0 active · newest first')",
  );
  results.push("an empty kind filter keeps its selector and shows the accepted empty copy");
  await model("echo");

  const automation = await api("/api/agents/default/automations", {
    name: `Daily review ${"W".repeat(45)}`,
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
    "a Chat session belongs to the agent and its Chat tab",
    `!!document.querySelector('aside a[href="/a/default"][aria-current="page"]') &&
      !document.querySelector('aside[aria-label="Sidebar"] a[data-tab][aria-current="page"]')`,
  );
  await command("/new carry the plan");
  await ui(
    "Chat reset divider shows the handoff",
    "!!document.querySelector('[role=separator]') && document.body.innerText.includes('Carried into the new context') && document.body.innerText.includes('carry the plan')",
  );
  await reload();
  await ui(
    "Chat history loads in view",
    "document.body.innerText.includes('context: [\"Plan a separate session\"]')",
  );
  await sendFromComposer("Fresh Chat context");
  await ui(
    "Chat clear keeps only the handoff in its model context",
    'document.body.innerText.includes(\'context: ["carry the plan","Fresh Chat context"]\')',
  );
  await clickLabel("Fork from here");
  await ui(
    "Fork from here opens the fork with its origin",
    `location.pathname !== '/a/default/c/${emptySession}' && document.body.innerText.includes('The original chat is unchanged. Files on disk are not rolled back.') && !!document.querySelector('main a[href="/a/default/c/${emptySession}"]')`,
  );
  await shot("04a-fork-banner");
  await sendFromComposer("in the fork");
  await ui(
    "a fork answers from the copied context",
    'document.body.innerText.includes(\'context: ["carry the plan","Fresh Chat context","in the fork"]\')',
  );
  results.push("Fork from here copies the context into a new Chat");
  await visit(`/a/default/c/${emptySession}`);
  await ui(
    "the source of a fork is unchanged",
    "document.body.innerText.includes('Fresh Chat context') && !document.body.innerText.includes('in the fork')",
  );
  await clickLabel("Edit in new chat");
  await ui(
    "Edit in new chat fills the composer of the new Chat",
    `location.pathname !== '/a/default/c/${emptySession}' && document.querySelector('textarea:not([inert] *)').value === 'Fresh Chat context' && document.body.innerText.includes('Forked from')`,
  );
  results.push("Edit in new chat forks before the message and fills the composer");
  await typeDraft("");
  await visit("/a/default");
  await ui(
    "main stream ready after Chat clear",
    "!!document.querySelector('textarea:not(:disabled):not([inert] *)')",
  );
  await check(
    "Chat input and reset do not enter main",
    "!document.querySelector('main').innerText.includes('Plan a separate session') && !document.querySelector('main').innerText.includes('Fresh Chat context') && document.querySelector('main').innerText.includes('after clear')",
  );

  await model("spawn");
  await visit(`/a/default/c/${emptySession}`);
  const sessionChildGate = join(isolated, "session-child.gate");
  await sendFromComposer(
    JSON.stringify({
      title: "Session audit",
      message: `while ! test -f '${sessionChildGate}'; do sleep 0.05; done; echo session-report`,
      provider: "faux",
      modelId: "bash",
    }),
  );

  const sessionChild = await poll("Chat child session", async () =>
    (await childSessions()).find((child) => child.title === "Session audit" && child.busy),
  );

  assert.equal(sessionChild.parentId, emptySession);
  await poll(
    "Chat dispatch reply settled",
    async () => !(await api(`/api/agents/default/chats/${emptySession}`)).busy,
  );
  await visit("/a/default");
  await ui(
    // An open question can make the row show Needs you, which wins over Working.
    "Chat child in bot status",
    '!!document.querySelector(\'aside a[href="/a/default"] [role="img"]\')',
  );
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

  // Archived sessions are not on this page.
  const allSessions = (await api("/api/agents/default/chats")).chats.filter((chat) => !chat.archivedAt);
  assert.ok(allSessions.every((chat) => !chat.main && !("task" in chat) && !("items" in chat)));
  await visit("/tasks");
  await ui(
    "old Tasks address redirects to Sessions",
    "location.pathname === '/a/default/sessions' && !!document.querySelector('select[aria-label=\"Kind\"]')",
  );
  await ui(
    "busy Chat and child session count once each",
    "document.body.innerText.includes('2 active · newest first')",
  );

  const targets = allSessions.map((chat) => `/a/default/c/${chat.id}`);

  assert.deepEqual(
    await evaluate(
      "[...document.querySelectorAll('main li:not([inert] *) a')].map(a => a.getAttribute('href'))",
    ),
    targets,
    "Sessions keeps the server's activity order",
  );

  // A child session is a Chat session, so the Chat filter lists it with the Chats and their forks.
  const chatRows = allSessions.filter((chat) => chat.channel === "web");

  assert.ok(chatRows.some((chat) => chat.parentId !== undefined));

  for (const [kind, active, count] of [
    ["Chat", chatRows.filter((chat) => chat.busy).length, chatRows.length],
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
        "child",
        `/a/default/tasks/${childTurn(sessionChild.id)}`,
        "location.pathname === '/a/default/c/" +
          sessionChild.id +
          "' && !!document.querySelector('header a[aria-label^=\"Started by\"]') && !!document.querySelector('textarea:not(:disabled):not([inert] *)')",
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
  await writeFile(sessionChildGate, "release");
  await poll(
    "Chat child answered",
    async () => !(await api(`/api/agents/default/chats/${sessionChild.id}`)).busy,
  );
  await visit(`/a/default/c/${emptySession}`);
  // Given a Chat session whose child finished, the result line names the child.
  await ui(
    "Chat receives its child session result",
    `${viewThreadButtons}.length === 1 && document.body.innerText.includes('Session audit replied')`,
  );
  // When the user opens the thread, then the panel holds the answer of the child.
  await evaluate(`${viewThreadButtons}.at(-1).click()`);
  await ui(
    "Chat thread panel shows the child answer",
    `${threadPanel}?.innerText.includes('session-report')`,
  );
  await visit("/a/default");
  await ui(
    "main snapshot after Chat report",
    "!!document.querySelector('textarea:not(:disabled):not([inert] *)')",
  );
  await check(
    "Chat report does not enter main",
    "!document.querySelector('main').innerText.includes('Session audit') && !document.querySelector('main').innerText.includes('session-report')",
  );

  await visit(`/a/default/c/${emptySession}`);
  const stopGate = join(isolated, "session-stop.gate");
  await sendFromComposer(
    JSON.stringify({
      title: "Session Stop check",
      message: `while ! test -f '${stopGate}'; do sleep 0.05; done`,
      provider: "faux",
      modelId: "bash",
    }),
  );

  const stopChild = await poll("Chat child before Stop", async () =>
    (await childSessions()).find((child) => child.title === "Session Stop check" && child.busy),
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
    "Chat Stop stops its child session",
    async () => !(await api(`/api/agents/default/chats/${stopChild.id}`)).busy,
  );
  results.push(
    "Chat context, title, Clear, history, child result, Stop and main Stop scope use the selected session",
  );

  // A child of a Chat names that Chat as its parent, also after the user archived the Chat.
  await model("spawn");
  const orphanParent = await api("/api/agents/default/chats", {}, "POST");

  await api(`/api/agents/default/chats/${orphanParent.id}/messages`, {
    id: randomUUID(),
    text: JSON.stringify({
      title: "Orphan check",
      message: "echo orphan",
      provider: "faux",
      modelId: "echo",
    }),
  });

  const orphan = await poll("child of a Chat", async () =>
    (await childSessions()).find(
      (child) => child.title === "Orphan check" && child.parentId === orphanParent.id && !child.busy,
    ),
  );

  await poll(
    "the parent Chat is idle",
    async () => !(await api(`/api/agents/default/chats/${orphanParent.id}`)).busy,
  );
  await api(`/api/agents/default/chats/${orphanParent.id}/archive`, undefined, "PUT");

  const orphanParentTitle =
    (await api(`/api/agents/default/chats/${orphanParent.id}`)).title || "New session";

  await visit("/tasks");
  await ui(
    "the child of an archived Chat names that Chat",
    `(() => { const row = document.querySelector('main a[href="/a/default/c/${orphan.id}"]:not([inert] *)')?.closest('li'); return !!row && row.innerText.includes(${JSON.stringify(`Started by ${orphanParentTitle}`)}) && !row.innerText.includes('Started by Main'); })()`,
  );
  results.push("a child of an archived Chat names its parent");

  // Saved channel inputs use real reconciliation. Go tests cover each platform boundary.
  await model("echo");
  await holdMaintenance();
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

  await api("/api/maintenance", undefined, "DELETE");

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
        // A phone cannot show every message at once, so each mark scrolls into view first.
        return document.documentElement.scrollWidth <= innerWidth + 1 &&
          [...document.querySelectorAll('[data-origin]')].every(node => {
            node.scrollIntoView({ block: 'nearest', behavior: 'instant' });
            const rect = node.getBoundingClientRect();
            // Scroll offsets snap to device pixels, so a mark can sit a fraction of a pixel outside.
            return rect.top >= scroll.top - 1 && rect.bottom <= scroll.bottom + 1;
          });
      })()`,
    );
    await shot(`11-channel-main-${label}`);
    await visit("/a/default/sessions");
    await ui("Sessions after channel input", "!!document.querySelector('select[aria-label=\"Kind\"]')");
    await check(
      `Sessions has only Chat and Automation (${label})`,
      `[...document.querySelector('select[aria-label="Kind"]').options].map(option => option.textContent).join(',') === 'All kinds,Chat,Automation' && ![...document.querySelectorAll('main li:not([inert] *)')].some(node => /Telegram|WeChat|Feishu/.test(node.textContent))`,
    );
    await shot(`12-channel-sessions-${label}`);
  }

  // The bot row opens Main; the bot menu holds its other pages and attention items.
  const tab = () => `aside[aria-label="Sidebar"] a[href="/a/default"]`;
  const botMenu = 'header nav a[href="/a/default/agent"]';
  const attentionMenu = 'header button[aria-label="Needs you"]';

  // The channel widths above leave a phone viewport behind; the bot column shows on a desktop one.
  await viewport(1280, 900);
  await visit("/a/default");
  await check(
    "the bot row is current without expanded pages",
    `!document.querySelector('aside[aria-label="Sidebar"] a[data-tab]') && document.querySelector('${tab("Chat")}').getAttribute('aria-current') === 'page'`,
  );

  // The echo model never reports a check, so the Daily review run above failed and is not done yet.
  await ui(
    "a failed routine run marks the bot row",
    `document.querySelector('nav[aria-label="Bots"] a[href="/a/default"] [role="img"]')?.getAttribute('aria-label') === 'Needs you' &&
      !/active|channels? on|Idle/.test(document.querySelector('nav[aria-label="Bots"] a[href="/a/default"]').getAttribute('aria-label'))`,
  );

  // Every orange dot has a place to act on it: the open bot lists the failed run.
  const enabledRoutines = (await api("/api/agents/default/automations")).automations.filter(
    (a) => a.enabled,
  ).length;

  await ui(
    "Routines navigation preserves count and failure",
    `(() => { const a = document.querySelector('header nav a[href="/a/default/automations"]'); return a?.getAttribute('aria-label') === ${JSON.stringify(`Routines${enabledRoutines ? `, ${enabledRoutines} on` : ""}, latest run failed`)} && !a.textContent.trim() && !!a.querySelector('[aria-label="Latest routine run failed"]'); })()`,
  );
  await tap(attentionMenu);
  await ui(
    "the attention menu lists the failed routine run",
    `[...document.querySelectorAll('[role="menu"] a[href^="/inbox/"]')].some(a => a.textContent.includes('Daily review') && a.textContent.includes('Routine run failed'))`,
  );
  await tap(attentionMenu);
  await viewport(320, 844);
  await tap('header button[aria-label^="Bot menu"]');
  await ui("mobile failed result menu", `!!document.querySelector('[role="menu"] a[href^="/inbox/"]')`);
  await check(
    "long attention title stays inside mobile menu",
    `(() => { const menu = document.querySelector('[role="menu"]'); const link = menu.querySelector('a[href^="/inbox/"]'); const title = link.querySelector('span span'); const m = menu.getBoundingClientRect(); const l = link.getBoundingClientRect(); const t = title.getBoundingClientRect(); return m.left >= 0 && m.right <= innerWidth && l.left >= m.left && l.right <= m.right && t.right <= l.right && title.scrollWidth > title.clientWidth; })()`,
  );
  await ui(
    "mobile menu finishes opening",
    `document.querySelector('[role="menu"]').getAnimations({ subtree: true }).every(a => a.playState !== 'running')`,
  );
  await tap('[role="menu"] a[href^="/inbox/"]');
  await ui("failed result opens from mobile menu", `location.pathname.startsWith('/inbox/')`);
  await viewport(1280, 900);
  await ui(
    "desktop sidebar returns",
    `!!document.querySelector('aside[aria-label="Sidebar"] a[href="/a/default"]')`,
  );
  await tap('aside[aria-label="Sidebar"] a[href="/a/default"]');
  await ui(
    "main after failed result",
    `location.pathname === '/a/default' && !!document.querySelector('textarea[aria-label^="Message"]:not([inert] *)')`,
  );
  await ui("mobile drawer closes", `!document.querySelector('[role="dialog"]')`);
  await evaluate("document.querySelector('textarea[aria-label^=\"Message\"]').dataset.probe = 'kept'");
  await tap('textarea[aria-label^="Message"]:not([inert] *)');
  await ui("composer receives focus", `document.activeElement?.matches('textarea[aria-label^="Message"]')`);
  await call("Input.insertText", { text: "unsent words" });
  await ui(
    "unsent draft entered",
    `document.querySelector('textarea[aria-label^="Message"]:not([inert] *)')?.value === 'unsent words'`,
  );

  // The sidebar hides itself, and the first header button shows it again with focus.
  await tap('aside[aria-label="Sidebar"] button[aria-label="Hide sidebar"]');
  await ui(
    "the sidebar hides, and focus moves to the header button",
    `!document.querySelector('aside[aria-label="Sidebar"]') && document.activeElement?.getAttribute('aria-label')?.startsWith('Show sidebar')`,
  );
  await tap('main header button[aria-label^="Show sidebar"]');
  await ui("the header button shows the sidebar", `!!document.querySelector('aside[aria-label="Sidebar"]')`);

  // Routines opens its page, and the bot's row stays current.
  await tap('header nav a[href="/a/default/automations"]');
  await ui(
    "the Routines row opens /a/default/automations",
    "location.pathname === '/a/default/automations' && document.querySelector('#page-title')?.textContent === 'Routines'",
  );
  await check(
    "the bot row stays current on Routines",
    `document.querySelector('nav[aria-label="Bots"] a[href="/a/default"]').getAttribute('aria-current') === 'page'`,
  );
  // Settings opens from the bot's ⋯, which stays lit there.
  await openBotSettings('aside[aria-label="Sidebar"]');
  await ui(
    "the bot menu opens /a/default/agent",
    "location.pathname === '/a/default/agent' && !!document.querySelector('#bot-name')",
  );
  await check(
    "the bot menu is lit on Settings",
    `document.querySelector('header nav a[href="/a/default/agent"]').getAttribute('aria-current') === 'page'`,
  );

  await check(
    "settings hold only the profile",
    "[...document.querySelectorAll('main h2')].map(h => h.textContent).join(',') === 'Profile'",
  );
  await shot("07-agent-settings");
  await tap(tab("Chat"));
  await ui(
    "the main timeline kept its composer and the unsent message",
    `location.pathname === '/a/default' && document.querySelector('textarea[aria-label^="Message"]:not([inert] *)').dataset.probe === 'kept' &&
      document.querySelector('textarea[aria-label^="Message"]:not([inert] *)').value === 'unsent words'`,
  );
  await evaluate(
    "(() => { const t = document.querySelector('textarea[aria-label^=\"Message\"]'); t.select(); document.execCommand('delete'); })()",
  );
  await ui("composer cleared", "document.querySelector('textarea[aria-label^=\"Message\"]').value === ''");
  await visit("/providers");
  await check(
    "a global settings page is not part of the agent",
    "!document.querySelector('aside[aria-label=\"Sidebar\"] a[data-tab]') && document.querySelector('nav[aria-label=\"Bots\"] a[href=\"/a/default\"]').getAttribute('aria-current') === null",
  );

  // The Settings tab renames the agent; the header, the sidebar and the composer follow.
  await visit("/a/default/agent");
  await ui("agent settings", "!!document.querySelector('#bot-name')");
  await evaluate("document.querySelector('#bot-name').focus(); document.querySelector('#bot-name').select()");
  await call("Input.insertText", { text: "  Browser Bot  " });
  await call("Input.dispatchKeyEvent", { type: "keyDown", key: "Enter", code: "Enter" });
  await poll("name saved", async () => (await api("/api/agents/default")).name === "Browser Bot");
  await ui(
    "the sidebar follows the new name",
    `document.querySelector('nav[aria-label="Bots"] a[href="/a/default"]').textContent.includes('Browser Bot') &&
      document.querySelector('nav[aria-label="Bots"] a[href="/a/default"]').getAttribute('aria-label').includes('Browser Bot')`,
  );
  await check(
    "channel settings have no links to old peer contexts",
    "![...document.querySelectorAll('a')].some(a => a.textContent.trim() === 'Open conversation')",
  );
  await visit("/settings");
  await ui("old settings address redirects", "location.pathname === '/a/default/agent'");

  await visit("/a/default");
  await ui("renamed composer", "!!document.querySelector('textarea[aria-label=\"Message Browser Bot\"]')");

  // Session controls follow the bot model when there is no session override.
  const chosen = (await api("/api/agents/default")).modelId;
  const settingsRow = '[role="region"][aria-label="Task settings"]';
  const settingsTask = await api("/api/agents/default/chats", {});

  const openChatSettings = async () => {
    await visit(`/a/default/c/${settingsTask.id}`);
    await ui("Task settings row", `!!document.querySelector('${settingsRow}')`);
  };

  await openChatSettings();
  await ui(
    "the Chat settings row shows the model",
    `document.querySelector('${settingsRow} button[aria-label="Set model"]')?.innerText.includes(${JSON.stringify(chosen)}) && !document.querySelector('${settingsRow} p')`,
  );
  await model("think");
  await visit("/a/default");
  await openChatSettings();
  await tap(`${settingsRow} button[aria-label="Set model"]`);
  await ui(
    "a reasoning model adds thinking choices to the model picker",
    `!!document.querySelector('[aria-label="Chat model"] [aria-label="Thinking level"]')`,
  );
  await model(chosen);
  await visit("/a/default");
  await openChatSettings();
  await tap(`${settingsRow} button[aria-label="Set model"]`);
  await ui(
    "a model without reasoning leaves the thinking level out",
    `!document.querySelector('[aria-label="Chat model"] [aria-label="Thinking level"]')`,
  );
  await call("Input.dispatchKeyEvent", { type: "keyDown", key: "Escape", code: "Escape" });

  // Instructions is a Settings page; client-side navigation keeps drafts alive.
  await openBotSettings('aside[aria-label="Sidebar"]');
  await tap('nav[aria-label="Settings pages"] a[href="/a/default/instructions"]');
  await ui("instructions tab", "!!document.querySelector('textarea[aria-label=\"Bot instructions\"]')");
  await evaluate("document.querySelector('textarea[aria-label=\"Bot instructions\"]').focus()");
  await call("Input.insertText", { text: "Answer in the language I write in." });
  await ui(
    "the draft marks the bot menu and the bot row",
    `!!document.querySelector('${botMenu} [aria-label="Unsaved instructions draft"]') &&
      document.querySelector('nav[aria-label="Bots"] a[href="/a/default"]').getAttribute('aria-label').includes('instructions draft')`,
  );
  await shot("07a-instructions");
  await tap(tab("Chat"));
  await openBotSettings('aside[aria-label="Sidebar"]');
  await tap('nav[aria-label="Settings pages"] a[href="/a/default/instructions"]');
  await ui(
    "the draft survives a tab change",
    "document.querySelector('textarea[aria-label=\"Bot instructions\"]').value === 'Answer in the language I write in.'",
  );
  await tap('button[aria-label="Save instructions"]');
  await poll(
    "instructions saved",
    async () => (await api("/api/agents/default")).instructions === "Answer in the language I write in.",
  );
  await ui(
    "saving clears the draft marks",
    `!document.querySelector('${botMenu} [aria-label="Unsaved instructions draft"]') &&
      !document.querySelector('nav[aria-label="Bots"] a[href="/a/default"]').getAttribute('aria-label').includes('instructions draft')`,
  );
  await evaluate(
    "(() => { const t = document.querySelector('textarea[aria-label=\"Bot instructions\"]'); t.focus(); t.select(); document.execCommand('delete'); })()",
  );
  await tap('button[aria-label="Save instructions"]');
  await poll("instructions restored", async () => (await api("/api/agents/default")).instructions === "");

  // On a phone the header names the open page, and nothing overflows the viewport.
  await viewport(390, 844);
  await visit("/a/default/agent");
  await ui(
    "the header names the page on a phone",
    `[...document.querySelectorAll('main header span')].some(span => span.textContent === 'Settings') &&
      document.documentElement.scrollWidth <= innerWidth + 1`,
  );
  await visit("/a/default");
  await shot("08-mobile");
  await evaluate("document.querySelector('button[aria-label^=\"Open navigation\"]').click()");
  await ui("mobile agent row", "!!document.querySelector('[role=dialog] a[href=\"/a/default\"]')");
  await shot("09-mobile-nav");
  await openGlobal('[role="dialog"]', "/providers");
  await ui("mobile global settings opened", "location.pathname === '/providers'");
  await evaluate("document.querySelector('button[aria-label^=\"Open navigation\"]').click()");
  await ui("mobile drawer reopened", "!!document.querySelector('[role=dialog]')");
  await tap('[role="dialog"] button[aria-label="Close navigation"]');
  await ui("mobile drawer closed", "!document.querySelector('[role=dialog]')");
  results.push("mobile drawer links and close button accept pointer clicks");
  // Given two agents, UI navigation keeps their messages and unsaved work separate.
  await viewport(1280, 900);
  await model("echo");
  await visit("/a/default");
  const visibleComposer = 'textarea[aria-label^="Message"]:not([inert] *)';
  const visibleInstructions = 'textarea[aria-label="Bot instructions"]:not([inert] *)';

  async function botSettingsPage(name) {
    await openBotSettings('aside[aria-label="Sidebar"]');
    await ui("settings switcher ready", "!!document.querySelector('nav[aria-label=\"Settings pages\"]')");
    await tap(`nav[aria-label="Settings pages"] a[href$="/${name}"]`);
  }

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
  await botSettingsPage("instructions");
  await ui("default instructions ready", `!!document.querySelector('${visibleInstructions}')`);
  await evaluate(`document.querySelector('${visibleInstructions}').focus()`);
  await call("Input.insertText", { text: "Default instructions draft" });
  await tap('aside button[aria-label="New bot"]');
  await ui("create agent dialog", "!!document.querySelector('form#create-bot input')");
  await evaluate("document.querySelector('form#create-bot input').focus()");
  await call("Input.insertText", { text: "  Second Bot  " });
  await tap("form#create-bot button");
  await ui("create dialog model picker", "!!document.querySelector('[cmdk-input]')");
  await evaluate("document.querySelector('[cmdk-input]').focus()");
  await call("Input.insertText", { text: "echo" });
  await tap('[cmdk-item][data-value="faux/echo"]');
  await ui("create picker closed", "!document.querySelector('[cmdk-input]')");
  await tap('button[form="create-bot"]');

  const second = await poll("second agent created", async () =>
    (await api("/api/agents")).agents.find((agent) => agent.name === "Second Bot"),
  );

  const secondPath = `/a/${second.id}`;
  const secondLink = `nav[aria-label="Bots"] a[href="${secondPath}"]`;
  const defaultLink = 'nav[aria-label="Bots"] a[href="/a/default"]';
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
  await botSettingsPage("instructions");
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
    `location.pathname === '/a/default' && document.querySelector('${visibleComposer}')?.value === 'default unsent text'`,
  );
  await check(
    "default timeline excludes second agent messages and keeps its upload",
    "!document.body.innerText.includes('echo: only the second agent sees this') && document.body.innerText.includes('default-draft.txt')",
  );
  await botSettingsPage("instructions");
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
    `location.pathname === '${secondPath}' && document.querySelector('${visibleComposer}')?.value === 'second unsent text'`,
  );
  await check(
    "second composer excludes default attachments",
    "!document.body.innerText.includes('default-draft.txt')",
  );
  await botSettingsPage("instructions");
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
    `document.querySelector('${visibleInstructions}')?.value === 'Second instructions draft' && !document.querySelector('${defaultLink}').getAttribute('aria-label').includes('instructions draft') && document.querySelector('${secondLink}').getAttribute('aria-label').includes('instructions draft')`,
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
  await botSettingsPage("channels");
  await ui(
    "second agent channel controls",
    "document.body.innerText.includes('Connections') && !!document.querySelector('main a[href$=\"/channels/telegram\"]')",
  );
  await check(
    "second agent channel links",
    `['telegram', 'wechat', 'feishu'].every(name => !!document.querySelector('main a[href="/a/${second.id}/channels/' + name + '"]:not([inert] *)'))`,
  );
  await shot("13-multi-agent-desktop");
  await viewport(390, 844);
  await check("second agent channels fit phone", "document.documentElement.scrollWidth <= innerWidth + 1");
  await shot("13-channels-phone");
  await viewport(1440, 1000);

  // Each channel page has the same sections and keeps the settings switcher.
  for (const channel of ["telegram", "wechat", "feishu"]) {
    await tap(`main a[href="/a/${second.id}/channels/${channel}"]`);
    await ui(
      `${channel} channel template`,
      `location.pathname.endsWith('/channels/${channel}') && !!document.querySelector('nav[aria-label="Settings pages"] a[aria-current="page"][href$="/channels"]') && ['Connection', 'How to use', '/clear'].every(text => document.querySelector('main').textContent.includes(text))`,
    );
    await shot(`13-channel-${channel}-desktop`);
    await tap('nav[aria-label="Settings pages"] a[href$="/channels"]');
    await ui(`${channel} back to channels`, `location.pathname.endsWith('/channels')`);
  }

  await check(
    "channel icons differ",
    "new Set([...document.querySelectorAll('main [data-channel-icon]')].map(icon => icon.innerHTML)).size === 3",
  );

  for (const root of [data, join(data, "agents", second.id)]) {
    await mkdir(join(root, ".agents"), { recursive: true });
    await writeFile(join(root, ".agents", ".env"), "EXTENSION_DEMO_KEY=never-show-this-value\n");
  }

  await botSettingsPage("extensions");
  await ui(
    "second agent tools",
    `document.body.innerText.includes('/agents/${second.id}/.agents/') && !!document.querySelector('main button:not([inert] *)')`,
  );
  await check(
    "tools belong to the selected agent",
    `!document.querySelector('aside a[href="/extensions"]') && document.querySelector('main h1:not([inert] *)')?.textContent === 'Tools' && !document.body.innerText.includes('Reload tools')`,
  );
  await check(
    "tool levels and secrecy",
    `['dex', 'agent', 'The model can', 'Tool output becomes part of the transcript', 'MCP servers'].every(text => document.body.innerText.includes(text)) && !!document.querySelector('main a[href="/environment"]') && !!document.querySelector('[data-env-key="EXTENSION_DEMO_KEY"][data-env-scope="agent"]') && !document.body.innerText.includes('never-show-this-value')`,
  );
  await shot("13a-agent-extensions-desktop");
  await viewport(390, 844);
  await check("tools fit phone", "document.documentElement.scrollWidth <= innerWidth + 1");
  await shot("13b-agent-extensions-phone");
  await evaluate(
    "Array.from(document.querySelectorAll('h2')).find(node => node.textContent === 'MCP servers').scrollIntoView({block: 'center', behavior: 'instant'})",
  );
  await shot("13c-extension-servers-phone");
  await viewport(1440, 1000);
  await shot("13d-extension-servers-desktop");

  // A bot edits only its own .env. The dex file shows as read-only, and the bot key
  // shadows it.
  await ui(
    "bot env form ready",
    `!!document.querySelector('input[name="key"]') && !!document.querySelector('[data-env-scope="dex"]')`,
  );
  await check(
    "wider bot env levels are read-only",
    `!document.querySelector('[data-env-scope="dex"] button') && !!document.querySelector('[data-env-scope="agent"] button') && document.body.innerText.includes('All bots · Read-only') && document.body.innerText.includes('Shadowed') && !document.querySelector('select[name="scope"]')`,
  );
  const secretKey = "BROWSER_WRITE_ONLY_KEY";
  const secretValue = "browser-private-value-do-not-show";
  const secretReplacement = "browser-private-replacement-do-not-show";
  await tap('input[name="key"]');
  await call("Input.insertText", { text: secretKey });
  await tap('input[name="value"]');
  await call("Input.insertText", { text: secretValue });
  await check(
    "secret input is a password",
    `document.querySelector('input[name="value"]').type === 'password' && !document.body.innerText.includes(${JSON.stringify(secretValue)})`,
  );
  await click("Save key");
  const keyRow = `[data-env-key="${secretKey}"][data-env-scope="agent"]`;
  await ui(
    "saved key name",
    `!!document.querySelector('${keyRow}') && document.querySelector('input[name="value"]').value === ''`,
  );
  assert.equal(
    await readFile(join(data, "agents", second.id, ".agents", ".env"), "utf8"),
    `EXTENSION_DEMO_KEY=never-show-this-value\n${secretKey}=${secretValue}\n`,
  );
  await evaluate("document.querySelector('button[aria-label=\"Dismiss notification\"]')?.click()");
  await evaluate(
    "Array.from(document.querySelectorAll('h2')).find(node => node.textContent === 'Environment keys').scrollIntoView({block: 'start', behavior: 'instant'})",
  );
  await shot("13e-secrets-desktop");
  await viewport(390, 844);
  await check("bot env form fits phone", "document.documentElement.scrollWidth <= innerWidth + 1");
  await evaluate(
    "Array.from(document.querySelectorAll('h2')).find(node => node.textContent === 'Environment keys').scrollIntoView({block: 'start', behavior: 'instant'})",
  );
  await shot("13f-secrets-phone");
  await evaluate(
    "Array.from(document.querySelectorAll('h2')).find(node => node.textContent === 'Saved keys').scrollIntoView({block: 'start', behavior: 'instant'})",
  );
  await check("bot env list fits phone", "document.documentElement.scrollWidth <= innerWidth + 1");
  await shot("13g-secrets-keys-phone");
  await viewport(1440, 1000);
  await shot("13h-secrets-keys-desktop");
  await tap(`${keyRow} button:first-child`);
  await ui(
    "replace selects key",
    `document.querySelector('input[name="key"]').value === '${secretKey}' && document.querySelector('input[name="value"]').value === ''`,
  );
  await tap('input[name="value"]');
  await call("Input.insertText", { text: secretReplacement });
  await click("Save key");
  await ui(
    "replacement saved",
    "document.querySelector('input[name=\"value\"]').value === '' && !document.querySelector('form button[type=\"submit\"]').disabled",
  );
  assert.ok(
    (await readFile(join(data, "agents", second.id, ".agents", ".env"), "utf8")).includes(
      `${secretKey}=${secretReplacement}\n`,
    ),
  );
  await check(
    "secret values stay out of page text",
    `![${JSON.stringify(secretValue)}, ${JSON.stringify(secretReplacement)}, 'never-show-this-value'].some(value => document.body.innerText.includes(value))`,
  );
  await tap(`${keyRow} button:last-child`);
  await ui("key deleted", `!document.querySelector('${keyRow}')`);
  assert.ok(
    !(await readFile(join(data, "agents", second.id, ".agents", ".env"), "utf8")).includes(secretKey),
  );
  await ui(
    "bot change applied without Reload",
    "!document.body.innerText.includes('The change applies automatically')",
  );

  // A value that looks like a pasted assignment is saved with a warning and a hint badge.
  const pastedKey = "PASTED_LINE_DEMO_KEY";
  const pastedValue = "NAME_ONE=https://proxy.example PASTED_LINE_DEMO_KEY=browser-pasted-fake";
  await tap('input[name="key"]');
  // The key field still holds the replaced key; select it so that the typed text replaces it.
  await evaluate("document.querySelector('input[name=\"key\"]').select()");
  await call("Input.insertText", { text: pastedKey });
  await tap('input[name="value"]');
  await call("Input.insertText", { text: pastedValue });
  await click("Save key");
  const pastedRow = `[data-env-key="${pastedKey}"][data-env-scope="agent"]`;
  await ui(
    "pasted assignment warned",
    `!!document.querySelector('[data-env-warning="${pastedKey}"]') && !!document.querySelector('${pastedRow} [data-env-hint="assignment"]') && document.querySelector('[data-env-warning]').innerText.includes('NAME=')`,
  );
  await check(
    "pasted value stays out of page text",
    `!document.body.innerText.includes('browser-pasted-fake') && !document.body.innerText.includes('proxy.example')`,
  );
  assert.ok(
    (await readFile(join(data, "agents", second.id, ".agents", ".env"), "utf8")).includes(pastedValue),
  );
  await shot("13j-pasted-assignment-warning");
  await tap(`${pastedRow} button:last-child`);
  await ui(
    "pasted key deleted",
    `!document.querySelector('${pastedRow}') && !document.querySelector('[data-env-warning]')`,
  );

  // The global page writes the dex file and names no bot.
  await tap('main a[href="/environment"]');
  await ui(
    "environment form ready",
    `location.pathname === '/environment' && !!document.querySelector('input[name="key"]') && !!document.querySelector('[data-env-scope="dex"]')`,
  );
  await check(
    "environment names no bot",
    `!document.querySelector('[data-env-scope="agent"]') && !document.querySelector('main').innerText.includes('Second Bot') && !document.querySelector('main').innerText.includes('This bot')`,
  );
  await tap('input[name="key"]');
  await call("Input.insertText", { text: secretKey });
  await tap('input[name="value"]');
  await call("Input.insertText", { text: secretValue });
  await click("Save key");
  const dexRow = `[data-env-key="${secretKey}"][data-env-scope="dex"]`;
  await ui("shared key saved", `!!document.querySelector('${dexRow}')`);
  assert.ok(
    (await readFile(join(data, ".agents", ".env"), "utf8")).includes(`${secretKey}=${secretValue}\n`),
  );
  await shot("13i-environment-desktop");
  await tap(`${dexRow} button:last-child`);
  await ui("shared key deleted", `!document.querySelector('${dexRow}')`);
  await ui(
    "all bots applied without Reload",
    "!document.body.innerText.includes('The change applies automatically')",
  );
  const secretLog = await readFile(join(artifacts, "browser-app.log"), "utf8");
  assert.ok(
    !secretLog.includes(secretValue) &&
      !secretLog.includes(secretReplacement) &&
      !secretLog.includes("browser-pasted-fake"),
    "secret values stay out of app logs",
  );

  // Global pages read no bot, so they show the same text after a visit to either bot. Client
  // navigation keeps the drafts that later checks read.
  const globalText = async (bot, path) => {
    await tap(`aside a[href="${bot}"]`);
    await ui(`${bot} open`, `location.pathname === '${bot}'`);
    await openGlobal('aside[aria-label="Sidebar"]', "/providers");
    await ui(
      `${path} from ${bot}`,
      "location.pathname === '/providers' && document.body.innerText.includes('Connections')",
    );

    if (path === "/environment") {
      await tap('nav[aria-label="Settings pages"] a[href="/environment"]');
      await ui(
        `${path} keys from ${bot}`,
        "location.pathname === '/environment' && !!document.querySelector('[data-env-scope=\"dex\"]')",
      );
    }

    return evaluate("document.querySelector('main').innerText");
  };

  for (const path of ["/providers", "/environment"]) {
    assert.equal(
      await globalText(secondPath, path),
      await globalText("/a/default", path),
      `${path} must not depend on the bot`,
    );
  }

  results.push(
    "env keys: bot Tools writes only its .env, global Environment writes dex and names no bot, replace, delete, automatic reload, /secrets redirect, no leaked values, desktop and 390px phone",
  );
  await tap(secondLink);
  await ui("second agent open", `location.pathname === '${secondPath}'`);
  await botSettingsPage("extensions");
  await ui(
    "returned to current agent extensions",
    `location.pathname === '/a/${second.id}/extensions' && document.querySelector('header nav a[href="/a/${second.id}/agent"]').getAttribute('aria-current') === 'page'`,
  );
  await viewport(390, 844);
  // On a phone the bot column sits in the drawer.
  await tap('button[aria-label^="Open navigation"]');
  await tap(`[role=dialog] a[href="${secondPath}"]`);
  await ui(
    "second timeline restored on phone",
    `location.pathname === '${secondPath}' && document.querySelector('${visibleComposer}')?.value === 'second unsent text' && document.body.innerText.includes('echo: only the second agent sees this')`,
  );
  await ui("second timeline fits phone", "document.documentElement.scrollWidth <= innerWidth + 1");
  await shot("14-multi-agent-phone");
  // The drawer closes with an animation after navigation. A tap before it ends lands on its backdrop.
  await ui("the drawer closed after navigation", "!document.querySelector('[role=dialog]')");
  await tap('button[aria-label^="Open navigation"]:not([inert] *)');
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
    `document.body.innerText.includes('Bot not found') && !!document.querySelector('main a[href="/"]')`,
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

  const lastAgentPath = `/a/${connectionAgents.at(-1).id}`;

  // A full page navigation would close the streams and hide a connection leak.
  await tap('header button[aria-label="New task"]');
  await ui(
    "new task project picker",
    `!!document.querySelector('[role="dialog"] [aria-label="Directory path"]')`,
  );
  await tap('[role="dialog"] button[data-slot="dialog-close"]');
  await ui(
    "New session works after seven agent switches",
    `location.pathname.startsWith('${lastAgentPath}/c/') && document.querySelector('#page-title')?.textContent === 'New task'`,
  );
  results.push("seven agent Chat visits leave ordinary New session requests working");
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
  results.push("all agents share one activity list poll with no per-agent channel, automation or chat reads");
  assert.deepEqual(exceptions, [], "no browser exceptions");
  assert.equal((await api("/api/agents/default/chats/main")).id, main.id);
  results.push(
    "child session links, running badge, list order, parent name, steer from the child composer, idle Stop",
  );
  results.push(
    "agent tabs, kept composer, instructions draft, trimmed name, phone tabs and drawer, stable main ID",
  );
  await visit("/secrets");
  await ui("secrets redirects to environment", "location.pathname === '/environment'");
  results.push("/secrets redirects to /environment");

  // A real question with a long session title remains usable in the narrow drawer.
  const questionBot = await api("/api/agents", {
    name: "Question bot",
    provider: "faux",
    modelId: "tool",
    thinkingLevel: "off",
    instructions: "",
  });

  const questionChat = await api(`/api/agents/${questionBot.id}/chats`, {});

  await api(`/api/agents/${questionBot.id}/chats/${questionChat.id}/messages`, {
    id: randomUUID(),
    text: `ask_user ${JSON.stringify({ question: "W".repeat(100), options: ["Yes", "No"] })}`,
  });
  await poll(
    "long-title question opens",
    async () =>
      (await api(`/api/agents/${questionBot.id}/chats/${questionChat.id}`)).question?.status === "open",
  );
  await viewport(320, 844);
  await tap('button[aria-label^="Open navigation"]');
  await ui(
    "question bot in drawer",
    `!!document.querySelector('[role="dialog"] a[href="/a/${questionBot.id}"]')`,
  );
  await tap(`[role="dialog"] a[href="/a/${questionBot.id}"]`);
  await ui(
    "question bot main",
    `location.pathname === '/a/${questionBot.id}' && !document.querySelector('[role="dialog"]')`,
  );
  await tap('header button[aria-label^="Bot menu"]');
  const questionLink = `[role="menu"] a[href="/a/${questionBot.id}/c/${questionChat.id}"]`;
  await ui("question appears in bot menu", `!!document.querySelector('${questionLink}')`);
  await check(
    "long question chat title is truncated within mobile menu",
    `(() => { const link = document.querySelector('${questionLink}'); const menu = link.closest('[role="menu"]'); const title = link.querySelector('span span'); const m = menu.getBoundingClientRect(); const l = link.getBoundingClientRect(); const t = title.getBoundingClientRect(); return m.left >= 0 && m.right <= innerWidth && l.right <= m.right && t.right <= l.right && title.scrollWidth > title.clientWidth && link.innerText.includes('Asks you a question'); })()`,
  );
  await ui(
    "question menu finishes opening",
    `document.querySelector('[role="menu"]').getAnimations({ subtree: true }).every(a => a.playState !== 'running')`,
  );
  await tap(questionLink);
  await ui(
    "question link opens its chat",
    `location.pathname === '/a/${questionBot.id}/c/${questionChat.id}'`,
  );
  await shot("17-long-question-mobile");
  results.push("long question title fits the mobile bot menu and opens its chat");

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
        messages,
        exceptions,
        turnReads: Object.fromEntries(turnReads),
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
    JSON.stringify({ clears, aborts, messages, exceptions }, null, 2),
  );

  if (socket?.readyState === WebSocket.OPEN) {
    await shot("failure").catch(() => {});
    await writeFile(
      join(artifacts, "failure.txt"),
      String(error) +
        "\n" +
        JSON.stringify(
          await evaluate(
            `({ path: location.pathname, active: document.activeElement?.outerHTML.slice(0, 500), composers: [...document.querySelectorAll('textarea')].map(t => ({value: t.value, label: t.getAttribute('aria-label'), inert: !!t.closest('[inert]'), probe: t.dataset.probe})) })`,
          ).catch(() => ({})),
          null,
          2,
        ) +
        "\n" +
        (await evaluate("document.body.innerText").catch(() => "")),
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
  // Resize observers, such as the one that centers the active agent tab, run in the next frames.
  // A tap before them can scroll its target away again.
  await evaluate("new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(done)))");
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

async function holdMaintenance() {
  await poll("maintenance admission", async () => {
    await api("/api/maintenance", undefined, "POST");

    return true;
  });
}

async function model(modelId) {
  const agent = await api("/api/agents/default");
  await api("/api/agents/default", { ...agent, provider: "faux", modelId }, "PUT");
}

// Moves only the clock that the page reads. The API data and the timers stay as they are.
async function advanceClock(ms) {
  await evaluate(`window.__realNow ??= Date.now; Date.now = () => window.__realNow() + ${ms}; true`);
}

async function restoreClock() {
  await evaluate("if (window.__realNow) Date.now = window.__realNow; true");
}

async function childSessions() {
  return (await api("/api/agents/default/chats")).chats.filter((chat) => chat.parentId !== undefined);
}

function withControlDB(read) {
  const db = new DatabaseSync(join(data, "control.sqlite"));
  db.exec("PRAGMA busy_timeout = 5000");

  try {
    return read(db);
  } finally {
    db.close();
  }
}

// The input that a parent sent to a child session: its turn.
function childTurn(conversationId) {
  return withControlDB(
    (db) =>
      db
        .prepare(
          "SELECT id FROM submissions WHERE conversation_id = ? AND parent_request_id IS NOT NULL ORDER BY rowid LIMIT 1",
        )
        .get(conversationId).id,
  );
}

// A task from before child sessions, as the migrations leave it: a conversation with a task: key,
// a parent, and a first input whose ID is the old task ID. The tasks table is gone.
function seedOldTask(parentId) {
  return withControlDB((db) => {
    const now = Date.now();

    const source = db
      .prepare("SELECT id FROM submissions WHERE conversation_id = ? LIMIT 1")
      .get(parentId).id;

    const taskId = `old-task-${randomUUID()}`;

    const { id } = db
      .prepare(
        "INSERT INTO conversations (agent_id, channel_key, title, parent_id, created_at) VALUES ('default', ?, 'Old task', ?, ?) RETURNING id",
      )
      .get(`task:${taskId}`, parentId, now);

    db.prepare(
      "INSERT INTO submissions (id, conversation_id, content, attachments, kind, status, answer, parent_request_id, created_at, settled_at) VALUES (?, ?, 'old goal', 'null', 'message', 'done', 'old report', ?, ?, ?)",
    ).run(taskId, id, source, now, now);

    return { taskId, conversationId: id };
  });
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

function commandOption(name) {
  return `document.querySelector('[role=listbox][aria-label=Commands] [role=option][id$="-${name}"]')`;
}

// Replace the draft. An empty text clears it.
async function typeDraft(text) {
  await ui("composer enabled", "!!document.querySelector('textarea:not(:disabled):not([inert] *)')");
  // execCommand edits like a user, so React sees the change.
  await evaluate(
    "(() => { const area = document.querySelector('textarea:not([inert] *)'); area.focus(); area.select(); document.execCommand('delete'); })()",
  );

  if (text) await call("Input.insertText", { text });
}

async function key(name) {
  await evaluate("document.querySelector('textarea:not([inert] *)').focus()");
  await call("Input.dispatchKeyEvent", { type: "keyDown", key: name, code: name });
  await call("Input.dispatchKeyEvent", { type: "keyUp", key: name, code: name });
}

async function command(text) {
  await typeDraft(text);
  await key("Enter");
}

async function click(text) {
  await evaluate(
    `(() => { const button = [...document.querySelectorAll('button:not([inert] *)')].find(b => b.textContent.trim() === ${JSON.stringify(text)}); if (!button || button.disabled) throw new Error('Button is unavailable'); button.click(); })()`,
  );
}

// Click the last button with an accessible name, such as a message action that shows on hover.
async function clickLabel(label) {
  await evaluate(
    `(() => { const button = [...document.querySelectorAll('main button[aria-label=${JSON.stringify(label)}]')].at(-1); if (!button || button.disabled) throw new Error('Button is unavailable'); button.click(); })()`,
  );
}

/** Opens a global page from the sidebar footer inside `scope`. */
async function openGlobal(scope, path) {
  await tap(`${scope} a[href="${path}"]`);
}

/** Opens settings for the current bot from the header. */
async function openBotSettings(_scope) {
  const botPath = await evaluate(`'/' + location.pathname.split('/').filter(Boolean).slice(0, 2).join('/')`);
  const desktop = await evaluate("innerWidth >= 1024");
  const settingsLink = `${desktop ? "header nav" : '[role="menu"]'} a[href="${botPath}/agent"]`;

  if (!desktop) await tap('header button[aria-label^="Bot menu"]');
  await ui("bot settings link", `!!document.querySelector('${settingsLink}')`);
  await tap(settingsLink);
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
