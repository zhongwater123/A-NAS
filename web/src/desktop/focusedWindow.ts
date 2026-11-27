import { phoneLayout } from "../ui/usePhoneLayout";
import { WINDOW_ID } from "./windowSnapshot";
import { MAXIMIZED_INSET, type WindowModel } from "./windows";

// The window on top of the desktop, read by what floats above the windows (the companion) so as to keep
// clear of it. The desktop publishes it when it changes: a window opened, brought to the front, moved,
// resized, maximized, minimized or closed. A move or a resize changes it once, as the pointer lets go.
// Where it lies is measured from the page, as its style and the page's size lay it out.

/** A box on the page, in CSS pixels. */
export interface PageBox {
  left: number;
  top: number;
  right: number;
  bottom: number;
}

type Frame = Pick<WindowModel, "id" | "x" | "y" | "width" | "height" | "maximized">;

let focused: Frame | null = null;
const listeners = new Set<() => void>();

const same = (a: Frame | null, b: Frame | null) =>
  a === b ||
  (a !== null &&
    b !== null &&
    a.id === b.id &&
    a.x === b.x &&
    a.y === b.y &&
    a.width === b.width &&
    a.height === b.height &&
    a.maximized === b.maximized);

export function publishFocusedWindow(frame: Frame | null): void {
  if (same(focused, frame)) return;
  focused = frame && {
    id: frame.id,
    x: frame.x,
    y: frame.y,
    width: frame.width,
    height: frame.height,
    maximized: frame.maximized,
  };
  for (const listener of listeners) listener();
}

/** The box of the window on top as it is laid out now, or null when no window shows. */
export function focusedWindowBox(): PageBox | null {
  if (focused === null) return null;
  const element = document.querySelector<HTMLElement>(`[${WINDOW_ID}="${focused.id}"]`);
  if (!element || element.hidden) return null;
  // Its layout box, without the transform of its opening animation.
  const layer = element.offsetParent?.getBoundingClientRect();
  const left = (layer?.left ?? 0) + element.offsetLeft;
  const top = (layer?.top ?? 0) + element.offsetTop;
  return { left, top, right: left + element.offsetWidth, bottom: top + element.offsetHeight };
}

/** The window on top as the desktop last published it, or null. */
export function focusedWindow(): Readonly<Frame> | null {
  return focused;
}

export function subscribeFocusedWindow(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

/** What the rail takes at the bottom of a page in the phone layout, as windows leave it there. */
const NARROW_RAIL = 75;
/** Between what floats over the windows and the page's edges. */
const EDGE = 8;

/**
 * Where on a page this large what floats over the windows may sit: not under the system rail, which is
 * drawn above it, on the left of a wide page (where a maximized window starts) or along the bottom in
 * the phone layout.
 */
export function floatingArea(page: { width: number; height: number }, phone = phoneLayout()): PageBox {
  if (phone) return { left: EDGE, top: EDGE, right: page.width - EDGE, bottom: page.height - NARROW_RAIL };
  return { left: MAXIMIZED_INSET.left, top: EDGE, right: page.width - EDGE, bottom: page.height - EDGE };
}
