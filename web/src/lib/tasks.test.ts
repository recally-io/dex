import { expect, test } from "vite-plus/test";
import type { Chat, TaskSummary } from "../api/generated/types.gen";
import {
  activeSessionCount,
  isStalled,
  orderTasks,
  pruneSeen,
  sessionKind,
  taskBucket,
  taskDuration,
  taskTimes,
} from "./tasks";

const task = (
  id: string,
  status: TaskSummary["status"],
  createdAt: number,
  extra: Partial<TaskSummary> = {},
): TaskSummary => ({
  id,
  title: "Same title",
  status,
  createdAt,
  conversationId: createdAt,
  parentConversationId: 1,
  ...extra,
});

test("user sees one count for every active session pill", () => {
  // Task status owns its pill, even when its worker input is still busy.
  const sessions = [
    ...(["queued", "running", "done", "failed", "cancelled", "interrupted"] as const).map((status) => ({
      busy: true,
      task: task(status, status, 1),
    })),
    { busy: true },
    { busy: true },
    { busy: false },
  ];

  expect(activeSessionCount(sessions)).toBe(4);
  expect(activeSessionCount([])).toBe(0);
});

test("user can filter each session kind", () => {
  const kind = (channel: Chat["channel"], main = false) => sessionKind({ channel, main });

  expect(kind("web", true)).toBe("Main");
  expect(kind("web")).toBe("Chat");
  expect(kind("task")).toBe("Task");
  expect(kind("automation")).toBe("Automation");

  for (const channel of ["telegram", "wechat", "feishu"] as const) expect(kind(channel)).toBe("Channel");
});

test("user sees task exceptions before active and completed work", () => {
  const now = 1_000_000;

  const make = (id: string, status: TaskSummary["status"], extra: Partial<TaskSummary> = {}) =>
    task(id, status, now - 1_000, extra);

  expect(isStalled(make("idle", "running", { lastActivityAt: now - 6 * 60_000 }), now)).toBe(true);
  expect(taskBucket(make("blocked", "done", { reportOutcome: "blocked" }), now)).toBe("needs-you");
  expect(
    orderTasks(
      [make("done", "done"), make("queued", "queued"), make("need", "failed"), make("running", "running")],
      now,
    ).map((item) => item.id),
  ).toEqual(["need", "running", "queued", "done"]);
});

test("user keeps only recent completed task ids in seen storage", () => {
  expect(pruneSeen({ fresh: 999_999, old: 1_000_000 - 25 * 60 * 60 * 1000 }, 1_000_000)).toEqual({
    fresh: 999_999,
  });
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
