import { describe, expect, it } from "vitest";

import { initial } from "./PersonAvatar";

describe("initial", () => {
  it("shows a Chinese name's last character, not the family name", () => {
    expect(initial("陈小禾")).toBe("禾");
    expect(initial("林晓")).toBe("晓");
    expect(initial(" 外婆 ")).toBe("婆");
  });

  it("shows other names' first letter", () => {
    expect(initial("mom")).toBe("M");
    expect(initial("👨‍👩‍👧 home")).toBe("👨‍👩‍👧");
    expect(initial("")).toBe("?");
  });
});
