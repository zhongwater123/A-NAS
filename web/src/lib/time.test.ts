import { describe, expect, it } from "vitest";

import {
  calendarDate,
  calendarDays,
  calendarLabel,
  calendarMoment,
  dateOnly,
  dayLabel,
  daysUntil,
  dueText,
  formatClock,
  formatDate,
  isoDate,
  relativeTime,
  untilNextDay,
} from "./time";

describe("due dates", () => {
  const now = new Date(2026, 10, 17, 9, 30);
  it("say how far off they are", () => {
    expect(dueText("2026-11-20", now)).toBe("还有 3 天");
    expect(dueText("2026-11-18", now)).toBe("还有 1 天");
    expect(dueText("2026-11-17", now)).toBe("今天截止");
    expect(dueText("2026-11-16", now)).toBe("已过期 1 天");
  });
  it("count calendar days across the year's end", () => {
    expect(dueText("2027-01-02", new Date(2026, 11, 31, 23, 50))).toBe("还有 2 天");
    expect(dueText("2026-12-29", new Date(2027, 0, 1, 0, 5))).toBe("已过期 3 天");
  });
  it("label a day, with the year only when it is not this year's", () => {
    expect(dayLabel("2026-11-20", now)).toBe("11月20日");
    expect(dayLabel("2027-01-02", now)).toBe("2027年1月2日");
  });
});

describe("date-only values", () => {
  it("read as the local calendar day, not midnight UTC", () => {
    const date = dateOnly("2026-03-01");
    expect([date.getFullYear(), date.getMonth(), date.getDate(), date.getHours()]).toEqual([2026, 2, 1, 0]);
    expect(isoDate(new Date(2026, 0, 5, 23, 59))).toBe("2026-01-05");
  });
  it("count calendar days from now, whatever the hour", () => {
    expect(daysUntil("2026-01-02", new Date(2026, 0, 1, 23, 59))).toBe(1);
    expect(daysUntil("2026-01-01", new Date(2026, 0, 1, 0, 1))).toBe(0);
    expect(daysUntil("2025-12-30", new Date(2026, 0, 1, 12))).toBe(-2);
  });
  it("tell how long until just after the next midnight", () => {
    expect(untilNextDay(new Date(2026, 0, 1, 23, 59), 60_000)).toBe(120_000);
    expect(untilNextDay(new Date(2026, 0, 1, 0, 0), 0)).toBe(86_400_000);
  });
});

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
