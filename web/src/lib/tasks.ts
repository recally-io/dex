import { format, isToday } from "date-fns";
import type { Chat, TaskSummary } from "../api/generated/types.gen";

export const STALLED_IDLE_MS = 5 * 60 * 1000;
export const STALLED_TOOL_MS = 30 * 60 * 1000;
const SEEN_TTL_MS = 24 * 60 * 60 * 1000;

export type TaskBucket = "needs-you" | "running" | "queued" | "done";

export function sessionKind(chat: Pick<Chat, "channel" | "main">) {
  if (chat.main) return "Main";

  if (chat.channel === "web") return "Chat";

  if (chat.channel === "task") return "Task";

  if (chat.channel === "automation") return "Automation";

  return "Channel";
}

export function activeSessionCount(sessions: Pick<Chat, "busy" | "task">[]) {
  return sessions.filter((chat) =>
    chat.task ? ["running", "admitting", "queued"].includes(chat.task.status) : chat.busy,
  ).length;
}

export function taskTime(at: number, full = false) {
  return format(at, full || !isToday(at) ? "d MMM, HH:mm" : "HH:mm");
}

export function taskDuration(ms: number) {
  const seconds = Math.floor(ms / 1000);

  if (seconds < 60) return `${seconds} s`;
  const minutes = Math.floor(seconds / 60);

  if (minutes < 60) return `${minutes} min ${seconds % 60} s`;

  return `${Math.floor(minutes / 60)} h ${minutes % 60} min`;
}

export function taskTimes(task: TaskSummary, detail = false) {
  const start =
    task.startedAt === undefined
      ? `Queued at ${taskTime(task.createdAt, detail)}`
      : `Started ${taskTime(task.startedAt, detail)}`;

  if (task.endedAt === undefined) return start;
  const ended = detail ? ` · Ended ${taskTime(task.endedAt)}` : "";

  return `${start}${ended} · ${taskDuration(task.endedAt - (task.startedAt ?? task.createdAt))}`;
}

export function isStalled(task: TaskSummary, now = Date.now()) {
  if (task.status !== "running") return false;

  if (task.toolStartedAt !== undefined && now - task.toolStartedAt > STALLED_TOOL_MS) return true;

  // A running task with no active tool can be waiting on a broken provider after five minutes.
  return (
    task.toolStartedAt === undefined &&
    task.lastActivityAt !== undefined &&
    now - task.lastActivityAt > STALLED_IDLE_MS
  );
}

export function taskBucket(task: TaskSummary, now = Date.now()): TaskBucket {
  if (
    task.reportOutcome === "blocked" ||
    task.status === "failed" ||
    task.status === "interrupted" ||
    isStalled(task, now)
  )
    return "needs-you";

  if (task.status === "running" || task.status === "admitting") return "running";

  if (task.status === "queued") return "queued";

  return "done";
}

export function orderTasks(tasks: TaskSummary[], now = Date.now()) {
  const order: TaskBucket[] = ["needs-you", "running", "queued", "done"];

  return [...tasks].sort((a, b) => {
    const bucket = order.indexOf(taskBucket(a, now)) - order.indexOf(taskBucket(b, now));

    if (bucket) return bucket;

    return (b.startedAt ?? b.createdAt) - (a.startedAt ?? a.createdAt);
  });
}

export function groupTasksByChat(tasks: TaskSummary[], chats: Pick<Chat, "id" | "title">[]) {
  const titles = new Map(chats.map((chat) => [chat.id, chat.title || "Chat"]));

  return Map.groupBy(tasks, (task) => titles.get(task.parentConversationId) ?? "Chat");
}

function seenKey(agentId: string) {
  return `dex-task-seen:${agentId}`;
}

export function pruneSeen(entries: Record<string, number>, now = Date.now()) {
  return Object.fromEntries(Object.entries(entries).filter(([, at]) => now - at < SEEN_TTL_MS));
}

export function readSeen(agentId: string, now = Date.now()) {
  try {
    const raw = localStorage.getItem(seenKey(agentId));
    // SAFETY: localStorage is untrusted input; pruneSeen filters the parsed record before use.
    const entries = raw ? (JSON.parse(raw) as Record<string, number>) : {};
    const pruned = pruneSeen(entries, now);

    if (Object.keys(pruned).length !== Object.keys(entries).length)
      localStorage.setItem(seenKey(agentId), JSON.stringify(pruned));

    return pruned;
  } catch {
    return {};
  }
}

export function markTaskSeen(agentId: string, taskId: string, now = Date.now()) {
  try {
    const entries = pruneSeen(readSeen(agentId, now), now);
    entries[taskId] = now;
    localStorage.setItem(seenKey(agentId), JSON.stringify(entries));

    return entries;
  } catch {
    return {};
  }
}
