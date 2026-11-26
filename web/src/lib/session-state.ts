import type {
  SessionEvent,
  ChildEvent,
  HistoryPage,
  Item,
  Retry,
  TimelineNotification,
} from "../api/generated/types.gen";

type SessionState = {
  items: Item[];
  running: boolean;
  runTarget?: string;
  children?: Record<number, ChildEvent["state"]>;
  retry?: Retry;
  history?: Item[];
  historyBefore?: number;
  headBefore?: number;
  notifications?: {
    entries: TimelineNotification[];
    head?: string;
    before?: string;
  };
};

export const emptySessionState: SessionState = { items: [], running: false };

export type SystemInput = Extract<Item, { kind: "session_result" | "wake" | "task_wake" }>;

export function isSystemInput(item: Item): item is SystemInput {
  return item.kind === "session_result" || item.kind === "wake" || item.kind === "task_wake";
}

export function isTurnStart(item: Item): item is SystemInput | Extract<Item, { kind: "user" | "boundary" }> {
  return item.kind === "user" || item.kind === "boundary" || isSystemInput(item);
}

export function isQueued(item: Item) {
  return "queued" in item && !!item.queued;
}

export function retryMessage(items: Item[], index: number) {
  const input = items.slice(0, index).findLast(isTurnStart);

  return input?.kind === "user" ? input : undefined;
}

export function applySessionEvent(state: SessionState, event: SessionEvent): SessionState {
  switch (event.type) {
    case "notifications": {
      const head = event.notifications[0]?.id;
      const previous = state.notifications;

      return {
        ...state,
        notifications: {
          entries: mergeNotifications(previous?.entries, event.notifications),
          head: head ?? previous?.head,
          before: event.initial && head !== previous?.head ? event.nextBeforeNotification : previous?.before,
        },
      };
    }

    case "child": {
      const children = { ...state.children };

      if (event.state === "unavailable") delete children[event.sessionId];
      else children[event.sessionId] = event.state;

      return { ...state, children };
    }

    case "snapshot": {
      const remote = new Set(event.items.map((item) => item.id));
      const archived = state.items.filter((item) => item.entryId !== undefined && !remote.has(item.id));
      const history = mergeItems(state.history ?? [], archived).filter((item) => !remote.has(item.id));

      return {
        items: mergeItems(history, event.items),
        running: event.running,
        runTarget: event.running ? event.runTarget : undefined,
        children: state.children,
        notifications: state.notifications,
        retry: event.retry,
        history,
        headBefore: event.historyBefore,
        historyBefore: state.headBefore === event.historyBefore ? state.historyBefore : event.historyBefore,
      };
    }

    case "item": {
      const index = state.items.findIndex((item) => item.id === event.item.id);

      const queued = state.items.findIndex(isQueued);

      if (index >= 0) {
        const items = state.items.map((item, i) => (i === index ? event.item : item));

        if (isTurnStart(event.item) && !isQueued(event.item) && queued >= 0 && index > queued)
          items.splice(queued, 0, ...items.splice(index, 1));

        return { ...state, items, history: state.history?.filter((item) => item.id !== event.item.id) };
      }

      const at = !isQueued(event.item) && queued >= 0 ? queued : state.items.length;

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
      return {
        ...state,
        running: event.running,
        runTarget: event.running ? event.runTarget : undefined,
        retry: event.retry,
      };
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

    const queued = items.findIndex(isQueued);
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
      (item) => isQueued(item) || (isTurnStart(item) && item.at !== undefined && item.at > inputAt),
    );

    items.splice(at >= 0 ? at : items.length, 0, ...group);
  }

  return items;
}

function mergeNotifications(earlier: TimelineNotification[] = [], current: TimelineNotification[]) {
  const byId = new Map(earlier.map((notice) => [notice.id, notice]));

  for (const notice of current) byId.set(notice.id, notice);

  return [...byId.values()].sort((a, b) => a.at - b.at || a.id.localeCompare(b.id));
}

export function mergeHistory(
  state: SessionState,
  page: HistoryPage,
  beforeNotification?: string,
): SessionState {
  const archived = new Set(state.history?.map((item) => item.id));
  const live = state.items.filter((item) => !archived.has(item.id));
  const liveIDs = new Set(live.map((item) => item.id));
  const history = mergeItems(state.history ?? [], page.items).filter((item) => !liveIDs.has(item.id));

  const notifications = state.notifications;

  return {
    ...state,
    history,
    items: mergeItems(history, live),
    historyBefore: page.nextBefore,
    notifications:
      notifications && page.notifications
        ? {
            ...notifications,
            entries: mergeNotifications(notifications.entries, page.notifications),
            before:
              notifications.before === beforeNotification
                ? page.nextBeforeNotification
                : notifications.before,
          }
        : notifications,
  };
}
