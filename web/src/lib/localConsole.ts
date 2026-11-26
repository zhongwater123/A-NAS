// isLocalConsole says whether this page runs on the device's own screen: the
// kiosk opens the desktop, and the pages the desktop opens, with
// ?local-console=1 (scripts/run-kiosk.sh).
export function isLocalConsole(search = window.location.search): boolean {
  return new URLSearchParams(search).get("local-console") === "1";
}

const activeListeners = new Set<() => void>();

// localConsoleActive tells the device screen's screen saver that the member
// is using the page where its window cannot see: input inside a frame never
// reaches the page around it (workspace/PageView.tsx).
export function localConsoleActive() {
  for (const listener of activeListeners) listener();
}

// onLocalConsoleActive calls listener with each such report.
export function onLocalConsoleActive(listener: () => void): () => void {
  activeListeners.add(listener);
  return () => {
    activeListeners.delete(listener);
  };
}
