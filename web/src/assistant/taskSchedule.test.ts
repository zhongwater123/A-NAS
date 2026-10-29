import { describe, expect, it } from "vitest";

import { scheduleText, whenText } from "./taskSchedule";

const now = new Date(2026, 9, 20, 9, 30);
const at = (month: number, day: number, hour: number, minute = 0, year = 2026) =>
  new Date(year, month - 1, day, hour, minute).getTime() / 1000;

describe("scheduleText", () => {
  it("says a daily cron expression with its day", () => {
    expect(scheduleText({ kind: "cron", expr: "0 8 * * *", display: "0 8 * * *" }, now)).toBe("每天 08:00");
    expect(scheduleText({ kind: "cron", expr: "0 8,20 * * *", display: "0 8,20 * * *" }, now)).toBe(
      "每天 08:00 和 20:00",
    );
  });

  it("words other cron expressions with cronstrue", () => {
    expect(scheduleText({ kind: "cron", expr: "30 7 * * 1-5", display: "30 7 * * 1-5" }, now)).toBe(
      "在07:30, 星期一至星期五",
    );
    expect(scheduleText({ kind: "cron", expr: "*/15 * * * *", display: "*/15 * * * *" }, now)).toBe("每隔 15 分钟");
  });

  it("keeps an expression cronstrue cannot read as it is", () => {
    expect(scheduleText({ kind: "cron", expr: "not a cron", display: "not a cron" }, now)).toBe("not a cron");
  });

  it("says intervals in the largest whole unit", () => {
    expect(scheduleText({ kind: "interval", minutes: 30, display: "every 30m" }, now)).toBe("每 30 分钟");
    expect(scheduleText({ kind: "interval", minutes: 60, display: "every 60m" }, now)).toBe("每小时");
    expect(scheduleText({ kind: "interval", minutes: 180, display: "every 180m" }, now)).toBe("每 3 小时");
    expect(scheduleText({ kind: "interval", minutes: 1440, display: "every 1440m" }, now)).toBe("每天");
    expect(scheduleText({ kind: "interval", minutes: 10080, display: "every 10080m" }, now)).toBe("每 7 天");
  });

  it("says when a one-off runs", () => {
    expect(scheduleText({ kind: "once", runAt: at(10, 20, 15), display: "once at 2026-10-20 15:00" }, now)).toBe(
      "仅一次 · 今天 15:00",
    );
  });

  it("falls back to Hermes's own words", () => {
    expect(scheduleText({ kind: "webhook", display: "on webhook" }, now)).toBe("on webhook");
  });
});

describe("whenText", () => {
  it("names days near now", () => {
    expect(whenText(at(10, 20, 8, 5), now)).toBe("今天 08:05");
    expect(whenText(at(10, 21, 8), now)).toBe("明天 08:00");
    expect(whenText(at(10, 19, 23, 59), now)).toBe("昨天 23:59");
    expect(whenText(at(10, 25, 8), now)).toBe("10月25日 08:00");
    expect(whenText(at(1, 2, 8, 0, 2027), now)).toBe("2027年1月2日 08:00");
  });
});
