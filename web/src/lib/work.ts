import { getWorkOptions } from "../api/generated/@tanstack/react-query.gen";
import type { AgentActivity, Chat, InboxItem, Work } from "../api/generated/types.gen";

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

export function boardBucket(chat: Pick<Chat, "busy" | "question">): "needs" | "working" | "idle" {
  if (openQuestion(chat)) return "needs";

  return chat.busy ? "working" : "idle";
}

export type BotStatus = "needs" | "working" | undefined;

/** The fields of the Work response that a bot status reads. */
type WorkState = {
  sessions: { agentId: string; chat: Pick<Chat, "busy" | "question"> }[];
  automations: { agentId: string; setup: boolean }[];
  inbox: Pick<InboxItem, "agentId" | "outcome" | "dismissed">[];
};

/**
 * The one status a bot row shows. "needs" wins over "working": an open question, a failed routine
 * run that is not done, or a routine that needs setup. The bot page shows each of these again:
 * questions and routines in the bot column. A failed child session is not
 * here: its parent session tells the user.
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

// Bot status reads no schedule, so the range is the smallest that the API accepts. One fixed range
// gives the rail and the bot column one shared query.
const opened = Date.now();

export const workOptions = () => ({
  ...getWorkOptions({ query: { from: opened, to: opened + 1 } }),
  refetchInterval: 10_000,
});

/** The items that make a bot need the user. The bot column lists them, so each orange dot has a place to act on it. */
export function needsItems(agentId: string, work: Work | undefined) {
  return {
    questions: (work?.sessions ?? [])
      .filter((item) => item.agentId === agentId && openQuestion(item.chat))
      .map((item) => item.chat),
    results: (work?.inbox ?? []).filter(
      (item) => item.agentId === agentId && item.outcome === "failed" && !item.dismissed,
    ),
    setup: (work?.automations ?? [])
      .filter((item) => item.agentId === agentId && item.setup)
      .map((item) => item.automation),
  };
}
