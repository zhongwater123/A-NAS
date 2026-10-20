// The member's unread results of scheduled tasks: the account's assistant_task
// notifications, at most one per task. The desktop's toasts and the VNAS window
// share them; reading a run anywhere clears its task's notice on every device.
import { useSyncExternalStore } from "react";

import { type Notification, acknowledgeNotification, listNotifications } from "../api";

let notices: Notification[] = [];
const listeners = new Set<() => void>();
const changed = () => listeners.forEach((listener) => listener());

export async function loadTaskNotices(): Promise<void> {
  try {
    const list = await listNotifications();
    notices = list.filter((item) => item.kind === "assistant_task" && item.task);
    changed();
  } catch {
    // The next load tries again.
  }
}

export function readTaskNotice(id: string): void {
  if (!notices.some((item) => item.id === id)) return;
  notices = notices.filter((item) => item.id !== id);
  changed();
  void acknowledgeNotification(id).catch(() => undefined);
}

/** A run of the task was read: its task's notice goes. */
export function readTaskNotices(taskId: string): void {
  for (const item of notices.filter((notice) => notice.task?.taskId === taskId)) readTaskNotice(item.id);
}

/** The task a run's conversation belongs to (cron_<task>_<time>). */
export function taskOfRun(sessionId: string): string | undefined {
  return /^cron_([0-9a-f]{12})_/.exec(sessionId)?.[1];
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => { listeners.delete(listener); };
}

export const useTaskNotices = () => useSyncExternalStore(subscribe, () => notices);
