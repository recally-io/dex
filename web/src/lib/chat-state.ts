import type { ChatEvent, Item, Retry } from "../api/generated/types.gen";

export type ChatState = { items: Item[]; running: boolean; retry?: Retry };

export const emptyChatState: ChatState = { items: [], running: false };

export function retryMessage(items: Item[], index: number) {
  const previous = items.slice(0, index).findLast((item) => item.kind === "user");

  // Task results retain control's request ID, but their transcript kind is also "user".
  const taskResult =
    /^task:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}:result(?::attempt:\d+)?$/i;

  return previous && !taskResult.test(previous.id) ? previous : undefined;
}

export function applyChatEvent(state: ChatState, event: ChatEvent): ChatState {
  switch (event.type) {
    case "snapshot":
      return { items: event.items, running: event.running, retry: event.retry };
    case "item": {
      const index = state.items.findIndex((item) => item.id === event.item.id);

      return {
        ...state,
        items:
          index < 0
            ? [...state.items, event.item]
            : state.items.map((item, i) => (i === index ? event.item : item)),
      };
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
