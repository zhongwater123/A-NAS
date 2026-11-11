import { describe, expect, it } from "vitest";

import { type Box, stepAside } from "./stepAside";

/** The companion's hit area in its default place on a 1366×820 page: 90×107 at the bottom right. */
const hit: Box = { left: 1177, top: 627, right: 1267, bottom: 734 };
const page = { width: 1366, height: 820 };
const box = (left: number, top: number, width: number, height: number): Box => ({
  left,
  top,
  right: left + width,
  bottom: top + height,
});
const moved = (to: { x: number; y: number } | null) =>
  to && { left: to.x, top: to.y, right: to.x + 90, bottom: to.y + 107 };
const covers = (a: Box | null, b: Box) =>
  a !== null &&
  Math.min(a.right, b.right) > Math.max(a.left, b.left) &&
  Math.min(a.bottom, b.bottom) > Math.max(a.top, b.top);

describe("where the companion steps aside to", () => {
  it("stays where it is without a window, or beside one", () => {
    expect(stepAside(hit, null, page)).toBeNull();
    expect(stepAside(hit, box(214, 56, 900, 620), page)).toBeNull();
  });

  it("goes out to the window's left at the same height", () => {
    // VNAS's window as it opens on this page.
    const window = box(210, 46, 1120, 720);
    const to = stepAside(hit, window, page);
    expect(to).toEqual({ x: 210 - 12 - 90, y: 627 });
    expect(covers(moved(to), window)).toBe(false);
  });

  it("goes to the window's right when its left has no room", () => {
    const window = box(60, 300, 900, 500);
    const to = stepAside({ left: 400, top: 600, right: 490, bottom: 707 }, window, page);
    expect(to).toEqual({ x: 960 + 12, y: 600 });
  });

  it("goes above the window when neither side has room", () => {
    const window = box(60, 400, 1250, 400);
    const to = stepAside({ left: 700, top: 600, right: 790, bottom: 707 }, window, page);
    expect(to).toEqual({ x: 700, y: 400 - 12 - 107 });
    expect(covers(moved(to), window)).toBe(false);
  });

  it("keeps on the page", () => {
    // Hanging off the bottom edge, it comes up onto the page as it steps aside.
    const to = stepAside({ left: 1177, top: 760, right: 1267, bottom: 867 }, box(210, 46, 1120, 790), page);
    expect(to).toEqual({ x: 108, y: 820 - 8 - 107 });
  });

  it("goes to the corner where it covers least of a maximized window, never its bottom right", () => {
    // A maximized window: 88 from the left, 12 from the top and right, 90 from the bottom.
    const window = { left: 88, top: 12, right: 1354, bottom: 730 };
    const to = stepAside(hit, window, page);
    expect(to).toEqual({ x: 8, y: 820 - 8 - 107 });
    const corner = { left: 1354 - 220, top: 730 - 130, right: 1354, bottom: 730 };
    expect(covers(moved(to), corner)).toBe(false);
    // Already there, it stays.
    expect(stepAside(moved(to)!, window, page)).toBeNull();
  });
});
