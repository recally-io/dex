import type { AgentActivity, Chat, InboxItem, TaskSummary } from "../api/generated/types.gen";
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

/** The question that waits for the user. An answered question is already on its way and does not count. */
export function openQuestion(chat: Pick<Chat, "question">) {
  return chat.question?.status === "open" ? chat.question : undefined;
}

export function boardBucket(chat: Pick<Chat, "task" | "busy" | "question">): "needs" | "working" | "idle" {
  if (openQuestion(chat) || (chat.task && needsTask(chat.task))) return "needs";

  if (chat.task ? ["running", "admitting", "queued"].includes(chat.task.status) : chat.busy) return "working";

  return "idle";
}

export function needsTask(task: TaskSummary) {
  if (task.status === "running" || task.status === "admitting" || task.status === "queued") return false;

  return taskBucket(task) === "needs-you";
}

export type BotStatus = "needs" | "working" | undefined;

/** The fields of the Work response that a bot status reads. */
type WorkState = {
  sessions: { agentId: string; chat: Pick<Chat, "task" | "busy" | "question"> }[];
  automations: { agentId: string; setup: boolean }[];
  inbox: Pick<InboxItem, "agentId" | "outcome" | "dismissed">[];
};

/**
 * The one status a bot row shows. "needs" wins over "working": a blocked or failed task, an open
 * question, a failed routine run that is not done, or a routine that needs setup. The bot page
 * shows each of these again: tasks and questions in the task panel, routines in the Routines tab.
 */
export function botStatus(
  agentId: string,
  work: WorkState | undefined,
  activity: Pick<AgentActivity, "activeSessions"> | undefined,
): BotStatus {
  const sessions = work?.sessions.filter((item) => item.agentId === agentId) ?? [];

  if (
    sessions.some((item) => boardBucket(item.chat) === "needs") ||
    work?.inbox.some((item) => item.agentId === agentId && item.outcome === "failed" && !item.dismissed) ||
    work?.automations.some((item) => item.agentId === agentId && item.setup)
  )
    return "needs";

  if ((activity?.activeSessions ?? 0) > 0 || sessions.some((item) => boardBucket(item.chat) === "working"))
    return "working";

  return undefined;
}
