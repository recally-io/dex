import { expect, test } from "vite-plus/test";
import { hasContext } from "./timeline-actions";
import type { Item } from "../api/generated/types.gen";

test("clear stays hidden for empty context after earlier messages load", () => {
  // Given an empty context or a reset with old history before it.
  const old: Item = { kind: "user", id: "u1", text: "Earlier input" };
  const reset: Item = { kind: "boundary", id: "e2", boundary: "reset" };
  const compact: Item = { kind: "boundary", id: "e3", boundary: "compaction" };

  // Then only items after the reset make Clear context available.
  expect(hasContext([])).toBe(false);
  expect(hasContext([old, reset])).toBe(false);
  expect(hasContext([old, reset, compact])).toBe(false);
  expect(hasContext([old, compact])).toBe(true);
  expect(hasContext([reset, { ...old, id: "u2" }])).toBe(true);
});
