import { describe, expect, it } from "vitest";

import { RateMeter } from "./meter";

describe("RateMeter", () => {
  it("measures bytes over the last seconds and falls to zero when they stop", () => {
    const meter = new RateMeter(3000);
    expect(meter.rate(0)).toBe(0);
    for (let second = 0; second <= 4; second += 1) meter.add(second * 1000, second * 5_000_000);
    expect(meter.rate(4000)).toBe(5_000_000);
    // Nothing more arrives: the speed falls and reaches zero within the window.
    expect(meter.rate(5500)).toBeLessThan(5_000_000);
    expect(meter.rate(7100)).toBe(0);
  });

  it("adds up many transfers that are each too short for a speed of their own", () => {
    const meter = new RateMeter(3000);
    let bytes = 0;
    // Forty 3 MB photos, one every 50 ms.
    for (let photo = 0; photo <= 40; photo += 1) meter.add(photo * 50, (bytes += photo ? 3_000_000 : 0));
    expect(meter.rate(2000)).toBeCloseTo(60_000_000, -6);
  });

  it("measures afresh after a pause instead of averaging the pause in", () => {
    const meter = new RateMeter(3000);
    meter.add(0, 0);
    meter.add(60_000, 2_000_000);
    expect(meter.rate(60_000)).toBe(2_000_000);
  });

  it("does not spike in its first instants", () => {
    const meter = new RateMeter(3000);
    meter.add(0, 0);
    meter.add(10, 1_000_000);
    expect(meter.rate(10)).toBe(1_000_000);
  });
});
