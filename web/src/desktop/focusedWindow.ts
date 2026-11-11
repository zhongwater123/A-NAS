import { type WindowBox, type WindowFrame, windowBox } from "./windows";

// The window on top of the desktop, read by what floats above the windows (the companion) so as to keep
// clear of it. The desktop publishes it when it changes: a window opened, brought to the front, moved,
// resized, maximized, minimized or closed. A move or a resize changes it once, as the pointer lets go.
let focused: WindowFrame | null = null;
const listeners = new Set<() => void>();

const same = (a: WindowFrame | null, b: WindowFrame | null) =>
  a === b ||
  (a !== null &&
    b !== null &&
    a.x === b.x &&
    a.y === b.y &&
    a.width === b.width &&
    a.height === b.height &&
    a.maximized === b.maximized);

export function publishFocusedWindow(frame: WindowFrame | null): void {
  if (same(focused, frame)) return;
  focused = frame && { x: frame.x, y: frame.y, width: frame.width, height: frame.height, maximized: frame.maximized };
  for (const listener of listeners) listener();
}

/** The box of the window on top as it is laid out on a page this large, or null when no window shows. */
export function focusedWindowBox(viewport: { width: number; height: number }): WindowBox | null {
  return focused && windowBox(focused, viewport);
}

export function subscribeFocusedWindow(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}
