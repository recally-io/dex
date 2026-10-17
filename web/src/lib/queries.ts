// HTTP and transport errors enter here without a shared runtime schema.
/* eslint-disable anti-slop/no-runtime-typeof */
import { QueryClient } from "@tanstack/react-query";
import { listModelsOptions } from "../api/generated/@tanstack/react-query.gen";

export const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: 1, staleTime: 1000 }, mutations: { retry: false } },
});

// Refreshes after a settings write. A failed read stays in its query for the UI to show; it does
// not reject, so a successful write is not reported as a failed one.
export async function refreshSettings() {
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: [{ _id: "getSettings" }] }),
    queryClient.prefetchQuery({ ...listModelsOptions(), staleTime: 0 }),
  ]);
}

export async function refreshChats(agentId: string) {
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: [{ _id: "listChats", path: { agentId } }] }),
    queryClient.invalidateQueries({ queryKey: [{ _id: "getChat", path: { agentId } }] }),
  ]);
}

export async function refreshAutomations(agentId: string) {
  await Promise.all([
    refreshChats(agentId),
    queryClient.invalidateQueries({ queryKey: [{ _id: "listAutomations", path: { agentId } }] }),
    queryClient.invalidateQueries({ queryKey: [{ _id: "getAutomation", path: { agentId } }] }),
    queryClient.invalidateQueries({ queryKey: [{ _id: "listAutomationRuns", path: { agentId } }] }),
  ]);
}

export function errorText(cause: unknown): string {
  if (cause instanceof Error) return cause.message;

  if (typeof cause === "object" && cause !== null && "error" in cause && typeof cause.error === "string")
    return cause.error;

  if (typeof cause === "string")
    return cause.startsWith("<") ? "The server returned an unexpected response." : cause;

  return "The request failed. Try again.";
}
