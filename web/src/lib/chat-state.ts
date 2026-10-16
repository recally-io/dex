import type { ChatEvent, Item, Retry } from "../api/generated/types.gen";

export type ChatState = { items: Item[]; running: boolean; retry?: Retry };

export const emptyChatState: ChatState = { items: [], running: false };

export function retryMessage(items: Item[], index: number) {
  const input = items.slice(0, index).findLast((item) => item.kind === "user" || item.kind === "task");

  return input?.kind === "user" ? input : undefined;
}

export function applyChatEvent(state: ChatState, event: ChatEvent): ChatState {
  switch (event.type) {
    case "snapshot":
      return { items: event.items, running: event.running, retry: event.retry };
    case "item": {
      const index = state.items.findIndex((item) => item.id === event.item.id);

      const queued = state.items.findIndex((item) => item.kind === "user" && item.queued);

      if (index >= 0) {
        const items = state.items.map((item, i) => (i === index ? event.item : item));

        if (event.item.kind === "user" && !event.item.queued && queued >= 0 && index > queued)
          items.splice(queued, 0, ...items.splice(index, 1));

        return { ...state, items };
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
