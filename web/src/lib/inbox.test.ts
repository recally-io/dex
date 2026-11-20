import { expect, test } from "vite-plus/test";
import type { InboxItem } from "../api/generated/types.gen";
import { continuationEnvironment, resultQuote } from "./inbox";

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
  environmentId: "local",
  cwd: "",
};

test("Continue can quote an unchanged run", () => {
  expect(resultQuote({ ...result, outcome: "unchanged" })).toContain("Outcome: unchanged");
});

test("Continue quotes the stored result and identifies the exact run transcript", () => {
  expect(resultQuote(result)).toBe(
    "Follow up on the Release check result (changed).\n\n> Release check\n> \n> Outcome: changed\n> \n> Release available\n> Version 2\n> \n> Run transcript: /a/first/c/4 (run run)",
  );
});

test("a continuation chat runs where the result ran", () => {
  const environments = [
    { id: "sandbox", name: "sandbox", kind: "e2b", default: true, status: "available" },
    { id: "local", name: "local", kind: "local", default: false, status: "available" },
    { id: "box", name: "box", kind: "pi-env", default: false, status: "available" },
  ];

  expect(continuationEnvironment("sandbox", environments)).toBe("");
  expect(continuationEnvironment("box", environments)).toBe("box");
  expect(continuationEnvironment("local", environments)).toBe("local");
  expect(continuationEnvironment("", environments)).toBe("");
  expect(() => continuationEnvironment("gone", environments)).toThrow("This result ran in gone");
  expect(() =>
    continuationEnvironment("other-sandbox", [
      ...environments,
      { id: "other-sandbox", name: "other", kind: "e2b", default: false, status: "available" },
    ]),
  ).toThrow("ran in other");
});
