import { describe, expect, it } from "vitest";

import { apps } from "../desktop/apps";
import { floatingArea } from "../desktop/focusedWindow";
import { type PageBox, stepAside, withinArea } from "./stepAside";

/** The companion's hit area in its default place on a 1366×820 page: 90×107 at the bottom right. */
const hit: PageBox = { left: 1177, top: 627, right: 1267, bottom: 734 };
const page = { width: 1366, height: 820 };
const area = floatingArea(page);
const box = ({ x, y, width, height }: { x: number; y: number; width: number; height: number }): PageBox => ({
  left: x,
  top: y,
  right: x + width,
  bottom: y + height,
});
const moved = (to: { x: number; y: number } | null, from = hit): PageBox | null =>
  to && { left: to.x, top: to.y, right: to.x + from.right - from.left, bottom: to.y + from.bottom - from.top };
const covers = (a: PageBox | null, b: PageBox) =>
  a !== null &&
  Math.min(a.right, b.right) > Math.max(a.left, b.left) &&
  Math.min(a.bottom, b.bottom) > Math.max(a.top, b.top);
/** The bottom right of a window, where VNAS's send and stop buttons are. */
const sendCorner = (window: PageBox): PageBox => ({ ...window, left: window.right - 220, top: window.bottom - 130 });
/** The system rail on a wide page (desktop.module.css). */
const rail: PageBox = { left: 16, top: 18, right: 76, bottom: page.height - 18 };

describe("where the companion steps aside to", () => {
  it("stays where it is without a window, or beside one", () => {
    expect(stepAside(hit, null, area)).toBeNull();
    expect(stepAside(hit, box({ x: 214, y: 56, width: 900, height: 620 }), area)).toBeNull();
  });

  it("goes out to the left of VNAS's window as it opens, clear of its send and stop buttons", () => {
    const window = box(apps.assistant.bounds);
    const to = stepAside(hit, window, area);
    expect(to).toEqual({ x: window.left - 12 - 90, y: 627 });
    expect(covers(moved(to), window)).toBe(false);
    expect(covers(moved(to), sendCorner(window))).toBe(false);
  });

  it("goes to the nearest side: the right, when the window covers only its edge", () => {
    // 回收站 as it opens reaches 3 px into the companion's corner.
    const window = box(apps.trash.bounds);
    expect(window.right).toBe(1180);
    expect(stepAside(hit, window, area)).toEqual({ x: 1180 + 12, y: 627 });
  });

  it("goes above the window when neither side has room", () => {
    const window = box({ x: 100, y: 400, width: 1210, height: 400 });
    const to = stepAside({ left: 700, top: 600, right: 790, bottom: 707 }, window, area);
    expect(to).toEqual({ x: 700, y: 400 - 12 - 107 });
    expect(covers(moved(to), window)).toBe(false);
  });

  it("keeps within the page, and out from under the system rail", () => {
    // Hanging off the bottom edge, it comes up onto the page as it steps aside.
    expect(
      stepAside(
        { left: 1177, top: 760, right: 1267, bottom: 867 },
        box({ x: 260, y: 46, width: 1060, height: 790 }),
        area,
      ),
    ).toEqual({ x: 260 - 12 - 90, y: 820 - 8 - 107 });
    // A window dragged near the rail leaves no room on its left: the right then.
    const window = box({ x: 120, y: 100, width: 1100, height: 600 });
    const to = stepAside(hit, window, area);
    expect(to).toEqual({ x: 1220 + 12, y: 627 });
    expect(covers(moved(to), rail)).toBe(false);
  });

  it("goes to the corner where it covers least of a maximized window: not under the rail, nor its bottom right", () => {
    // A maximized window: 88 from the left, 12 from the top and right, 90 from the bottom.
    const window = { left: 88, top: 12, right: 1354, bottom: 730 };
    const to = stepAside(hit, window, area);
    expect(to).toEqual({ x: 88, y: 820 - 8 - 107 });
    expect(covers(moved(to), sendCorner(window))).toBe(false);
    expect(covers(moved(to), rail)).toBe(false);
    // Already there, it stays.
    expect(stepAside(moved(to)!, window, area)).toBeNull();
  });

  it("goes to the nearer of two corners that cover the window as little", () => {
    // Neither side nor above has room, and both bottom corners lie below the window: the right one is nearer.
    const window = box({ x: 177, y: 100, width: 1100, height: 600 });
    expect(stepAside(hit, window, area)).toEqual({ x: 1366 - 8 - 90, y: 820 - 8 - 107 });
  });

  it("finds the gap beside a window on a narrow page, above the rail along its bottom", () => {
    // On a 400×800 page a window spans 8 to 104 short of the right edge (desktop.module.css).
    const narrow = floatingArea({ width: 400, height: 800 });
    expect(narrow.bottom).toBe(800 - 75);
    const window = { left: 8, top: 8, right: 296, bottom: 725 };
    const to = stepAside({ left: 200, top: 600, right: 290, bottom: 707 }, window, narrow);
    expect(to).toEqual({ x: 400 - 8 - 90, y: 725 - 107 });
    expect(covers(moved(to, { left: 200, top: 600, right: 290, bottom: 707 }), window)).toBe(false);
  });
});

describe("a remembered place, brought back on the page", () => {
  it("keeps it where it was when it is on the page, and brings in what hangs off it or lies under the rail", () => {
    expect(withinArea({ rx: 0.5, ry: 0.5 }, 240, page)).toEqual({ rx: 0.5, ry: 0.5 });
    // From the bottom right corner: the hit area's right and bottom 8 px inside the page.
    const corner = withinArea({ rx: 1, ry: 1 }, 240, page);
    expect(corner.rx * page.width - 120 + 165).toBeCloseTo(page.width - 8, 6);
    // From the top left, under the rail: out to its right.
    const rail = withinArea({ rx: 0, ry: 0 }, 240, page);
    expect(rail.rx * page.width - 120 + 75).toBeCloseTo(88, 6);
  });
});
