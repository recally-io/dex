// HTTP and transport errors enter here without a shared runtime schema.
/* eslint-disable anti-slop/no-runtime-typeof */
import { QueryClient } from "@tanstack/react-query";
import { getBoardAttentionOptions, listModelsOptions } from "../api/generated/@tanstack/react-query.gen";

export const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: 1, staleTime: Infinity }, mutations: { retry: false } },
});

// Refreshes after a settings write. A failed read stays in its query for the UI to show; it does
// not reject, so a successful write is not reported as a failed one.
export async function refreshSettings() {
  await queryClient.invalidateQueries({ queryKey: [{ _id: "getChannels" }] });
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: [{ _id: "getSettings" }] }),
    queryClient.prefetchQuery({ ...listModelsOptions(), staleTime: 0 }),
  ]);
}

export async function refreshRunners() {
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: [{ _id: "listRunners" }] }),
    queryClient.invalidateQueries({ queryKey: [{ _id: "listBotRunners" }] }),
    queryClient.invalidateQueries({ queryKey: [{ _id: "listBots" }] }),
    queryClient.invalidateQueries({ queryKey: [{ _id: "getBot" }] }),
  ]);
}

export async function refreshSessions(botId: string) {
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: [{ _id: "listSessions", path: { botId } }] }),
    queryClient.invalidateQueries({ queryKey: [{ _id: "getSession", path: { botId } }] }),
  ]);
}

export async function refreshAutomations(botId: string) {
  await Promise.all([
    refreshSessions(botId),
    queryClient.invalidateQueries({ queryKey: [{ _id: "listAutomations", path: { botId } }] }),
    queryClient.invalidateQueries({ queryKey: [{ _id: "getAutomation", path: { botId } }] }),
    queryClient.invalidateQueries({ queryKey: [{ _id: "listAutomationRuns", path: { botId } }] }),
    refreshBoardAttention(),
  ]);
}

export function refreshBoardAttention(client = queryClient) {
  return client.invalidateQueries({ queryKey: getBoardAttentionOptions().queryKey });
}

export function errorText(cause: unknown): string {
  if (cause instanceof Error) return cause.message;

  if (typeof cause === "object" && cause !== null && "error" in cause && typeof cause.error === "string")
    return cause.error;

  if (typeof cause === "string")
    return cause.startsWith("<") ? "The server returned an unexpected response." : cause;

  return "The request failed. Try again.";
}
