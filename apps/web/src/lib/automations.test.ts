import { describe, expect, test } from "vite-plus/test";
import { scheduleText } from "./automations";

describe("automation summaries", () => {
  test("user sees weekdays without a list of five days", () => {
    expect(scheduleText({ kind: "weekly", days: [5, 1, 4, 2, 3], time: "09:00" }, "UTC")).toBe(
      "Every weekday at 09:00",
    );
    expect(scheduleText({ kind: "weekly", days: [7, 1], time: "21:30" }, "UTC")).toBe(
      "Every Mon, Sun at 21:30",
    );
    expect(scheduleText({ kind: "daily", time: "21:30" }, "UTC")).toBe("Every day at 21:30");
    expect(scheduleText({ kind: "every", minutes: 30 }, "UTC")).toBe("Every 30 minutes");
  });
});
