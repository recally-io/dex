import { expect, test } from "vite-plus/test";
import type { InboxItem } from "../api/generated/types.gen";
import { resultQuote } from "./inbox";

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

test("Continue can quote an unchanged run", () => {
  expect(resultQuote({ ...result, outcome: "unchanged" })).toContain("Outcome: unchanged");
});

test("Continue quotes the stored result and identifies the exact run transcript", () => {
  expect(resultQuote(result)).toBe(
    "Follow up on the Release check result (changed).\n\n> Release check\n> \n> Outcome: changed\n> \n> Release available\n> Version 2\n> \n> Run transcript: /a/first/c/4 (run run)",
  );
});
