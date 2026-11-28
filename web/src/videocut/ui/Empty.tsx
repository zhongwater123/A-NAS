import type { ReactNode } from "react";

import styles from "./venus.module.css";

// Empty says, in one sentence, why there is nothing here; no picture (PRD A.5).
export function Empty({ text, action }: { text: string; action?: ReactNode }) {
  return (
    <div className={styles.empty}>
      <p>{text}</p>
      {action}
    </div>
  );
}
