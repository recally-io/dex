import { expect, test } from "vite-plus/test";
import type { Task } from "../api/generated/types.gen";
import { activeTaskCount, orderedTasks, taskDuration, taskTimes } from "./tasks";

const task = (id: string, status: Task["status"], createdAt: number): Task => ({
  id,
  title: "Same title",
  status,
  createdAt,
  report: "",
  conversationId: createdAt,
});

test("user sees queued and running work in the active count", () => {
  // Given queued work, running work and four settled outcomes.
  const tasks = [
    task("q", "queued", 2),
    task("r", "running", 1),
    task("d", "done", 3),
    task("f", "failed", 4),
    task("c", "cancelled", 5),
    task("i", "interrupted", 6),
  ];

  // Then the badge counts both active states and the list puts active work first.
  expect(activeTaskCount(tasks)).toBe(2);
  expect(orderedTasks(tasks).map((record) => record.id)).toEqual(["r", "q", "i", "c", "f", "d"]);
  expect(activeTaskCount([])).toBe(0);
});

test("user sees factual times for queued and completed work", () => {
  expect(taskDuration(48_000)).toBe("48 s");
  expect(taskDuration(312_000)).toBe("5 min 12 s");
  expect(taskDuration(3_840_000)).toBe("1 h 4 min");
  expect(taskTimes(task("q", "queued", 1))).toMatch(/^Queued at /);
  expect(taskTimes({ ...task("c", "cancelled", 1), endedAt: 48_001 }, true)).toMatch(
    /^Queued at .* · Ended .* · 48 s$/,
  );
});
