import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderToStaticMarkup } from "react-dom/server";
import { createRootRoute, createRouter, createMemoryHistory, RouterProvider } from "@tanstack/react-router";
import { expect, test } from "vite-plus/test";
import type { Chat, Item, ToolItem } from "../../api/generated/types.gen";
import { listChatsOptions } from "../../api/generated/@tanstack/react-query.gen";
import { threadCall, threadState } from "../../lib/threads";
import { describeTool, stepTitle } from "../../lib/tools";
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

test("Task tools show their action and Task key in transcript steps", () => {
  for (const [name, verb, done] of [
    ["task_read", "Read Task", "Read Task"],
    ["task_post", "Post to Task", "Posted to Task"],
    ["task_review", "Review Task", "Reviewed Task"],
  ]) {
    const tool = session("call", name, { id: "T-42" }, "{}");

    expect(describeTool(tool)).toEqual({ verb, target: "T-42" });
    expect(stepTitle([tool]).done).toBe(done);
  }
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

async function render(children: ReactNode, client = seeded()) {
  const router = createRouter({
    routeTree: createRootRoute({
      component: () =>
        createElement(
          QueryClientProvider,
          { client },
          createElement(
            ThreadContext.Provider,
            { value: { children: { 7: "running" }, connection: "ready" } },
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

  expect(html.match(/Child session: Check logs/g)).toHaveLength(1);
  expect(html.indexOf("Ran a command")).toBeLessThan(html.indexOf("Child session"));
  expect(html.indexOf("Child session")).toBeLessThan(html.lastIndexOf("Ran a command"));
  expect(html).toContain('data-state="running"');
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

  expect(html.match(/Child session:/g)).toHaveLength(2);
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
  expect(html).not.toContain("Child session");
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
  const client = seeded();

  client.setQueryData(listChatsOptions({ path: { agentId: "default" } }).queryKey, {
    chats: [{ ...chat(7, "Check logs", false), agentId: "coder" }, chat(8, "Build", false)],
  });

  const items: Item[] = [
    { kind: "task", id: "r1", taskId: "t1", title: "Check logs", childSessionId: 7 },
    { kind: "assistant", id: "a1", text: "Main reply" },
    {
      kind: "task",
      id: "r2",
      taskId: "t2",
      title: "Build",
      status: "failed",
      reason: "model down",
      childSessionId: 8,
    },
  ];

  const html = await render(
    createElement(Transcript, { chatId: 1, items, running: false, retry: () => {}, canRetry: false }),
    client,
  );

  expect(html).toContain("Check logs</span> replied");
  expect(html).toContain("Build</span> failed: model down");
  expect(html.match(/View session/g)).toHaveLength(2);
  expect(html).toContain('href="/a/coder/c/7"');
  expect(html).toContain('href="/a/default/c/8"');
  expect(html).not.toContain("<details");
  expect(html).toContain("Main reply");

  const line = await render(
    createElement(
      ThreadContext.Provider,
      { value: { open: () => {} } },
      createElement(ResultLine, {
        item: { kind: "task", id: "r", taskId: "t", title: "Check logs", childSessionId: 7 },
      }),
    ),
    client,
  );

  expect(line).toMatch(/<button type="button"[^>]*>View session<\/button>/);
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
      status: "idle",
      connection: "ready",
      reconnect: () => {},
      onPage: () => {},
      onClose: () => {},
    }),
    client,
  );

  expect(html).toContain('aria-label="Child session"');
  expect(html).toContain("Check logs");
  expect(html).toContain("Check the logs");
  expect(html).toContain("Third line");
  expect(html).toContain('data-state="idle"');
  expect(html).not.toContain('data-state="done"');
  expect(html).toContain('aria-label="Close child session"');
  expect(html).toContain('href="/a/default/c/7"');
});

test("thread state is current state, and disconnect makes it unknown", () => {
  expect(threadState("running")).toBe("running");
  expect(threadState("waiting")).toBe("waiting");
  expect(threadState("idle")).toBe("idle");
  expect(threadState("running", false)).toBe("unavailable");
  expect(threadState()).toBe("unavailable");
});

test("user sees preparation before a child exists and running without text", async () => {
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
      status: "running",
      connection: "ready",
      reconnect: () => {},
      onPage: () => {},
      onClose: () => {},
    }),
  );

  expect(html).toContain("Running");
  expect(html).not.toContain("Waiting for model");
});

test("user sees silent system turn trace folded under task mark", async () => {
  const items: Item[] = [
    { kind: "task", id: "r1", taskId: "t1", title: "Background wake" },
    {
      kind: "tool",
      id: "tool-1",
      name: "stay_silent",
      args: { reason: "already reported" },
      status: "done",
      output: "Staying silent",
    },
    { kind: "assistant", id: "a1", text: "Internal thinking note" },
  ];

  const html = await render(
    createElement(Transcript, { chatId: 1, items, running: false, retry: () => {}, canRetry: false }),
  );

  expect(html).toContain("<details");
  expect(html).toContain("Internal trace (silent)");
});

test("user sees a child report to its parent as a step, never as a thread line", async () => {
  const report = (id: string, args: ToolItem["args"], status: ToolItem["status"] = "done"): ToolItem => ({
    kind: "tool",
    id,
    name: "session_report",
    args,
    status,
    output: "Reported to parent: report:bot/1",
  });

  const mark = (id: string): Item => ({
    kind: "task",
    id: `${id}-mark`,
    taskId: id,
    title: "Background wake",
  });

  const transcript = (...tools: ToolItem[]) =>
    render(
      createElement(Transcript, {
        chatId: 2,
        items: [mark(tools[0].id), ...tools],
        running: false,
        retry: () => {},
        canRetry: false,
      }),
    );

  const final = report("final", { content: "Checks pass" });
  const milestone = report("milestone", { content: "Half done", final: false });

  const [finalHtml, milestoneHtml, groupedHtml] = await Promise.all([
    transcript(final),
    transcript(milestone),
    transcript(milestone, final),
  ]);

  expect(
    groupedHtml.match(/<button\b[^>]*aria-expanded="false"[^>]*>.*?<\/button>/g)?.join(" ") ?? "",
  ).toContain("Reported to parent 2 times");

  for (const html of [finalHtml, milestoneHtml]) {
    expect(html).toContain("Reported to parent");
    expect(html).not.toContain("Sent to");
    expect(html).not.toContain("Internal trace (silent)");
  }

  expect(threadCall(final)).toBeUndefined();
  expect(describeTool(milestone)).toEqual({ verb: "Report parent", target: "Half done" });

  // A rejected report does not claim that it reached the parent.
  expect(await transcript(report("rejected", { content: "Batched" }, "error"))).toContain(
    "Used session_report · 1 failed",
  );

  // A streaming call without arguments yet is still the report of a child.
  expect(await transcript(report("pending", {}, "running"))).not.toContain("Sent to");

  // A retired tool in history does not render as a session_report.

  // A session_send stays a thread line, even if its arguments are invalid.
  const dispatch = session(
    "d1",
    "session_send",
    { id: "7", message: "More" },
    JSON.stringify({ id: "7", title: "Check logs" }),
  );

  const malformed = {
    ...report("d2", { content: "Wrong form" }, "error"),
    name: "session_send",
    output: "content: unknown field",
  };

  const [dispatchHtml, malformedHtml] = await Promise.all([transcript(dispatch), transcript(malformed)]);

  expect(threadCall(dispatch)?.kind).toBe("send");
  expect(threadCall(malformed)?.kind).toBe("send");
  expect(dispatchHtml).not.toContain("Reported to parent");
  expect(malformedHtml).not.toContain("Reported to parent");
  // A parent dispatch in a System Turn does not stop the trace from folding.
  expect(dispatchHtml).toContain("Internal trace (silent)");
});

test("user sees a Task alert on Main with the Task title", async () => {
  const items: Item[] = [
    { kind: "task", id: "task-alert:e1", taskId: "", title: "GitHub issue #1", alert: true },
  ];

  const html = await render(
    createElement(Transcript, { chatId: 1, items, running: false, retry: () => {}, canRetry: false }),
  );

  expect(html).toContain("Task alert");
  expect(html).toContain("GitHub issue #1");
  expect(html).not.toContain("Background command");
});
