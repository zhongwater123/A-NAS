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
const tenths = Array.from({ length: 51 }, (_, i) => i);

export const thumbWidth = (mark: Pick<Mark, "width" | "height">) =>
  Math.min(thumbMaxWidth, (thumbHeight * mark.width) / Math.max(1, mark.height));

// Ruler is the score from 0 to 5 in hundredths, drawn as a fine scale: dragged
// or clicked with the pointer, moved with the keys the stage reads. Ticks near
// the needle grow as under a loupe. Above the scale the rater's own latest
// scores stand as whole thumbnails at their scores, in up to three lanes;
// those near the needle light up, and hovering one asks the stage to set it
// beside the photo being scored.
export function Ruler({
  value,
  marks,
  latest,
  active,
  arriving,
  onChange,
  onCompare,
  rulerRef,
}: {
  value: number;
  marks: Mark[];
  latest?: string;
  active?: string;
  arriving?: string;
  onChange: (hundredths: number) => void;
  onCompare: (mark: Mark | undefined) => void;
  rulerRef: RefObject<HTMLDivElement | null>;
}) {
  const { width } = useSize(rulerRef);
  const [dragging, setDragging] = useState(false);
  const [hover, setHover] = useState<number>();
  const dragged = useRef(false);
  const at = (hundredths: number) => `${(hundredths / maxScore) * 100}%`;
  const valueAt = (clientX: number) => {
    const box = rulerRef.current?.getBoundingClientRect();
    if (!box || box.width === 0) return value;
    return clampScore(((clientX - box.left) / box.width) * maxScore);
  };
  const placed = lanes(
    marks.map((mark) => ({ at: mark.score / maxScore, width: thumbWidth(mark) })),
    width,
  );

  const down = (event: PointerEvent<HTMLDivElement>) => {
    if ((event.target as HTMLElement).closest(`.${styles.scThumb}`)) return;
    dragged.current = true;
    setDragging(true);
    event.currentTarget.setPointerCapture?.(event.pointerId);
    onChange(valueAt(event.clientX));
    event.currentTarget.focus({ preventScroll: true });
  };
  const move = (event: PointerEvent<HTMLDivElement>) => {
    if (dragged.current) onChange(valueAt(event.clientX));
    else if ((event.target as HTMLElement).closest(`.${styles.scThumb}`)) setHover(undefined);
    else setHover(valueAt(event.clientX));
  };
  const up = () => {
    dragged.current = false;
    setDragging(false);
  };

  return (
    <div
      ref={rulerRef}
      className={`${styles.scRuler}${dragging ? ` ${styles.dragging}` : ""}`}
      role="slider"
      tabIndex={0}
      aria-label="分数"
      aria-valuemin={0}
      aria-valuemax={5}
      aria-valuenow={value / 100}
      aria-valuetext={formatScore(value)}
      onPointerDown={down}
      onPointerMove={move}
      onPointerUp={up}
      onPointerCancel={up}
      onPointerLeave={() => setHover(undefined)}
    >
      <div className={styles.scBaseline} />
      {tenths.map((tenth) => {
        const whole = tenth % 10 === 0;
        const half = tenth % 5 === 0;
        const near = Math.max(0, 1 - Math.abs(tenth * 10 - value) / loupe);
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
      {[0, 1, 2, 3, 4, 5].map((point) => (
        <div
          key={point}
          className={`${styles.scNum}${Math.abs(value - point * 100) < 30 ? ` ${styles.near}` : ""}`}
          style={{ insetInlineStart: at(point * 100) }}
        >
          {point}
        </div>
      ))}
      {marks.map((mark, index) => {
        const top = laneTops[placed[index]];
        const classes = [
          styles.scThumb,
          Math.abs(mark.score - value) <= nearby && styles.near,
          mark.token === latest && styles.latest,
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
              onPointerEnter={() => onCompare(mark)}
              onPointerLeave={() => onCompare(undefined)}
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
      <div className={styles.scNeedle} style={{ insetInlineStart: at(value) }} />
      <div className={styles.scKnob} style={{ insetInlineStart: at(value) }} />
      <div className={styles.scTag} style={{ insetInlineStart: at(value) }} aria-hidden="true">
        {formatScore(value)}
      </div>
      {hover !== undefined && !dragging && (
        <div className={styles.scGhost} style={{ insetInlineStart: at(hover) }} aria-hidden="true">
          <span>{formatScore(hover)}</span>
        </div>
      )}
    </div>
  );
}
