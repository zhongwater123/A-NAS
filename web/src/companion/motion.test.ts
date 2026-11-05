import { describe, expect, it } from "vitest";

import { speechSide, speechSpot } from "./motion";

describe("the desktop pet's speech bubble stays on the part of the window on screen", () => {
  const pet = { x: 112, y: 84 };
  const bubble = { width: 240, height: 22 };
  it("speaks from above while the room above the pet is on screen", () => {
    expect(speechSpot(pet, 128, { left: 0, top: 0, right: 352, bottom: 220 }, bubble)).toEqual({ x: 56, y: 54 });
  });
  it("keeps the bubble inside the visible width when the window runs off the left edge", () => {
    expect(speechSpot(pet, 128, { left: 111, top: 0, right: 352, bottom: 220 }, bubble)?.x).toBe(117);
  });
  it("finds no spot above or below at the top of the screen, and picks the roomier side", () => {
    const top = { left: 0, top: 84, right: 352, bottom: 220 };
    expect(speechSpot(pet, 128, top, bubble)).toBeUndefined();
    expect(speechSide(pet, 128, top)).toEqual({ side: "right", width: 98 });
    expect(speechSide(pet, 128, { ...top, right: 240 })).toEqual({ side: "left", width: 98 });
  });
});
