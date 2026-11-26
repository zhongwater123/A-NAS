import { type ReactNode, type RefObject, useLayoutEffect, useRef, useState } from "react";

import { dimensionGuides } from "./handbook";
import { isLevels, overall, type Scale } from "./model";
import styles from "./scoring.module.css";

// The steps of the guide a rater is walked through on a practice photo the
// first time they open a batch, each a bubble beside what it is about.
// pick and confirm wait for the rater to do it; free shows no bubble while
// the practice photo is scored to the end; emotion shows when the rater
// reaches 情绪 on it.
export type CoachStep =
  | "photo"
  | "item"
  | "pick"
  | "feedback"
  | "acts"
  | "guide"
  | "confirm"
  | "free"
  | "emotion"
  | "done";

// The practice photo: the handbook's example of level 3 for the batch's
// first 打分项, with the handbook's answer for it. A batch whose first 打分项
// the handbook does not explain practises on its 总体 example, without an
// answer.
export interface Practice {
  image: string;
  width: number;
  height: number;
  answer?: { dimension: string; level: number; why: string };
}

export const practiceToken = "practice";

export function practiceFor(dimensions: readonly { id: string }[]): Practice {
  const explained = dimensionGuides.find((guide) => guide.id === dimensions[0].id);
  const guide = explained ?? dimensionGuides.find((candidate) => candidate.id === overall) ?? dimensionGuides[0];
  const example = guide.examples.find((candidate) => candidate.level === 3) ?? guide.examples[0];
  return {
    image: example.image,
    width: example.width,
    height: example.height,
    answer: explained && { dimension: explained.id, level: example.level, why: example.why },
  };
}

// What each step points at, by its data-coach mark on the stage.
export const coachTarget: Record<Exclude<CoachStep, "free">, string> = {
  photo: "photo",
  item: "item",
  pick: "scale",
  feedback: "scale",
  acts: "acts",
  guide: "guide",
  confirm: "confirm",
  emotion: "scale",
  done: "photo",
};

// coachSteps are the steps of a batch's guide in order, for the count on
// each bubble; emotion only where 情绪 comes after the first 打分项.
export function coachSteps(
  dimensions: readonly { id: string }[],
  named: boolean,
  practice: Practice,
): Exclude<CoachStep, "free" | "done">[] {
  return [
    "photo",
    ...(named ? (["item"] as const) : []),
    "pick",
    ...(practice.answer ? (["feedback"] as const) : []),
    "acts",
    "guide",
    "confirm",
    ...(dimensions.slice(1).some((dimension) => dimension.id === "emotion") ? (["emotion"] as const) : []),
  ];
}

// following is the step after one the rater moves on from with 下一步.
export function following(step: CoachStep, named: boolean): CoachStep {
  switch (step) {
    case "photo":
      return named ? "item" : "pick";
    case "item":
      return "pick";
    case "feedback":
      return "acts";
    case "acts":
      return "guide";
    case "guide":
      return "confirm";
    default:
      return step;
  }
}

// coachText is what a step's bubble says. picked is the rater's score on the
// practice photo's answered 打分项, in points.
export function coachText(
  step: CoachStep,
  {
    scale,
    first,
    several,
    hidden,
    lowest,
    next,
    practice,
    picked,
  }: {
    scale: Scale;
    first: string;
    several: boolean;
    hidden: boolean;
    lowest: number;
    next: boolean;
    practice: Practice | undefined;
    picked: number | undefined;
  },
): string {
  switch (step) {
    case "photo":
      return first === overall
        ? "这一张是练习，不计分。先看整张 3–5 秒，凭第一感觉打总体。"
        : "这一张是练习，不计分。先看整张，再按顺序逐项打。";
    case "item":
      return several ? "这里是现在要打的项和它的说明。每项只看它自己的那一方面。" : "这里是要打的项和它的说明。";
    case "pick":
      return scale === "five"
        ? "试着打一下：点一档，或按 1–5。只打整数。"
        : scale === "three"
          ? "试着打一下：点一档，或按 1、3、5。"
          : `试着打一下：在数轴上点选或拖动，也可以按 ${lowest}–5。`;
    case "feedback": {
      const answer = practice?.answer;
      if (!answer) return "";
      const handbook = `手册给这张 ${answer.level} 分：${answer.why}`;
      if (hidden || picked === undefined) return handbook;
      const off = Math.abs(picked - answer.level);
      return `你打了 ${picked} 分。${handbook}${off === 0 ? "和你一样。" : `差 ${Number(off.toFixed(2))} 分。`}`;
    }
    case "acts":
      return "看不出来按 S，图片有问题按 X：这张就跳过，不计分。";
    case "guide":
      return "每项看什么、各档的样子、容易打错的地方，都在“打分标准”里，随时点开，或按 ?。";
    case "confirm":
      return `按 Enter 或点“${next ? "下一项" : "确定"}”。${isLevels(scale) ? "拿不准就按 ← → 调一档。" : "← → 可以微调。"}`;
    case "emotion":
      return "情绪不分好坏，只看感觉：1 平静，5 激烈。";
    case "done":
      return "练习结束，这一张不计分。下面是要打分的照片。";
    case "free":
      return "";
  }
}

// Bubble sets a step's words beside its target inside the stage, with a
// ring around the target, and keeps them there as the stage changes size.
// It sits above a target in the lower half of the stage and below one in the
// upper half; on the photo it sits inside its top corner.
export function Bubble({
  root,
  target,
  count,
  children,
  actions,
}: {
  root: RefObject<HTMLDivElement | null>;
  target: string;
  count?: string;
  children: ReactNode;
  actions: ReactNode;
}) {
  const bubble = useRef<HTMLElement>(null);
  const [place, setPlace] = useState<{
    left: number;
    top: number;
    ring?: { left: number; top: number; width: number; height: number };
  }>();
  useLayoutEffect(() => {
    const stage = root.current;
    if (!stage) return;
    const measure = () => {
      const element = stage.querySelector(`[data-coach="${target}"]`);
      const box = stage.getBoundingClientRect();
      const size = bubble.current?.getBoundingClientRect();
      if (!element || !size || box.width === 0) {
        setPlace(undefined);
        return;
      }
      const rect = element.getBoundingClientRect();
      const at = { left: rect.left - box.left, top: rect.top - box.top, width: rect.width, height: rect.height };
      const margin = 12;
      const clamp = (value: number, most: number) => Math.max(margin, Math.min(value, most - margin));
      if (target === "photo") {
        setPlace({
          left: clamp(at.left + 20, box.width - size.width),
          top: clamp(at.top + 20, box.height - size.height),
        });
        return;
      }
      const above = at.top + at.height / 2 > box.height / 2 && at.top - size.height - margin >= margin;
      setPlace({
        left: clamp(at.left + at.width / 2 - size.width / 2, box.width - size.width),
        top: clamp(above ? at.top - size.height - margin : at.top + at.height + margin, box.height - size.height),
        ring: { left: at.left - 4, top: at.top - 4, width: at.width + 8, height: at.height + 8 },
      });
    };
    measure();
    const observer = typeof ResizeObserver === "function" ? new ResizeObserver(measure) : undefined;
    observer?.observe(stage);
    if (bubble.current) observer?.observe(bubble.current);
    window.addEventListener("resize", measure);
    return () => {
      observer?.disconnect();
      window.removeEventListener("resize", measure);
    };
  }, [root, target]);
  return (
    <>
      {place?.ring && <div className={styles.scCoachRing} style={place.ring} aria-hidden="true" />}
      <section
        ref={bubble}
        className={`${styles.scCoach}${place ? ` ${styles.placed}` : ""}`}
        aria-label="新手引导"
        style={place && { left: place.left, top: place.top }}
      >
        <p aria-live="polite">{children}</p>
        <footer>
          {count && <span>{count}</span>}
          {actions}
        </footer>
      </section>
    </>
  );
}
