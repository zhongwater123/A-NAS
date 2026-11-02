import { useEffect, useSyncExternalStore } from "react";

import { readWebSearch, type WebSearchStatus } from "./api";

// Whether web search is on and which tabs it shows. The desktop hides the app
// while an administrator has turned it off; open desktops read the switches
// again every minute and when they come back into view, so a change in
// 设置 → 网页搜索 reaches other browsers without a reload. The components
// that show them share one timer: each read reaches the Host Agent.

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

// readers counts the mounted components: the first starts the timer and the
// last stops it. Each one that mounts reads the switches again, so a window
// opens with the current state; reads already under way are shared.
let readers = 0;
let reading: Promise<void> | undefined;
let stopTimer: (() => void) | undefined;

function load() {
  if (reading) return;
  const read = readWebSearch()
    .then(publishWebSearch)
    .catch(() => undefined)
    .finally(() => {
      if (reading === read) reading = undefined;
    });
  reading = read;
}

function startTimer() {
  const timer = window.setInterval(load, webSearchRefreshMs);
  const visible = () => {
    if (!document.hidden) load();
  };
  document.addEventListener("visibilitychange", visible);
  return () => {
    window.clearInterval(timer);
    document.removeEventListener("visibilitychange", visible);
  };
}

// useWebSearch is undefined until the first read; a failed read leaves the
// last state, so a passing outage does not make the app flicker.
export function useWebSearch(): WebSearchStatus | undefined {
  const status = useSyncExternalStore(subscribe, () => current);
  useEffect(() => {
    readers += 1;
    if (readers === 1) stopTimer = startTimer();
    load();
    return () => {
      readers -= 1;
      if (readers === 0) {
        stopTimer?.();
        stopTimer = undefined;
      }
    };
  }, []);
  return status;
}

// resetWebSearch forgets the switches when another account signs in.
export function resetWebSearch() {
  current = undefined;
  reading = undefined;
}
