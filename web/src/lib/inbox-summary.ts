import { getInboxSummaryOptions } from "../api/generated/@tanstack/react-query.gen";
import type { AgentActivity, Chat, InboxItem, InboxSummary } from "../api/generated/types.gen";

/** The question that waits for the user. An answered question is already on its way and does not count. */
function openQuestion(chat: Pick<Chat, "question">) {
  return chat.question?.status === "open" ? chat.question : undefined;
}

function needsReview(item: Pick<InboxItem, "dismissed" | "outcome">) {
  return !item.dismissed && (item.outcome === "failed" || item.outcome === "changed");
}

export type BotStatus = "needs" | "working" | undefined;

/** The fields of the InboxSummary response that a bot status reads. */
type BotAttention = {
  sessions: { agentId: string; chat: Pick<Chat, "busy" | "question"> }[];
  automations: { agentId: string; setup: boolean }[];
  inbox: Pick<InboxItem, "agentId" | "outcome" | "dismissed">[];
  taskReviews?: Pick<InboxSummary["taskReviews"][number], "assigneeBotId">[];
};

/**
 * Actionable Inbox items for this bot take priority over its current activity.
 * Held events do not belong to a bot until the owner attaches them.
 */
export function botStatus(
  agentId: string,
  summary: BotAttention | undefined,
  activity: Pick<AgentActivity, "activeSessions"> | undefined,
): BotStatus {
  const sessions = summary?.sessions.filter((item) => item.agentId === agentId) ?? [];

  if (
    sessions.some((item) => openQuestion(item.chat)) ||
    summary?.inbox.some((item) => item.agentId === agentId && needsReview(item)) ||
    summary?.automations.some((item) => item.agentId === agentId && item.setup) ||
    summary?.taskReviews?.some((item) => item.assigneeBotId === agentId)
  )
    return "needs";

  if ((activity?.activeSessions ?? 0) > 0 || sessions.some((item) => item.chat.busy)) return "working";

  return undefined;
}

export const inboxSummaryOptions = () => ({
  ...getInboxSummaryOptions(),
  refetchInterval: 10_000,
});

/** Project only the work items that have an owner action. */
export function attentionItems(summary: InboxSummary | undefined) {
  return {
    questions: (summary?.sessions ?? []).flatMap(({ agentId, chat }) => {
      const question = openQuestion(chat);

      return question ? [{ agentId, chat, question }] : [];
    }),
    results: (summary?.inbox ?? []).filter(needsReview),
    taskReviews: summary?.taskReviews ?? [],
    setup: (summary?.automations ?? []).filter((item) => item.setup),
  };
}
