import { Fragment, type PointerEvent, type RefObject, useRef, useState } from "react";

import { claimThumbnailURL } from "./api";
import { formatScore, lanes, levels, maxScore, type Scale, snapScore } from "./model";
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
// Three levels: the places to click start here, under the lowest lane.
const zoneTop = 124;
const zoneGap = 12;
// The loupe: ticks within this many hundredths of the needle grow.
const loupe = 60;
// Thumbnails within this many hundredths of the needle light up.
const nearby = 25;
// A numeral this close to the pointer's reading gives way to it.
const covered = 32;
// Thumbnails of one score stand this far apart, a little more than the
// lanes keep, so that they share the lowest lane.
const thumbGap = 8;
const tenths = Array.from({ length: 51 }, (_, i) => i);
const wholes = [0, 1, 2, 3, 4, 5];

export const thumbWidth = (mark: Pick<Mark, "width" | "height">) =>
  Math.min(thumbMaxWidth, (thumbHeight * mark.width) / Math.max(1, mark.height));

// share is where a score stands across the ruler, 0 to 1: on the number
// line from 0 to 5, or for three levels in the middle of three equal places.
const share = (hundredths: number, scale: Scale) => hundredths / (scale === "three" ? 600 : maxScore);

// spread is where each mark stands across a ruler width px wide, 0 to 1.
// Where every score shares a few places, the marks of one stand side by
// side: around a whole point's stop, or over a level's place. On the
// number lines with decimals each stands at its score.
function spread(marks: Mark[], scale: Scale, width: number): number[] {
  const at = marks.map((mark) => share(mark.score, scale));
  if ((scale !== "whole" && scale !== "three") || width <= 0) return at;
  const groups = new Map<number, { mark: Mark; index: number }[]>();
  marks.forEach((mark, index) => groups.set(mark.score, [...(groups.get(mark.score) ?? []), { mark, index }]));
  for (const [score, members] of groups) {
    if (scale === "three") {
      const level = levels.findIndex((candidate) => candidate.score === score);
      if (level < 0) continue;
      const room = width / 3 - zoneGap;
      const left = (level * width) / 3 + zoneGap / 2;
      members.forEach(({ index }, n) => {
        at[index] = (left + ((n + 0.5) * room) / members.length) / width;
      });
      continue;
    }
    const widths = members.map(({ mark }) => thumbWidth(mark));
    let x = share(score, scale) * width - (widths.reduce((sum, w) => sum + w, 0) + thumbGap * (members.length - 1)) / 2;
    members.forEach(({ index }, n) => {
      const half = widths[n] / 2;
      at[index] = Math.min(width - half, Math.max(half, x + half)) / width;
      x += widths[n] + thumbGap;
    });
  }
  return at;
}

// Ruler is the score of the batch's scale, dragged or clicked with the
// pointer and moved with the keys the stage reads. On the number lines with
// decimals it is a fine scale whose ticks near the needle grow as under a
// loupe; while nothing is set, the dot follows the pointer over the scale,
// and once set a faint line shows where a click would land. The pointer's
// reading takes the numerals' row below the baseline, where no thumbnail
// covers it. With the decimals hidden the scale keeps only whole and half
// points and shows no reading. Whole points are six stops; three levels are
// three places to click. A pale dot marks the last photo's score. Above the
// scale the rater's own latest scores stand as whole thumbnails at their
// scores, in up to three lanes; the one under the pointer or set beside the
// photo comes to the front and the others step back. Releasing the pointer
// after a pick tells the stage, which moves on.
export function Ruler({
  value,
  label,
  marks,
  last,
  active,
  arriving,
  scale,
  hidden,
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
  scale: Scale;
  // The rater sees no scores: only the place on the number line.
  hidden: boolean;
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
  const line = scale !== "three";
  const fine = scale === "hundredths" || scale === "tenths";
  const at = (hundredths: number) => `${share(hundredths, scale) * 100}%`;
  const valueAt = (clientX: number) => {
    const box = rulerRef.current?.getBoundingClientRect();
    if (!box || box.width === 0) return value ?? hover ?? (line ? 0 : levels[1].score);
    const across = Math.min(1, Math.max(0, (clientX - box.left) / box.width));
    if (!line) return levels[Math.min(levels.length - 1, Math.floor(across * levels.length))].score;
    return snapScore(across * maxScore, scale);
  };
  const places = spread(marks, scale, width);
  const placed = lanes(
    marks.map((mark, index) => ({ at: places[index], width: thumbWidth(mark) })),
    width,
  );
  const set = value !== undefined;
  // The reading below the scale: the value while dragging, else the pointer;
  // only on a line with decimals the rater sees.
  const reading = fine && !hidden ? (dragging ? value : hover) : undefined;
  const peek = hoveredThumb !== undefined || active !== undefined;
  const said = (hundredths: number) => (hidden ? "已选" : formatScore(hundredths, scale));
  const markAlt = (mark: Mark) =>
    hidden
      ? "你打过的照片"
      : line
        ? `${formatScore(mark.score, scale)} 分的照片`
        : `打了“${formatScore(mark.score, scale)}”的照片`;

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
      className={[styles.scRuler, !line && styles.levels, dragging && styles.dragging, peek && styles.peek]
        .filter(Boolean)
        .join(" ")}
      role="slider"
      tabIndex={0}
      aria-label={label}
      aria-valuemin={line ? 0 : 1}
      aria-valuemax={5}
      aria-valuenow={set ? value / 100 : undefined}
      aria-valuetext={set ? said(value) : "未打分"}
      onPointerDown={down}
      onPointerMove={move}
      onPointerUp={up}
      onPointerCancel={cancel}
      onPointerLeave={() => setHover(undefined)}
    >
      {line && <div className={styles.scBaseline} />}
      {fine &&
        tenths
          .filter((tenth) => !hidden || tenth % 5 === 0)
          .map((tenth) => {
            const whole = tenth % 10 === 0;
            const half = tenth % 5 === 0;
            const near = set && !hidden ? Math.max(0, 1 - Math.abs(tenth * 10 - value) / loupe) : 0;
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
      {scale === "whole" &&
        wholes.map((point) => (
          <div
            key={point}
            className={`${styles.scStop}${!dragging && hover === point * 100 ? ` ${styles.hover}` : ""}`}
            style={{ insetInlineStart: at(point * 100) }}
          />
        ))}
      {line &&
        wholes.map((point) => {
          const classes = [
            styles.scNum,
            set && Math.abs(value - point * 100) < 30 && styles.near,
            reading !== undefined && Math.abs(reading - point * 100) < covered && styles.covered,
          ];
          return (
            <div
              key={point}
              className={classes.filter(Boolean).join(" ")}
              style={{ insetInlineStart: at(point * 100) }}
            >
              {point}
            </div>
          );
        })}
      {!line &&
        levels.map((level, index) => {
          const on = set && value === level.score;
          return (
            <div
              key={level.score}
              className={[styles.scZone, on && styles.on, !on && hover === level.score && styles.hover]
                .filter(Boolean)
                .join(" ")}
              style={{
                insetInlineStart: `calc(${(index * 100) / levels.length}% + ${zoneGap / 2}px)`,
                inlineSize: `calc(${100 / levels.length}% - ${zoneGap}px)`,
              }}
            >
              <b>{level.word}</b>
              <small>{level.score / 100}</small>
              <kbd aria-hidden="true">{level.score / 100}</kbd>
            </div>
          );
        })}
      {marks.map((mark, index) => {
        const top = laneTops[placed[index]];
        const near = set && (fine ? Math.abs(mark.score - value) <= nearby : mark.score === value);
        const classes = [
          styles.scThumb,
          near && styles.near,
          mark.token === active && styles.active,
          mark.token === arriving && styles.arriving,
        ]
          .filter(Boolean)
          .join(" ");
        return (
          <Fragment key={mark.token}>
            {line && <div className={styles.scPin} style={{ insetInlineStart: at(mark.score) }} />}
            <div
              className={classes}
              data-token={mark.token}
              style={{ insetInlineStart: `${places[index] * 100}%`, top }}
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
                alt={markAlt(mark)}
                draggable={false}
                style={{ inlineSize: thumbWidth(mark), blockSize: thumbHeight }}
              />
              <div
                className={styles.scStem}
                style={{ top: thumbHeight, blockSize: (line ? baseline : zoneTop) - top - thumbHeight }}
              />
            </div>
          </Fragment>
        );
      })}
      {last !== undefined && (
        <div
          className={styles.scLastDot}
          style={{ insetInlineStart: at(last) }}
          title={hidden ? "上一张" : `上一张 ${formatScore(last, scale)}`}
          aria-hidden="true"
        />
      )}
      {fine && set && hover !== undefined && !dragging && (
        <div className={styles.scHoverLine} style={{ insetInlineStart: at(hover) }} aria-hidden="true" />
      )}
      {line && set && (
        <>
          <div className={styles.scNeedle} style={{ insetInlineStart: at(value) }} />
          <div className={styles.scKnob} style={{ insetInlineStart: at(value) }} />
        </>
      )}
      {line && !set && hover !== undefined && (
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
          {formatScore(reading, scale)}
        </div>
      )}
    </div>
  );
}
