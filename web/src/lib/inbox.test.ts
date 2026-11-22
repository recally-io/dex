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

test("a continuation chat pins the environment where the result ran", () => {
  const environments = [
    { id: "box", name: "box", kind: "pi-env", default: true, status: "available" },
    { id: "local", name: "local", kind: "local", default: false, status: "available" },
    { id: "runner", name: "runner", kind: "runner", default: false, status: "available" },
  ];

  expect(continuationEnvironment("runner", environments)).toBe("runner");
  expect(continuationEnvironment("box", environments)).toBe("box");
  expect(continuationEnvironment("local", environments)).toBe("local");
  expect(() => continuationEnvironment("", environments)).toThrow("has no recorded environment");
  expect(() => continuationEnvironment("gone", environments)).toThrow("This result ran in gone");
  expect(() =>
    continuationEnvironment("sandbox", [
      { id: "sandbox", name: "sandbox", kind: "e2b", default: true, status: "available" },
      ...environments,
    ]),
  ).toThrow("ran in sandbox, which cannot continue this run");
});
