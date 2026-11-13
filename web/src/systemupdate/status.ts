import { useEffect, useSyncExternalStore } from "react";

import { readSystemUpdate, type SystemUpdateStatus } from "./api";

// The system update's state for administrators: the settings sidebar marks a
// new version, the overview lists it, and 设置 → 系统更新 shows all of it.
// They share one reader: every minute while nothing runs, every two seconds
// while an operation does. An install restarts the Product Service, so reads
// fail for a while; the view keeps the last state and the reads go on until
// the service answers again.

export const quietRefreshMs = 60_000;
export const busyRefreshMs = 2_000;

export interface SystemUpdateView {
  status?: SystemUpdateStatus;
  // error is why the last read got no answer, as while the services restart.
  error?: unknown;
  // pageVersion is the version this page's code is from: the one it was
  // built at, or, in a build without one, the first version it read.
  pageVersion?: string;
}

const builtVersion = import.meta.env.VITE_ANAS_VERSION || undefined;

let view: SystemUpdateView = { pageVersion: builtVersion };
const listeners = new Set<() => void>();

function publish(next: SystemUpdateView) {
  view = next;
  listeners.forEach((listener) => listener());
  schedule();
}

export function publishSystemUpdate(status: SystemUpdateStatus) {
  publish({ status, pageVersion: view.pageVersion ?? status.current });
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

// updateBusy: an operation runs, or one that restarts the services is under way.
export const updateBusy = (status?: SystemUpdateStatus) =>
  !!status &&
  (status.running ||
    status.phase === "downloading" ||
    status.phase === "installing" ||
    status.phase === "rolling-back");

let readers = 0;
let reading: Promise<void> | undefined;
let timer = 0;

function schedule() {
  window.clearTimeout(timer);
  if (readers === 0) return;
  // A failed read keeps the last state, so reads failing mid-install stay frequent.
  timer = window.setTimeout(load, updateBusy(view.status) ? busyRefreshMs : quietRefreshMs);
}

function load() {
  if (reading) return;
  const read = readSystemUpdate()
    .then(publishSystemUpdate)
    .catch((error: unknown) => publish({ ...view, error }))
    .finally(() => {
      if (reading === read) reading = undefined;
    });
  reading = read;
}

// reloadSystemUpdate reads now, as after a request.
export function reloadSystemUpdate() {
  load();
}

const none: SystemUpdateView = {};

// useSystemUpdate reads while a component that shows it is mounted; enabled
// is false for members, who neither see nor read it. The state is the
// device's, the same for every administrator, so a new sign-in keeps it.
export function useSystemUpdate(enabled: boolean): SystemUpdateView {
  const current = useSyncExternalStore(subscribe, () => view);
  useEffect(() => {
    if (!enabled) return;
    readers += 1;
    load();
    const visible = () => {
      if (!document.hidden) load();
    };
    document.addEventListener("visibilitychange", visible);
    return () => {
      document.removeEventListener("visibilitychange", visible);
      readers -= 1;
      if (readers === 0) window.clearTimeout(timer);
    };
  }, [enabled]);
  return enabled ? current : none;
}

// resetSystemUpdate forgets everything read, for tests that each start anew.
export function resetSystemUpdate() {
  view = { pageVersion: builtVersion };
  reading = undefined;
  window.clearTimeout(timer);
}

// hasNewVersion: a version waits to be downloaded or installed.
export const hasNewVersion = (status?: SystemUpdateStatus) =>
  !!status?.candidate && (status.phase === "available" || status.phase === "ready");
