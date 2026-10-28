import { describe, expect, it } from "vitest";

import { formatRate } from "./StatusBar";

describe("network rate formatting", () => {
  it("uses decimal units with one decimal below 100", () => {
    expect(formatRate(0)).toEqual(["0", "B/s"]);
    expect(formatRate(999)).toEqual(["999", "B/s"]);
    expect(formatRate(1_500)).toEqual(["1.5", "KB/s"]);
    expect(formatRate(327_680)).toEqual(["328", "KB/s"]);
    expect(formatRate(2_516_582)).toEqual(["2.5", "MB/s"]);
    expect(formatRate(1_250_000_000)).toEqual(["1.3", "GB/s"]);
  });
});
