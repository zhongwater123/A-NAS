// How the companion is on this device, as the member set it (Settings → 概览, or Esc on the companion):
// as usual; quiet, asleep until the member wakes it, which VNAS cannot; or hidden, its card not loaded.
// Kept in this browser only: another device, or the local console, has its own.
import { useSyncExternalStore } from "react";

export type CompanionMode = "normal" | "quiet" | "hidden";

const MODE_KEY = "a-nas.companion.mode";
const listeners = new Set<() => void>();

function stored(): CompanionMode {
  try {
    const value = localStorage.getItem(MODE_KEY);
    return value === "quiet" || value === "hidden" ? value : "normal";
  } catch {
    return "normal";
  }
}

let mode: CompanionMode = stored();

export function companionMode(): CompanionMode {
  return mode;
}

/** The member asked the companion to be quiet: VNAS's wake and lock do not wake it (commands.ts). */
export function memberQuiet(): boolean {
  return mode === "quiet";
}

/**
 * The companion sleeps at the member's asking: it went to sleep, or was asleep, while quiet. Kept here rather
 * than in the card, which hidden takes off the page while the sleep goes on (host.ts), so that back to normal
 * from hidden wakes it as from quiet. Its waking ends it.
 */
let memberSlept = false;

export function memberSleep(): boolean {
  return memberSlept;
}

export function setMemberSleep(slept: boolean): void {
  memberSlept = slept;
}

export function setCompanionMode(next: CompanionMode): void {
  if (next === mode) return;
  mode = next;
  try {
    if (next === "normal") localStorage.removeItem(MODE_KEY);
    else localStorage.setItem(MODE_KEY, next);
  } catch {
    // The choice holds for this page still.
  }
  for (const listener of listeners) listener();
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export const useCompanionMode = () => useSyncExternalStore(subscribe, companionMode);

/** For tests: as this device stores it now. */
export function resetCompanionMode(): void {
  mode = stored();
  memberSlept = false;
  for (const listener of listeners) listener();
}
