import type { ReactNode } from "react";

import type { TrashItem } from "../api.generated";
import { type FileCategory, fileCategory } from "../lib/fileTypes";
import styles from "./trash.module.css";

// What each kind of file shows on its page, drawn in the kind's colour.
const marks: Record<FileCategory, ReactNode> = {
  document: <path d="M25 34h22M25 43h22M25 52h13" />,
  sheet: (
    <>
      <rect x="24" y="32" width="24" height="22" rx="3.5" />
      <path d="M24 43h24M34 32v22" />
    </>
  ),
  pdf: (
    <>
      <path d="M25 33h22M25 41h12" />
      <rect className={styles.solid} x="24" y="47" width="24" height="9" rx="3" />
    </>
  ),
  image: (
    <>
      <circle className={styles.solid} cx="29.5" cy="34.5" r="3.6" />
      <path className={styles.solid} d="M23 55.5l9-10.5 5.6 6 4.4-4.6 6.5 9.1z" />
    </>
  ),
  video: <path className={styles.solid} d="M30.5 34.2v18.6l15.4-9.3z" />,
  audio: (
    <>
      <path d="M32.5 51.5v-17l13-3v16.5" />
      <circle className={styles.solid} cx="29" cy="51.5" r="3.8" />
      <circle className={styles.solid} cx="42" cy="48" r="3.8" />
    </>
  ),
  archive: (
    <>
      <path d="M36 25v12" strokeDasharray="3.4 3.4" />
      <rect x="31.5" y="41" width="9" height="12" rx="3" />
    </>
  ),
  other: null,
};

// FileGlyph draws a trash item's icon: a folder, or a page marked with what
// kind of file it is. It is drawn at twice its shown size.
export function FileGlyph({ name, kind }: Pick<TrashItem, "name" | "kind">) {
  if (kind === "directory")
    return (
      <svg className={`${styles.trGlyph} ${styles.folder}`} viewBox="0 0 72 72" aria-hidden="true">
        <path
          className={styles.back}
          d="M8 21a6 6 0 0 1 6-6h14.3a6 6 0 0 1 4.5 2l3 3.4H58a6 6 0 0 1 6 6V52a6 6 0 0 1-6 6H14a6 6 0 0 1-6-6z"
        />
        <path className={styles.front} d="M8 31a5 5 0 0 1 5-5h46a5 5 0 0 1 5 5v21a6 6 0 0 1-6 6H14a6 6 0 0 1-6-6z" />
      </svg>
    );
  const category = fileCategory(name);
  return (
    <svg className={`${styles.trGlyph} ${styles[category] ?? ""}`} viewBox="0 0 72 72" aria-hidden="true">
      <path className={styles.page} d="M21 7h21.6L57 21.4V60a5 5 0 0 1-5 5H21a5 5 0 0 1-5-5V12a5 5 0 0 1 5-5z" />
      <path className={styles.fold} d="M42.6 7v9.4a5 5 0 0 0 5 5H57z" />
      <g className={styles.mark}>{marks[category]}</g>
    </svg>
  );
}
