import type { Chat, TaskSummary } from "../api/generated/types.gen";
import { taskBucket } from "./tasks";

export function boardBucket(chat: Pick<Chat, "task" | "busy">): "needs" | "working" | "idle" {
  if (chat.task && needsTask(chat.task)) return "needs";

  if (chat.task ? ["running", "admitting", "queued"].includes(chat.task.status) : chat.busy) return "working";

  return "idle";
}

export function needsTask(task: TaskSummary) {
  if (task.status === "running" || task.status === "admitting" || task.status === "queued") return false;

  return taskBucket(task) === "needs-you";
}
