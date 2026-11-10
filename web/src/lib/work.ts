import { addDays, isValid, parse, startOfDay, startOfWeek } from "date-fns";
import type { AutomationRun, Chat, InboxItem, TaskSummary } from "../api/generated/types.gen";
import { taskBucket } from "./tasks";

export function automationNeedsYou(
  id: string,
  setup: boolean,
  items: Pick<InboxItem, "automationId" | "outcome" | "dismissed">[],
) {
  return (
    setup || items.some((item) => item.automationId === id && item.outcome === "failed" && !item.dismissed)
  );
}

/** The question that waits for the user. An answered question is already on its way and does not count. */
export function openQuestion(chat: Pick<Chat, "question">) {
  return chat.question?.status === "open" ? chat.question : undefined;
}

export function boardBucket(chat: Pick<Chat, "task" | "busy" | "question">): "needs" | "working" | "idle" {
  if (openQuestion(chat) || (chat.task && needsTask(chat.task))) return "needs";

  if (chat.task ? ["running", "admitting", "queued"].includes(chat.task.status) : chat.busy) return "working";

  return "idle";
}

export function needsTask(task: TaskSummary) {
  if (task.status === "running" || task.status === "admitting" || task.status === "queued") return false;

  return taskBucket(task) === "needs-you";
}

// The Work page keeps its view in the URL: ?view=calendar&range=today|YYYY-MM-DD&kind=once|repeat.
// An absent field means the default (board, this week, all kinds). Invalid values mean the default.
export type WorkSearch = { view?: "calendar"; range?: string; kind?: "once" | "repeat" };

export function parseWorkSearch(raw: { view?: unknown; range?: unknown; kind?: unknown }): WorkSearch {
  const range = String(raw.range ?? "");

  return {
    view: raw.view === "calendar" ? "calendar" : undefined,
    range: range === "today" || /^\d{4}-\d{2}-\d{2}$/.test(range) ? range : undefined,
    kind: raw.kind === "once" || raw.kind === "repeat" ? raw.kind : undefined,
  };
}

/** The visible week starts on Monday. A day outside that week falls back to the week. */
export function resolveRange(range: string | undefined, now: number) {
  const week = startOfWeek(now, { weekStartsOn: 1 });
  const weekEnd = addDays(week, 7);

  if (range === "today") {
    const start = startOfDay(now);

    return { name: "today" as const, start, end: addDays(start, 1) };
  }

  const day = range ? parse(range, "yyyy-MM-dd", now) : undefined;

  if (day && isValid(day) && day >= week && day < weekEnd)
    return { name: "day" as const, start: day, end: addDays(day, 1) };

  return { name: "week" as const, start: week, end: weekEnd };
}

export type EventTone = "working" | "needs" | "result" | "quiet" | "planned";

export const tones: EventTone[] = ["working", "needs", "result", "quiet", "planned"];

// One class string per tone, shared by the calendar events and the legend, so the legend is true.
export const toneClass: Record<EventTone, string> = {
  working: "border-primary bg-primary/10 text-foreground",
  needs: "border-orange bg-orange-tint text-orange",
  result: "border-green bg-green-tint text-green",
  quiet: "border-border bg-muted text-muted-foreground",
  planned: "border-dashed border-muted-foreground bg-background text-muted-foreground",
};

export const toneLabel: Record<EventTone, string> = {
  working: "Working",
  needs: "Needs you",
  result: "Result, unread",
  quiet: "Read, unchanged or skipped",
  planned: "Scheduled",
};

/** The tone of one automation run on the calendar. */
export function runTone(
  run: Pick<AutomationRun, "status" | "outcome">,
  item: Pick<InboxItem, "outcome" | "read" | "dismissed"> | undefined,
): EventTone {
  if (run.status === "running") return "working";

  if (item?.outcome === "failed" && !item.dismissed) return "needs";

  if (item?.outcome === "changed" && !item.read) return "result";

  return "quiet";
}
