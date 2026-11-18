import { expect, test } from "vite-plus/test";
import type { Chat, Work } from "../api/generated/types.gen";
import { botStatus, needsItems } from "./work";

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
    botStatus(
      "a",
      {
        ...empty,
        sessions: [{ agentId: "a", chat: { busy: false, question: { ...question, status: "answered" } } }],
      },
      undefined,
    ),
  ).toBeUndefined();

  expect(
    botStatus("a", { ...empty, sessions: [{ agentId: "b", chat: { busy: false, question } }] }, undefined),
  ).toBeUndefined();

  expect(botStatus("a", { ...empty, inbox: [failedRun] }, undefined)).toBe("needs");
  expect(botStatus("a", { ...empty, inbox: [{ ...failedRun, dismissed: true }] }, undefined)).toBeUndefined();
  expect(botStatus("a", { ...empty, automations: [{ agentId: "a", setup: true }] }, undefined)).toBe("needs");
});

test("needsItems keeps open questions, unhandled failed runs, and setup automations for one bot", () => {
  const openQ = { id: "q1", text: "Pick one", options: ["A"], status: "open" as const };
  const answeredQ = { id: "q2", text: "Old", options: ["A"], status: "answered" as const };

  const chat = {
    id: 1,
    channel: "web",
    main: true,
    readOnly: false,
    title: "Main",
    busy: false,
    settings: {},
    cwd: "",
    createdAt: 0,
    updatedAt: 0,
  } satisfies Chat;

  const work: Work = {
    sessions: [
      { agentId: "a", chat: { ...chat, id: 1, question: openQ } },
      { agentId: "a", chat: { ...chat, id: 2, question: answeredQ } },
      { agentId: "b", chat: { ...chat, id: 3, question: openQ } },
    ],
    automations: [
      {
        agentId: "a",
        setup: true,
        automation: {
          id: "auto-1",
          name: "Daily",
          cwd: "",
          prompt: "",
          session: "new",
          enabled: true,
          nextRuns: [],
          createdAt: 0,
          updatedAt: 0,
          schedule: { kind: "every", minutes: 60 },
          timeZone: "UTC",
        },
      },
    ],
    inbox: [
      {
        runId: "r1",
        agentId: "a",
        agentName: "Dex",
        automationId: "auto-1",
        automationName: "Daily",
        chatId: 10,
        outcome: "failed",
        summary: "boom",
        createdAt: 1,
        read: false,
        dismissed: false,
      },
      {
        runId: "r2",
        agentId: "a",
        agentName: "Dex",
        automationId: "auto-1",
        automationName: "Daily",
        chatId: 11,
        outcome: "failed",
        summary: "old",
        createdAt: 1,
        read: true,
        dismissed: true,
      },
    ],
  };

  const items = needsItems("a", work);
  expect(items.questions.map((c) => c.id)).toEqual([1]);
  expect(items.results.map((r) => r.runId)).toEqual(["r1"]);
  expect(items.setup.map((a) => a.id)).toEqual(["auto-1"]);
});
