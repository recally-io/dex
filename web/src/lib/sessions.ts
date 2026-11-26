import { format, isToday } from "date-fns";
import type { Session } from "../api/generated/types.gen";

const STALLED_IDLE_MS = 5 * 60 * 1000;
const STALLED_TOOL_MS = 30 * 60 * 1000;

export function sessionKind(session: Pick<Session, "channel" | "main">) {
  if (session.main) return "Main";

  if (session.channel === "web") return "Session";

  if (session.channel === "automation") return "Automation";

  return "Channel";
}

/** The bot in the route of a session. A child in another bot names it, so its list row opens there. */
export function sessionAgentId(session: Pick<Session, "agentId">, agentId: string) {
  return session.agentId ?? agentId;
}

/**
 * Who started a child session. A parent in another bot has no title in this bot's list, so the
 * name of that bot stands for it. A child that runs in another bot adds the name of that bot.
 */
function startedBy(
  session: Pick<Session, "parentId" | "parentAgentName" | "agentName">,
  parentTitle: string | undefined,
) {
  if (session.parentId === undefined) return undefined;

  const parent = session.parentAgentName ?? parentTitle ?? "a session";

  return session.agentName ? `${parent} · ${session.agentName}` : parent;
}

/**
 * The name that a child row shows after "Started by". Main is not in the session list, so only its
 * ID says Main. The list must hold archived sessions too: an archived parent keeps its title.
 */
export function parentName(
  session: Pick<Session, "parentId" | "parentAgentName" | "agentName">,
  mainId: number | undefined,
  all: Pick<Session, "id" | "title">[],
) {
  if (session.parentId === undefined) return undefined;

  const parent = all.find((item) => item.id === session.parentId);

  const title =
    session.parentId === mainId ? "Main" : parent === undefined ? undefined : parent.title || "New session";

  return startedBy(session, title);
}

export function activeSessionCount(sessions: Pick<Session, "busy">[]) {
  return sessions.filter((session) => session.busy).length;
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
export function isStalled(
  session: Pick<Session, "busy" | "lastActivityAt" | "toolStartedAt">,
  now = Date.now(),
) {
  if (!session.busy) return false;

  if (session.toolStartedAt !== undefined && now - session.toolStartedAt > STALLED_TOOL_MS) return true;

  // A busy session with no active tool can be waiting on a broken provider after five minutes.
  return (
    session.toolStartedAt === undefined &&
    session.lastActivityAt !== undefined &&
    now - session.lastActivityAt > STALLED_IDLE_MS
  );
}
