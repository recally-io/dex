import { format, isToday } from "date-fns";
import type { Chat, TaskSummary } from "../api/generated/types.gen";

export function sessionKind(chat: Pick<Chat, "channel" | "main">) {
  if (chat.main) return "Main";

  if (chat.channel === "web") return "Chat";

  if (chat.channel === "task") return "Task";

  if (chat.channel === "automation") return "Automation";

  return "Channel";
}

export function activeSessionCount(sessions: Pick<Chat, "busy" | "task">[]) {
  return sessions.filter((chat) =>
    chat.task ? chat.task.status === "running" || chat.task.status === "queued" : chat.busy,
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
