import { describe, expect, it } from "vitest";

import { reminder, todo } from "../test/handlers/todos";
import { reminderLines, snoozed, urgent } from "./format";

const now = new Date(2026, 10, 17, 9, 30);

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
});

describe("reminderLines", () => {
  const reminders = [
    reminder({ todoId: "a", title: "交物业费", due: "2026-11-19" }),
    reminder({ todoId: "b", title: "儿童医院复查", category: "medical", due: "2026-11-17", stage: "today" }),
    reminder({ todoId: "c", title: "换护照", scope: "private", due: "2026-11-15", stage: "overdue" }),
  ];
  it("names every reminder on the account's own screen", () => {
    const { lines, privateCount } = reminderLines(reminders, false, now);
    expect(lines.map((line) => `${line.text} ${line.note}`)).toEqual([
      "交物业费 还有 2 天",
      "儿童医院复查 今天截止",
      "换护照 已过期 2 天",
    ]);
    expect(privateCount).toBe(0);
  });
  it("on the family's shared screen keeps a medical matter to its category and private ones to a count", () => {
    const { lines, privateCount } = reminderLines(reminders, true, now);
    expect(lines.map((line) => `${line.text} ${line.note}`)).toEqual(["交物业费 还有 2 天", "一件医疗的事 今天截止"]);
    expect(privateCount).toBe(1);
  });
});
