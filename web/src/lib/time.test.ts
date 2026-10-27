import { describe, expect, it } from "vitest";

import {
  calendarDate,
  calendarDays,
  calendarLabel,
  calendarMoment,
  formatClock,
  formatDate,
  relativeTime,
} from "./time";

describe("shared local time formatting", () => {
  const now = new Date(2026, 0, 1, 0, 5);
  it("uses elapsed time for past/future events and handles boundaries", () => {
    expect(relativeTime(now.getTime() - 59_999, now)).toBe("刚刚");
    expect(relativeTime(now.getTime() - 60_000, now)).toBe("1 分钟前");
    expect(relativeTime(now.getTime() + 120_000, now)).toBe("2 分钟后");
    expect(relativeTime(now.getTime() - 3_600_000, now)).toBe("1 小时前");
    expect(relativeTime(now.getTime() - 86_400_000, now)).toBe("1 天前");
  });
  it("uses local calendar days across midnight and year boundaries", () => {
    const yesterday = new Date(2025, 11, 31, 23, 59);
    expect(calendarDays(yesterday, now)).toBe(-1);
    expect(calendarLabel(yesterday, now)).toBe("昨天");
    expect(calendarMoment(yesterday, now)).toBe("昨天 23:59");
    expect(calendarDate(new Date(2025, 11, 20), now)).toBe("2025年12月20日");
    expect(calendarLabel(new Date(2026, 0, 2), now)).toBe("明天");
    expect(formatClock(new Date(2026, 0, 1))).toBe("00:00");
  });
  it("keeps elapsed units numeric even when calendar offsets differ", () => {
    const at = new Date(2026, 9, 24, 23, 59);
    const now = new Date(2026, 9, 27, 0, 5);
    expect(calendarDays(at, now)).toBe(-3);
    expect(relativeTime(at, now)).toBe("2 天前");
    expect(relativeTime(at, now, { dateAfterDays: Infinity })).toBe("2 天前");
    expect(relativeTime(new Date(2026, 9, 25, 0, 6), now)).toBe("1 天前");
  });
  it("returns a stable placeholder for invalid dates", () => {
    expect(formatDate("invalid")).toBe("—");
    expect(calendarLabel("invalid")).toBe("—");
    expect(calendarMoment("invalid")).toBe("—");
    expect(relativeTime("invalid")).toBe("—");
  });
  it("does not count a daylight-saving change as a fraction of a day", () => {
    const old = process.env.TZ;
    process.env.TZ = "America/New_York";
    try {
      const before = new Date(2026, 2, 7, 12);
      const after = new Date(2026, 2, 8, 12);
      expect(calendarDays(after, before)).toBe(1);
      expect(calendarLabel(before, after)).toBe("昨天");
    } finally {
      if (old === undefined) delete process.env.TZ;
      else process.env.TZ = old;
    }
  });
});
