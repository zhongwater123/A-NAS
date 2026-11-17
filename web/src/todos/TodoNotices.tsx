// 待办 tells each account, on the desktop, what is coming due: within three
// days, today, and overdue, once each. Only 知道了 tells the server; a notice
// that hides itself, is closed or is followed to the window stays hidden on
// this page alone, and comes back on the next visit until its reader says
// they know; on the local console, whose page never leaves, it comes back
// when someone wakes the screen. On the family's shared screen a medical
// matter shows only its category, and private to-dos only how many there are.
import { ChevronRight, ListTodo, X } from "lucide-react";
import { useEffect, useState } from "react";

import { untilNextDay } from "../lib/time";
import { reminderLines } from "./format";
import styles from "./notices.module.css";
import { acknowledgeTodoReminder, loadTodoReminders, useTodoReminders } from "./reminders";

const SHOWN = 3;
const HIDE_MS = 12_000;

export function TodoNotices({
  shared,
  onWake,
  onOpen,
}: {
  /** The screen is the family's (the local console). */
  shared: boolean;
  /** On the local console, subscribes to someone waking the screen; the desktop passes it in, so this feature does not reach into desktop/. */
  onWake?: (listener: () => void) => () => void;
  onOpen(todoId: string): void;
}) {
  const reminders = useTodoReminders();
  // Reminders hidden on this page; the server still has them unacknowledged.
  const [hidden, setHidden] = useState<ReadonlySet<string>>(new Set());
  const [held, setHeld] = useState(false);

  // Read after signing in and whenever the page comes back into view. A page
  // that stays open, such as the local console, reads again just after
  // midnight, when to-dos move into their next stage, and the local console
  // also when someone wakes its screen.
  useEffect(() => {
    let timer = 0;
    const read = () => void loadTodoReminders();
    const nextDay = () => {
      window.clearTimeout(timer);
      timer = window.setTimeout(() => {
        read();
        nextDay();
      }, untilNextDay());
    };
    const back = () => {
      if (document.visibilityState !== "visible") return;
      read();
      nextDay();
    };
    read();
    nextDay();
    document.addEventListener("visibilitychange", back);
    const woken = onWake?.(() => {
      setHidden(new Set());
      read();
    });
    return () => {
      window.clearTimeout(timer);
      document.removeEventListener("visibilitychange", back);
      woken?.();
    };
  }, [onWake]);

  const showing = reminders.filter((reminder) => !hidden.has(reminder.key));
  const showingKeys = showing.map((reminder) => reminder.key).join(" ");
  const hide = () => setHidden((current) => new Set([...current, ...showing.map((reminder) => reminder.key)]));
  // A notice taken away under the pointer gets no pointerleave: the next one is not held.
  if (!showingKeys && held) setHeld(false);
  useEffect(() => {
    if (!showingKeys || held) return;
    const timer = window.setTimeout(
      () => setHidden((current) => new Set([...current, ...showingKeys.split(" ")])),
      HIDE_MS,
    );
    return () => window.clearTimeout(timer);
  }, [showingKeys, held]);

  if (!showing.length) return null;
  const { lines, privateCount } = reminderLines(showing, shared);
  const shown = lines.slice(0, SHOWN);
  const more = lines.length - shown.length;
  const acknowledge = () => {
    const keys = shown.map((line) => line.key);
    if (keys.length) void acknowledgeTodoReminder(keys).catch(() => undefined);
  };
  return (
    // Pointing at the notice or focusing it keeps it up.
    <div
      className={styles.tnHolder}
      // VNAS does not look at the desktop's notices when it reads the desktop.
      data-vnas-overlay=""
      onPointerEnter={() => setHeld(true)}
      onPointerLeave={() => setHeld(false)}
      onFocus={() => setHeld(true)}
      onBlur={() => setHeld(false)}
    >
      <section className={styles.tnToast} role="status" aria-label="待办提醒">
        <span className={styles.tnMark} aria-hidden="true">
          <ListTodo />
        </span>
        <div className={styles.tnCopy}>
          <small>待办</small>
          <ul>
            {shown.map((line) => (
              <li key={line.key}>
                <strong>{line.title}</strong>
                <span>{line.when}</span>
              </li>
            ))}
            {privateCount > 0 && (
              <li>
                <strong>你有 {privateCount} 件自己的待办要看</strong>
              </li>
            )}
          </ul>
          {more > 0 && <p className={styles.tnMore}>另有 {more} 件</p>}
          <div className={styles.tnActions}>
            {shown.length > 0 && (
              <button type="button" className={styles.tnKnown} onClick={acknowledge}>
                知道了
              </button>
            )}
            <button
              type="button"
              className={styles.tnOpen}
              onClick={() => {
                hide();
                onOpen(showing[0].todoId);
              }}
            >
              去看看
              <ChevronRight />
            </button>
          </div>
        </div>
        <button type="button" className={styles.tnClose} aria-label="关闭" onClick={hide}>
          <X />
        </button>
      </section>
    </div>
  );
}
