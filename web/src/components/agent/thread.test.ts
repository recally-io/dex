import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderToStaticMarkup } from "react-dom/server";
import { createRootRoute, createRouter, createMemoryHistory, RouterProvider } from "@tanstack/react-router";
import { expect, test } from "vite-plus/test";
import type { Chat, Item, ToolItem, SessionExecution } from "../../api/generated/types.gen";
import { threadCall, threadState } from "../../lib/threads";
import { ResultLine, ThreadContext } from "./thread";
import { ThreadView } from "./thread-panel";
import { Steps, Transcript } from "./transcript";

const bash = (id: string): ToolItem => ({
  kind: "tool",
  id,
  name: "bash",
  args: { command: `echo ${id}` },
  output: id,
  status: "done",
});

const session = (id: string, name: string, args: ToolItem["args"], output: string): ToolItem => ({
  kind: "tool",
  id,
  name,
  args,
  output,
  status: "done",
});

const created = (id: string, child: number, title: string) =>
  session(
    id,
    "session_new",
    { title, message: "Do it" },
    JSON.stringify({
      id: String(child),
      title,
      botId: "default",
      bot: "Default",
      turnId: "dispatch",
      state: "running",
    }),
  );

const chat = (id: number, title: string, busy: boolean): Chat => ({
  id,
  channel: "web",
  main: false,
  readOnly: false,
  cwd: "",
  title,
  createdAt: 1,
  updatedAt: 2,
  busy,
  settings: {},
  parentId: 1,
});

const answer = "First line\nSecond line\nThird line";

function seeded() {
  return new QueryClient({ defaultOptions: { queries: { staleTime: Infinity } } });
}

const execution: SessionExecution = {
  chatId: 7,
  agentId: "default",
  phase: "tool",
  queued: 1,
  turns: [{ id: "dispatch", status: "running" }],
};

async function render(children: ReactNode, client = seeded()) {
  const router = createRouter({
    routeTree: createRootRoute({
      component: () =>
        createElement(
          QueryClientProvider,
          { client },
          createElement(
            ThreadContext.Provider,
            { value: { sessions: [execution], connection: "ready" } },
            children,
          ),
        ),
    }),
    history: createMemoryHistory({ initialEntries: ["/"] }),
  });

  await router.load();

  return renderToStaticMarkup(createElement(RouterProvider, { router }));
}

test("user sees one card with native execution status and no copied child text", async () => {
  const html = await render(
    createElement(Steps, { steps: [bash("a"), created("s1", 7, "Check logs"), bash("b")] }),
  );

  expect(html.match(/Session thread: Check logs/g)).toHaveLength(1);
  expect(html.indexOf("Ran a command")).toBeLessThan(html.indexOf("Session thread"));
  expect(html.indexOf("Session thread")).toBeLessThan(html.lastIndexOf("Ran a command"));
  expect(html).toContain('data-state="tool"');
  expect(html).toContain("1 queued");
  expect(html).not.toContain("First line");
  expect(html).not.toContain("Third line");
  // Without a panel, the card opens the child as a session page.
  expect(html).toContain('href="/a/default/c/7"');
  // The other steps stay collapsed while the card shows.
  expect(html).not.toContain("echo a");
});

test("user sees the bot of a child in another bot, and one card for each fork", async () => {
  const other = session(
    "s2",
    "session_fork",
    { id: "7", message: "Branch" },
    JSON.stringify({ id: "9", title: "Branch", botId: "ops", bot: "Ops" }),
  );

  const html = await render(createElement(Steps, { steps: [created("s1", 7, "Check logs"), other] }));

  expect(html.match(/Session thread:/g)).toHaveLength(2);
  expect(html).toContain("Ops");
  expect(html).toContain('href="/a/ops/c/9"');
  expect(html.match(/>Default</g) ?? []).toHaveLength(0);
});

test("user sees a send as one compact line that opens the same thread", async () => {
  const send = session(
    "s3",
    "session_send",
    { id: "7", message: "More" },
    JSON.stringify({ id: "7", title: "Check logs", botId: "default" }),
  );

  const html = await render(createElement(Steps, { steps: [send] }));

  expect(html).toContain("Sent to Check logs");
  expect(html).not.toContain("Session thread");
  expect(html).toContain('href="/a/default/c/7"');

  // With a panel on the page, the line is a button instead of a link.
  const inPanel = await render(
    createElement(
      ThreadContext.Provider,
      { value: { open: () => {} } },
      createElement(Steps, { steps: [send] }),
    ),
  );

  expect(inPanel).toMatch(/<button type="button"[^>]*>Sent to Check logs<\/button>/);
});

test("user sees a failed dispatch with its reason and no thread", async () => {
  const failed = { ...created("s1", 7, "Check logs"), status: "error" as const, output: "no model" };

  const html = await render(createElement(Steps, { steps: [failed] }));

  expect(html).toContain("not started");
  expect(html).toContain("no model");
  expect(html).not.toContain("<a ");
});

test("user sees a result as one line without the answer", async () => {
  const items: Item[] = [
    { kind: "task", id: "r1", taskId: "t1", title: "Check logs" },
    { kind: "assistant", id: "a1", text: "Main reply" },
    { kind: "task", id: "r2", taskId: "t2", title: "Build", status: "failed", reason: "model down" },
  ];

  const html = await render(
    createElement(Transcript, { chatId: 1, items, running: false, retry: () => {}, canRetry: false }),
  );

  expect(html).toContain("Check logs</span> replied");
  expect(html).toContain("Build</span> failed: model down");
  expect(html.match(/View thread/g)).toHaveLength(2);
  expect(html).toContain('href="/a/default/tasks/t1"');
  expect(html).not.toContain("<details");
  expect(html).toContain("Main reply");

  const line = await render(
    createElement(
      ThreadContext.Provider,
      { value: { open: () => {} } },
      createElement(ResultLine, { item: { kind: "task", id: "r", taskId: "t", title: "Check logs" } }),
    ),
  );

  expect(line).toMatch(/<button type="button"[^>]*>View thread<\/button>/);
});

test("user sees the child transcript in the panel, with its title, bot and state", async () => {
  const client = seeded();

  const items: Item[] = [
    { kind: "user", id: "u1", text: "Check the logs" },
    { kind: "assistant", id: "a1", text: answer },
  ];

  const html = await render(
    createElement(ThreadView, {
      agentId: "default",
      chatId: 7,
      chat: { data: chat(7, "Check logs", false), isError: false, isPending: false, refetch: () => {} },
      items,
      running: false,
      execution: { ...execution, phase: "idle", queued: 0, turns: [{ id: "dispatch", status: "done" }] },
      connection: "ready",
      reconnect: () => {},
      onPage: () => {},
      onClose: () => {},
    }),
    client,
  );

  expect(html).toContain('aria-label="Session thread"');
  expect(html).toContain("Check logs");
  expect(html).toContain("Check the logs");
  expect(html).toContain("Third line");
  expect(html).toContain('data-state="done"');
  expect(html).toContain('aria-label="Close thread"');
  expect(html).toContain('href="/a/default/c/7"');
});

test("thread state follows native run and submission outcomes", () => {
  expect(threadState(execution)).toBe("tool");
  expect(threadState({ ...execution, turns: [{ id: "dispatch", status: "done" }] }, "dispatch")).toBe("done");
  expect(threadState({ ...execution, turns: [{ id: "dispatch", status: "queued" }] }, "dispatch")).toBe(
    "queued",
  );
  expect(threadState(execution, "dispatch", false)).toBe("unavailable");
  expect(threadState({ ...execution, phase: "model" })).toBe("model");
  expect(threadState({ ...execution, phase: "running" })).toBe("running");
  expect(threadState({ ...execution, phase: "retry" })).toBe("retry");
  expect(
    threadState({ ...execution, phase: "waiting", turns: [{ id: "dispatch", status: "waiting" }] }),
  ).toBe("waiting");

  for (const [reason, want] of [
    ["model_error", "failed"],
    ["interrupted", "interrupted"],
    ["aborted", "stopped"],
  ]) {
    expect(
      threadState({ ...execution, phase: "idle", turns: [{ id: "dispatch", status: "failed", reason }] }),
    ).toBe(want);
  }
});

test("a legacy task call names its first input", () => {
  const legacy = session("l", "spawn_task", { title: "Old", goal: "Goal" }, '{"id":"task-9"}');

  expect(threadCall(legacy)).toMatchObject({ kind: "card", title: "Old", turn: "task-9" });
  expect(threadCall(bash("x"))).toBeUndefined();
});

test("user sees preparation before a child exists and model work without text", async () => {
  const pending = { ...created("pending", 7, "Check logs"), status: "running" as const, output: "" };
  const preparation = await render(createElement(Steps, { steps: [pending] }));
  expect(preparation).toContain("Preparing delegation");
  expect(preparation).not.toContain("starting");

  const html = await render(
    createElement(ThreadView, {
      agentId: "default",
      chatId: 7,
      chat: { data: chat(7, "Check logs", true), isError: false, isPending: false, refetch: () => {} },
      items: [{ kind: "user", id: "dispatch", text: "Go" }],
      running: true,
      execution: { ...execution, phase: "model" },
      connection: "ready",
      reconnect: () => {},
      onPage: () => {},
      onClose: () => {},
    }),
  );

  expect(html).toContain("Waiting for model");
  expect(html).toContain("1 queued");
});
