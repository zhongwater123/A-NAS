import { describe, expect, it } from "vitest";

import { reminder, todo } from "../test/handlers/todos";
import { byUrgency, dueText, reminderLines, snoozed, urgent } from "./format";

const now = new Date(2026, 10, 17, 9, 30);

describe("dueText", () => {
  it("says how far off the due date is, as the prototype does", () => {
    expect(dueText("2026-11-20", now)).toBe("还有 3 天");
    expect(dueText("2026-11-18", now)).toBe("还有 1 天");
    expect(dueText("2026-11-17", now)).toBe("今天截止");
    expect(dueText("2026-11-16", now)).toBe("已过期 1 天");
  });
  it("counts calendar days across the year's end", () => {
    const newYearsEve = new Date(2026, 11, 31, 23, 50);
    expect(dueText("2027-01-02", newYearsEve)).toBe("还有 2 天");
    expect(dueText("2026-12-29", new Date(2027, 0, 1, 0, 5))).toBe("已过期 3 天");
  });
});

describe("to-do states", () => {
  it("marks what is due within three days or overdue as urgent", () => {
    expect(urgent(todo({ due: "2026-11-20" }), now)).toBe(true);
    expect(urgent(todo({ due: "2026-11-21" }), now)).toBe(false);
    expect(urgent(todo({ due: "2026-11-01" }), now)).toBe(true);
    expect(urgent(todo(), now)).toBe(false);
  });
  it("keeps a snoozed to-do waiting until the day it was snoozed to", () => {
    expect(snoozed(todo({ snoozedUntil: "2026-11-18" }), now)).toBe(true);
    expect(snoozed(todo({ snoozedUntil: "2026-11-17" }), now)).toBe(false);
    expect(snoozed(todo({ snoozedUntil: "2026-11-18", status: "done" }), now)).toBe(false);
  });
  it("orders the most urgent first, undated last and closed ones the latest first", () => {
    const items = [
      todo({ id: "closed-early", status: "done", closedAt: "2026-11-10T08:00:00Z" }),
      todo({ id: "undated" }),
      todo({ id: "later", due: "2026-12-01" }),
      todo({ id: "closed-late", status: "ignored", closedAt: "2026-11-15T08:00:00Z" }),
      todo({ id: "sooner", due: "2026-11-18" }),
    ];
    expect(items.toSorted(byUrgency).map((item) => item.id)).toEqual([
      "sooner",
      "later",
      "undated",
      "closed-late",
      "closed-early",
    ]);
  });
});

describe("reminderLines", () => {
  const reminders = [
    reminder({ todoId: "a", title: "交物业费", due: "2026-11-19" }),
    reminder({ todoId: "b", title: "儿童医院复查", category: "medical", due: "2026-11-17", stage: "today" }),
    reminder({ todoId: "c", title: "换护照", scope: "private", due: "2026-11-15", stage: "overdue" }),
  ];
  it("names every reminder on the account's own screen", () => {
    const { lines, privateCount } = reminderLines(reminders, false, now);
    expect(lines.map((line) => `${line.title} ${line.when}`)).toEqual([
      "交物业费 还有 2 天",
      "儿童医院复查 今天截止",
      "换护照 已过期 2 天",
    ]);
    expect(privateCount).toBe(0);
  });
  it("on the family's shared screen keeps a medical matter to its category and private ones to a count", () => {
    const { lines, privateCount } = reminderLines(reminders, true, now);
    expect(lines.map((line) => `${line.title} ${line.when}`)).toEqual(["交物业费 还有 2 天", "一件医疗的事 今天截止"]);
    expect(privateCount).toBe(1);
  });
});
