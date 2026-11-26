import { getBoardAttentionOptions, listHeldEventsOptions } from "../api/generated/@tanstack/react-query.gen";
import type { AgentActivity, BoardAttention, HeldEvent } from "../api/generated/types.gen";

export const boardAttentionOptions = () => ({ ...getBoardAttentionOptions(), refetchInterval: 10_000 });
export const heldEventsOptions = () => ({ ...listHeldEventsOptions(), refetchInterval: 10_000 });

export function attentionItems(
  attention: BoardAttention | undefined,
  heldEvents?: { events: HeldEvent[]; total: number },
  bot?: string,
) {
  const forBot = ({ agentId }: { agentId: string }) => !bot || agentId === bot;
  const sessions = (attention?.sessions ?? []).filter(forBot);

  const questions = sessions.flatMap((session) =>
    session.question ? [{ session, question: session.question }] : [],
  );

  const results = (attention?.results ?? []).filter(forBot);
  const setup = (attention?.automations ?? []).filter((item) => item.setup && forBot(item));
  const taskReviews = (attention?.taskReviews ?? []).filter((item) => !bot || item.assigneeBotId === bot);
  // A held event has no Bot until the Owner attaches it. Blocked Tasks have their own section.
  const held = bot ? undefined : heldEvents;
  const needsYouCount = questions.length + setup.length + taskReviews.length + (held?.total ?? 0);

  return {
    questions,
    results,
    setup,
    taskReviews,
    held,
    needsYouCount,
    count: needsYouCount + results.length,
    working: sessions.some((session) => session.busy),
  };
}

export type BotStatus = "needs" | "working" | undefined;

export function botStatus(
  agentId: string,
  attention: BoardAttention | undefined,
  activity: Pick<AgentActivity, "activeSessions"> | undefined,
): BotStatus {
  const items = attentionItems(attention, undefined, agentId);

  if (items.count) return "needs";

  if (items.working || (activity?.activeSessions ?? 0) > 0) return "working";

  return undefined;
}
