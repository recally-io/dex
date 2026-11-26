import { expect, test } from "vite-plus/test";
import type { AutomationResult } from "../api/generated/types.gen";
import { continuationRunner, resultQuote } from "./automation-results";

const result: AutomationResult = {
  runId: "run",
  agentId: "first",
  agentName: "First",
  automationId: "routine",
  automationName: "Release check",
  sessionId: 4,
  outcome: "changed",
  summary: "Release available\nVersion 2",
  createdAt: 1,
  read: false,
  dismissed: false,
  runnerId: "local",
  cwd: "",
};

test("Continue can quote an unchanged run", () => {
  expect(resultQuote({ ...result, outcome: "unchanged" })).toContain("Outcome: unchanged");
});

test("Continue quotes the stored result and identifies the exact run transcript", () => {
  expect(resultQuote(result)).toBe(
    "Follow up on the Release check result (changed).\n\n> Release check\n> \n> Outcome: changed\n> \n> Release available\n> Version 2\n> \n> Run transcript: /a/first/s/4 (run run)",
  );
});

test("a continuation session pins the environment where the result ran", () => {
  const runners = [
    { id: "box", name: "box", kind: "ssh", default: true, status: "available" },
    { id: "local", name: "local", kind: "local", default: false, status: "available" },
    { id: "runner", name: "runner", kind: "runner", default: false, status: "available" },
  ];

  expect(() =>
    continuationRunner("offline", [
      { id: "offline", name: "Runner A", kind: "runner", status: "offline", default: false },
    ]),
  ).toThrow("Try again when the Outbound Machine reconnects.");
  expect(continuationRunner("runner", runners)).toBe("runner");
  expect(continuationRunner("box", runners)).toBe("box");
  expect(continuationRunner("local", runners)).toBe("local");
  expect(() => continuationRunner("", runners)).toThrow("has no recorded Runner");
  expect(() => continuationRunner("gone", runners)).toThrow("This result ran in gone");
  expect(() =>
    continuationRunner("sandbox", [
      { id: "sandbox", name: "sandbox", kind: "e2b", default: true, status: "available" },
      ...runners,
    ]),
  ).toThrow("ran in sandbox, which cannot continue this run");
});
