import type { ChatEvent, HistoryPage, Item, Retry } from "../api/generated/types.gen";

export type ChatState = {
  items: Item[];
  running: boolean;
  retry?: Retry;
  history?: Item[];
  historyBefore?: number;
  headBefore?: number;
};

export const emptyChatState: ChatState = { items: [], running: false };

export function retryMessage(items: Item[], index: number) {
  const input = items
    .slice(0, index)
    .findLast((item) => item.kind === "user" || item.kind === "task" || item.kind === "boundary");

  return input?.kind === "user" ? input : undefined;
}

export function applyChatEvent(state: ChatState, event: ChatEvent): ChatState {
  switch (event.type) {
    case "snapshot": {
      const remote = new Set(event.items.map((item) => item.id));
      const archived = state.items.filter((item) => item.entryId !== undefined && !remote.has(item.id));
      const history = mergeItems(state.history ?? [], archived).filter((item) => !remote.has(item.id));

      return {
        items: mergeItems(history, event.items),
        running: event.running,
        retry: event.retry,
        history,
        headBefore: event.historyBefore,
        historyBefore: state.headBefore === event.historyBefore ? state.historyBefore : event.historyBefore,
      };
    }

    case "item": {
      const index = state.items.findIndex((item) => item.id === event.item.id);

      const queued = state.items.findIndex((item) => item.kind === "user" && item.queued);

      if (index >= 0) {
        const items = state.items.map((item, i) => (i === index ? event.item : item));

        if (event.item.kind === "user" && !event.item.queued && queued >= 0 && index > queued)
          items.splice(queued, 0, ...items.splice(index, 1));

        return { ...state, items, history: state.history?.filter((item) => item.id !== event.item.id) };
      }

      const at =
        !(event.item.kind === "user" && event.item.queued) && queued >= 0 ? queued : state.items.length;

      const items = state.items.toSpliced(at, 0, event.item);

      return { ...state, items };
    }

    case "append":
      return {
        ...state,
        items: state.items.map((item) =>
          item.id === event.id && "text" in item ? { ...item, text: item.text + event.text } : item,
        ),
      };
    case "run":
      return { ...state, running: event.running, retry: event.retry };
  }
}

// Live items win overlaps. Worker entries keep entry order; queued inputs stay last.
function mergeItems(earlier: Item[], current: Item[]): Item[] {
  const ids = new Set(current.map((item) => item.id));
  const missing = earlier.filter((item) => !ids.has(item.id));
  const items = [...current];

  for (const item of missing.toSorted((a, b) => (a.entryId ?? 0) - (b.entryId ?? 0))) {
    const at = items.findIndex(
      (other) => other.entryId !== undefined && item.entryId !== undefined && other.entryId > item.entryId,
    );

    const queued = items.findIndex((other) => other.kind === "user" && other.queued);
    items.splice(at >= 0 ? at : queued >= 0 ? queued : items.length, 0, item);
  }

  // A control-only input has no entry order. Keep its error with it, before the next
  // newer input or reset, as the server does when it builds the snapshot.
  for (const input of current) {
    if (input.kind !== "user" || input.entryId !== undefined || input.queued || input.at === undefined)
      continue;

    const from = items.findIndex((item) => item.id === input.id);
    const group = items.splice(from, items[from + 1]?.id === `${input.id}:error` ? 2 : 1);
    const inputAt = input.at;

    const at = items.findIndex(
      (item) =>
        (item.kind === "user" && item.queued) ||
        ((item.kind === "user" || item.kind === "task" || item.kind === "boundary") &&
          item.at !== undefined &&
          item.at > inputAt),
    );

    items.splice(at >= 0 ? at : items.length, 0, ...group);
  }

  return items;
}

export function mergeHistory(state: ChatState, page: HistoryPage): ChatState {
  const archived = new Set(state.history?.map((item) => item.id));
  const live = state.items.filter((item) => !archived.has(item.id));
  const liveIDs = new Set(live.map((item) => item.id));
  const history = mergeItems(state.history ?? [], page.items).filter((item) => !liveIDs.has(item.id));

  return { ...state, history, items: mergeItems(history, live), historyBefore: page.nextBefore };
}
