import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { minutesUntil, useMinutesLeft } from "./StorageSection";

describe("a storage plan's expiry", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("counts whole minutes, rounding up, and none once the time passed", () => {
    const until = Date.parse("2026-10-23T10:02:30Z");
    expect(minutesUntil(until, Date.parse("2026-10-23T10:00:00Z"))).toBe(3);
    expect(minutesUntil(until, Date.parse("2026-10-23T10:01:30Z"))).toBe(1);
    expect(minutesUntil(until, until)).toBe(0);
    expect(minutesUntil(until, until + 1)).toBe(0);
  });

  it("counts down on its own and reaches none when the plan expires", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-10-23T10:00:00Z"));
    const { result } = renderHook(() => useMinutesLeft(Date.parse("2026-10-23T10:02:30Z")));
    expect(result.current).toBe(3);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(29_000);
    });
    expect(result.current).toBe(3);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
    });
    expect(result.current).toBe(2);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(120_000);
    });
    expect(result.current).toBe(0);
  });

  it("counts nothing without a plan", () => {
    const { result } = renderHook(() => useMinutesLeft(undefined));
    expect(result.current).toBe(0);
  });
});
