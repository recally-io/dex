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

  test.each(["", ":attempt:2"])(
    "user cannot retry an automatic task result%s or fall back to an older message",
    (attempt) => {
      // Given an automatic result after an ordinary message and a failed response.
      const state = applyChatEvent(emptyChatState, {
        type: "snapshot",
        running: false,
        items: [
          message,
          { kind: "user", id: `task:3a54d497-5875-405d-97c0-3cddc752d889:result${attempt}`, text: "Report" },
          { kind: "assistant", id: "response", text: "Partial response" },
          failure,
        ],
      });

      // When Retry resolves its submission, then it has no ordinary message to resend.
      expect(retryMessage(state.items, 3)).toBeUndefined();

      // A later ordinary message restores Retry for that message.
      const next = applyChatEvent(state, {
        type: "item",
        item: { ...message, id: "next-message", text: "Continue" },
      });

      expect(retryMessage(next.items, 5)).toMatchObject({ id: "next-message", text: "Continue" });
    },
  );
});

describe("chat transcript", () => {
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
