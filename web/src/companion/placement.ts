// Where on the page the companion may be left: wholly within the part of the page not under the system
// rail, which is drawn above it. It stays above the windows and does not move out of their way
// (petCard.tsx); a place remembered on a larger page, or hanging off its edge, comes back on the page.
import { MAXIMIZED_INSET } from "../desktop/windows";
import { type Center, derivePetMetrics, type Viewport } from "./dsh-pet/client/pet/motion/geometry";
import { bodyPixelBox } from "./dsh-pet/shared/physics";

/** A box on the page, in CSS pixels. */
export interface PageBox {
  left: number;
  top: number;
  right: number;
  bottom: number;
}

/** On a page this narrow or narrower the system rail lies along the bottom (desktop.module.css). */
const NARROW_PAGE = 620;
/** What the rail takes at the bottom of a narrow page, as windows leave it there. */
const NARROW_RAIL = 75;
/** Between what floats over the windows and the page's edges. */
const EDGE = 8;

/**
 * Where on a page this large what floats over the windows may sit: not under the system rail, which is
 * drawn above it, on the left of a wide page (where a maximized window starts) or along the bottom of a
 * narrow one.
 */
export function floatingArea(page: { width: number; height: number }): PageBox {
  if (page.width <= NARROW_PAGE)
    return { left: EDGE, top: EDGE, right: page.width - EDGE, bottom: page.height - NARROW_RAIL };
  return { left: MAXIMIZED_INSET.left, top: EDGE, right: page.width - EDGE, bottom: page.height - EDGE };
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
