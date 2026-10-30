import { motion } from "motion/react";
import { useRef, useState } from "react";

import styles from "./family.module.css";
import { monthLabel } from "./format";

// Scrubber is a ruler of the baby's months of life beside the timeline,
// now at the top and birth at the bottom. Months with records carry a dot,
// the month in view a marker; pressing or dragging along it jumps there,
// and the arrow keys step a month at a time.
export function Scrubber({
  now,
  filled,
  active,
  onJump,
}: {
  // now is the baby's current month of life.
  now: number;
  filled: Set<number>;
  active?: number;
  onJump: (month: number, smooth: boolean) => void;
}) {
  const track = useRef<HTMLDivElement>(null);
  const [hover, setHover] = useState<number>();
  const pressed = useRef<number | undefined>(undefined);
  const span = Math.max(1, now);
  const at = (month: number) => `${((now - month) / span) * 100}%`;
  const monthAt = (clientY: number) => {
    const box = track.current!.getBoundingClientRect();
    const share = Math.min(1, Math.max(0, (clientY - box.top) / box.height));
    return Math.round(now - share * span);
  };
  const years = Array.from({ length: Math.floor(now / 12) + 1 }, (_, index) => index * 12);
  const shown = active ?? now;
  return (
    <div
      className={styles.fcScrubber}
      role="slider"
      tabIndex={0}
      aria-label="按月龄跳转"
      aria-orientation="vertical"
      aria-valuemin={0}
      aria-valuemax={now}
      aria-valuenow={shown}
      aria-valuetext={monthLabel(shown)}
      onKeyDown={(event) => {
        const step = event.key === "ArrowUp" ? 1 : event.key === "ArrowDown" ? -1 : 0;
        if (!step) return;
        event.preventDefault();
        onJump(Math.min(now, Math.max(0, shown + step)), true);
      }}
      onPointerDown={(event) => {
        event.currentTarget.setPointerCapture(event.pointerId);
        pressed.current = monthAt(event.clientY);
        onJump(pressed.current, true);
      }}
      onPointerMove={(event) => {
        const month = monthAt(event.clientY);
        setHover(month);
        if (pressed.current !== undefined && month !== pressed.current) {
          pressed.current = month;
          onJump(month, false);
        }
      }}
      onPointerUp={() => {
        pressed.current = undefined;
      }}
      onPointerLeave={() => setHover(undefined)}
    >
      <div className={styles.fcScrubberTrack} ref={track}>
        {Array.from({ length: now + 1 }, (_, month) => (
          <i
            key={month}
            className={`${month % 12 === 0 ? styles.year : ""} ${filled.has(month) ? styles.filled : ""}`}
            style={{ top: at(month) }}
          />
        ))}
        {years.map((month) => (
          <small key={month} style={{ top: at(month) }}>
            {month === 0 ? "出生" : `${month / 12}岁`}
          </small>
        ))}
        <motion.span
          className={styles.fcScrubberThumb}
          initial={false}
          animate={{ top: at(shown) }}
          transition={{ type: "spring", stiffness: 420, damping: 40 }}
        />
        {hover !== undefined && (
          <span className={styles.fcScrubberHint} style={{ top: at(hover) }}>
            {monthLabel(hover)}
          </span>
        )}
      </div>
    </div>
  );
}
