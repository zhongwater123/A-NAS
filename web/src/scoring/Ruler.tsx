import { Fragment, type PointerEvent, type RefObject, useRef, useState } from "react";

import { claimThumbnailURL } from "./api";
import { clampScore, formatScore, lanes, maxScore } from "./model";
import styles from "./scoring.module.css";
import { useSize } from "./useSize";

// A thumbnail on the ruler: one of the rater's latest scores.
export interface Mark {
  token: string;
  // In hundredths.
  score: number;
  width: number;
  height: number;
}

export const thumbHeight = 36;
const thumbMaxWidth = 84;
// Lanes count up from the baseline; lane 0 sits just above it.
const laneTops = [84, 42, 0];
const baseline = 132;
// The loupe: ticks within this many hundredths of the needle grow.
const loupe = 60;
// Thumbnails within this many hundredths of the needle light up.
const nearby = 25;
// A numeral this close to the pointer's reading gives way to it.
const covered = 32;
const tenths = Array.from({ length: 51 }, (_, i) => i);

export const thumbWidth = (mark: Pick<Mark, "width" | "height">) =>
  Math.min(thumbMaxWidth, (thumbHeight * mark.width) / Math.max(1, mark.height));

// Ruler is the score from 0 to 5 in hundredths, drawn as a fine scale:
// dragged or clicked with the pointer, moved with the keys the stage reads.
// Ticks near the needle grow as under a loupe. While nothing is set, the dot
// follows the pointer over the scale; once set, a faint line shows where a
// click would land. The pointer's reading takes the numerals' row below the
// baseline, where no thumbnail covers it. A pale dot marks the last photo's
// score. Above the scale the rater's own latest scores stand as whole
// thumbnails at their scores, in up to three lanes; the one under the pointer
// or set beside the photo comes to the front and the others step back.
// Releasing the pointer after a pick tells the stage, which moves on.
export function Ruler({
  value,
  label,
  marks,
  last,
  active,
  arriving,
  onChange,
  onRelease,
  onCompare,
  rulerRef,
}: {
  value: number | undefined;
  label: string;
  marks: Mark[];
  last?: number;
  active?: string;
  arriving?: string;
  onChange: (hundredths: number) => void;
  onRelease?: () => void;
  onCompare: (mark: Mark | undefined) => void;
  rulerRef: RefObject<HTMLDivElement | null>;
}) {
  const { width } = useSize(rulerRef);
  const [dragging, setDragging] = useState(false);
  const [hover, setHover] = useState<number>();
  const [hoveredThumb, setHoveredThumb] = useState<string>();
  const dragged = useRef(false);
  const at = (hundredths: number) => `${(hundredths / maxScore) * 100}%`;
  const valueAt = (clientX: number) => {
    const box = rulerRef.current?.getBoundingClientRect();
    if (!box || box.width === 0) return value ?? hover ?? 0;
    return clampScore(((clientX - box.left) / box.width) * maxScore);
  };
  const placed = lanes(
    marks.map((mark) => ({ at: mark.score / maxScore, width: thumbWidth(mark) })),
    width,
  );
  const set = value !== undefined;
  // The reading below the scale: the value while dragging, else the pointer.
  const reading = dragging ? value : hover;
  const peek = hoveredThumb !== undefined || active !== undefined;

  const onThumb = (event: PointerEvent<HTMLDivElement>) =>
    (event.target as HTMLElement).closest(`.${styles.scThumb}`) !== null;
  const down = (event: PointerEvent<HTMLDivElement>) => {
    if (onThumb(event)) return;
    dragged.current = true;
    setDragging(true);
    event.currentTarget.setPointerCapture?.(event.pointerId);
    onChange(valueAt(event.clientX));
    event.currentTarget.focus({ preventScroll: true });
  };
  const move = (event: PointerEvent<HTMLDivElement>) => {
    if (dragged.current) onChange(valueAt(event.clientX));
    else if (onThumb(event)) setHover(undefined);
    else setHover(valueAt(event.clientX));
  };
  const cancel = () => {
    dragged.current = false;
    setDragging(false);
  };
  const up = () => {
    const picked = dragged.current;
    cancel();
    if (picked) onRelease?.();
  };

  return (
    <div
      ref={rulerRef}
      className={[styles.scRuler, dragging && styles.dragging, peek && styles.peek].filter(Boolean).join(" ")}
      role="slider"
      tabIndex={0}
      aria-label={label}
      aria-valuemin={0}
      aria-valuemax={5}
      aria-valuenow={set ? value / 100 : undefined}
      aria-valuetext={set ? formatScore(value) : "未打分"}
      onPointerDown={down}
      onPointerMove={move}
      onPointerUp={up}
      onPointerCancel={cancel}
      onPointerLeave={() => setHover(undefined)}
    >
      <div className={styles.scBaseline} />
      {tenths.map((tenth) => {
        const whole = tenth % 10 === 0;
        const half = tenth % 5 === 0;
        const near = set ? Math.max(0, 1 - Math.abs(tenth * 10 - value) / loupe) : 0;
        const height = (whole ? 13 : half ? 9 : 5) * (1 + 0.8 * near);
        const opacity = (whole ? 0.5 : half ? 0.32 : 0.18) + 0.5 * near;
        return (
          <div
            key={tenth}
            className={styles.scTick}
            style={{ insetInlineStart: at(tenth * 10), blockSize: height, opacity }}
          />
        );
      })}
      {[0, 1, 2, 3, 4, 5].map((point) => {
        const classes = [
          styles.scNum,
          set && Math.abs(value - point * 100) < 30 && styles.near,
          reading !== undefined && Math.abs(reading - point * 100) < covered && styles.covered,
        ];
        return (
          <div key={point} className={classes.filter(Boolean).join(" ")} style={{ insetInlineStart: at(point * 100) }}>
            {point}
          </div>
        );
      })}
      {marks.map((mark, index) => {
        const top = laneTops[placed[index]];
        const classes = [
          styles.scThumb,
          set && Math.abs(mark.score - value) <= nearby && styles.near,
          mark.token === active && styles.active,
          mark.token === arriving && styles.arriving,
        ]
          .filter(Boolean)
          .join(" ");
        return (
          <Fragment key={mark.token}>
            <div className={styles.scPin} style={{ insetInlineStart: at(mark.score) }} />
            <div
              className={classes}
              data-token={mark.token}
              style={{ insetInlineStart: at(mark.score), top }}
              onPointerEnter={() => {
                setHoveredThumb(mark.token);
                onCompare(mark);
              }}
              onPointerLeave={() => {
                setHoveredThumb((current) => (current === mark.token ? undefined : current));
                onCompare(undefined);
              }}
            >
              <img
                src={claimThumbnailURL(mark.token)}
                alt={`${formatScore(mark.score)} 分的照片`}
                draggable={false}
                style={{ inlineSize: thumbWidth(mark), blockSize: thumbHeight }}
              />
              <div className={styles.scStem} style={{ top: thumbHeight, blockSize: baseline - top - thumbHeight }} />
            </div>
          </Fragment>
        );
      })}
      {last !== undefined && (
        <div
          className={styles.scLastDot}
          style={{ insetInlineStart: at(last) }}
          title={`上一张 ${formatScore(last)}`}
          aria-hidden="true"
        />
      )}
      {set && hover !== undefined && !dragging && (
        <div className={styles.scHoverLine} style={{ insetInlineStart: at(hover) }} aria-hidden="true" />
      )}
      {set && (
        <>
          <div className={styles.scNeedle} style={{ insetInlineStart: at(value) }} />
          <div className={styles.scKnob} style={{ insetInlineStart: at(value) }} />
        </>
      )}
      {!set && hover !== undefined && (
        <div
          className={`${styles.scKnob} ${styles.follow}`}
          style={{ insetInlineStart: at(hover) }}
          aria-hidden="true"
        />
      )}
      {reading !== undefined && (
        <div
          className={`${styles.scReadingChip}${dragging ? ` ${styles.set}` : ""}`}
          style={{ insetInlineStart: at(reading) }}
          aria-hidden="true"
        >
          {formatScore(reading)}
        </div>
      )}
    </div>
  );
}
