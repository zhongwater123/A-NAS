// Where the companion goes so as not to cover the window on top: it stays above the windows (its layer is
// above theirs), so a window under it could not be clicked there. Its hit area, the part a click takes,
// moves out of the window's box to the nearest free place in the part of the page it may use, which
// leaves out the system rail drawn above it (focusedWindow.ts floatingArea).
import { floatingArea, focusedWindowBox, type PageBox } from "../desktop/focusedWindow";
import { type BoxPoint, type Center, derivePetMetrics, type Viewport } from "./dsh-pet/client/pet/motion/geometry";
import { bodyPixelBox } from "./dsh-pet/shared/physics";

export type { PageBox } from "../desktop/focusedWindow";

/** Between the companion's hit area and the window it steps away from. */
const GAP = 12;
/**
 * The bottom right of a window, which the companion never covers when it cannot keep clear of the window:
 * VNAS's send and stop buttons are there, and other windows' main actions too.
 */
const WINDOW_CORNER = { width: 220, height: 130 };

function overlap(a: PageBox, b: PageBox): number {
  const width = Math.min(a.right, b.right) - Math.max(a.left, b.left);
  const height = Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top);
  return width > 0 && height > 0 ? width * height : 0;
}

function clamp(value: number, low: number, high: number): number {
  return Math.min(Math.max(value, low), Math.max(low, high));
}

/**
 * Where the companion's hit area goes (its new top left) so that it does not cover the window, or null
 * when it does not cover it, there is no window, or it is already where it would go. Of the places out to
 * the window's left or right at the same height, or above it, wholly within the area, the one the hit area
 * moves least to (left, right, above on a tie). When none fits (a maximized window), the corner of the area
 * where it covers the least of the window, never the window's bottom right.
 */
export function stepAside(hit: PageBox, window: PageBox | null, area: PageBox): { x: number; y: number } | null {
  if (window === null || overlap(hit, window) === 0) return null;
  const width = hit.right - hit.left;
  const height = hit.bottom - hit.top;
  const at = (x: number, y: number): PageBox => ({ left: x, top: y, right: x + width, bottom: y + height });
  const inside = (x: number, y: number) =>
    x >= area.left && y >= area.top && x + width <= area.right && y + height <= area.bottom;
  const moved = (x: number, y: number) => (Math.abs(x - hit.left) >= 1 || Math.abs(y - hit.top) >= 1 ? { x, y } : null);
  const distance = ([x, y]: [number, number]) => Math.hypot(x - hit.left, y - hit.top);

  const y = clamp(hit.top, area.top, area.bottom - height);
  const x = clamp(hit.left, area.left, area.right - width);
  const beside = (
    [
      [window.left - GAP - width, y],
      [window.right + GAP, y],
      [x, window.top - GAP - height],
    ] as [number, number][]
  ).filter(([left, top]) => inside(left, top));
  // The first of the nearest: sort keeps the order of equal ones.
  const nearest = beside.sort((a, b) => distance(a) - distance(b))[0];
  if (nearest) return moved(...nearest);

  const corner: PageBox = {
    left: window.right - WINDOW_CORNER.width,
    top: window.bottom - WINDOW_CORNER.height,
    right: window.right,
    bottom: window.bottom,
  };
  const low = area.bottom - height;
  const far = area.right - width;
  const corners: [number, number][] = [
    [area.left, low],
    [far, low],
    [area.left, area.top],
    [far, area.top],
  ];
  const best = corners
    .filter(([left, top]) => overlap(at(left, top), corner) === 0)
    .reduce<[number, number] | null>(
      (chosen, place) =>
        chosen === null || overlap(at(...place), window) < overlap(at(...chosen), window) ? place : chosen,
      null,
    );
  return best === null ? null : moved(...best);
}

/** The companion's box (its top left on the page) moved clear of the window on top, or null when it stays. */
export function clearBox(box: BoxPoint, size: number, page: Viewport): BoxPoint | null {
  const hit = bodyPixelBox({ ...box, size, bottomPad: derivePetMetrics(size).bottomPad });
  const to = stepAside(hit, focusedWindowBox(), floatingArea(page));
  return to && { x: box.x + to.x - hit.left, y: box.y + to.y - hit.top };
}

/**
 * A centre as a share of the page moved, if need be, so that the companion's hit area is wholly within the
 * part of the page it may use: a place remembered on a larger page, or at its very edge, comes back on it.
 */
export function withinArea(center: Center, size: number, page: Viewport): Center {
  const { halfW, halfH, bottomPad } = derivePetMetrics(size);
  const box = { x: center.rx * page.width - halfW, y: center.ry * page.height - halfH };
  const hit = bodyPixelBox({ ...box, size, bottomPad });
  const area = floatingArea(page);
  const shift = (low: number, high: number, start: number, end: number) =>
    end - start > high - low ? (low + high - start - end) / 2 : start < low ? low - start : end > high ? high - end : 0;
  const dx = shift(area.left, area.right, hit.left, hit.right);
  const dy = shift(area.top, area.bottom, hit.top, hit.bottom);
  return { rx: (box.x + dx + halfW) / page.width, ry: (box.y + dy + halfH) / page.height };
}

/** The same for a centre as a share of the page, as a position command gives it. */
export function clearCenter(center: Center, size: number, page: Viewport): Center | null {
  const { halfW, halfH } = derivePetMetrics(size);
  const box = clearBox({ x: center.rx * page.width - halfW, y: center.ry * page.height - halfH }, size, page);
  return box && { rx: (box.x + halfW) / page.width, ry: (box.y + halfH) / page.height };
}
