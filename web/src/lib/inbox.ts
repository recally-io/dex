import { listInboxOptions } from "../api/generated/@tanstack/react-query.gen";
import type { InboxItem } from "../api/generated/types.gen";

export const inboxOptions = () => listInboxOptions({ query: { includeDismissed: true } });

// The first line is plain text because the chat takes its title from the start of the message.
export function resultQuote(
  item: Pick<InboxItem, "automationName" | "summary" | "agentId" | "chatId" | "runId"> & {
    outcome: InboxItem["outcome"] | "unchanged" | "unknown";
  },
) {
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
