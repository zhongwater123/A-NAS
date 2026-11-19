// A notice a feature leaves on the desktop, under the status bar: what needs
// the member, a line each, and a way into the feature's window. It hides
// itself after a while unless the pointer or the focus is on it. 家庭中心 and
// 剪辑中心 show theirs with it; each sets its place and colours through the
// --desk-notice-* variables of a class of its own.
import { ChevronRight, X } from "lucide-react";
import { type ReactNode, useEffect, useState } from "react";

import styles from "./deskNotice.module.css";

const HIDE_MS = 15_000;

export function DeskNotice({
  label,
  source,
  icon,
  lines,
  onOpen,
  onDismiss,
  className,
}: {
  // label names the notice for assistive technology; source is the feature, as the notice shows it.
  label: string;
  source: string;
  icon: ReactNode;
  lines: string[];
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
          {lines.map((line) => (
            <strong key={line}>{line}</strong>
          ))}
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
        <button type="button" className={styles.deskNoticeClose} aria-label="关闭" onClick={onDismiss}>
          <X />
        </button>
      </section>
    </div>
  );
}
