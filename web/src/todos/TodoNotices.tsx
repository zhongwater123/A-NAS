// 待办 tells each account, on the desktop, what is coming due: within three
// days, today, and overdue, once each. Only 知道了 tells the server; a notice
// that hides itself, is closed or is followed to the window stays hidden on
// this page alone, and comes back on the next visit until its reader says
// they know; on the local console, whose page never leaves, it comes back
// when someone wakes the screen. On the family's shared screen a medical
// matter shows only its category, and private to-dos only how many there are.
import { ListTodo } from "lucide-react";
import { useEffect, useState } from "react";

import { DeskNotice } from "../ui/DeskNotice";
import { reminderLines } from "./format";
import styles from "./notices.module.css";
import { readAgain, useAcknowledgeReminders, useTodoReminders } from "./queries";

const SHOWN = 3;

export function TodoNotices({
  userId,
  shared,
  onWake,
  onOpen,
}: {
  /** The signed-in account, whose reminders these are. */
  userId: string;
  /** The screen is the family's (the local console). */
  shared: boolean;
  /** On the local console, subscribes to someone waking the screen; the desktop passes it in, so this feature does not reach into desktop/. */
  onWake?: (listener: () => void) => () => void;
  onOpen(todoId: string): void;
}) {
  // Read after signing in, whenever the page comes back into view and just
  // after midnight, when to-dos move into their next stage (queries.ts).
  const reminders = useTodoReminders(userId);
  const acknowledge = useAcknowledgeReminders(userId);
  // Reminders hidden on this page; the server still has them unacknowledged.
  const [hidden, setHidden] = useState<ReadonlySet<string>>(new Set());

  // On the local console, someone waking the screen shows again what hid
  // itself, and reads what is due now.
  useEffect(
    () =>
      onWake?.(() => {
        setHidden(new Set());
        void readAgain(userId);
      }),
    [onWake, userId],
  );

  const showing = reminders.filter((reminder) => !hidden.has(reminder.key));
  // Nothing to show takes the notice away, so a pointer that was over it,
  // and gets no pointerleave, does not hold the next one up.
  if (!showing.length) return null;
  const hide = () => setHidden((current) => new Set([...current, ...showing.map((reminder) => reminder.key)]));
  const { lines, privateCount } = reminderLines(showing, shared);
  const shown = lines.slice(0, SHOWN);
  const more = lines.length - shown.length;
  return (
    <DeskNotice
      className={styles.tnNotice}
      label="待办提醒"
      source="待办"
      icon={<ListTodo />}
      lines={[...shown, ...(privateCount > 0 ? [`你有 ${privateCount} 件自己的待办要看`] : [])]}
      more={more > 0 ? `另有 ${more} 件` : undefined}
      action={
        shown.length > 0
          ? // A refusal puts the reminders back when they are read again; the notice says nothing more.
            { label: "知道了", onClick: () => acknowledge.mutate(shown.map((line) => line.key)) }
          : undefined
      }
      onOpen={() => onOpen(showing[0].todoId)}
      onDismiss={hide}
    />
  );
}
