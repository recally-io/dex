import { expect, test } from "vite-plus/test";
import type { TaskSummary } from "../api/generated/types.gen";
import { boardBucket, needsTask, automationNeedsYou, parseWorkSearch, resolveRange, runTone } from "./work";

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

test("setup and any unhandled failed run keep an automation in Needs you", () => {
  expect(automationNeedsYou("a", true, [])).toBe(true);
  expect(automationNeedsYou("a", false, [{ automationId: "a", outcome: "failed", dismissed: false }])).toBe(
    true,
  );
  expect(automationNeedsYou("a", false, [{ automationId: "a", outcome: "failed", dismissed: true }])).toBe(
    false,
  );
});

test("running tasks stay in Working even when activity is old", () => {
  expect(
    boardBucket({ task: { ...task, status: "running", endedAt: undefined, lastActivityAt: 1 }, busy: true }),
  ).toBe("working");
  expect(boardBucket({ task, busy: false })).toBe("needs");
  expect(boardBucket({ task: { ...task, handledAt: 3 }, busy: false })).toBe("idle");
});

test("Work search keeps valid fields and drops invalid ones", () => {
  expect(parseWorkSearch({ view: "calendar", range: "2026-11-10", kind: "repeat" })).toEqual({
    view: "calendar",
    range: "2026-11-10",
    kind: "repeat",
  });
  expect(parseWorkSearch({ view: "table", range: "next week", kind: "all" })).toEqual({
    view: undefined,
    range: undefined,
    kind: undefined,
  });
  expect(parseWorkSearch({ range: "today" }).range).toBe("today");
});

test("Work range parses a day in local time and ignores days outside the week", () => {
  // Tuesday 10 Nov 2026, local time. A UTC parse of the day string would shift it west of UTC.
  const now = new Date(2026, 10, 10, 15, 0).getTime();
  const day = resolveRange("2026-11-12", now);
  expect(day.name).toBe("day");
  expect(day.start).toEqual(new Date(2026, 10, 12));
  expect(day.end).toEqual(new Date(2026, 10, 13));
  expect(resolveRange("2026-11-16", now).name).toBe("week");
  expect(resolveRange("2026-02-30", now).name).toBe("week");
  expect(resolveRange(undefined, now).start).toEqual(new Date(2026, 10, 9));
  expect(resolveRange("today", now).start).toEqual(new Date(2026, 10, 10));
});

test("calendar run tone follows the inbox state of the run", () => {
  const done = { status: "done", outcome: "changed" } as const;
  expect(runTone({ status: "running", outcome: "" }, undefined)).toBe("working");
  expect(runTone(done, { outcome: "failed", read: false, dismissed: false })).toBe("needs");
  expect(runTone(done, { outcome: "failed", read: true, dismissed: true })).toBe("quiet");
  expect(runTone(done, { outcome: "changed", read: false, dismissed: false })).toBe("result");
  expect(runTone(done, { outcome: "changed", read: true, dismissed: false })).toBe("quiet");
  expect(runTone({ status: "skipped", outcome: "" }, undefined)).toBe("quiet");
});
