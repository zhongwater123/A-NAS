import type { ReactNode } from "react";

import styles from "./venus.module.css";

// Slot stands where a feature goes, named by its PRD number, until the
// feature replaces it; remove this once no slot is left.
export function Slot({ id, children }: { id: string; children?: ReactNode }) {
  return (
    <section className={styles.slot} aria-label={id} data-slot={id}>
      <span className={styles.slotId}>{id}</span>
      {children}
    </section>
  );
}
