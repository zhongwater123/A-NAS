// Where the companion goes so as not to cover the window on top: it stays above the windows (its layer is
// above theirs), so a window under it could not be clicked there. Its hit area, the part a click takes,
// moves out of the window's box to the nearest free place on the page.

/** A box on the page, in CSS pixels. */
export interface Box {
  left: number;
  top: number;
  right: number;
  bottom: number;
}

export interface Viewport {
  width: number;
  height: number;
}

/** Between the companion's hit area and the window it steps away from. */
const GAP = 12;
/** Between the companion's hit area and the page's edges. */
const EDGE = 8;
/**
 * The bottom right of a window, which the companion never covers when it cannot keep clear of the window:
 * VNAS's send and stop buttons are there, and other windows' main actions too.
 */
const WINDOW_CORNER = { width: 220, height: 130 };

function overlap(a: Box, b: Box): number {
  const width = Math.min(a.right, b.right) - Math.max(a.left, b.left);
  const height = Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top);
  return width > 0 && height > 0 ? width * height : 0;
}

function clamp(value: number, low: number, high: number): number {
  return Math.min(Math.max(value, low), Math.max(low, high));
}

/**
 * Where the companion's hit area goes (its new top left) so that it does not cover the window, or null
 * when it does not cover it, there is no window, or it is already where it would go. In order: out to the
 * window's left at the same height, then to its right, then above it, wholly on the page. When none fits
 * (a maximized window), the corner of the page where it covers the least of the window, never the
 * window's bottom right.
 */
export function stepAside(hit: Box, window: Box | null, viewport: Viewport): { x: number; y: number } | null {
  if (window === null || overlap(hit, window) === 0) return null;
  const width = hit.right - hit.left;
  const height = hit.bottom - hit.top;
  const at = (x: number, y: number): Box => ({ left: x, top: y, right: x + width, bottom: y + height });
  const onPage = (x: number, y: number) =>
    x >= EDGE && y >= EDGE && x + width <= viewport.width - EDGE && y + height <= viewport.height - EDGE;
  const moved = (x: number, y: number) => (Math.abs(x - hit.left) >= 1 || Math.abs(y - hit.top) >= 1 ? { x, y } : null);

  const y = clamp(hit.top, EDGE, viewport.height - EDGE - height);
  const x = clamp(hit.left, EDGE, viewport.width - EDGE - width);
  const beside: [number, number][] = [
    [window.left - GAP - width, y],
    [window.right + GAP, y],
    [x, window.top - GAP - height],
  ];
  for (const [left, top] of beside) if (onPage(left, top)) return moved(left, top);

  const corner: Box = {
    left: window.right - WINDOW_CORNER.width,
    top: window.bottom - WINDOW_CORNER.height,
    right: window.right,
    bottom: window.bottom,
  };
  const low = viewport.height - EDGE - height;
  const far = viewport.width - EDGE - width;
  const corners: [number, number][] = [
    [EDGE, low],
    [far, low],
    [EDGE, EDGE],
    [far, EDGE],
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
