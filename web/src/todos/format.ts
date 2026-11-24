// Which to-dos are urgent or waiting, whose they are, and the lines of the
// desktop's notice. How a due date is worded is lib/time's dueText; the
// order they list in is the server's.
import { daysUntil, dueText, type TimeValue } from "../lib/time";
import { nameOfAccount, type People } from "../people/people";
import { CATEGORY_LABEL, type Todo, type TodoReminder } from "./api";

// Reminders start this many days before the due date.
const SOON_DAYS = 3;

// urgent says a to-do is due within the reminder days or overdue.
export function urgent(todo: Pick<Todo, "due">, now: TimeValue = Date.now()): boolean {
  return Boolean(todo.due) && daysUntil(todo.due ?? "", now) <= SOON_DAYS;
}

// snoozed says a to-do waits until a later day.
export function snoozed(todo: Pick<Todo, "status" | "snoozedUntil">, now: TimeValue = Date.now()): boolean {
  return todo.status === "open" && Boolean(todo.snoozedUntil) && daysUntil(todo.snoozedUntil ?? "", now) > 0;
}

// accountName names an account a to-do records, by its profile, or by the
// name the to-do kept once the account is gone.
export function accountName(people: People, id: string, kept: string | undefined): string {
  return nameOfAccount(people, id, kept ?? "已删除的账号");
}

// reminderLines turns reminders into the lines of the desktop's notice. On
// the family's shared screen (shared), a medical matter shows only its
// category and someone's private to-dos only how many there are.
export function reminderLines(reminders: TodoReminder[], shared: boolean, now: TimeValue = Date.now()) {
  const lines: Array<{ key: string; text: string; note: string }> = [];
  let hidden = 0;
  for (const reminder of reminders) {
    if (shared && reminder.scope === "private") {
      hidden += 1;
      continue;
    }
    lines.push({
      key: reminder.key,
      text: shared && reminder.category === "medical" ? `一件${CATEGORY_LABEL.medical}的事` : reminder.title,
      note: dueText(reminder.due, now),
    });
  }
  return { lines, privateCount: hidden };
}
