import { useAgentId } from "../lib/agent-scope";
import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { clearContext, compactChat, getChatCompaction } from "../api/generated/sdk.gen";
import { getChatHistoryOptions } from "../api/generated/@tanstack/react-query.gen";
import type { HistoryPage, Item } from "../api/generated/types.gen";
import { queryClient } from "../lib/queries";
import { newId } from "../lib/utils";
import { Button } from "./ui/button";
import { Failure } from "./feedback";

export function hasContext(items: Item[]) {
  const last = items.findLast((item) => item.kind !== "boundary" || item.boundary !== "compaction");

  return last !== undefined && !(last.kind === "boundary" && last.boundary === "reset");
}

export function useClearContext(chatId: number, running: boolean, onCleared: () => void) {
  const agentId = useAgentId();
  const request = useRef(newId());
  const lastHandoff = useRef("");
  const [refused, setRefused] = useState(false);
  const wasRunning = useRef(running);

  useEffect(() => {
    if (!running && wasRunning.current) setRefused(false);
    wasRunning.current = running;
  }, [running]);

  const clear = useMutation({
    mutationFn: async (handoff: string) => {
      if (lastHandoff.current !== handoff) {
        request.current = newId();
        lastHandoff.current = handoff;
      }

      const result = await clearContext({
        path: { agentId, chatId },
        body: { id: request.current, handoff },
      });

      if (result.response?.status === 409) return false;

      if (result.error) throw result.error;

      return true;
    },
    onMutate: () => {
      setRefused(false);
    },
    onSuccess: (accepted) => {
      setRefused(!accepted);

      if (accepted) {
        request.current = newId();
        onCleared();
      }
    },
  });

  return {
    submit: (handoff = "") => clear.mutate(handoff),
    pending: clear.isPending,
    error: clear.error,
    refused,
  };
}

const compactTerminal = new Set(["applied", "no_op", "stale", "failed"]);

const compactLabels = {
  pending: "Compaction requested",
  generating: "Preparing summary",
  pending_write: "Summary waits for a boundary",
  applied: "Summary applied",
  no_op: "Nothing to compact",
  stale: "Summary was not applied because the context changed",
  failed: "Compaction failed",
};

export function CompactContext({ chatId }: { chatId: number }) {
  const agentId = useAgentId();
  const storageKey = `dex-compact:${agentId}:${chatId}`;
  const [operationId, setOperationId] = useState(() => localStorage.getItem(storageKey) ?? "");
  const request = useRef(operationId || newId());

  const status = useQuery({
    queryKey: ["compact-context", agentId, chatId, operationId],
    enabled: !!operationId,
    queryFn: async () =>
      (await getChatCompaction({ path: { agentId, chatId, operationId }, throwOnError: true })).data,
    refetchInterval: (query) =>
      query.state.status === "error" || compactTerminal.has(query.state.data?.status ?? "") ? false : 1000,
    retry: false,
  });

  const compact = useMutation({
    mutationFn: async (id: string) =>
      (await compactChat({ path: { agentId, chatId }, body: { id }, throwOnError: true })).data,
    onSuccess: (result) =>
      void queryClient.invalidateQueries({ queryKey: ["compact-context", agentId, chatId, result.id] }),
  });

  const current =
    status.data?.id === operationId
      ? status.data
      : compact.data?.id === operationId
        ? compact.data
        : undefined;

  const active = current && !compactTerminal.has(current.status);

  function submit() {
    if (current && compactTerminal.has(current.status)) request.current = newId();
    localStorage.setItem(storageKey, request.current);
    setOperationId(request.current);
    compact.mutate(request.current);
  }

  return (
    <div className="flex items-center gap-2">
      <button
        type="button"
        disabled={compact.isPending || !!active}
        onClick={submit}
        className="flex h-7 items-center rounded-md px-1.5 text-[12px] font-medium text-muted-foreground hover:bg-accent hover:text-foreground disabled:opacity-40"
      >
        Compact context
      </button>
      {current ? (
        <span role="status" className="text-[12px] text-muted-foreground">
          {compactLabels[current.status]}
        </span>
      ) : null}
      {compact.isError ? <span role="alert">Could not request compaction. Try again.</span> : null}
      {status.isError && !compact.isPending && !compact.isError ? (
        <span role="alert">Could not read compaction status. Try again.</span>
      ) : null}
    </div>
  );
}

export function LoadEarlierButton({
  chatId,
  before,
  onPage,
}: {
  chatId: number;
  before?: number;
  onPage: (page: HistoryPage) => void;
}) {
  const agentId = useAgentId();

  const history = useMutation({
    mutationFn: (cursor: number) =>
      queryClient.fetchQuery(getChatHistoryOptions({ path: { agentId, chatId }, query: { before: cursor } })),
    onSuccess: onPage,
  });

  if (before === undefined) return null;

  return (
    <div className="flex flex-col items-center justify-center gap-2">
      <Button
        size="xs"
        variant="secondary"
        disabled={history.isPending}
        onClick={() => history.mutate(before)}
      >
        {history.isPending ? "Loading…" : "Show earlier messages"}
      </Button>
      {history.isError ? <Failure title="Could not load earlier messages" error={history.error} /> : null}
    </div>
  );
}
