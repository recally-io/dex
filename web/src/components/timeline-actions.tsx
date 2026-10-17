import { useAgentId } from "../lib/agent-scope";
import { useEffect, useRef, useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { clearContext } from "../api/generated/sdk.gen";
import { getChatHistoryOptions } from "../api/generated/@tanstack/react-query.gen";
import type { HistoryPage, Item } from "../api/generated/types.gen";
import { queryClient } from "../lib/queries";
import { newId } from "../lib/utils";
import { Button } from "./atoms/Button";
import { Failure } from "./feedback";

export function hasContext(items: Item[]) {
  const last = items.findLast((item) => item.kind !== "boundary" || item.boundary !== "compaction");

  return last !== undefined && !(last.kind === "boundary" && last.boundary === "reset");
}

export function useClearContext(chatId: number, running: boolean, onCleared: () => void) {
  const agentId = useAgentId();
  const request = useRef(newId());
  const [refused, setRefused] = useState(false);
  const wasRunning = useRef(running);

  useEffect(() => {
    if (!running && wasRunning.current) setRefused(false);
    wasRunning.current = running;
  }, [running]);

  const clear = useMutation({
    mutationFn: async () => {
      const result = await clearContext({ path: { agentId, chatId }, body: { id: request.current } });

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

  return { submit: () => clear.mutate(), pending: clear.isPending, error: clear.error, refused };
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
