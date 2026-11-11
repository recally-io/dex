import { useSyncExternalStore } from "react";

// A busy session turns Stalled after 5 or 30 minutes of silence, while its data stays the same.
// One shared timer redraws every list that shows the hint. A tick of 15 s keeps the hint within 15 s
// of its threshold.
const TICK_MS = 15_000;

let now = Date.now();
let timer: ReturnType<typeof setInterval> | undefined;
const listeners = new Set<() => void>();

function subscribe(listener: () => void) {
  listeners.add(listener);

  if (listeners.size === 1) {
    now = Date.now();
    timer = setInterval(() => {
      now = Date.now();
      listeners.forEach((notify) => notify());
    }, TICK_MS);
  }

  return () => {
    listeners.delete(listener);

    if (listeners.size === 0) clearInterval(timer);
  };
}

/** The current time in ms. It changes every 15 s while a component uses it. */
export function useNow() {
  return useSyncExternalStore(subscribe, () => now);
}
