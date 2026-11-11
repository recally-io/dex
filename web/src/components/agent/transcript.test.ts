import { createElement, type ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { createRootRoute, createRouter, createMemoryHistory, RouterProvider } from "@tanstack/react-router";
import { expect, test } from "vite-plus/test";
import type { Item, ToolItem } from "../../api/generated/types.gen";
import { blocks } from "../../lib/tools";
import { Markdown, splitAnswer, Steps, Transcript } from "./transcript";

const tool = (id: string, status: ToolItem["status"], output: string): ToolItem => ({
  kind: "tool",
  id,
  name: "bash",
  args: { command: `printf ${output}` },
  output,
  status,
  durationMs: 1200,
});

const ids = (items: Item[]) =>
  blocks(items).map((b) =>
    b.kind === "steps" ? b.steps.map((t) => t.id) : b.kind === "item" ? b.item.id : b.kind,
  );

test("thoughts and tool calls between two answers make one collapsed row", async () => {
  const first = [
    { kind: "thinking", id: "k1", text: "Look at the files\nthen decide" } as const,
    tool("t1", "done", "one"),
    tool("t2", "done", "two"),
  ];

  const items: Item[] = [
    { kind: "user", id: "u", text: "hi" },
    ...first,
    { kind: "assistant", id: "a", text: "ok" },
    tool("t3", "done", "three"),
  ];

  expect(ids(items)).toEqual(["u", ["k1", "t1", "t2"], "a", ["t3"]]);

  // A finished row is closed. Its header names the thought and counts the calls.
  const html = await render(createElement(Steps, { steps: first }));
  expect(html).toContain("Thought process · Ran 2 tools");
  expect(html).toContain('aria-expanded="false"');
  expect(html).not.toContain("printf one");
  expect(html).not.toContain("Look at the files");

  // A row that runs stays closed and shows the command that runs now.
  const running = await render(createElement(Steps, { steps: [first[0], tool("t4", "running", "now")] }));
  expect(running).toContain("Running bash");
  expect(running).toContain("printf now");
  expect(running).toContain('aria-expanded="false"');

  const thinking = await render(createElement(Steps, { steps: [first[0]], thinking: true }));
  expect(thinking).toContain(">Thinking<");
});

test("a dispatch to the background stays a row of its own", () => {
  const dispatch: ToolItem = { ...tool("d1", "done", ""), name: "spawn_task", args: { title: "Logs" } };

  expect(
    ids([
      { kind: "thinking", id: "k1", text: "" },
      dispatch,
      tool("t1", "done", "one"),
      { kind: "thinking", id: "k2", text: "" },
    ]),
  ).toEqual([["k1"], ["d1"], ["t1", "k2"]]);
});

test("user sees the failed call of a step, with its command, time and output", async () => {
  const html = await render(
    createElement(Steps, { steps: [tool("t1", "done", "one"), tool("t2", "error", "boom")] }),
  );

  expect(html).toContain("Ran 2 tools · 1 failed");
  expect(html).toContain("printf one");
  expect(html).toContain("printf boom");
  expect(html).toContain("1.2 s");
  expect(html).toContain(">boom</pre>");
  expect(html).not.toContain(">one</pre>");
});

test("user sees which background work returned after a topic change", async () => {
  // Given a result after an unrelated answer, followed by another user message.
  const items: Item[] = [
    { kind: "user", id: "u1", text: "Check the logs in the background" },
    { kind: "assistant", id: "a1", text: "I will report back." },
    { kind: "user", id: "u2", text: "What does HTTP 202 mean?" },
    { kind: "assistant", id: "a2", text: "The server accepted the request." },
    { kind: "task", id: "result-1", taskId: "task-1", title: "检查日志写入" },
    { kind: "assistant", id: "a3", text: "The check passed." },
    tool("t1", "done", "verified"),
    { kind: "assistant", id: "a4", text: "The file contains hello-dex." },
    { kind: "user", id: "u3", text: "Thanks" },
  ];

  // When the transcript groups the result, then all its output has one named group.
  const grouped = blocks(items);
  expect(grouped.map((block) => block.kind)).toEqual(["item", "item", "item", "item", "task", "item"]);
  const result = grouped[4];
  expect(result).toMatchObject({
    kind: "task",
    item: { title: "检查日志写入" },
    blocks: [
      { kind: "item", index: 5, item: { id: "a3" } },
      { kind: "steps", steps: [{ id: "t1" }] },
      { kind: "item", index: 7, item: { id: "a4" } },
    ],
  });

  // The title remains visible even when the model's reply does not name the work.
  const html = await render(
    createElement(Transcript, { items, running: false, retry: () => {}, canRetry: true }),
  );

  expect(html).toContain("Background result");
  expect(html).toContain("检查日志写入");
  expect(html).not.toContain("result-1");
  expect(html).not.toContain("Completed");
});

test("user sees two returned results as separate groups", async () => {
  // Given two results that arrive together.
  const items: Item[] = [
    { kind: "task", id: "result-1", taskId: "task-1", title: "Access logs" },
    { kind: "assistant", id: "a1", text: "First report" },
    { kind: "task", id: "result-2", taskId: "task-2", title: "Error logs" },
    { kind: "error", id: "error-2", reason: "The provider failed" },
  ];

  // When the transcript groups the results, then each keeps only its own output.
  expect(blocks(items)).toMatchObject([
    { kind: "task", item: { title: "Access logs" }, blocks: [{ item: { id: "a1" } }] },
    { kind: "task", item: { title: "Error logs" }, blocks: [{ item: { id: "error-2" }, index: 3 }] },
  ]);
});

test("user sees a delegation by its name without internal arguments", async () => {
  // Given a successful background dispatch.
  const dispatch: ToolItem = {
    ...tool("dispatch-1", "done", '{"id":"internal-id"}'),
    name: "spawn_task",
    taskId: "internal-id",
    args: { title: "检查日志写入", goal: "private execution instructions" },
  };

  // When the call is shown, then the user sees the work name and dispatch outcome.
  const html = await render(createElement(Steps, { steps: [dispatch] }));
  expect(html).toContain("Sent to background");
  expect(html).toContain("检查日志写入");
  expect(html).toContain('href="/a/default/tasks/internal-id"');
  expect(html).not.toContain("private execution instructions");
  expect(html).not.toContain("spawn_task");

  // A failed dispatch still exposes its cause through the existing tool trace.
  const failed = await render(
    createElement(Steps, { steps: [{ ...dispatch, status: "error", output: "No capacity" }] }),
  );

  expect(failed).toContain("No capacity");
  expect(failed).not.toContain("Sent to background");
});

test.each(["", " \n "])("user sees no copy action for an empty reply (%j)", async (text) => {
  // Given a tool-only reply before a background dispatch.
  const items: Item[] = [
    { kind: "assistant", id: "empty", text },
    {
      ...tool("dispatch-1", "done", '{"id":"internal-id"}'),
      name: "spawn_task",
      args: { title: "Check logs", goal: "Check the saved logs" },
    },
  ];

  // When the finished reply is shown, then only the named dispatch has visible content.
  const html = await render(
    createElement(Transcript, { items, running: false, retry: () => {}, canRetry: true }),
  );

  expect(html).toContain("Check logs");
  expect(html).not.toContain('aria-label="Copy answer"');
});

test("memory calls read as memory use", async () => {
  const memory = "/data/memory";
  const read: ToolItem = { ...tool("t1", "done", ""), name: "read", args: { path: `${memory}/MEMORY.md` } };
  const html = await render(createElement(Steps, { steps: [read], memory }));

  expect(html).toContain("Read memory");
});

async function render(children: ReactNode) {
  const router = createRouter({
    routeTree: createRootRoute({ component: () => children }),
    history: createMemoryHistory({ initialEntries: ["/"] }),
  });

  await router.load();

  return renderToStaticMarkup(createElement(RouterProvider, { router }));
}

test("user sees reset handoffs and each compaction outcome", async () => {
  const items: Item[] = [
    { kind: "boundary", id: "e1", boundary: "compaction", text: "the old plan" },
    { kind: "boundary", id: "e2", boundary: "compaction", status: "nothing" },
    { kind: "boundary", id: "e3", boundary: "compaction", status: "failed", reason: "model down" },
    { kind: "boundary", id: "e4", boundary: "reset", at: 1, text: "keep the blue plan" },
    { kind: "boundary", id: "compaction:9", boundary: "compaction", status: "pending" },
    { kind: "boundary", id: "compaction-write:7", boundary: "compaction", status: "queued" },
  ];

  const html = await render(
    createElement(Transcript, { items, running: false, retry: () => {}, canRetry: false }),
  );

  // Only a placed summary and a reset divide the transcript.
  expect(html.match(/role="separator"/g)).toHaveLength(2);
  expect(html).toContain("Context compacted");
  expect(html).toContain("the old plan");
  expect(html).toContain("Nothing to compact");
  expect(html).toContain("model down");
  expect(html).toContain("Context cleared");
  expect(html).toContain("keep the blue plan");
  expect(html).toContain("Compacting context");
  expect(html).toContain("Summary ready");
});

test("user sees message actions only where a fork can start", async () => {
  const items: Item[] = [
    { kind: "user", id: "u1", entryId: 1, text: "plan it" },
    { kind: "assistant", id: "a1", entryId: 2, text: "Let me look." },
    { ...tool("t1", "done", "files"), entryId: 2 },
    { kind: "assistant", id: "a2", entryId: 4, text: "Here is the plan." },
    { kind: "user", id: "u2", text: "and then", queued: true },
  ];

  const actions = { pending: false, fork: () => {} };

  const html = await render(
    createElement(Transcript, {
      items,
      running: false,
      retry: () => {},
      canRetry: false,
      messageActions: actions,
    }),
  );

  // Only the answer that ends the turn has actions. The answer that called a tool cannot start
  // a fork; the queued message has no entry yet.
  expect(html.match(/aria-label="Fork from here"/g)).toHaveLength(1);
  expect(html.match(/aria-label="Edit in new chat"/g)).toHaveLength(1);
  expect(html.match(/aria-label="Copy answer"/g)).toHaveLength(1);
  expect(html.match(/aria-label="Copy message"/g)).toHaveLength(1);

  // A streaming answer has no actions.
  const live = await render(
    createElement(Transcript, {
      items: [items[0], { kind: "assistant", id: "a3", entryId: 5, text: "Writ" }],
      running: true,
      retry: () => {},
      canRetry: false,
      messageActions: actions,
    }),
  );

  expect(live).not.toContain("Fork from here");
  expect(live).not.toContain("Copy answer");
});

test("user sees an ask_user answer as the muted question above the answer", async () => {
  // Given the wrapper that the server stores for an answer.
  const text = "Answer to: Which branch?\nUse main\nand rebase";

  // When the transcript splits it, then the first line is the question.
  expect(splitAnswer(text)).toEqual({ question: "Which branch?", answer: "Use main\nand rebase" });

  // And other text, even one that starts alike without a line break, stays as typed.
  expect(splitAnswer("Answer to: nothing")).toBeNull();
  expect(splitAnswer("hello")).toBeNull();

  // And the bubble does not show the wrapper.
  const html = await render(
    createElement(Transcript, {
      items: [{ kind: "user", id: "u", text }],
      running: false,
      retry: () => {},
      canRetry: false,
    }),
  );

  expect(html).toContain(">Which branch?</span>");
  expect(html).toContain("Use main");
  expect(html).not.toContain("Answer to:");
});

test("user sees bold text next to CJK punctuation as bold", async () => {
  // Given forms that CommonMark leaves as text, and forms it parses.
  const html = await render(
    createElement(Markdown, {
      text: [
        "**限制：**这篇混合",
        "这是**“引号”**结尾和**ok**",
        "plain **bold** and `**code**`",
        "```",
        "**限制：**这篇",
        "```",
      ].join("\n"),
    }),
  );

  // Then each pair shows as bold, and code stays as typed.
  expect(html).toContain(">限制：</strong>这篇混合");
  expect(html).toContain("这是<strong");
  expect(html).toContain(">“引号”</strong>结尾和<strong");
  expect(html).toContain(">ok</strong>");
  expect(html).toContain(">bold</strong>");
  expect(html).toContain("**code**</code>");
  expect(html).toContain(">**限制：**这篇\n</code>");
  expect(html).not.toContain("\uE000");
});

test("user sees where and when a message came in below it", async () => {
  const at = new Date();
  at.setHours(8, 30, 0, 0);

  const html = await render(
    createElement(Transcript, {
      items: [{ kind: "user", id: "u", origin: "telegram:7", at: at.getTime(), text: "hello" }],
      running: false,
      retry: () => {},
      canRetry: false,
    }),
  );

  expect(html).toContain('data-origin="telegram:7">Telegram</span>');
  expect(html).toContain(">08:30</time>");
});
