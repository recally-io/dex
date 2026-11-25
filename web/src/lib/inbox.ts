import { listHeldEventsOptions, listInboxOptions } from "../api/generated/@tanstack/react-query.gen";
import {
  createChat,
  listAgentEnvironments,
  sendMessage,
  setChatEnvironment,
  setChatProject,
} from "../api/generated/sdk.gen";
import type { AgentEnvironments, InboxItem } from "../api/generated/types.gen";
import { refreshChats } from "./queries";
import { newId } from "./utils";

export const inboxOptions = () => listInboxOptions({ query: { includeDismissed: true } });
export const heldEventsOptions = () => ({ ...listHeldEventsOptions(), refetchInterval: 10_000 });

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

// continuationEnvironment returns the environment that a chat about a run selects. The run's
// paths and files exist only where it ran, so the chat pins local, SSH, or Runner.
// An E2B selection does not identify the exact sandbox. Older runs may record
// no environment. Stop rather than guess.
export function continuationEnvironment(ranIn: string, environments: AgentEnvironments["environments"]) {
  const env = environments.find((e) => e.id === ranIn);

  if (env && env.kind !== "e2b" && env.status === "available") return env.id;

  if (env?.kind === "runner" && env.status === "offline") {
    throw new Error(
      `This result ran on ${env.name}, which is offline. Try again when the runner reconnects.`,
    );
  }

  const where = ranIn
    ? `ran in ${env?.name ?? ranIn}, which cannot continue this run`
    : "has no recorded environment";

  throw new Error(`This result ${where}. Start a chat and select an environment.`);
}

// A retry after a partial failure reuses the chat and the message ID, so the quote is sent once.
export type Continuation = { chatId: number; messageId: string; environmentId: string };

// continueResult starts a chat in the environment and the cwd of the run, and sends text. It
// checks the environment before it creates the chat. It returns the chat ID.
export async function continueResult(
  agentId: string,
  run: { environmentId?: string; cwd?: string },
  text: string,
  continuation: { current: Continuation | null },
) {
  if (!continuation.current) {
    const list = await listAgentEnvironments({ path: { agentId }, throwOnError: true });
    const environmentId = continuationEnvironment(run.environmentId ?? "", list.data.environments);
    const chat = await createChat({ path: { agentId }, throwOnError: true });

    continuation.current = { chatId: chat.data.id, messageId: newId(), environmentId };
  }

  const { chatId, messageId, environmentId } = continuation.current;
  const path = { agentId, chatId };

  // The project is a path in the run's environment, so select that environment first.
  await setChatEnvironment({ path, body: { environmentId }, throwOnError: true });

  await setChatProject({ path, body: { cwd: run.cwd ?? "" }, throwOnError: true });
  await sendMessage({ path, body: { id: messageId, text }, throwOnError: true });
  await refreshChats(agentId);

  return chatId;
}
