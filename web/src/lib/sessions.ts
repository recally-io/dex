import { format, isToday } from "date-fns";
import type { Chat } from "../api/generated/types.gen";

export const STALLED_IDLE_MS = 5 * 60 * 1000;
export const STALLED_TOOL_MS = 30 * 60 * 1000;

export function sessionKind(chat: Pick<Chat, "channel" | "main">) {
  if (chat.main) return "Main";

  if (chat.channel === "web") return "Chat";

  if (chat.channel === "automation") return "Automation";

  return "Channel";
}

/** The bot in the route of a session. A child in another bot names it, so its list row opens there. */
export function sessionAgentId(chat: Pick<Chat, "agentId">, agentId: string) {
  return chat.agentId ?? agentId;
}

/**
 * Who started a child session. A parent in another bot has no title in this bot's list, so the
 * name of that bot stands for it. A child that runs in another bot adds the name of that bot.
 */
export function startedBy(
  chat: Pick<Chat, "parentId" | "parentAgentName" | "agentName">,
  parentTitle: string | undefined,
) {
  if (chat.parentId === undefined) return undefined;

  const parent = chat.parentAgentName ?? parentTitle ?? "a chat";

  return chat.agentName ? `${parent} · ${chat.agentName}` : parent;
}

/**
 * The name that a child row shows after "Started by". Main is not in the session list, so only its
 * ID says Main. The list must hold archived sessions too: an archived parent keeps its title.
 */
export function parentName(
  chat: Pick<Chat, "parentId" | "parentAgentName" | "agentName">,
  mainId: number | undefined,
  all: Pick<Chat, "id" | "title">[],
) {
  if (chat.parentId === undefined) return undefined;

  const parent = all.find((item) => item.id === chat.parentId);

  const title =
    chat.parentId === mainId ? "Main" : parent === undefined ? undefined : parent.title || "New session";

  return startedBy(chat, title);
}

export function activeSessionCount(sessions: Pick<Chat, "busy">[]) {
  return sessions.filter((chat) => chat.busy).length;
}

export function sessionTime(at: number) {
  return format(at, isToday(at) ? "HH:mm" : "d MMM, HH:mm");
}

export function formatDuration(ms: number) {
  const seconds = Math.floor(ms / 1000);

  if (seconds < 60) return `${seconds} s`;
  const minutes = Math.floor(seconds / 60);

  if (minutes < 60) return `${minutes} min ${seconds % 60} s`;

  return `${Math.floor(minutes / 60)} h ${minutes % 60} min`;
}

/**
 * A busy session that shows no sign of work. The list sets the activity fields only while the
 * session is busy.
 */
export function isStalled(chat: Pick<Chat, "busy" | "lastActivityAt" | "toolStartedAt">, now = Date.now()) {
  if (!chat.busy) return false;

  if (chat.toolStartedAt !== undefined && now - chat.toolStartedAt > STALLED_TOOL_MS) return true;

  // A busy session with no active tool can be waiting on a broken provider after five minutes.
  return (
    chat.toolStartedAt === undefined &&
    chat.lastActivityAt !== undefined &&
    now - chat.lastActivityAt > STALLED_IDLE_MS
  );
}
