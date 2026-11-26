// isLocalConsole says whether this page runs on the device's own screen: the
// kiosk opens the desktop with ?local-console=1 (scripts/run-kiosk.sh). Only
// there does the page load the screen saver (desktop/LocalConsoleScreenSaver.tsx).
export function isLocalConsole(search = window.location.search): boolean {
  return new URLSearchParams(search).get("local-console") === "1";
}

// localConsoleScreenSaverIdleMs is how long the screen waits without input
// before the screen saver starts; 设置 → 个性化 says so.
export const localConsoleScreenSaverIdleMs = 180_000;

let covered = false;
const dismissedListeners = new Set<() => void>();

// localConsoleCovered tells whether either cover, the screen saver or 息屏,
// is up over the page: nobody sees what the companion would do then, so VNAS
// hears that it is paused (companion/commands.ts).
export function localConsoleCovered(): boolean {
  return covered;
}

// setLocalConsoleCovered is how the screen saver says that a cover went up or
// came down.
export function setLocalConsoleCovered(value: boolean) {
  covered = value;
}

// onLocalConsoleDismissed calls listener with each input that takes either
// cover away. That input stops at the screen saver, and a wheel or a pointer
// move has no release that reaches the page after it, so the page would not
// know the member is back until the next input (companion/petCard.tsx).
export function onLocalConsoleDismissed(listener: () => void): () => void {
  dismissedListeners.add(listener);
  return () => {
    dismissedListeners.delete(listener);
  };
}

// localConsoleDismissed is how the screen saver tells those listeners.
export function localConsoleDismissed() {
  for (const listener of dismissedListeners) listener();
}
