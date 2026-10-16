import { expect, test } from "vite-plus/test";
import type { Chat, TaskSummary } from "../api/generated/types.gen";
import { activeSessionCount, sessionKind, taskDuration, taskTimes } from "./tasks";

const task = (id: string, status: TaskSummary["status"], createdAt: number): TaskSummary => ({
  id,
  title: "Same title",
  status,
  createdAt,
  conversationId: createdAt,
  parentConversationId: 1,
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

test("user sees factual times for queued and completed work", () => {
  expect(taskDuration(48_000)).toBe("48 s");
  expect(taskDuration(312_000)).toBe("5 min 12 s");
  expect(taskDuration(3_840_000)).toBe("1 h 4 min");
  expect(taskTimes(task("q", "queued", 1))).toMatch(/^Queued at /);
  expect(taskTimes({ ...task("c", "cancelled", 1), endedAt: 48_001 }, true)).toMatch(
    /^Queued at .* · Ended .* · 48 s$/,
  );
});
