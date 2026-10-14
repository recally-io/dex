import { describe, expect, test } from "vite-plus/test";
import { applyChatEvent, emptyChatState } from "./chat-state";
import type { ChatEvent } from "../api/generated/types.gen";

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
