// What the dot at the companion's feet says (docs/specs/virtual-companion.md, 出事才亮的小点): the
// first critical reason of the system health the desktop already reads (docs/specs/system-health.md),
// worded as Settings' overview words it. Nothing when the system is not critical: a warning such as a
// warm disk or a volume 90% full is Settings' to tell, not a dot that would be up most days.
//
// The dot holds steady (useDeviceTrouble): it comes with the first critical read, but goes only after
// CLEAR_READS reads in a row that are not, so a reading near a limit (a volume about to be full) does not
// make it blink; and a desktop that lost its connection keeps it DISCONNECTED_KEEP_MS before it lets it
// go, as a device in trouble is also the one most likely not to answer for a while.

import { useEffect, useState } from "react";

import type { HostState } from "../desktop/api";
import { describeFinding } from "../settings/health";

/** The words for a critical system with no critical reason the page knows, as an older response may be. */
export const CRITICAL_FALLBACK = "设备出了严重问题";

/** How many reads in a row that are not critical take the dot away; one is a blip. */
export const CLEAR_READS = 2;

/** How long a desktop that cannot read the device keeps the dot it had before letting it go. */
export const DISCONNECTED_KEEP_MS = 2 * 60 * 1000;

/**
 * A critical trouble: what it is (key: the finding's code and disk, the same trouble while those stay, though
 * its words may carry a value that changes) and Settings' words for it.
 */
export interface DeviceTrouble {
  key: string;
  text: string;
}

/**
 * The trouble one read of the device shows a dot for, undefined for none: none until the host state is read,
 * and none while it cannot be (the desktop's status bar says the connection is down), since a reading that
 * old says nothing of the device now.
 */
export function deviceTrouble(snapshot: HostState | undefined, disconnected: boolean): DeviceTrouble | undefined {
  if (!snapshot || disconnected || snapshot.system.health !== "critical") return undefined;
  const finding = (snapshot.system.healthFindings ?? []).find((candidate) => candidate.severity === "critical");
  if (!finding) return { key: "critical", text: CRITICAL_FALLBACK };
  return { key: `${finding.code}:${finding.diskId ?? ""}`, text: describeFinding(finding, snapshot.disks).title };
}

/** The dot as the reads so far leave it, and the read it last took. */
interface Held {
  read?: HostState;
  shown?: DeviceTrouble;
  clearReads: number;
}

/**
 * The trouble the companion shows a dot for, held steady over the desktop's reads of the device (above): a
 * new read is a new snapshot object, as useHostState gives one for each answer.
 */
export function useDeviceTrouble(snapshot: HostState | undefined, disconnected: boolean): DeviceTrouble | undefined {
  const [held, setHeld] = useState<Held>({ clearReads: 0 });
  if (snapshot && !disconnected && snapshot !== held.read) {
    const now = deviceTrouble(snapshot, false);
    if (now) {
      const same = held.shown && held.shown.key === now.key && held.shown.text === now.text;
      setHeld({ read: snapshot, shown: same ? held.shown : now, clearReads: 0 });
    } else {
      const clearReads = held.clearReads + 1;
      setHeld({ read: snapshot, shown: clearReads >= CLEAR_READS ? undefined : held.shown, clearReads });
    }
  }
  useEffect(() => {
    if (!disconnected) return;
    const timer = window.setTimeout(
      () => setHeld((current) => ({ ...current, shown: undefined })),
      DISCONNECTED_KEEP_MS,
    );
    return () => window.clearTimeout(timer);
  }, [disconnected]);
  return held.shown;
}
