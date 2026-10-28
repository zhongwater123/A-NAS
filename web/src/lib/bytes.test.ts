import { describe, expect, it } from "vitest";

import { formatBytes, formatNominalCapacity, formatRate } from "./bytes";

describe("sizes", () => {
  it("count in powers of 1024 with binary units and one rounding rule", () => {
    expect(formatBytes(0)).toBe("0 B");
    expect(formatBytes(980)).toBe("980 B");
    expect(formatBytes(3.4 * 1024 ** 2)).toBe("3.4 MiB");
    // The volume of the feedback: the file manager and Settings now agree.
    expect(formatBytes(14_929_000_000)).toBe("13.9 GiB");
    expect(formatBytes(500_107_862_016)).toBe("466 GiB");
    expect(formatBytes(99.97 * 1024)).toBe("100 KiB");
    expect(formatBytes(1023.7 * 1024)).toBe("1.0 MiB");
    expect(formatBytes(2.5 * 1024 ** 4)).toBe("2.5 TiB");
  });

  it("show a dash for sizes that are not known", () => {
    expect(formatBytes(-1)).toBe("—");
    expect(formatBytes(Number.NaN)).toBe("—");
  });

  it("name rates per second in the same units", () => {
    expect(formatRate(2_000_000)).toBe("1.9 MiB/s");
  });

  it("label disks the way their maker does", () => {
    expect(formatNominalCapacity(500_107_862_016)).toBe("500 GB");
    expect(formatNominalCapacity(4_000_787_030_016)).toBe("4 TB");
    expect(formatNominalCapacity(12_000_138_625_024)).toBe("12 TB");
    expect(formatNominalCapacity(1_500_301_910_016)).toBe("1.5 TB");
  });
});
