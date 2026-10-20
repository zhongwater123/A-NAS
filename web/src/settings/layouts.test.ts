import { describe, expect, it } from "vitest";

import { conversionPercent, fits, formatRemaining, layoutLabel, remainingSeconds, usableBytes, usedBytes } from "./layouts";

const GiB = 1024 ** 3;

describe("data-volume layouts", () => {
  it("estimates the space each layout offers like the server", () => {
    expect(usableBytes("single", [500])).toBe(500);
    expect(usableBytes("mirror", [500, 500])).toBe(500);
    expect(usableBytes("mirror", [1000, 500])).toBe(500);
    expect(usableBytes("mirror", [1000, 500, 500])).toBe(1000);
    expect(usableBytes("span", [1000, 500])).toBe(1500);
    expect(usableBytes("stripe", [500, 500])).toBe(1000);
  });

  it("refuses a layout the files would not fit with the reserve left", () => {
    expect(fits(500 * GiB, undefined)).toBe(true);
    expect(fits(500 * GiB, 470 * GiB)).toBe(true);
    expect(fits(500 * GiB, 480 * GiB)).toBe(false);
    expect(usedBytes({ id: "v", diskIds: [], layout: "single", state: "available", capacityBytes: 100, availableBytes: 30 })).toBe(70);
    expect(usedBytes({ id: "v", diskIds: [], layout: "single", state: "available", capacityBytes: 100 })).toBeUndefined();
  });

  it("names layouts with their RAID level", () => {
    expect(layoutLabel("mirror")).toBe("镜像（RAID 1）");
    expect(layoutLabel("span")).toBe("扩容（不冗余）");
    expect(layoutLabel("single")).toBe("单盘");
  });

  it("shows conversion progress and estimates the time left from its speed", () => {
    const running = { state: "running" as const, convertedBytes: 60 * GiB, totalBytes: 120 * GiB };
    expect(conversionPercent(running)).toBe(50);
    expect(conversionPercent({ ...running, convertedBytes: 120 * GiB - 1 })).toBe(99);
    const first = { at: 0, bytes: 30 * GiB };
    expect(remainingSeconds(first, 10_000, running)).toBeUndefined();
    // 30 GiB in 10 minutes leaves 60 GiB for 20 more.
    expect(remainingSeconds(first, 600_000, running)).toBe(1200);
    expect(remainingSeconds(first, 600_000, { ...running, state: "waiting" })).toBeUndefined();
    expect(formatRemaining(600)).toBe("约 10 分钟");
    expect(formatRemaining(5400)).toBe("约 1 小时 30 分钟");
    expect(formatRemaining(7200)).toBe("约 2 小时");
  });
});
