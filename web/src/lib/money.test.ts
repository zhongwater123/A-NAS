import { describe, expect, it } from "vitest";

import { formatYuan, parseYuan, yuanText } from "./money";

describe("renminbi amounts", () => {
  it("shows fen as yuan with Intl", () => {
    expect(formatYuan(12850)).toBe("¥128.50");
    expect(formatYuan(128000050)).toBe("¥1,280,000.50");
    expect(formatYuan(0)).toBe("¥0.00");
  });
  it("reads what people type in yuan as whole fen", () => {
    expect(parseYuan("128")).toBe(12800);
    expect(parseYuan("128.5")).toBe(12850);
    expect(parseYuan(" ¥1,280.05 ")).toBe(128005);
    expect(parseYuan("0.1")).toBe(10);
    expect(parseYuan("1.")).toBe(100);
    expect(parseYuan("1.234")).toBeUndefined();
    expect(parseYuan("-3")).toBeUndefined();
    expect(parseYuan("一百")).toBeUndefined();
    expect(parseYuan("")).toBeUndefined();
  });
  it("writes fen back the way people type them", () => {
    expect(yuanText(12850)).toBe("128.50");
    expect(yuanText(300)).toBe("3");
    expect(yuanText(5)).toBe("0.05");
  });
});
