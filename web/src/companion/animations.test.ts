import { describe, expect, it } from "vitest";

import { installedAnimations } from "./animations";
import { decideEndedCue } from "./dsh-pet/client/pet/animation/cues";
import { isEventAnim } from "./dsh-pet/shared/pickers";

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
