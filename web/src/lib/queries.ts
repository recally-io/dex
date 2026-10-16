// HTTP and transport errors enter here without a shared runtime schema.
/* eslint-disable anti-slop/no-runtime-typeof */
import { QueryClient } from "@tanstack/react-query";
import {
  getSettingsQueryKey,
  listModelsOptions,
  listChatsQueryKey,
  listAutomationsQueryKey,
} from "../api/generated/@tanstack/react-query.gen";

export const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: 1, staleTime: 1000 }, mutations: { retry: false } },
});

// Refreshes after a settings write. A failed read stays in its query for the UI to show; it does
// not reject, so a successful write is not reported as a failed one.
export async function refreshSettings() {
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: getSettingsQueryKey() }),
    queryClient.prefetchQuery({ ...listModelsOptions(), staleTime: 0 }),
  ]);
}

export async function refreshChats() {
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: listChatsQueryKey() }),
    queryClient.invalidateQueries({ queryKey: [{ _id: "getChat" }] }),
  ]);
}

export async function refreshAutomations() {
  await Promise.all([
    refreshChats(),
    queryClient.invalidateQueries({ queryKey: listAutomationsQueryKey() }),
    queryClient.invalidateQueries({ queryKey: [{ _id: "getAutomation" }] }),
    queryClient.invalidateQueries({ queryKey: [{ _id: "listAutomationRuns" }] }),
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
