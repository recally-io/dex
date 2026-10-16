import { format, isToday } from "date-fns";
import type { TaskSummary } from "../api/generated/types.gen";

export function activeTaskCount(tasks: Pick<TaskSummary, "status">[]) {
  return tasks.filter((task) => task.status === "running" || task.status === "queued").length;
}

export function orderedTasks(tasks: TaskSummary[]) {
  const rank = (task: TaskSummary) => (task.status === "running" ? 0 : task.status === "queued" ? 1 : 2);

  return tasks.toSorted((a, b) => rank(a) - rank(b) || b.createdAt - a.createdAt || b.id.localeCompare(a.id));
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
