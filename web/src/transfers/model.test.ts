import { describe, expect, it } from "vitest";

import { speedText } from "./model";

describe("speedText", () => {
  it("adds how long is left only while bytes are left", () => {
    expect(speedText(2_000_000, 300_000_000)).toBe("1.9 MiB/s · 约 3 分钟");
    // A file fully sent waits for the NAS to confirm it: no dangling separator.
    expect(speedText(2_000_000, 0)).toBe("1.9 MiB/s");
  });
});
