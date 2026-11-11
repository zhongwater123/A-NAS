import { describe, expect, it } from "vitest";

import { installedAnimations } from "./animations";
import { TASK_CLIPS } from "./behaviors";
import config from "./dsh-pet/animations.json";
import { decideEndedCue } from "./dsh-pet/client/pet/animation/cues";
import { isEventAnim } from "./dsh-pet/shared/pickers";
import type { Animations } from "./dsh-pet/shared/types";

describe("the animations a partial install plays", () => {
  const installed = (names: string[]) => installedAnimations((name) => names.includes(name));

  it("needs at least an idle animation", () => {
    expect(installed(["东张西望"])).toBeUndefined();
  });

  it("leaves a stage without its animation empty, so idle stays idle and the random chain goes on", () => {
    const animations = installed(["待机呼吸休闲", "东张西望", "工作状态-忙碌点按"]);
    if (!animations) throw new Error("idle is installed");
    const stages = animations.events.workStatus ?? [];
    expect(stages).toHaveLength(6);
    expect(stages[1]).toBe("工作状态-忙碌点按");
    expect(stages.filter((slot) => slot.length === 0)).toHaveLength(5);
    expect(isEventAnim(animations.events, "待机呼吸休闲")).toBe(false);
    expect(decideEndedCue({ animations, current: "待机呼吸休闲", workState: null }).kind).toBe("random");
  });
});

describe("the animations the companion plays on its own and when it is clicked", () => {
  const everything = installedAnimations(() => true);
  if (!everything) throw new Error("idle is installed");
  const categoryActions = everything.categories.flatMap((category) => category.actions);

  it("keeps the steps' clips for the steps", () => {
    for (const clip of TASK_CLIPS) {
      expect(categoryActions).not.toContain(clip);
      expect(everything.turn).not.toContain(clip);
      expect(everything.clicks).not.toContain(clip);
      // Still the pet's, as an event the stages play.
      expect(isEventAnim(everything.events, clip)).toBe(true);
    }
    // Not even an idle clip as the turn: the card would turn the pet at the end of each one.
    expect(everything.turn).toEqual([]);
  });

  it("plays only a click response when clicked with nothing to ask about, the category actions left to chance", () => {
    const all = config.animations as unknown as Animations;
    expect(everything.clicks).toEqual(all.clicks);
    expect(everything.clicks.every((name) => name.startsWith("点击回应-"))).toBe(true);
    expect(everything.categories.flatMap((category) => category.actions)).toContain("三球抛接");
    // A step's own clip plays on while its stage lasts, and ends as an event once the stage is over.
    expect(
      decideEndedCue({ animations: everything, current: "写代码", workState: "working", workAnim: "写代码" }).kind,
    ).toBe("work-replay");
    expect(decideEndedCue({ animations: everything, current: "写代码", workState: null, workAnim: null }).kind).toBe(
      "event-done",
    );
    // A category action is the random chain's alone now, and ends as one of it; a click response as a click.
    expect(decideEndedCue({ animations: everything, current: "三球抛接", workState: null }).kind).toBe("random");
    expect(decideEndedCue({ animations: everything, current: "点击回应-元气挥手", workState: null }).kind).toBe(
      "interaction-done",
    );
  });
});
