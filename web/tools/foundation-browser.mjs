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
let projectChat;
let routineId;
let resultRun;
let hasInbox;
let routineProject;
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
      modelId: "bash",
      thinkingLevel: agent.thinkingLevel,
      instructions: agent.instructions,
    }),
  });
  const main = await request("/api/agents/default/chats/main");
  await request(`/api/agents/default/chats/${main.id}/messages`, {
    method: "POST",
    body: JSON.stringify({ id: crypto.randomUUID(), text: "foundation screenshot seed" }),
  });
  // The fake model echoes the text, so the reply renders Markdown: a list, inline code and a code block.
  await request(`/api/agents/default/chats/${main.id}/messages`, {
    method: "POST",
    body: JSON.stringify({
      id: crypto.randomUUID(),
      text: 'Markdown seed\n\n- first item\n- second `inline` item\n\n```go\nfmt.Println("hello")\n```',
    }),
  });
  for (let index = 0; index < 7; index++) {
    const session = await request("/api/agents/default/chats", { method: "POST" });
    await request(`/api/agents/default/chats/${session.id}/messages`, {
      method: "POST",
      body: JSON.stringify({ id: crypto.randomUUID(), text: `sidebar seed chat ${index + 1}` }),
    });
    if (index === 1) projectChat = session.id;
    if (index === 0)
      await request(`/api/agents/default/chats/${session.id}/archive`, { method: "PUT", body: "{}" });
  }
  const recentProject = join(isolated, "home", "Documents");
  await mkdir(recentProject, { recursive: true });
  const recentSession = await request("/api/agents/default/chats", { method: "POST" });
  await request(`/api/agents/default/chats/${recentSession.id}/project`, {
    method: "PUT",
    body: JSON.stringify({ cwd: recentProject }),
  });
  await request("/api/agents/default/automations", {
    method: "POST",
    body: JSON.stringify({
      name: "Foundation automation",
      prompt: "Report the current time.",
      schedule: { kind: "daily", time: "09:00" },
      timeZone: "UTC",
      session: "new",
      enabled: true,
    }),
  });
  routineProject = recentProject;
  await request("/api/agents/default", {
    method: "PUT",
    body: JSON.stringify({ ...agent, provider: "faux", modelId: "tool" }),
  });
  const other = await request("/api/agents", {
    method: "POST",
    body: JSON.stringify({ name: "Research bot", provider: "faux", modelId: "tool", thinkingLevel: "off" }),
  });
  for (const [agentId, name, outcomes] of [
    ["default", "Release check", ["changed", "changed", "unchanged", "failed"]],
    [other.id, "Weekly notes", ["changed"]],
  ]) {
    let routine;
    for (const outcome of outcomes) {
      const body = {
        name,
        prompt: `report_check ${JSON.stringify({ outcome, summary: { changed: "A new release is ready to review. Version 2 includes the project fixes.", unchanged: "No new releases since the last check.", failed: "The release check could not reach the upstream service. Review the run before retrying." }[outcome], changeKey: "version-2" })}`,
        schedule: { kind: "daily", time: "09:00" },
        timeZone: "UTC",
        session: "new",
        enabled: true,
        cwd: routineProject,
      };
      routine = await request(`/api/agents/${agentId}/automations${routine ? `/${routine.id}` : ""}`, {
        method: routine ? "PUT" : "POST",
        body: JSON.stringify(body),
      });
      const run = await request(`/api/agents/${agentId}/automations/${routine.id}/run`, { method: "POST" });
      await waitFor("routine settled", async () => {
        const { runs } = await request(`/api/agents/${agentId}/automations/${routine.id}/runs`);
        return runs.find((row) => row.id === run.id)?.status !== "running";
      });
      if (agentId === "default") {
        routineId = routine.id;
        resultRun = run.id;
      }
    }
  }
  await request("/api/agents/default", {
    method: "PUT",
    body: JSON.stringify({ ...agent, provider: "faux", modelId: "bash" }),
  });
  hasInbox = (await fetch(`${base}/api/inbox`)).ok;
}
let chrome;
let socket;
let nextId = 1;
const pending = new Map();
const loadedDocuments = new Set();
function call(method, params = {}) {
  const id = nextId++;
  socket.send(JSON.stringify({ id, method, params }));
  return new Promise((resolveCall, reject) => pending.set(id, { resolve: resolveCall, reject, method }));
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
// Wait for the new document's load event. The previous React tree can still be
// visible after Page.navigate returns, including when the URL stays the same.
async function navigate(path) {
  const navigation = await call("Page.navigate", { url: `${base}${path}` });
  await waitFor(`${path} document loaded`, () => loadedDocuments.has(navigation.loaderId));
  await waitFor(`${path} app mounted`, () => evaluate("!!document.querySelector('#root')?.children.length"));
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
async function typeText(value) {
  for (const character of value) {
    if (character === " ") await press(" ", "Space", 32, " ");
    else
      await press(
        character,
        `Key${character.toUpperCase()}`,
        character.toUpperCase().charCodeAt(0),
        character,
      );
  }
}
async function projectEvidence(theme, size) {
  const trigger = `document.querySelector('button[aria-label="Set project directory"]')`;
  // The same harness also captures the base revision, which has no project control.
  if (!(await evaluate(`!!${trigger}`))) return { available: false };
  const project = join(isolated, "project");
  const toolProject = join(isolated, "tool-project");
  await mkdir(project, { recursive: true });
  await mkdir(toolProject, { recursive: true });
  const browseRoot = join(isolated, "home");
  await mkdir(join(browseRoot, "Projects", ".git"), { recursive: true });
  await mkdir(join(browseRoot, "Documents"), { recursive: true });
  await request(`/api/agents/default/chats/${projectChat}/project`, {
    method: "PUT",
    body: JSON.stringify({ cwd: "" }),
  });
  await evaluate(`${trigger}.focus()`);
  await press("Enter", "Enter", 13, "\r");
  await waitFor("project dialog", () => evaluate("!!document.querySelector('[role=dialog]')"));
  const picker = await evaluate("!!document.querySelector('[aria-label=\"Directory path\"]')");
  if (picker)
    await waitFor("directory listing", () =>
      evaluate(
        "document.querySelector('[aria-label=\"Subdirectories\"]')?.getAttribute('aria-busy') === 'false'",
      ),
    );
  if (picker)
    assert.ok(
      await evaluate(
        `[...document.querySelectorAll('[aria-label="Recent projects"], [aria-label="Subdirectories"]')].every(el => el.scrollWidth <= el.clientWidth)`,
      ),
      "picker lists do not overflow horizontally",
    );
  await capture(`${theme}-chat-project-open-${size}.png`);
  if (size === "mobile") {
    await press("Escape", "Escape", 27);
    return { available: true, picker, open: true };
  }
  async function fill(value) {
    await evaluate(
      "document.querySelector('[role=dialog] input').focus(); document.querySelector('[role=dialog] input').select()",
    );
    await call("Input.insertText", { text: value });
  }
  async function click(label) {
    await evaluate(
      `[...document.querySelectorAll('[role=dialog] button')].find(b => b.textContent.trim() === ${JSON.stringify(label)}).click()`,
    );
  }
  if (picker) {
    const height = await evaluate("document.querySelector('[role=dialog]').getBoundingClientRect().height");
    const home = join(isolated, "home");
    const repo = join(home, "Projects");
    async function atPath(path) {
      await waitFor(`directory ${path}`, () =>
        evaluate(
          `document.querySelector('[aria-label="Directory path"]')?.value === ${JSON.stringify(path)} && document.querySelector('[aria-label="Subdirectories"]')?.getAttribute('aria-busy') === 'false'`,
        ),
      );
    }
    await evaluate(
      `[...document.querySelectorAll('[aria-label="Subdirectories"] button')].find(b => b.textContent.trim() === 'Projects').focus()`,
    );
    await press(" ", "Space", 32, " ");
    await atPath(repo);
    assert.ok(
      await evaluate(
        "document.activeElement === document.querySelector('[aria-label=\"Directory path\"]') && document.activeElement.matches(':focus-visible')",
      ),
      "keyboard navigation keeps visible focus",
    );
    await evaluate("document.querySelector('button[aria-label=Up]').click()");
    await atPath(home);
    await evaluate("document.querySelector('button[aria-label=Up]').click()");
    await atPath(isolated);
    await evaluate("document.querySelector('button[aria-label=Home]').click()");
    await atPath(home);
    await evaluate("document.querySelector('[aria-label=\"Subdirectories\"] button').focus()");
    assert.ok(
      await evaluate("document.activeElement.matches(':focus-visible')"),
      "directory row has visible keyboard focus",
    );
    await capture(`${theme}-chat-project-keyboard-${size}.png`);
    assert.equal(
      await evaluate("document.querySelector('[role=dialog]').getBoundingClientRect().height"),
      height,
    );
    await evaluate("document.querySelector('[aria-label=\"Recent projects\"] button').focus()");
    await press("Enter", "Enter", 13, "\r");
    const recentProject = join(home, "Documents");
    await waitFor("recent project saved", () =>
      evaluate(
        `${trigger}?.title === ${JSON.stringify(recentProject)} && !document.querySelector('[role=dialog]')`,
      ),
    );
    await evaluate(`${trigger}.click()`);
    await waitFor("picker opens at current cwd", () => evaluate("!!document.querySelector('[role=dialog]')"));
    await atPath(recentProject);
  }
  await fill("relative/project");
  if (picker) await press("Enter", "Enter", 13, "\r");
  else await click("Save");
  await waitFor("project validation error", () =>
    evaluate(`document.querySelector('[role=alert]')?.textContent.includes('path must be absolute')`),
  );
  await capture(`${theme}-chat-project-error.png`);
  await fill(project);
  if (picker) {
    await press("Enter", "Enter", 13, "\r");
    await waitFor("navigate project", () =>
      evaluate(
        `document.querySelector('[aria-label="Directory path"]')?.value === ${JSON.stringify(project)} && document.querySelector('[aria-label="Subdirectories"]')?.getAttribute('aria-busy') === 'false' && !document.querySelector('[role=alert]')`,
      ),
    );
  }
  await click(picker ? "Use this folder" : "Save");
  await waitFor("project saved", () =>
    evaluate(`${trigger}?.title === ${JSON.stringify(project)} && !document.querySelector('[role=dialog]')`),
  );
  await capture(`${theme}-chat-project-selected.png`);
  // The tool writes control state. Its completion must update the header without navigation.
  const agent = await request("/api/agents/default");
  await request("/api/agents/default", {
    method: "PUT",
    body: JSON.stringify({ ...agent, provider: "faux", modelId: "tool" }),
  });
  await request(`/api/agents/default/chats/${projectChat}/messages`, {
    method: "POST",
    body: JSON.stringify({
      id: crypto.randomUUID(),
      text: `set_project ${JSON.stringify({ path: toolProject })}`,
    }),
  });
  await waitFor("tool project header refresh", () =>
    evaluate(`${trigger}?.title === ${JSON.stringify(toolProject)}`),
  );
  await capture(`${theme}-chat-project-tool-refreshed.png`);
  await evaluate(`${trigger}.click()`);
  await waitFor("project dialog reopens", () => evaluate("!!document.querySelector('[role=dialog]')"));
  await click("Clear project");
  await waitFor("project cleared", () =>
    evaluate(`${trigger}?.textContent.trim() === 'No project' && !document.querySelector('[role=dialog]')`),
  );
  await request("/api/agents/default", {
    method: "PUT",
    body: JSON.stringify({ ...agent, provider: "faux", modelId: "bash" }),
  });
  return {
    available: true,
    picker,
    keyboard: picker,
    recent: picker,
    validation: true,
    saved: true,
    toolRefresh: true,
    cleared: true,
  };
}
async function composerEvidence(theme) {
  const chatsToggle = `document.querySelector('button[aria-label="Chats"]')`;
  await waitFor("Chats toggle", () => evaluate(`!!${chatsToggle}`));
  await evaluate(`${chatsToggle}?.focus()`);
  await press("Enter", "Enter", 13, "\\r");
  if (!(await evaluate(`!!document.querySelector('aside[aria-label="Chats"]')`)))
    await evaluate(`${chatsToggle}?.click()`);
  await waitFor("Chats sidebar", () => evaluate(`!!document.querySelector('aside[aria-label="Chats"]')`));
  const textarea = `document.querySelector('textarea[aria-label^="Message"]')`;
  await evaluate(`${textarea}?.focus()`);
  await typeText("draft survives sidebar switch");
  await delay(100);
  const root = (await call("DOM.getDocument")).root.nodeId;
  const input = await call("DOM.querySelector", { nodeId: root, selector: 'input[type="file"]' });
  const file = join(output, "harness-attachment.txt");
  await writeFile(file, "attachment evidence");
  await call("DOM.setFileInputFiles", { nodeId: input.nodeId, files: [file] });
  await waitFor("attachment chip", () =>
    evaluate(
      `document.querySelector('[aria-label="Attached files"]')?.innerText.includes('harness-attachment.txt')`,
    ),
  );
  await capture(`${theme}-chat-composer-draft-attachment.png`);
  await evaluate(`${textarea}?.focus()`);
  await delay(50);
  await call("Input.dispatchKeyEvent", {
    type: "keyDown",
    key: "Enter",
    code: "Enter",
    windowsVirtualKeyCode: 13,
    modifiers: 8,
    // Chrome inserts a line break only for the carriage return text, as for a plain Enter.
    text: "\r",
  });
  await call("Input.dispatchKeyEvent", {
    type: "keyUp",
    key: "Enter",
    code: "Enter",
    windowsVirtualKeyCode: 13,
    modifiers: 8,
  });
  // Shift+Enter keeps the draft and adds a line; a sent message would empty the box.
  const shiftLine = await evaluate(
    `${textarea}?.value.startsWith('draft survives') && ${textarea}.value.includes('\\n')`,
  );
  await evaluate(
    `(() => { const el = ${textarea}; const set = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set; set.call(el, 'sleep 3'); el.dispatchEvent(new Event('input', { bubbles: true })); })()`,
  );
  await delay(100);
  await press("Enter", "Enter", 13, "\\r");
  await waitFor(
    "reply running",
    () =>
      evaluate(
        `document.querySelector('[aria-live="polite"]')?.innerText.includes('working') || !!document.querySelector('button[aria-label="Stop run"]')`,
      ),
    10000,
  );
  await capture(`${theme}-chat-composer-running.png`);
  const menu = `document.querySelector('button[aria-label*="more actions"]')`;
  await waitFor("row menu button", () => evaluate(`!!${menu}`));
  await evaluate(`${menu}?.focus()`);
  await press("Enter", "Enter", 13, "\\r");
  if (!(await evaluate(`!!document.querySelector('[role="menu"]')`))) await press(" ", "Space", 32, " ");
  await waitFor("row menu open", () => evaluate(`!!document.querySelector('[role="menu"]')`));
  await capture(`${theme}-chat-row-menu-keyboard.png`);
  await press("Escape", "Escape", 27);
  const escaped = await waitFor("row menu closes", () =>
    evaluate(`!document.querySelector('[role="menu"]')`),
  );
  return { shiftLine, escaped };
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
      // Each scenario navigates to a new document. Cached documents must not keep SSE sockets.
      "--disable-features=BackForwardCache",
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
    if (event.method === "Page.lifecycleEvent" && event.params.name === "load")
      loadedDocuments.add(event.params.loaderId);
    if (event.id && pending.has(event.id)) {
      const task = pending.get(event.id);
      pending.delete(event.id);
      event.error
        ? task.reject(new Error(`${task.method}: ${JSON.stringify(event.error)}`))
        : task.resolve(event.result);
    }
  });
  await new Promise((resolveOpen, reject) => {
    socket.addEventListener("open", resolveOpen, { once: true });
    socket.addEventListener("error", reject, { once: true });
  });
  await call("Page.enable");
  await call("Page.setLifecycleEventsEnabled", { enabled: true });
  await call("Runtime.enable");
  await call("DOM.enable");
  await call("Emulation.setDeviceMetricsOverride", {
    width: 1280,
    height: 900,
    deviceScaleFactor: 1,
    mobile: false,
  });
  await seed();
  const pages = [
    ["chat", "/a/default"],
    ["chat-session", `/a/default/c/${projectChat}`],
    ["workspace-tabs", "/a/default"],
    ["inbox", "/inbox"],
    ["inbox-detail", `/inbox/${resultRun}`],
    ["routine-history", `/a/default/automations/${routineId}`],
    ["bot-settings", "/a/default/agent"],
    ["providers", "/providers"],
    ["routines", "/a/default/automations"],
    ["extensions", "/a/default/extensions"],
    ["channels", "/a/default/channels"],
    ["secrets", "/secrets"],
    ["about", "/about"],
  ];
  for (const theme of ["light", "dark"])
    for (const [name, path] of pages.filter(
      ([name]) =>
        !process.env.DEX_FOUNDATION_INBOX_ONLY ||
        ["workspace-tabs", "inbox", "inbox-detail", "routines", "routine-history"].includes(name),
    ))
      for (const [size, width, height, mobile] of [
        ["desktop", 1280, 900, false],
        ["mobile", 390, 844, true],
      ]) {
        await call("Emulation.setDeviceMetricsOverride", { width, height, deviceScaleFactor: 1, mobile });
        await navigate(path);
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
        if (hasInbox && name.startsWith("inbox"))
          await waitFor("Inbox loaded", () =>
            evaluate(
              "document.body.innerText.includes('Release check') || document.body.innerText.includes('Run transcript')",
            ),
          );
        if (hasInbox && name === "routine-history")
          await waitFor("history loaded", () => evaluate("document.body.innerText.includes('duplicate')"));
        if (name === "routine-history")
          await evaluate(
            "[...document.querySelectorAll('h2')].find(h => ['Check history', 'Run history'].includes(h.textContent))?.scrollIntoView({block: 'start'})",
          );
        await capture(`${theme}-${name}-${size}.png`);
        const text =
          (await call("Runtime.evaluate", { expression: "document.body.innerText", returnByValue: true }))
            .result?.value ?? "";
        results.push({ theme, page: name, size, path, textLength: text.length });
        if (name === "chat-session") {
          await waitFor("chat session ready", () =>
            evaluate("!!document.querySelector('textarea[aria-label^=Message]')"),
          );
          results.push({
            theme,
            page: "chat-project-interactions",
            size,
            ...(await projectEvidence(theme, size)),
          });
        }
        if (name === "chat" && size === "desktop") {
          results.push({ theme, page: "chat-interactions", size, path, ...(await composerEvidence(theme)) });
        }
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
        if (name === "bot-settings" && size === "desktop") {
          await evaluate("document.querySelector('button[aria-label=\"Create bot\"]')?.focus()");
          await press("Enter", "Enter", 13, "\r");
          await waitFor("bot create dialog open", () =>
            evaluate("!!document.querySelector('[role=dialog]')"),
          );
          await capture(`${theme}-bot-create-dialog-desktop.png`);
          await press("Escape", "Escape", 27);
          await waitFor("bot create dialog closed", () =>
            evaluate("!document.querySelector('[role=dialog]')"),
          );
          const modelTrigger = await evaluate(
            "[...document.querySelectorAll('button')].find((button) => button.textContent.includes('Change model')) !== undefined",
          );
          if (modelTrigger) {
            await evaluate(
              "[...document.querySelectorAll('button')].find((button) => button.textContent.includes('Change model')).focus()",
            );
            await press("Enter", "Enter", 13, "\r");
            await waitFor("model picker open", () =>
              evaluate("!!document.querySelector('[aria-label=\"Search models\"]')"),
            );
            for (const [key, code] of [
              ["e", "KeyE"],
              ["c", "KeyC"],
              ["h", "KeyH"],
              ["o", "KeyO"],
            ])
              await press(key, code, key.toUpperCase().charCodeAt(0), key);
            const filtered = await evaluate(
              "document.querySelector('[aria-label=\"Search models\"]')?.value === 'echo'",
            );
            await press("Enter", "Enter", 13, "\r");
            const selected = await waitFor("model picker selection", () =>
              evaluate("!document.querySelector('[aria-label=\"Search models\"]')"),
            );
            await evaluate(
              "[...document.querySelectorAll('button')].find((button) => button.textContent.includes('Change model')).focus()",
            );
            await press("Enter", "Enter", 13, "\r");
            await waitFor("model picker reopen", () =>
              evaluate("!!document.querySelector('[aria-label=\"Search models\"]')"),
            );
            await capture(`${theme}-model-picker-open-desktop.png`);
            await press("Escape", "Escape", 27);
            const escaped = await waitFor("model picker closed", () =>
              evaluate("!document.querySelector('[aria-label=\"Search models\"]')"),
            );
            results.push({
              theme,
              page: "model-picker-keyboard",
              size: "desktop",
              path,
              filtered,
              selected,
              escaped,
            });
          }
        }
      }

  if (hasInbox) {
    await call("Emulation.setDeviceMetricsOverride", {
      width: 1280,
      height: 900,
      deviceScaleFactor: 1,
      mobile: false,
    });
    await navigate("/a/default");
    await waitFor("four tabs", () =>
      evaluate("document.querySelectorAll('nav[aria-label$=sections] a').length === 4"),
    );
    assert.deepEqual(
      await evaluate(
        "[...document.querySelectorAll('nav[aria-label$=sections] a')].map(a => a.textContent.trim().replace(/\\s*\\d+$/, ''))",
      ),
      ["Chat", "Routines", "Extensions", "Settings"],
    );
    await navigate(`/inbox/${resultRun}`);
    await waitFor("result detail ready", () =>
      evaluate("[...document.querySelectorAll('button')].some(b => b.textContent === 'Continue in chat')"),
    );
    await waitFor(
      "result read",
      async () => (await request("/api/inbox")).items.find((item) => item.runId === resultRun)?.read,
    );
    await navigate(`/inbox/${resultRun}`);
    await waitFor("continue ready", () =>
      evaluate("[...document.querySelectorAll('button')].some(b => b.textContent === 'Continue in chat')"),
    );
    await evaluate(
      "[...document.querySelectorAll('button')].find(b => b.textContent === 'Continue in chat').click()",
    );
    await waitFor("continued chat", () =>
      evaluate("location.pathname.includes('/c/') && !!document.querySelector('textarea')"),
    );
    const continuedPath = await evaluate("location.pathname");
    const continued = await request(`/api/agents/default/chats/${continuedPath.split("/").at(-1)}`);
    assert.equal(continued.cwd, routineProject);
    await waitFor("quoted result", () =>
      evaluate(
        "document.body.innerText.includes('Outcome: failed') && document.body.innerText.includes('Run transcript: /a/default/c/')",
      ),
    );
    await capture("inbox-continue-chat.png");
    await navigate(`/inbox/${resultRun}`);
    await waitFor("Done ready", () =>
      evaluate("[...document.querySelectorAll('button')].some(b => b.textContent === 'Done')"),
    );
    await evaluate("[...document.querySelectorAll('button')].find(b => b.textContent === 'Done').click()");
    await waitFor(
      "dismissed",
      async () => !(await request("/api/inbox")).items.some((item) => item.runId === resultRun),
    );
    await waitFor("Inbox list", () => evaluate("location.pathname === '/inbox'"));
    await evaluate(
      "[...document.querySelectorAll('button')].find(b => b.textContent === 'Needs you').click()",
    );
    await waitFor("needs you filter", () =>
      evaluate('document.body.innerText.includes("You\'re all caught up")'),
    );
    results.push({
      page: "inbox-interactions",
      fourTabs: true,
      read: true,
      continueProject: true,
      quotedResult: true,
      dismiss: true,
      needsYouFilter: true,
    });
  }
  await writeFile(join(output, "result.json"), JSON.stringify({ base, results }, null, 2));
} catch (error) {
  if (socket?.readyState === WebSocket.OPEN) {
    try {
      await capture("failure.png");
    } catch {
      /* Keep the original failure if Chrome lost its page. */
    }
  }
  results.push({ error: String(error) });
  await writeFile(join(output, "result.json"), JSON.stringify({ base, results }, null, 2));
  throw error;
} finally {
  socket?.close();
  chrome?.kill();
  app.kill();
}
