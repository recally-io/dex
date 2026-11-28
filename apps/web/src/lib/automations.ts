import type { Schedule } from "../api/generated/types.gen";

export const weekdays = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];

// The prototype format, for example "Thu 15 Oct, 08:30". The UI text is English, so the format is too.
export function runTime(at: number, timeZone: string) {
  return new Intl.DateTimeFormat("en-GB", {
    timeZone,
    weekday: "short",
    day: "numeric",
    month: "short",
    hour: "2-digit",
    minute: "2-digit",
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

// A short age, for example "5 min" or "2 d".
export function ago(at: number, now: number) {
  const minutes = Math.round((now - at) / 60_000);

  if (minutes < 1) return "now";

  if (minutes < 60) return `${minutes} min`;

  const hours = Math.round(minutes / 60);

  return hours < 24 ? `${hours} h` : `${Math.round(hours / 24)} d`;
}
