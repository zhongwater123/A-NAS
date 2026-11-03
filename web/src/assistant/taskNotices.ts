// The member's unread results of scheduled tasks: the account's assistant_task
// notifications, at most one per task. The desktop's toasts and the VNAS window
// share them; reading a run anywhere clears its task's notice on every device.
import { useLayoutEffect, useSyncExternalStore } from "react";

import type { Notification } from "../api.generated";
import { acknowledgeNotification, listNotifications } from "../desktop/api";

let notices: Notification[] = [];
const empty: Notification[] = [];
let member: string | undefined;
let memberEpoch = 0;
let revision = 0;
let readSequence = 0;
const listeners = new Set<() => void>();
const changed = () => listeners.forEach((listener) => listener());

export function setTaskNoticeMember(id: string): void {
  if (member === id) return;
  member = id;
  memberEpoch++;
  revision++;
  notices = [];
  changed();
}

export async function loadTaskNotices(): Promise<void> {
  const started = revision;
  const sequence = ++readSequence;
  try {
    const list = await listNotifications();
    if (started !== revision || sequence !== readSequence) return;
    notices = list.filter((item) => item.kind === "assistant_task" && item.task);
    changed();
  } catch {
    // The next load tries again.
  }
}

export function readTaskNotice(id: string): void {
  void acknowledgeTaskNotice(id);
}

/** Keep the result unread if the server did not accept the receipt. */
export async function acknowledgeTaskNotice(id: string): Promise<boolean> {
  const epoch = memberEpoch;
  if (!notices.some((item) => item.id === id)) return false;
  try {
    await acknowledgeNotification(id);
    if (memberEpoch !== epoch) return false;
    revision++;
    notices = notices.filter((item) => item.id !== id);
    changed();
    return true;
  } catch {
    return false;
  }
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
  return () => {
    listeners.delete(listener);
  };
}

export function useTaskNotices(memberId?: string) {
  useLayoutEffect(() => {
    if (memberId !== undefined) setTaskNoticeMember(memberId);
  }, [memberId]);
  return useSyncExternalStore(subscribe, () => (memberId !== undefined && member !== memberId ? empty : notices));
}
