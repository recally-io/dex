import { expect, test } from "vite-plus/test";
import type { TaskSummary } from "../api/generated/types.gen";
import { boardBucket, botStatus, needsTask, automationNeedsYou, openQuestion } from "./work";

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

test("an open question needs the user until it is answered", () => {
  const question = { id: "q", text: "Which one?", options: [], status: "open" as const };

  expect(openQuestion({ question })).toEqual(question);
  expect(boardBucket({ busy: false, question })).toBe("needs");
  expect(openQuestion({ question: { ...question, status: "answered" } })).toBeUndefined();
  expect(boardBucket({ busy: false, question: { ...question, status: "answered" } })).toBe("idle");
  expect(boardBucket({ busy: false })).toBe("idle");
});

test("a bot row shows needs you before working, and only for its own items", () => {
  const empty = { sessions: [], automations: [], inbox: [] };
  const failedRun = { agentId: "a", outcome: "failed", dismissed: false } as const;

  expect(botStatus("a", empty, { activeSessions: 0 })).toBeUndefined();
  expect(botStatus("a", empty, { activeSessions: 1 })).toBe("working");

  expect(botStatus("a", { ...empty, sessions: [{ agentId: "a", chat: { busy: true } }] }, undefined)).toBe(
    "working",
  );

  expect(
    botStatus(
      "a",
      { ...empty, sessions: [{ agentId: "a", chat: { busy: false, task } }] },
      { activeSessions: 1 },
    ),
  ).toBe("needs");

  expect(
    botStatus("a", { ...empty, sessions: [{ agentId: "b", chat: { busy: false, task } }] }, undefined),
  ).toBeUndefined();

  expect(botStatus("a", { ...empty, inbox: [failedRun] }, undefined)).toBe("needs");
  expect(botStatus("a", { ...empty, inbox: [{ ...failedRun, dismissed: true }] }, undefined)).toBeUndefined();
  expect(botStatus("a", { ...empty, automations: [{ agentId: "a", setup: true }] }, undefined)).toBe("needs");
});
