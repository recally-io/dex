import { useRef } from "react";
import { useMutation } from "@tanstack/react-query";
import { clearContextMutation, getChatHistoryOptions } from "../api/generated/@tanstack/react-query.gen";
import type { HistoryPage } from "../api/generated/types.gen";
import { queryClient } from "../lib/queries";
import { newId } from "../lib/utils";
import { Button } from "./atoms/Button";
import { Failure } from "./feedback";

export function ClearContextButton({ chatId, busy }: { chatId: number; busy: boolean }) {
  const request = useRef(newId());

  const clear = useMutation({
    ...clearContextMutation(),
    onSuccess: () => {
      request.current = newId();
    },
  });

  return (
    <div>
      <Button
        size="xs"
        variant="secondary"
        disabled={busy || clear.isPending}
        onClick={() => clear.mutate({ path: { chatId }, body: { id: request.current } })}
      >
        Clear context
      </Button>
      {clear.isError ? <Failure title="Could not clear context" error={clear.error} /> : null}
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
  const history = useMutation({
    mutationFn: (cursor: number) =>
      queryClient.fetchQuery(getChatHistoryOptions({ path: { chatId }, query: { before: cursor } })),
    onSuccess: onPage,
  });

  if (before === undefined) return null;

  return (
    <div>
      <Button
        size="xs"
        variant="secondary"
        disabled={history.isPending}
        onClick={() => history.mutate(before)}
      >
        {history.isPending ? "Loading earlier…" : "Load earlier"}
      </Button>
      {history.isError ? <Failure title="Could not load history" error={history.error} /> : null}
    </div>
  );
}
