import type { ButtonHTMLAttributes } from "react";

import { useActionLock } from "./lock";
import styles from "./venus.module.css";

// ActionButton is a button for an action the lock holds back: it greys out
// by itself while the lock is on. It keeps a minimum width, so a label that
// changes (导出, 导出中) moves nothing, and a hit area of 24 × 24 at least;
// an icon-only button names itself with title.
export function ActionButton({ disabled, className, ...props }: ButtonHTMLAttributes<HTMLButtonElement>) {
  const locked = useActionLock();
  return (
    <button type="button" {...props} disabled={disabled || locked} className={`${styles.action} ${className ?? ""}`} />
  );
}
