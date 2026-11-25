import type { BoardStatus, BoardTaskCreate } from "../api/generated/types.gen";

export const statusLabel: Record<BoardStatus, string> = {
  backlog: "Backlog",
  ready: "Ready",
  in_progress: "In Progress",
  in_review: "In Review",
  done: "Done",
  cancelled: "Cancelled",
};

export const initialStatuses = [
  "backlog",
  "ready",
  "in_progress",
] as const satisfies readonly BoardTaskCreate["status"][];

export function nextStatuses(status: BoardStatus, hasReviewer: boolean): BoardStatus[] {
  switch (status) {
    case "done":
    case "cancelled":
      return ["in_progress"];
    case "in_review":
      return ["cancelled"];
    case "in_progress":
      return hasReviewer
        ? ["backlog", "ready", "in_review", "cancelled"]
        : ["backlog", "ready", "in_review", "done", "cancelled"];
    case "backlog":
      return ["ready", "in_progress", "cancelled"];
    case "ready":
      return ["backlog", "in_progress", "cancelled"];
  }
}

export function isClosed(status: BoardStatus) {
  return status === "done" || status === "cancelled";
}
