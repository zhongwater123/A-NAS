// The account's to-do reminders as last read. The desktop's notice and the
// 待办 window share them, so 知道了 in one takes the reminder away in both.
import { useSyncExternalStore } from "react";

import { acknowledgeTodoReminders, listTodoReminders, type TodoReminder } from "./api";

let reminders: TodoReminder[] = [];
const listeners = new Set<() => void>();
const changed = () => listeners.forEach((listener) => listener());

export async function loadTodoReminders(): Promise<void> {
  try {
    reminders = await listTodoReminders();
    changed();
  } catch {
    // The next read tries again.
  }
}

/** The reader knows of these. When the server did not take it, the reminders are read again and the error is passed on. */
export async function acknowledgeTodoReminder(keys: string[]): Promise<void> {
  reminders = reminders.filter((reminder) => !keys.includes(reminder.key));
  changed();
  try {
    await acknowledgeTodoReminders(keys);
  } catch (caught) {
    void loadTodoReminders();
    throw caught;
  }
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export const useTodoReminders = () => useSyncExternalStore(subscribe, () => reminders);

/** For tests: nothing read yet. */
export function resetTodoReminders(): void {
  reminders = [];
  changed();
}
