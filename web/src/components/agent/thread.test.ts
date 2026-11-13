import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderToStaticMarkup } from "react-dom/server";
import { createRootRoute, createRouter, createMemoryHistory, RouterProvider } from "@tanstack/react-router";
import { expect, test } from "vite-plus/test";
import type { Chat, Item, ToolItem } from "../../api/generated/types.gen";
import { getChatHistoryOptions, getChatOptions } from "../../api/generated/@tanstack/react-query.gen";
import { threadCall, threadState, threadSummary } from "../../lib/threads";
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
    JSON.stringify({ id: String(child), title, botId: "default", bot: "Default", state: "running" }),
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

// The cache holds a running child with two messages and an answer, as the web reads them.
function seeded() {
  const client = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity } } });
  const path = { agentId: "default", chatId: 7 };
  client.setQueryData(getChatOptions({ path }).queryKey, chat(7, "Check logs", true));

  client.setQueryData(getChatHistoryOptions({ path, query: { before: Number.MAX_SAFE_INTEGER } }).queryKey, {
    items: [
      { kind: "user", id: "u1", text: "Check the logs" },
      { kind: "assistant", id: "a1", text: answer },
      { kind: "user", id: "u2", text: "And the errors" },
    ],
  });

  return client;
}

async function render(children: ReactNode, client = seeded()) {
  const router = createRouter({
    routeTree: createRootRoute({ component: () => createElement(QueryClientProvider, { client }, children) }),
    history: createMemoryHistory({ initialEntries: ["/"] }),
  });

  await router.load();

  return renderToStaticMarkup(createElement(RouterProvider, { router }));
}

test("user sees one card at the place of the call, with its state and the first two lines", async () => {
  const html = await render(
    createElement(Steps, { steps: [bash("a"), created("s1", 7, "Check logs"), bash("b")] }),
  );

  expect(html.match(/Session thread: Check logs/g)).toHaveLength(1);
  expect(html.indexOf("Ran a command")).toBeLessThan(html.indexOf("Session thread"));
  expect(html.indexOf("Session thread")).toBeLessThan(html.lastIndexOf("Ran a command"));
  expect(html).toContain('data-state="running"');
  expect(html).toContain("2 turns");
  expect(html).toContain("First line\nSecond line");
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

test("thread state and summary come from the chat and its history", () => {
  const user: Item = { kind: "user", id: "u", text: "go" };

  expect(threadState(true, [user])).toBe("running");
  expect(threadState(false, [])).toBe("idle");
  expect(threadState(false, [user, { kind: "assistant", id: "a", text: "ok" }])).toBe("done");
  expect(threadState(false, [user, { kind: "error", id: "e", reason: "x", failure: "model_error" }])).toBe(
    "failed",
  );
  expect(threadState(false, [user, { kind: "error", id: "e", reason: "x", failure: "interrupted" }])).toBe(
    "interrupted",
  );
  expect(threadState(false, [user, { kind: "error", id: "e", reason: "x", aborted: true }])).toBe("stopped");
  expect(threadSummary([user, { kind: "assistant", id: "a", text: `\n${answer}` }])).toEqual({
    turns: 1,
    preview: "First line\nSecond line",
  });
});

test("a legacy task call names its first input", () => {
  const legacy = session("l", "spawn_task", { title: "Old", goal: "Goal" }, '{"id":"task-9"}');

  expect(threadCall(legacy)).toMatchObject({ kind: "card", title: "Old", turn: "task-9" });
  expect(threadCall(bash("x"))).toBeUndefined();
});
