import { Tooltip } from "radix-ui";

import { formatShortDay } from "../backup/format";
import { formatBytes } from "../lib/bytes";
import type { PurgeDay } from "./model";
import styles from "./trash.module.css";

// PurgeStrip draws the coming days as a row of bars: how much the recycle
// bin deletes for good on each. A day's bar filters the list to its items.
export function PurgeStrip({
  days,
  selected,
  onSelect,
}: {
  days: PurgeDay[];
  selected?: number;
  onSelect: (offset?: number) => void;
}) {
  const peak = Math.max(1, ...days.map((day) => day.bytes));
  return (
    <div className={styles.trStrip} role="group" aria-label="清理日程">
      <span className={styles.trStripTitle}>清理日程</span>
      <div className={styles.trStripBars} style={{ gridTemplateColumns: `repeat(${days.length}, minmax(0, 1fr))` }}>
        {days.map((day) =>
          day.count === 0 ? (
            <i key={day.offset} />
          ) : (
            <Tooltip.Root key={day.offset}>
              <Tooltip.Trigger asChild>
                <button
                  type="button"
                  className={styles[day.tone]}
                  aria-pressed={selected === day.offset}
                  aria-label={`${formatShortDay(day.date)}清理 ${day.count} 项，共 ${formatBytes(day.bytes)}`}
                  onClick={() => onSelect(selected === day.offset ? undefined : day.offset)}
                >
                  {/* The square root keeps a small day's bar visible beside a large one. */}
                  <i style={{ height: `${Math.round(24 + 76 * Math.sqrt(day.bytes / peak))}%` }} />
                </button>
              </Tooltip.Trigger>
              <Tooltip.Portal>
                <Tooltip.Content className={`${styles.trFloat} ${styles.trTip}`} sideOffset={6}>
                  <strong>{formatShortDay(day.date)}</strong>
                  {day.count} 项 · {formatBytes(day.bytes)}
                </Tooltip.Content>
              </Tooltip.Portal>
            </Tooltip.Root>
          ),
        )}
      </div>
      <div className={styles.trStripAxis}>
        <span>今天</span>
        <span>{formatShortDay(days[days.length - 1].date)}</span>
      </div>
    </div>
  );
}
