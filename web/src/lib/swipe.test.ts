import { describe, expect, it } from "vitest";

import { swipeStep } from "./swipe";

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
