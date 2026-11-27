import { describe, expect, it } from "vitest";

import { swipeEnd, swipeStart, swipeStep } from "./swipe";

const at = (clientX: number, clientY = 300) => ({ clientX, clientY });

describe("a sideways swipe", () => {
  it("steps once it has travelled far enough or was flicked", () => {
    expect(swipeStep(-81, 0)).toBe(1);
    expect(swipeStep(81, 0)).toBe(-1);
    expect(swipeStep(-20, -600)).toBe(1);
    expect(swipeStep(20, 600)).toBe(-1);
  });

  it("stays for a short, slow move", () => {
    expect(swipeStep(-80, -500)).toBe(0);
    expect(swipeStep(30, 100)).toBe(0);
  });
});

describe("a swipe across the photos viewer", () => {
  it("goes to the next photo leftwards and the previous rightwards", () => {
    const start = swipeStart([at(300)], 0, false);
    expect(swipeEnd(start, at(150), 400)).toBe(1);
    expect(swipeEnd(start, at(450), 400)).toBe(-1);
    // A quick flick counts though it is short.
    expect(swipeEnd(start, at(270), 40)).toBe(1);
  });

  it("does not step for a move more up or down than sideways", () => {
    const start = swipeStart([at(300, 300)], 0, false);
    expect(swipeEnd(start, at(150, 420), 400)).toBe(0);
    expect(swipeEnd(start, at(150, 380), 400)).toBe(1);
  });

  it("does not start with two fingers, on a zoomed photo or on a control", () => {
    expect(swipeStart([at(300), at(200)], 0, false)).toBeUndefined();
    expect(swipeStart([at(300)], 0, true)).toBeUndefined();
    expect(swipeEnd(undefined, at(100), 400)).toBe(0);
  });
});
