import { expect, test } from "vite-plus/test";
import type { Chat, InboxSummary } from "../api/generated/types.gen";
import { attentionItems, botStatus } from "./inbox-summary";

test("a bot row shows attention before working, and only for its own items", () => {
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
  expect(botStatus("a", { ...empty, inbox: [{ ...failedRun, outcome: "changed" }] }, undefined)).toBe(
    "needs",
  );
  expect(botStatus("a", { ...empty, inbox: [{ ...failedRun, dismissed: true }] }, undefined)).toBeUndefined();
  expect(botStatus("a", { ...empty, automations: [{ agentId: "a", setup: true }] }, undefined)).toBe("needs");
});

test("Inbox shows open questions, unhandled failures and changes, and missing models across bots", () => {
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

  const work: InboxSummary = {
    taskReviews: [],
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
        environmentId: "local",
        cwd: "",
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
        environmentId: "local",
        cwd: "",
      },
    ],
  };

  work.inbox.push({ ...work.inbox[0], runId: "r3", agentId: "b", outcome: "changed", read: true });
  work.automations.push({ ...work.automations[0], agentId: "b", setup: false });

  const items = attentionItems(work);
  expect(items.questions.map((item) => item.chat.id)).toEqual([1, 3]);
  expect(items.results.map((result) => result.runId)).toEqual(["r1", "r3"]);
  expect(items.setup.map(({ automation }) => automation.id)).toEqual(["auto-1"]);
});
