// What the dot at the companion's feet says (docs/specs/virtual-companion.md, 出事才亮的小点): the
// first critical reason of the system health the desktop already reads (docs/specs/system-health.md),
// worded as Settings' overview words it. Nothing when the system is not critical: a warning such as a
// warm disk or a volume 90% full is Settings' to tell, not a dot that would be up most days.

import type { HostState } from "../desktop/api";
import { describeFinding } from "../settings/health";

/** The words for a critical system with no critical reason the page knows, as an older response may be. */
export const CRITICAL_FALLBACK = "设备出了严重问题";

/**
 * The trouble the companion shows a dot for, undefined for none: none until the host state is read,
 * and none while it cannot be (the desktop's status bar says the connection is down), since a reading
 * that old says nothing of the device now.
 */
export function deviceTrouble(snapshot: HostState | undefined, disconnected: boolean): string | undefined {
  if (!snapshot || disconnected || snapshot.system.health !== "critical") return undefined;
  const finding = (snapshot.system.healthFindings ?? []).find((candidate) => candidate.severity === "critical");
  return finding ? describeFinding(finding, snapshot.disks).title : CRITICAL_FALLBACK;
}
