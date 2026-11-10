import type { TaskSummary } from "../api/generated/types.gen";
import { taskBucket } from "./tasks";

export function needsTask(task: TaskSummary) {
  if (task.status === "running" || task.status === "admitting" || task.status === "queued") return false;

  return taskBucket(task) === "needs-you";
}
