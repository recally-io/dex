import { describe, expect, it } from "vite-plus/test";
import { dateGroup } from "./chat-list";

describe("dateGroup", () => {
  // 21 Oct 2026, 15:00 local time.
  const now = new Date(2026, 9, 21, 15).getTime();
  const at = (month: number, day: number, hour = 12) => new Date(2026, month, day, hour).getTime();

  it("puts recent chats in day buckets and older ones in months", () => {
    expect(dateGroup(at(9, 21, 0), now)).toBe("Today");
    expect(dateGroup(at(9, 20, 23), now)).toBe("Yesterday");
    expect(dateGroup(at(9, 14), now)).toBe("Previous 7 days");
    expect(dateGroup(at(8, 22), now)).toBe("Previous 30 days");
    expect(dateGroup(at(8, 20), now)).toBe("September 2026");
  });
});
