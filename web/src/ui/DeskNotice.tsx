// A notice a feature leaves on the desktop, under the status bar: what needs
// the member, a line each, and a way into the feature's window. It hides
// itself after a while unless the pointer or the focus is on it. 家庭中心,
// 剪辑中心 and 待办 show theirs with it; the desktop stacks the notices one
// under another, and each feature sets its colours through the --desk-notice-*
// variables of a class of its own.
import { ChevronRight, X } from "lucide-react";
import { type ReactNode, useEffect, useState } from "react";

import styles from "./deskNotice.module.css";

const HIDE_MS = 15_000;

// A line is a sentence, or a thing kept to one line with a short note after
// it, such as a to-do and when it is due.
export type DeskNoticeLine = string | { key: string; text: string; note?: string };

export function DeskNotice({
  label,
  source,
  icon,
  lines,
  more,
  action,
  onOpen,
  onDismiss,
  className,
}: {
  // label names the notice for assistive technology; source is the feature, as the notice shows it.
  label: string;
  source: string;
  icon: ReactNode;
  lines: DeskNoticeLine[];
  // more says, under the lines, what the notice leaves out, such as "另有 2 件".
  more?: string;
  // action is the feature's own button beside 去看看, such as 知道了; the notice stays until the feature takes it away.
  action?: { label: string; onClick: () => void };
  // onOpen opens the feature's window, after onDismiss; onDismiss also runs when the notice hides itself.
  onOpen: () => void;
  onDismiss: () => void;
  className?: string;
}) {
  const [held, setHeld] = useState(false);

  useEffect(() => {
    if (!lines.length || held) return;
    const timer = window.setTimeout(onDismiss, HIDE_MS);
    return () => window.clearTimeout(timer);
  });

  if (!lines.length) return null;
  return (
    // Pointing at the notice or focusing it keeps it up.
    <div
      className={`${styles.deskNotice} ${className ?? ""}`}
      data-vnas-overlay=""
      onPointerEnter={() => setHeld(true)}
      onPointerLeave={() => setHeld(false)}
      onFocus={() => setHeld(true)}
      onBlur={() => setHeld(false)}
    >
      <section className={styles.deskNoticeToast} role="status" aria-label={label}>
        <span className={styles.deskNoticeMark} aria-hidden="true">
          {icon}
        </span>
        <div className={styles.deskNoticeCopy}>
          <small>{source}</small>
          {lines.map((line) =>
            typeof line === "string" ? (
              <strong key={line}>{line}</strong>
            ) : (
              <span key={line.key} className={styles.deskNoticeRow}>
                <strong>{line.text}</strong>
                {line.note && <span>{line.note}</span>}
              </span>
            ),
          )}
          {more && <p className={styles.deskNoticeMore}>{more}</p>}
          <div className={styles.deskNoticeActions}>
            {action && (
              <button type="button" className={styles.deskNoticeAction} onClick={action.onClick}>
                {action.label}
              </button>
            )}
            <button
              type="button"
              className={styles.deskNoticeOpen}
              onClick={() => {
                onDismiss();
                onOpen();
              }}
            >
              去看看
              <ChevronRight />
            </button>
          </div>
        </div>
        <button type="button" className={styles.deskNoticeClose} aria-label="关闭" onClick={onDismiss}>
          <X />
        </button>
      </section>
    </div>
  );
}
