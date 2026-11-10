import type { Chat, InboxItem, TaskSummary } from "../api/generated/types.gen";
import { taskBucket } from "./tasks";

export function automationNeedsYou(
  id: string,
  setup: boolean,
  items: Pick<InboxItem, "automationId" | "outcome" | "dismissed">[],
) {
  return (
    setup || items.some((item) => item.automationId === id && item.outcome === "failed" && !item.dismissed)
  );
}

export function boardBucket(chat: Pick<Chat, "task" | "busy">): "needs" | "working" | "idle" {
  if (chat.task && needsTask(chat.task)) return "needs";

  if (chat.task ? ["running", "admitting", "queued"].includes(chat.task.status) : chat.busy) return "working";

  return "idle";
}

export function needsTask(task: TaskSummary) {
  if (task.status === "running" || task.status === "admitting" || task.status === "queued") return false;

  return taskBucket(task) === "needs-you";
}
