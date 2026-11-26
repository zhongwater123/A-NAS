// The phone simulator (phone.html) opens the desktop with ?touch; a
// development build then loads this module, never a device's build.
// It makes the computer a touch screen for the styles and touchScreen(), counts
// every mouse press as a finger's, and makes a button held still a long press.

import { installLongPress } from "./longPress";
import { recordPointer } from "./touch";

export function simulateTouch() {
  const stop = new AbortController();
  const root = document.documentElement;
  root.setAttribute("data-touch", "simulated");
  // After installTouchInput's own listener, so this one has the last word.
  document.addEventListener("pointerdown", () => recordPointer("touch"), { capture: true, signal: stop.signal });
  const longPress = installLongPress({ mouse: true });
  return () => {
    stop.abort();
    longPress();
    root.removeAttribute("data-touch");
  };
}
