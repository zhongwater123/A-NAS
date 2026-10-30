import { useEffect, useSyncExternalStore } from "react";

import { readWebSearch, type WebSearchStatus } from "./api";

// Whether web search is on and which tabs it shows. The desktop hides the app
// while an administrator has turned it off; open desktops read the switches
// again every minute and when they come back into view, so a change in
// 设置 → 网页搜索 reaches other browsers without a reload.

export const webSearchRefreshMs = 60_000;

let current: WebSearchStatus | undefined;
const listeners = new Set<() => void>();

export function publishWebSearch(next: WebSearchStatus) {
  current = next;
  listeners.forEach((listener) => listener());
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

// useWebSearch is undefined until the first read; a failed read leaves the
// last state, so a passing outage does not make the app flicker.
export function useWebSearch(): WebSearchStatus | undefined {
  const status = useSyncExternalStore(subscribe, () => current);
  useEffect(() => {
    let active = true;
    const load = () => {
      void readWebSearch()
        .then((next) => {
          if (active) publishWebSearch(next);
        })
        .catch(() => undefined);
    };
    load();
    const timer = window.setInterval(load, webSearchRefreshMs);
    const visible = () => {
      if (!document.hidden) load();
    };
    document.addEventListener("visibilitychange", visible);
    return () => {
      active = false;
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", visible);
    };
  }, []);
  return status;
}

// resetWebSearch forgets the switches when another account signs in.
export function resetWebSearch() {
  current = undefined;
}
