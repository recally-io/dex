import { expect, test } from "vite-plus/test";
import type { TaskSummary } from "../api/generated/types.gen";
import { boardBucket, needsTask } from "./work";

const task: TaskSummary = {
  id: "task",
  title: "Work",
  status: "done",
  createdAt: 1,
  endedAt: 2,
  conversationId: 1,
  parentConversationId: 2,
  reportOutcome: "blocked",
};

test("blocked and failed tasks need action until handled", () => {
  expect(needsTask(task)).toBe(true);
  expect(needsTask({ ...task, handledAt: 3 })).toBe(false);
  expect(needsTask({ ...task, status: "interrupted", reportOutcome: undefined })).toBe(true);
  expect(needsTask({ ...task, status: "running", endedAt: undefined })).toBe(false);
});

test("running tasks stay in Working even when activity is old", () => {
  expect(
    boardBucket({ task: { ...task, status: "running", endedAt: undefined, lastActivityAt: 1 }, busy: true }),
  ).toBe("working");
  expect(boardBucket({ task, busy: false })).toBe("needs");
  expect(boardBucket({ task: { ...task, handledAt: 3 }, busy: false })).toBe("idle");
});
