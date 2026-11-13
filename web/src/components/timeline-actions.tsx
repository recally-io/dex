import { useAgentId } from "../lib/agent-scope";
import { useEffect, useRef, useState } from "react";
import { flushSync } from "react-dom";
import { useMutation } from "@tanstack/react-query";
import { clearContext } from "../api/generated/sdk.gen";
import { getChatHistoryOptions } from "../api/generated/@tanstack/react-query.gen";
import type { HistoryPage, Item } from "../api/generated/types.gen";
import { queryClient } from "../lib/queries";
import { newId } from "../lib/utils";
import { Button } from "./ui/button";
import { Failure } from "./feedback";

// A reset with a handoff starts the new context with that text, so it counts as context.
export function hasContext(items: Item[]) {
  const last = items.findLast((item) => item.kind !== "boundary" || item.boundary !== "compaction");

  return last !== undefined && !(last.kind === "boundary" && last.boundary === "reset" && !last.text);
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

  // A transport retry of the same handoff keeps the reset ID; another handoff is another reset.
  const handoff = useRef("");

  const clear = useMutation({
    mutationFn: async (text: string) => {
      if (text !== handoff.current) {
        request.current = newId();
        handoff.current = text;
      }

      const result = await clearContext({
        path: { agentId, chatId },
        body: { id: request.current, handoff: text || undefined },
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
        handoff.current = "";
        onCleared();
      }
    },
  });

  return {
    submit: (text = "") => clear.mutateAsync(text),
    pending: clear.isPending,
    error: clear.error,
    refused,
  };
}

// The button loads a history page when it scrolls into view, and keeps the visible items in place.
// A user who cannot scroll, or a failed load, uses the button.
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
  const anchor = useRef<HTMLDivElement>(null);

  const history = useMutation({
    mutationFn: (cursor: number) =>
      queryClient.fetchQuery(getChatHistoryOptions({ path: { agentId, chatId }, query: { before: cursor } })),
    onSuccess: (page) => {
      // The page goes above the first item. Without this, a scroller at the top stays at the top,
      // keeps the button in view, and loads every page.
      const first = anchor.current?.nextElementSibling;
      const top = first?.getBoundingClientRect().top ?? 0;
      flushSync(() => onPage(page));

      const box = first?.isConnected ? scrollParent(first) : undefined;

      if (first && box) box.scrollTop += first.getBoundingClientRect().top - top;
    },
  });

  const { mutate } = history;

  // An observer reports the current state when it starts, so a page that leaves the button in view
  // loads the next page.
  // A failed page does not change the cursor, so it does not retry until the button scrolls in again.
  useEffect(() => {
    const node = anchor.current;

    if (before === undefined || !node) return;

    const observer = new IntersectionObserver(([entry]) => {
      if (entry?.isIntersecting) mutate(before);
    });

    observer.observe(node);

    return () => observer.disconnect();
  }, [before, mutate]);

  if (before === undefined) return null;

  return (
    <div ref={anchor} className="flex flex-col items-center justify-center gap-2">
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

function scrollParent(node: Element): Element | undefined {
  for (let el = node.parentElement; el; el = el.parentElement)
    if (/auto|scroll/.test(getComputedStyle(el).overflowY)) return el;
}
