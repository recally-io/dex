import { useEffect, useRef } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { listAutomationResultsOptions } from "../api/generated/@tanstack/react-query.gen";
import {
  createChat,
  dismissAutomationResult,
  listAgentRunners,
  readAutomationResult,
  sendMessage,
  setChatRunner,
  setChatProject,
} from "../api/generated/sdk.gen";
import type { AgentRunners, AutomationResult, Ok } from "../api/generated/types.gen";
import { queryClient, refreshBoardAttention, refreshChats } from "./queries";
import { newId } from "./utils";

export const automationResultsOptions = () => ({
  ...listAutomationResultsOptions({ query: { includeDismissed: true } }),
  refetchInterval: 10_000,
});

export function refreshAutomationResults(client = queryClient) {
  return Promise.all([
    client.invalidateQueries({ queryKey: listAutomationResultsOptions().queryKey }),
    refreshBoardAttention(client),
  ]);
}

async function updateResults(update: (runId: string) => Promise<{ data: Ok }>, runIds: string | string[]) {
  const ids = Array.isArray(runIds) ? runIds : [runIds];
  const outcomes = await Promise.allSettled(ids.map(update));
  const failed = outcomes.find((result) => result.status === "rejected");

  if (failed) throw failed.reason;
}

export function useReadResults() {
  const client = useQueryClient();

  return useMutation({
    mutationFn: (runIds: string | string[]) =>
      updateResults((runId) => readAutomationResult({ path: { runId }, throwOnError: true }), runIds),
    onSettled: () => refreshAutomationResults(client),
  });
}

export function useReadResultsOnOpen(results?: AutomationResult[], automationId?: string) {
  const read = useReadResults();
  const { mutate } = read;
  const asked = useRef(new Set<string>());

  useEffect(() => {
    const unread = (results ?? []).filter(
      (item) =>
        (!automationId || item.automationId === automationId) && !item.read && !asked.current.has(item.runId),
    );

    if (!unread.length) return;

    for (const item of unread) asked.current.add(item.runId);
    mutate(unread.map((item) => item.runId));
  }, [automationId, results, mutate]);

  return read;
}

export function useDismissResults() {
  const client = useQueryClient();

  return useMutation({
    mutationFn: (runIds: string | string[]) =>
      updateResults((runId) => dismissAutomationResult({ path: { runId }, throwOnError: true }), runIds),
    // Refresh even if only part of the group succeeds.
    onSettled: () => refreshAutomationResults(client),
  });
}

type ResultQuote = Pick<AutomationResult, "automationName" | "summary" | "agentId" | "chatId" | "runId"> & {
  outcome: AutomationResult["outcome"] | "unchanged" | "unknown";
};

// The first line is plain text because the chat takes its title from the start of the message.
export function resultQuote(item: ResultQuote) {
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

// continuationRunner returns the environment that a chat about a run selects. The run's
// paths and files exist only where it ran, so the chat pins local, SSH, or Runner.
// An E2B selection does not identify the exact sandbox. Older runs may record
// no environment. Stop rather than guess.
export function continuationRunner(ranIn: string, runners: AgentRunners["runners"]) {
  const env = runners.find((e) => e.id === ranIn);

  if (env && env.kind !== "e2b" && env.status === "available") return env.id;

  if (env?.kind === "runner" && env.status === "offline") {
    throw new Error(
      `This result ran on ${env.name}, which is offline. Try again when the Outbound Machine reconnects.`,
    );
  }

  const where = ranIn
    ? `ran in ${env?.name ?? ranIn}, which cannot continue this run`
    : "has no recorded Runner";

  throw new Error(`This result ${where}. Start a session and select a Runner.`);
}

type Continuation = { chatId: number; messageId: string; runnerId: string; configured: boolean };

type ContinuableResult = ResultQuote & { runnerId?: string; cwd?: string };

export function useContinueResult() {
  const navigate = useNavigate();
  const client = useQueryClient();
  const attempts = useRef(new Map<string, Continuation>());

  return useMutation({
    mutationFn: async ({ result, message }: { result: ContinuableResult; message?: string }) => {
      const { agentId, runId } = result;
      let attempt = attempts.current.get(runId);

      if (!attempt) {
        const list = await listAgentRunners({ path: { agentId }, throwOnError: true });
        const runnerId = continuationRunner(result.runnerId ?? "", list.data.runners);
        const chat = await createChat({ path: { agentId }, throwOnError: true });
        attempt = { chatId: chat.data.id, messageId: newId(), runnerId, configured: false };
        // A retry of the same run keeps the chat and message ID.
        attempts.current.set(runId, attempt);
      }

      const { chatId, messageId, runnerId } = attempt;
      const path = { agentId, chatId };

      if (!attempt.configured) {
        // The project path belongs to this Runner. Select it before setting the project.
        await setChatRunner({ path, body: { runnerId }, throwOnError: true });
        await setChatProject({ path, body: { cwd: result.cwd ?? "" }, throwOnError: true });
        attempt.configured = true;
      }

      const quote = resultQuote(result);
      const extra = message?.trim();
      await sendMessage({
        path,
        body: { id: messageId, text: extra ? `${quote}\n\n${extra}` : quote },
        throwOnError: true,
      });
      await Promise.all([refreshChats(agentId), refreshAutomationResults(client)]);
      await navigate({ to: "/a/$agentId/c/$chatId", params: { agentId, chatId: String(chatId) } });
      attempts.current.delete(runId);
    },
  });
}
