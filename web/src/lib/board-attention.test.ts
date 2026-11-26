import { expect, test } from "vite-plus/test";
import type { BoardAttention, HeldEvent } from "../api/generated/types.gen";
import { attentionItems, botStatus } from "./board-attention";

const empty: BoardAttention = { sessions: [], automations: [], results: [], taskReviews: [] };
const main = { botId: "a", id: 1, title: "Main", main: true, busy: true };

const result = {
  runId: "run",
  botId: "a",
  automationId: "auto",
  automationName: "Daily",
  sessionId: 10,
  outcome: "failed",
  summary: "Failed",
  createdAt: 1,
  read: false,
  runnerId: "local",
  cwd: "",
} as const;

const held: HeldEvent = {
  id: "held",
  namespace: "source",
  eventType: "review",
  objects: [],
  reason: "unmatched",
  content: "Review",
  sourceUrl: "",
  authorLogin: "",
  authorAssociation: "",
  occurredAt: 1,
  heldAt: 1,
};

test("Board and the sidebar count each Owner action, with held events only across all Bots", () => {
  const board: BoardAttention = {
    sessions: [{ ...main, question: { id: "q", text: "Which one?", options: ["A"] } }],
    automations: [
      { id: "auto", botId: "a", name: "Daily", setup: true },
      { id: "ready", botId: "b", name: "Ready", setup: false },
    ],
    results: [result, { ...result, runId: "other", botId: "b", outcome: "changed", read: true }],
    taskReviews: [{ key: "T-1", title: "Review it", assigneeBotId: "b" }],
  };

  const heldPage = { events: [held], total: 2 };
  const items = attentionItems(board, heldPage);
  expect(items.count).toBe(7);
  expect(items.needsYouCount).toBe(5);
  expect(items.questions.map(({ session }) => session.id)).toEqual([1]);
  expect(items.results.map(({ runId }) => runId)).toEqual(["run", "other"]);
  expect(items.setup.map(({ id }) => id)).toEqual(["auto"]);
  expect(attentionItems(board, heldPage, "a").count).toBe(3);
  expect(attentionItems(board, heldPage, "b").count).toBe(2);
  expect(attentionItems(board, heldPage, "c").count).toBe(0);
});

test("Owner actions take priority over work on the Bot that owns them", () => {
  const working = { ...empty, sessions: [main] };
  const review = { key: "T-1", title: "Review it", assigneeBotId: "a" };

  expect(botStatus("a", empty, undefined)).toBeUndefined();
  expect(botStatus("a", empty, { activeSessions: 1 })).toBe("working");
  expect(botStatus("a", working, undefined)).toBe("working");
  expect(botStatus("a", { ...working, taskReviews: [review] }, undefined)).toBe("needs");
  expect(botStatus("b", { ...working, taskReviews: [review] }, undefined)).toBeUndefined();
});
