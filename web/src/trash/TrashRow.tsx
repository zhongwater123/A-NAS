import { ArrowUpRight, Check, Minus, RotateCcw, Trash2 } from "lucide-react";
import { motion } from "motion/react";
import type { MouseEvent } from "react";

import type { Space, TrashItem } from "../api.generated";
import { formatClock, formatDateTime } from "../backup/format";
import { formatBytes } from "../lib/bytes";
import { FileGlyph } from "./FileGlyph";
import { type ExpiryTone, expiryOf, spaceName, splitName } from "./model";
import styles from "./trash.module.css";

export function Checkbox({
  checked,
  mixed,
  disabled,
  label,
  onChange,
}: {
  checked: boolean;
  mixed?: boolean;
  disabled?: boolean;
  label: string;
  onChange: (event: MouseEvent) => void;
}) {
  return (
    <button
      type="button"
      role="checkbox"
      aria-checked={mixed ? "mixed" : checked}
      aria-label={label}
      disabled={disabled}
      className={`${styles.trCheck}${checked || mixed ? ` ${styles.on}` : ""}`}
      onClick={onChange}
    >
      {mixed ? <Minus strokeWidth={3} /> : <Check strokeWidth={3} />}
    </button>
  );
}

// Name shows a file's name with its extension kept when the name is cut short.
export function Name({ item }: { item: Pick<TrashItem, "name" | "kind"> }) {
  const { stem, extension } = splitName(item.name, item.kind);
  return (
    <strong className={styles.trName} title={item.name}>
      <span>{stem}</span>
      {extension && <span>{extension}</span>}
    </strong>
  );
}

// TrashRow is one item: what it is and where it goes back to, its size, when
// it was deleted and how long it stays. A click on the row selects it.
export function TrashRow({
  item,
  space,
  timeOnly,
  selected,
  restoring,
  disabled,
  onToggle,
  onRestore,
  onPurge,
  onOpenOrigin,
}: {
  item: TrashItem;
  space?: Space;
  timeOnly: boolean;
  selected: boolean;
  restoring: boolean;
  disabled: boolean;
  onToggle: (range: boolean) => void;
  onRestore: () => void;
  onPurge: () => void;
  onOpenOrigin: () => void;
}) {
  const expiry = expiryOf(item);
  const path = [spaceName(space), ...(item.originalPath ? item.originalPath.split("/") : [])].join(" / ");
  return (
    // The checkbox selects by keyboard; a click anywhere else on the row is the same for a pointer.
    // oxlint-disable-next-line jsx-a11y/click-events-have-key-events, jsx-a11y/no-noninteractive-element-interactions
    <motion.li
      className={`${styles.trRow}${selected ? ` ${styles.selected}` : ""}`}
      data-item={item.id}
      exit={{ opacity: 0, height: 0, minHeight: 0, paddingTop: 0, paddingBottom: 0 }}
      transition={{ duration: 0.22, ease: [0.2, 0.8, 0.2, 1] }}
      onClick={(event) => {
        if (disabled || (event.target as HTMLElement).closest("button")) return;
        onToggle(event.shiftKey);
      }}
    >
      <Checkbox
        checked={selected}
        disabled={disabled}
        label={`选择 ${item.name}`}
        onChange={(event) => onToggle(event.shiftKey)}
      />
      <FileGlyph name={item.name} kind={item.kind} />
      <div className={styles.trMain}>
        <Name item={item} />
        {item.originalGone ? (
          <span className={styles.trOrigin} title={`原文件夹已不在，恢复后放在${spaceName(space)}`}>
            <span>{path}</span>
            <em>已不在</em>
          </span>
        ) : (
          <button
            type="button"
            className={styles.trOrigin}
            title="在文件管理中打开"
            aria-label={`在文件管理中打开 ${path}`}
            onClick={onOpenOrigin}
          >
            <span>{path}</span>
            <ArrowUpRight />
          </button>
        )}
      </div>
      {item.kind === "directory" && item.sizeBytes === 0 ? (
        <span className={`${styles.trSize} ${styles.unknown}`} title="文件夹中没有已统计的文件">
          —
        </span>
      ) : (
        <span className={styles.trSize}>{formatBytes(item.sizeBytes)}</span>
      )}
      <span className={styles.trTime} title={`删除于 ${formatDateTime(item.deletedAt)}`}>
        <span className={styles.trLabel}>删除于 </span>
        {timeOnly ? formatClock(item.deletedAt) : formatDateTime(item.deletedAt)}
      </span>
      <span
        className={`${styles.trExpiry} ${styles[expiry.tone] ?? ""}`}
        title={`将于 ${formatDateTime(item.purgeAt)} 永久删除`}
      >
        <ExpiryRing left={expiry.left} tone={expiry.tone} />
        <span>
          <span className={styles.trLabel}>剩余 </span>
          {expiry.short}
        </span>
      </span>
      <div className={styles.trActions}>
        <button
          type="button"
          className={styles.trRestore}
          aria-label={`恢复 ${item.name}`}
          aria-busy={restoring}
          disabled={disabled || restoring}
          onClick={onRestore}
        >
          {restoring ? <span className={styles.trSpinner} aria-hidden="true" /> : <RotateCcw />}
          <span>恢复</span>
        </button>
        <button
          type="button"
          className={styles.trPurge}
          aria-label={`永久删除 ${item.name}`}
          title="永久删除"
          disabled={disabled || restoring}
          onClick={onPurge}
        >
          <Trash2 />
        </button>
      </div>
    </motion.li>
  );
}

// ExpiryRing shows how much of an item's time in the bin is left, as a
// dial that empties.
function ExpiryRing({ left, tone }: { left: number; tone: ExpiryTone }) {
  // A stroke as wide as twice its radius fills the dial as a wedge.
  const radius = 2.6;
  const circumference = 2 * Math.PI * radius;
  return (
    <svg className={`${styles.trRing} ${styles[tone] ?? ""}`} viewBox="0 0 16 16" aria-hidden="true">
      <circle cx="8" cy="8" r="6.9" />
      <circle
        cx="8"
        cy="8"
        r={radius}
        strokeWidth={radius * 2}
        strokeDasharray={`${circumference * Math.max(left, 0.06)} ${circumference}`}
      />
    </svg>
  );
}
