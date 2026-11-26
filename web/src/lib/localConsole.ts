// isLocalConsole says whether this page runs on the device's own screen: the
// kiosk opens the desktop, and the pages the desktop opens, with
// ?local-console=1 (scripts/run-kiosk.sh).
export function isLocalConsole(search = window.location.search): boolean {
  return new URLSearchParams(search).get("local-console") === "1";
}
