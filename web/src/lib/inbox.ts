import { listInboxOptions } from "../api/generated/@tanstack/react-query.gen";
import type { Agent, InboxItem } from "../api/generated/types.gen";

export const inboxOptions = () => listInboxOptions({ query: { includeDismissed: true } });

export type InboxFilter = "All" | "Unread" | "Needs you";

export function inboxGroups(items: InboxItem[], agentId: string, filter: InboxFilter) {
  const visible = items.filter(
    (item) =>
      !item.dismissed &&
      (!agentId || item.agentId === agentId) &&
      (filter !== "Unread" || !item.read) &&
      (filter !== "Needs you" || item.outcome === "failed"),
  );

  return {
    needsYou: visible.filter((item) => item.outcome === "failed"),
    results: visible.filter((item) => item.outcome === "changed"),
  };
}

export function inboxCount(agents: Agent[]) {
  return agents.reduce(
    (count, agent) => ({
      unread: count.unread + (agent.activity?.inboxUnread ?? 0),
      failed: count.failed + (agent.activity?.inboxFailed ?? 0),
    }),
    { unread: 0, failed: 0 },
  );
}

// The first line is plain text because the chat takes its title from the start of the message.
export function resultQuote(item: InboxItem) {
  const quote = [
    item.automationName,
    `Outcome: ${item.outcome}`,
    item.summary,
    `Run transcript: /a/${item.agentId}/c/${item.chatId} (run ${item.runId})`,
  ]
    .join("\n\n")
    .split("\n")
    .map((line) => `> ${line}`)
    .join("\n");

  return `Follow up on the ${item.automationName} result (${item.outcome}).\n\n${quote}`;
}
