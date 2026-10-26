import { expect, test } from "vite-plus/test";
import type { Agent, InboxItem } from "../api/generated/types.gen";
import { inboxCount, inboxGroups, resultQuote } from "./inbox";

const result: InboxItem = {
  runId: "run",
  agentId: "first",
  agentName: "First",
  automationId: "routine",
  automationName: "Release check",
  chatId: 4,
  outcome: "changed",
  summary: "Release available\nVersion 2",
  createdAt: 1,
  read: false,
  dismissed: false,
};

test("Inbox filters and groups keep bot and read state independent", () => {
  const failed: InboxItem = { ...result, runId: "failure", agentId: "second", outcome: "failed", read: true };
  const items = [result, failed, { ...result, runId: "dismissed", dismissed: true }];
  expect(inboxGroups(items, "", "All")).toEqual({ needsYou: [failed], results: [result] });
  expect(inboxGroups(items, "", "Unread")).toEqual({ needsYou: [], results: [result] });
  expect(inboxGroups(items, "first", "Needs you")).toEqual({ needsYou: [], results: [] });
  expect(inboxGroups(items, "second", "Needs you")).toEqual({ needsYou: [failed], results: [] });
});

test("rail counts sum the shared agent response, including zero activity", () => {
  const base = { provider: "faux", modelId: "echo", thinkingLevel: "off" as const, instructions: "" };

  const agents: Agent[] = [
    {
      ...base,
      name: "First",
      activity: {
        activeSessions: 0,
        enabledAutomations: 1,
        runningChannels: 0,
        inboxUnread: 2,
        inboxFailed: 0,
        routineFailed: false,
      },
    },
    {
      ...base,
      name: "Second",
      activity: {
        activeSessions: 0,
        enabledAutomations: 1,
        runningChannels: 0,
        inboxUnread: 1,
        inboxFailed: 1,
        routineFailed: true,
      },
    },
    { ...base, name: "New bot" },
  ];

  expect(inboxCount(agents)).toEqual({ unread: 3, failed: 1 });
  expect(inboxCount([])).toEqual({ unread: 0, failed: 0 });
});

test("Continue quotes the stored result and identifies the exact run transcript", () => {
  expect(resultQuote(result)).toBe(
    "Follow up on the Release check result (changed).\n\n> Release check\n> \n> Outcome: changed\n> \n> Release available\n> Version 2\n> \n> Run transcript: /a/first/c/4 (run run)",
  );
});
