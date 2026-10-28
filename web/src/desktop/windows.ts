import type { WindowID } from "./apps";

export interface WindowModel {
  id: WindowID;
  open: boolean;
  minimized: boolean;
  maximized: boolean;
  x: number;
  y: number;
  width: number;
  height: number;
  z: number;
  // Sequence number of the latest open, so the dock lists apps in the order they were launched.
  opened: number;
}

export type WindowAction =
  | { type: "open"; id: WindowID }
  | { type: "close"; id: WindowID }
  | { type: "minimize"; id: WindowID }
  | { type: "focus"; id: WindowID }
  | { type: "maximize"; id: WindowID }
  | { type: "move"; id: WindowID; x: number; y: number }
  | { type: "resize"; id: WindowID; width: number; height: number }
  | { type: "show-desktop" }
  | { type: "restore"; ids: WindowID[] };

// closedWindow is an app's window before it is first opened.
export function closedWindow(id: WindowID, bounds: Pick<WindowModel, "x" | "y" | "width" | "height">): WindowModel {
  return { id, open: false, minimized: false, maximized: false, ...bounds, z: 0, opened: 0 };
}

// windowReducer returns the same state for an action that changes nothing, such
// as focusing the window already on top, so the desktop does not re-render.
export function windowReducer(windows: WindowModel[], action: WindowAction): WindowModel[] {
  const next = nextWindows(windows, action);
  return next.every((window, index) => window === windows[index]) ? windows : next;
}

function nextWindows(windows: WindowModel[], action: WindowAction): WindowModel[] {
  if (action.type === "show-desktop")
    return windows.map((window) => (window.open && !window.minimized ? { ...window, minimized: true } : window));
  if (action.type === "restore") {
    // Bring the windows back above the rest, keeping their stacking order.
    const base = Math.max(...windows.map((window) => window.z)) + 1;
    const order = windows
      .filter((window) => window.open && action.ids.includes(window.id))
      .sort((a, b) => a.z - b.z)
      .map((window) => window.id);
    return windows.map((window) =>
      order.includes(window.id) ? { ...window, minimized: false, z: base + order.indexOf(window.id) } : window,
    );
  }
  const top = Math.max(...windows.map((window) => window.z)) + 1;
  const nextOpened = Math.max(...windows.map((window) => window.opened)) + 1;
  const onTop = (window: WindowModel) => window.z === top - 1;
  return windows.map((window) => {
    if (window.id !== action.id) return window;
    switch (action.type) {
      case "open":
        if (window.open && !window.minimized && onTop(window)) return window;
        return { ...window, open: true, minimized: false, z: top, opened: window.open ? window.opened : nextOpened };
      case "close":
        return window.open ? { ...window, open: false, minimized: false } : window;
      case "minimize":
        return window.minimized ? window : { ...window, minimized: true };
      case "focus":
        return onTop(window) ? window : { ...window, z: top };
      case "maximize":
        return { ...window, maximized: !window.maximized, z: top };
      case "move":
        return window.x === action.x && window.y === action.y ? window : { ...window, x: action.x, y: action.y };
      case "resize":
        return window.width === action.width && window.height === action.height
          ? window
          : { ...window, width: action.width, height: action.height };
    }
  });
}
