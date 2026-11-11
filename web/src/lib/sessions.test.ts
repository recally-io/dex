import { expect, test } from "vite-plus/test";
import type { Chat } from "../api/generated/types.gen";
import {
  activeSessionCount,
  formatDuration,
  isStalled,
  sessionAgentId,
  sessionKind,
  startedBy,
} from "./sessions";

test("user sees one count for every busy session", () => {
  expect(activeSessionCount([{ busy: true }, { busy: true }, { busy: false }])).toBe(2);
  expect(activeSessionCount([])).toBe(0);
});

test("user can filter each session kind, and a child session is a Chat", () => {
  const kind = (channel: Chat["channel"], main = false) => sessionKind({ channel, main });

  expect(kind("web", true)).toBe("Main");
  expect(kind("web")).toBe("Chat");
  expect(kind("automation")).toBe("Automation");

  for (const channel of ["telegram", "wechat", "feishu"] as const) expect(kind(channel)).toBe("Channel");
});

test("a busy session is stalled after a long tool or long silence, and an idle one never is", () => {
  const now = 10_000_000;

  expect(isStalled({ busy: true, lastActivityAt: now - 6 * 60_000 }, now)).toBe(true);
  expect(isStalled({ busy: true, lastActivityAt: now - 4 * 60_000 }, now)).toBe(false);
  expect(isStalled({ busy: true, lastActivityAt: now - 6 * 60_000, toolStartedAt: now - 60_000 }, now)).toBe(
    false,
  );
  expect(isStalled({ busy: true, toolStartedAt: now - 31 * 60_000 }, now)).toBe(true);
  expect(isStalled({ busy: true }, now)).toBe(false);
  expect(isStalled({ busy: false, lastActivityAt: now - 60 * 60_000 }, now)).toBe(false);
});

test("user sees factual durations", () => {
  expect(formatDuration(48_000)).toBe("48 s");
  expect(formatDuration(312_000)).toBe("5 min 12 s");
  expect(formatDuration(3_840_000)).toBe("1 h 4 min");
});

test("a child session in another bot opens in that bot and names it", () => {
  expect(sessionAgentId({}, "default")).toBe("default");
  expect(sessionAgentId({ agentId: "coder" }, "default")).toBe("coder");

  // No parent, no byline.
  expect(startedBy({}, undefined)).toBeUndefined();
  // A parent in this bot has a title in the list.
  expect(startedBy({ parentId: 1 }, "Main")).toBe("Main");
  expect(startedBy({ parentId: 1 }, undefined)).toBe("a chat");
  // A child in another bot adds that bot, and a parent in another bot is named by its bot.
  expect(startedBy({ parentId: 1, agentName: "Coder" }, "Main")).toBe("Main · Coder");
  expect(startedBy({ parentId: 1, parentAgentName: "Dex" }, undefined)).toBe("Dex");
});
