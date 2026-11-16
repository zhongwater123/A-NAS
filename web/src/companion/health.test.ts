import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { HostState } from "../desktop/api";
import { healthyState } from "../test/fixtures";
import { CLEAR_READS, CRITICAL_FALLBACK, DISCONNECTED_KEEP_MS, deviceTrouble, useDeviceTrouble } from "./health";

const state = (system: Partial<HostState["system"]>): HostState => ({
  ...healthyState,
  system: { ...healthyState.system, ...system },
});
/** A new read of a read-only volume: a new object each time, as useHostState gives one for each answer. */
const readOnly = () =>
  state({ health: "critical", healthFindings: [{ code: "volume_read_only", severity: "critical" }] });
const full = () =>
  state({ health: "critical", healthFindings: [{ code: "volume_full", severity: "critical", value: 96 }] });
const healthy = () => ({ ...healthyState });

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

  it("is the first critical reason, in Settings' words, known by its code and disk", () => {
    const failing = state({
      health: "critical",
      healthFindings: [
        { code: "disk_hot", severity: "warning", diskId: "disk:fake-data-01", value: 57 },
        { code: "disk_failing", severity: "critical", diskId: "disk:fake-data-01" },
        { code: "volume_read_only", severity: "critical" },
      ],
    });
    expect(deviceTrouble(failing, false)).toEqual({
      key: "disk_failing:disk:fake-data-01",
      text: "A-NAS Fake HDD 的 SMART 自检未通过",
    });
    expect(deviceTrouble(readOnly(), false)).toEqual({ key: "volume_read_only:", text: "数据卷变成了只读" });
  });

  it("still shows for a critical device whose reasons the page does not know", () => {
    const fallback = { key: "critical", text: CRITICAL_FALLBACK };
    expect(deviceTrouble(state({ health: "critical", healthFindings: [] }), false)).toEqual(fallback);
    const older = state({ health: "critical" });
    delete (older.system as Partial<HostState["system"]>).healthFindings;
    expect(deviceTrouble(older, false)).toEqual(fallback);
  });

  it("is none before the device is read, while it is unknown, and while it cannot be read", () => {
    expect(deviceTrouble(undefined, false)).toBeUndefined();
    expect(deviceTrouble(state({ health: "unknown", healthFindings: [] }), false)).toBeUndefined();
    expect(deviceTrouble(readOnly(), true)).toBeUndefined();
  });
});

describe("the dot held steady over the desktop's reads", () => {
  afterEach(() => vi.useRealTimers());
  const hold = () =>
    renderHook(({ snapshot, disconnected }) => useDeviceTrouble(snapshot, disconnected), {
      initialProps: { snapshot: undefined as HostState | undefined, disconnected: false },
    });

  it("comes with the first critical read", () => {
    const { result, rerender } = hold();
    expect(result.current).toBeUndefined();
    rerender({ snapshot: readOnly(), disconnected: false });
    expect(result.current?.key).toBe("volume_read_only:");
  });

  it(`goes only after ${CLEAR_READS} reads in a row that are not critical, so a reading near a limit does not blink`, () => {
    const { result, rerender } = hold();
    rerender({ snapshot: full(), disconnected: false });
    // A volume about to be full, read on either side of the limit: the dot stays.
    for (const snapshot of [healthy(), full(), healthy(), full()]) {
      rerender({ snapshot, disconnected: false });
      expect(result.current?.key).toBe("volume_full:");
    }
    rerender({ snapshot: healthy(), disconnected: false });
    expect(result.current).toBeDefined();
    rerender({ snapshot: healthy(), disconnected: false });
    expect(result.current).toBeUndefined();
  });

  it("follows another trouble at once, and keeps the same one though its words change", () => {
    const { result, rerender } = hold();
    rerender({ snapshot: readOnly(), disconnected: false });
    const first = result.current;
    rerender({ snapshot: readOnly(), disconnected: false });
    expect(result.current).toBe(first);
    rerender({ snapshot: full(), disconnected: false });
    expect(result.current?.key).toBe("volume_full:");
  });

  it(`keeps the dot through a lost connection for ${DISCONNECTED_KEEP_MS / 60_000} minutes, then lets it go`, () => {
    vi.useFakeTimers();
    const { result, rerender } = hold();
    const last = readOnly();
    rerender({ snapshot: last, disconnected: false });
    // The desktop keeps the last snapshot and says the connection is down.
    rerender({ snapshot: last, disconnected: true });
    act(() => vi.advanceTimersByTime(DISCONNECTED_KEEP_MS - 1));
    expect(result.current?.key).toBe("volume_read_only:");
    act(() => vi.advanceTimersByTime(1));
    expect(result.current).toBeUndefined();
    // Back, and the device still in trouble: the dot again at once.
    rerender({ snapshot: readOnly(), disconnected: false });
    expect(result.current?.key).toBe("volume_read_only:");
  });

  it("keeps the dot when the connection comes back before then", () => {
    vi.useFakeTimers();
    const { result, rerender } = hold();
    const last = readOnly();
    rerender({ snapshot: last, disconnected: false });
    rerender({ snapshot: last, disconnected: true });
    act(() => vi.advanceTimersByTime(30_000));
    rerender({ snapshot: readOnly(), disconnected: false });
    act(() => vi.advanceTimersByTime(DISCONNECTED_KEEP_MS));
    expect(result.current?.key).toBe("volume_read_only:");
  });
});
