import { expect, test } from "vite-plus/test";
import type { TaskSummary } from "../api/generated/types.gen";
import { needsTask } from "./work";

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
