import type { Chat, Schedule } from "../api/generated/types.gen";

export const weekdays = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];

export function runTime(at: number, timeZone: string) {
  return new Intl.DateTimeFormat(undefined, {
    timeZone,
    dateStyle: "medium",
    timeStyle: "short",
  }).format(at);
}

export function scheduleText(schedule: Schedule, timeZone: string) {
  switch (schedule.kind) {
    case "every":
      return `Every ${schedule.minutes} minutes`;
    case "daily":
      return `Every day at ${schedule.time}`;
    case "once":
      return `Once on ${runTime(schedule.at!, timeZone)}`;
    case "weekly": {
      const days = schedule.days!.toSorted((a, b) => a - b);

      const label =
        days.length === 5 && days.every((day, i) => day === i + 1)
          ? "weekday"
          : days.length === 7
            ? "day"
            : days.map((day) => weekdays[day - 1]).join(", ");

      return `Every ${label} at ${schedule.time}`;
    }
  }
}

export function unreadAutomationChats(chats: Pick<Chat, "channel" | "updatedAt">[], seen: number) {
  return chats.filter((chat) => chat.channel === "automation" && chat.updatedAt > seen).length;
}
