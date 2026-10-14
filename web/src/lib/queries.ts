// HTTP and transport errors enter here without a shared runtime schema.
/* eslint-disable anti-slop/no-runtime-typeof */
import { QueryClient } from "@tanstack/react-query";
import {
  getSettingsQueryKey,
  listModelsQueryKey,
  listChatsQueryKey,
} from "../api/generated/@tanstack/react-query.gen";

export const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: 1, staleTime: 1000 }, mutations: { retry: false } },
});

export async function refreshSettings() {
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: getSettingsQueryKey() }),
    queryClient.invalidateQueries({ queryKey: listModelsQueryKey() }),
  ]);
}

export async function refreshChats() {
  await queryClient.invalidateQueries({ queryKey: listChatsQueryKey() });
}

export function errorText(cause: unknown): string {
  if (cause instanceof Error) return cause.message;

  if (typeof cause === "object" && cause !== null && "error" in cause && typeof cause.error === "string")
    return cause.error;

  if (typeof cause === "string")
    return cause.startsWith("<") ? "The server returned an unexpected response." : cause;

  return "The request failed. Try again.";
}
