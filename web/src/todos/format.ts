// Which to-dos are urgent or waiting, in what order they list, and the lines
// of the desktop's notice. How a due date is worded is lib/time's dueText.
import { daysUntil, dueText, type TimeValue } from "../lib/time";
import { CATEGORY_LABEL, type Todo, type TodoReminder } from "./api";

// Reminders start this many days before the due date.
export const SOON_DAYS = 3;

// urgent says a to-do is due within the reminder days or overdue.
export function urgent(todo: Pick<Todo, "due">, now: TimeValue = Date.now()): boolean {
  return Boolean(todo.due) && daysUntil(todo.due ?? "", now) <= SOON_DAYS;
}

// snoozed says a to-do waits until a later day.
export function snoozed(todo: Pick<Todo, "status" | "snoozedUntil">, now: TimeValue = Date.now()): boolean {
  return todo.status === "open" && Boolean(todo.snoozedUntil) && daysUntil(todo.snoozedUntil ?? "", now) > 0;
}

// byUrgency orders open to-dos the most urgent first, those without a due
// date last; closed ones the latest closed first.
export function byUrgency(a: Todo, b: Todo): number {
  if ((a.status === "open") !== (b.status === "open")) return a.status === "open" ? -1 : 1;
  if (a.status !== "open") return (b.closedAt ?? "").localeCompare(a.closedAt ?? "");
  if (a.due !== b.due) {
    if (!a.due) return 1;
    if (!b.due) return -1;
    return a.due.localeCompare(b.due);
  }
  return a.createdAt.localeCompare(b.createdAt);
}

export interface ReminderLine {
  key: string;
  title: string;
  when: string;
}

// reminderLines turns reminders into the lines of the desktop's notice. On
// the family's shared screen (shared), a medical matter shows only its
// category and someone's private to-dos only how many there are.
export function reminderLines(reminders: TodoReminder[], shared: boolean, now: TimeValue = Date.now()) {
  const lines: ReminderLine[] = [];
  let hidden = 0;
  for (const reminder of reminders) {
    if (shared && reminder.scope === "private") {
      hidden += 1;
      continue;
    }
    lines.push({
      key: reminder.key,
      title: shared && reminder.category === "medical" ? `一件${CATEGORY_LABEL.medical}的事` : reminder.title,
      when: dueText(reminder.due, now),
    });
  }
  return { lines, privateCount: hidden };
}
