import { expect, test } from "vite-plus/test";
import { boardBucket, botStatus, automationNeedsYou, openQuestion } from "./work";

test("setup and any unhandled failed run keep an automation in Needs you", () => {
  expect(automationNeedsYou("a", true, [])).toBe(true);
  expect(automationNeedsYou("a", false, [{ automationId: "a", outcome: "failed", dismissed: false }])).toBe(
    true,
  );
  expect(automationNeedsYou("a", false, [{ automationId: "a", outcome: "failed", dismissed: true }])).toBe(
    false,
  );
});

test("a busy session is Working, and a failed child session does not need the user", () => {
  expect(boardBucket({ busy: true })).toBe("working");
  expect(boardBucket({ busy: false })).toBe("idle");
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

  const question = { id: "q", text: "Which one?", options: [], status: "open" as const };

  expect(
    botStatus("a", { ...empty, sessions: [{ agentId: "a", chat: { busy: true, question } }] }, undefined),
  ).toBe("needs");

  expect(
    botStatus("a", { ...empty, sessions: [{ agentId: "b", chat: { busy: false, question } }] }, undefined),
  ).toBeUndefined();

  expect(botStatus("a", { ...empty, inbox: [failedRun] }, undefined)).toBe("needs");
  expect(botStatus("a", { ...empty, inbox: [{ ...failedRun, dismissed: true }] }, undefined)).toBeUndefined();
  expect(botStatus("a", { ...empty, automations: [{ agentId: "a", setup: true }] }, undefined)).toBe("needs");
});
