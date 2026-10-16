import { describe, expect, test } from "vite-plus/test";
import { applyChatEvent, emptyChatState, retryMessage } from "./chat-state";
import type { ChatEvent, Item } from "../api/generated/types.gen";

describe("chat retry", () => {
  const message: Item = {
    kind: "user",
    id: "706a5b55-e39a-4ffc-a71c-34a542afda12",
    text: '<task_result>{"report":"user-provided text"}</task_result>',
    attachments: [
      {
        id: "upload-1",
        name: "notes.txt",
        mimeType: "text/plain",
        size: 5,
        path: "uploads/upload-1/notes.txt",
      },
    ],
  };

  const failure: Item = { kind: "error", id: "run:error", reason: "Provider failed" };

  test("user does not retry an earlier message when a background result fails", () => {
    // Given a user message followed by a hidden report's response.
    const items: Item[] = [
      message,
      { kind: "assistant", id: "answer", text: "I will report back." },
      { kind: "task", id: "result", title: "Check logs" },
      failure,
    ];

    // When the result reply fails, then Retry cannot send the earlier user message.
    expect(retryMessage(items, 3)).toBeUndefined();

    // A later user input has its own Retry boundary.
    expect(retryMessage([...items, { ...message, id: "next" }, failure], 5)?.id).toBe("next");
  });

  test("user can retry an ordinary message with its text and attachments", () => {
    // Given an ordinary submission, even when its text looks like a task result.
    const state = applyChatEvent(emptyChatState, {
      type: "snapshot",
      running: false,
      items: [message, failure],
    });

    // When the user retries the failed response, then the original payload is available.
    expect(retryMessage(state.items, 1)).toEqual(message);
    expect(retryMessage(state.items, 0)).toBeUndefined();
  });
});

describe("chat transcript", () => {
  test("user can queue a question while a background result keeps its output", () => {
    // Given a result that is still running when the next question is queued.
    const result: Item = { kind: "task", id: "result", title: "Check logs" };
    const question: Item = { kind: "user", id: "next", text: "What does HTTP 202 mean?", queued: true };

    const initial = applyChatEvent(emptyChatState, {
      type: "snapshot",
      running: true,
      items: [result, question],
    });

    // When the result adds more output, then it stays before the queued question.
    const answer: Item = { kind: "assistant", id: "answer", text: "The check passed." };
    const tool: Item = { kind: "tool", id: "read", name: "read", args: {}, output: "", status: "running" };

    const events: ChatEvent[] = [
      { type: "item", item: tool },
      { type: "item", item: answer },
      { type: "append", id: "answer", text: " The file exists." },
    ];

    const live = events.reduce(applyChatEvent, initial);
    expect(live.items.map((item) => item.id)).toEqual(["result", "read", "answer", "next"]);
    expect(live.items[2]).toMatchObject({ text: "The check passed. The file exists." });

    // Once consumed, the question starts its own reply after the result.
    const consumed = applyChatEvent(live, { type: "item", item: { ...question, queued: false } });

    const final = applyChatEvent(consumed, {
      type: "item",
      item: { kind: "assistant", id: "next-answer", text: "The server accepted the request." },
    });

    expect(final.items.map((item) => item.id)).toEqual(["result", "read", "answer", "next", "next-answer"]);
    expect(retryMessage(final.items, 4)?.id).toBe("next");
  });

  test.each(["user", "task"] as const)(
    "user sees a failed queued question with its own error after a %s input",
    (kind) => {
      // Given two questions queued while the current reply is still running.
      const input: Item =
        kind === "user"
          ? { kind, id: "earlier", text: "Earlier question" }
          : { kind, id: "earlier", title: "Check logs" };

      const first: Item = { kind: "user", id: "first", text: "First question", queued: true };
      const second: Item = { kind: "user", id: "second", text: "Second question", queued: true };

      const initial = applyChatEvent(emptyChatState, {
        type: "snapshot",
        running: true,
        items: [input, { kind: "assistant", id: "answer", text: "Working" }, first, second],
      });

      // When the second question fails first, then its error stays beside that question.
      const events: ChatEvent[] = [
        { type: "item", item: { ...second, queued: false } },
        { type: "item", item: { kind: "error", id: "second:error", reason: "Could not submit" } },
      ];

      const failed = events.reduce(applyChatEvent, initial);

      expect(failed.items.map((item) => item.id)).toEqual([
        "earlier",
        "answer",
        "second",
        "second:error",
        "first",
      ]);
      expect(retryMessage(failed.items, 3)?.id).toBe("second");
      expect(failed.items.at(-1)).toEqual(first);
    },
  );

  test("user sees the complete state after a reconnect snapshot", () => {
    const events: ChatEvent[] = [
      { type: "snapshot", running: true, items: [{ kind: "assistant", id: "a:0", text: "Hello" }] },
      { type: "append", id: "a:0", text: " world" },
      {
        type: "item",
        item: {
          kind: "tool",
          id: "tool-1",
          name: "read",
          args: { path: "README.md" },
          output: "",
          status: "running",
        },
      },
      {
        type: "item",
        item: {
          kind: "tool",
          id: "tool-1",
          name: "read",
          args: { path: "README.md" },
          output: "dex",
          status: "done",
          durationMs: 20,
        },
      },
      { type: "run", running: true, retry: { attempt: 2, at: 100, reason: "Rate limit" } },
      {
        type: "snapshot",
        running: false,
        items: [{ kind: "assistant", id: "a:0", text: "Hello world", usage: { input: 10, output: 2 } }],
      },
    ];

    const live = events.slice(0, 5).reduce(applyChatEvent, emptyChatState);
    expect(live.items).toHaveLength(2);
    expect(live.items[0]).toMatchObject({ text: "Hello world" });
    expect(live.items[1]).toMatchObject({ status: "done", output: "dex", durationMs: 20 });
    expect(live.running).toBe(true);
    expect(live.retry).toEqual({ attempt: 2, at: 100, reason: "Rate limit" });
    expect(events.reduce(applyChatEvent, emptyChatState)).toEqual({
      items: [{ kind: "assistant", id: "a:0", text: "Hello world", usage: { input: 10, output: 2 } }],
      running: false,
      retry: undefined,
    });
  });
  test("user sees replaced text and one tool result in the original order", () => {
    const state = applyChatEvent(emptyChatState, {
      type: "snapshot",
      running: true,
      items: [
        { kind: "assistant", id: "a", text: "old" },
        { kind: "tool", id: "t", name: "bash", args: {}, output: "", status: "running" },
      ],
    });

    const next = applyChatEvent(state, {
      type: "item",
      item: { kind: "assistant", id: "a", text: "replacement" },
    });

    expect(next.items.map((x) => x.id)).toEqual(["a", "t"]);
    expect(next.items[0]).toMatchObject({ text: "replacement" });
    expect(state.items[0]).toMatchObject({ text: "old" });
  });
  test("user sees deltas on the named text item only and retries end with the run", () => {
    const state = applyChatEvent(emptyChatState, {
      type: "snapshot",
      running: true,
      retry: { attempt: 1, at: 100, reason: "Busy" },
      items: [
        { kind: "thinking", id: "t", text: "Plan" },
        { kind: "assistant", id: "a", text: "Answer" },
        { kind: "tool", id: "tool", name: "read", args: {}, output: "", status: "running" },
      ],
    });

    const next = applyChatEvent(state, { type: "append", id: "t", text: " next" });
    expect(next.items[0]).toMatchObject({ text: "Plan next" });
    expect(next.items[1]).toMatchObject({ text: "Answer" });
    expect(applyChatEvent(next, { type: "append", id: "unknown", text: "ignored" })).toEqual(next);
    expect(applyChatEvent(next, { type: "append", id: "tool", text: "ignored" })).toEqual(next);
    expect(applyChatEvent(next, { type: "run", running: false }).retry).toBeUndefined();
  });
});
