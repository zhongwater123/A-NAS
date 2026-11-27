import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { recordPointer } from "../lib/touch";
import { rowGestures } from "./rowGestures";

let row: HTMLElement;
let other: HTMLElement;
const click = (currentTarget: EventTarget = row, keys = {}) => ({
  shiftKey: false,
  ctrlKey: false,
  metaKey: false,
  currentTarget,
  ...keys,
});

describe("row gestures", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    row = document.createElement("div");
    other = document.createElement("div");
  });
  afterEach(() => {
    recordPointer("");
    document.documentElement.removeAttribute("data-touch");
  });

  it("selects on a click and opens on a double click with a mouse", () => {
    const open = vi.fn();
    const select = vi.fn();
    const gestures = rowGestures({ open, select, draggable: true });
    recordPointer("mouse");
    gestures.onClick(click());
    expect(select).toHaveBeenCalledTimes(1);
    expect(open).not.toHaveBeenCalled();
    gestures.onDoubleClick();
    expect(open).toHaveBeenCalledTimes(1);
    expect(gestures.draggable).toBe(true);
  });

  it("opens on a tap, selects a range with Shift and does not drag on a touch screen", () => {
    document.documentElement.setAttribute("data-touch", "");
    const open = vi.fn();
    const select = vi.fn();
    const gestures = rowGestures({ open, select, draggable: true });
    recordPointer("touch");
    gestures.onClick(click());
    expect(open).toHaveBeenCalledTimes(1);
    gestures.onClick(click(row, { shiftKey: true }));
    expect(select).toHaveBeenCalledTimes(1);
    expect(gestures.draggable).toBe(false);
  });

  it("opens once for a double tap, but again for a later tap or another row", () => {
    const open = vi.fn();
    const gestures = rowGestures({ open, select: vi.fn() });
    recordPointer("touch");
    // A double tap: tap, tap, dblclick.
    gestures.onClick(click());
    gestures.onClick(click());
    gestures.onDoubleClick();
    expect(open).toHaveBeenCalledTimes(1);
    // Another row tapped straight after opens.
    gestures.onClick(click(other));
    expect(open).toHaveBeenCalledTimes(2);
    // The same row, tapped again later, opens again.
    vi.advanceTimersByTime(600);
    gestures.onClick(click(other));
    expect(open).toHaveBeenCalledTimes(3);
  });
});
