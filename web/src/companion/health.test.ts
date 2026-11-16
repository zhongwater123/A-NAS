import { describe, expect, it } from "vitest";

import type { HostState } from "../desktop/api";
import { healthyState } from "../test/fixtures";
import { CRITICAL_FALLBACK, deviceTrouble } from "./health";

const state = (system: Partial<HostState["system"]>): HostState => ({
  ...healthyState,
  system: { ...healthyState.system, ...system },
});

describe("the trouble the companion shows a dot for", () => {
  it("is none while the device is healthy, or only asks for attention", () => {
    expect(deviceTrouble(healthyState, false)).toBeUndefined();
    const warm = state({
      health: "warning",
      healthFindings: [
        { code: "disk_hot", severity: "warning", diskId: "disk:fake-data-01", value: 57 },
        { code: "volume_nearly_full", severity: "warning", value: 92 },
      ],
    });
    expect(deviceTrouble(warm, false)).toBeUndefined();
  });

  it("is the first critical reason, in Settings' words", () => {
    const failing = state({
      health: "critical",
      healthFindings: [
        { code: "disk_hot", severity: "warning", diskId: "disk:fake-data-01", value: 57 },
        { code: "disk_failing", severity: "critical", diskId: "disk:fake-data-01" },
        { code: "volume_read_only", severity: "critical" },
      ],
    });
    expect(deviceTrouble(failing, false)).toBe("A-NAS Fake HDD 的 SMART 自检未通过");
    const readOnly = state({
      health: "critical",
      healthFindings: [{ code: "volume_read_only", severity: "critical" }],
    });
    expect(deviceTrouble(readOnly, false)).toBe("数据卷变成了只读");
  });

  it("still shows for a critical device whose reasons the page does not know", () => {
    expect(deviceTrouble(state({ health: "critical", healthFindings: [] }), false)).toBe(CRITICAL_FALLBACK);
    const older = state({ health: "critical" });
    delete (older.system as Partial<HostState["system"]>).healthFindings;
    expect(deviceTrouble(older, false)).toBe(CRITICAL_FALLBACK);
  });

  it("is none before the device is read, while it is unknown, and while it cannot be read", () => {
    const readOnly = state({
      health: "critical",
      healthFindings: [{ code: "volume_read_only", severity: "critical" }],
    });
    expect(deviceTrouble(undefined, false)).toBeUndefined();
    expect(deviceTrouble(state({ health: "unknown", healthFindings: [] }), false)).toBeUndefined();
    expect(deviceTrouble(readOnly, true)).toBeUndefined();
  });
});
