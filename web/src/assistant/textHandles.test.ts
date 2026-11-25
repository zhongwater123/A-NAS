import { describe, expect, it } from "vitest";

import { linkTextHandles, textHandleOf } from "./textHandles";

describe("handles in an answer", () => {
  it("become links the window recognises, wherever the assistant wrote them", () => {
    const linked = linkTextHandles(
      "你的身份证号是 ⟦身份证号·1a2b3c⟧，卡号 **⟦卡号·00ff9e⟧**，流水号 `⟦流水号·abcdef⟧`。",
    );
    expect(linked).toBe(
      "你的身份证号是 [身份证号](#a-nas-text-handle/1a2b3c)，卡号 **[卡号](#a-nas-text-handle/00ff9e)**，流水号 [流水号](#a-nas-text-handle/abcdef)。",
    );
    expect(textHandleOf("#a-nas-text-handle/1a2b3c")).toBe("1a2b3c");
  });

  it("leave alone what is no handle", () => {
    for (const text of [
      "没有句柄",
      "⟦身份证号·1a2b3⟧",
      "⟦身份证号·1A2B3C⟧",
      "⟦身份证号 1a2b3c⟧",
      "⟦a](javascript:alert(1))·1a2b3c⟧",
      "⟦这个标签实在是太长了不会是句柄·1a2b3c⟧",
    ])
      expect(linkTextHandles(text)).toBe(text);
    // A code span keeps its backticks when it holds more than the handle.
    expect(linkTextHandles("`号码 ⟦号码·1a2b3c⟧`")).toBe("`号码 [号码](#a-nas-text-handle/1a2b3c)`");
    for (const href of [undefined, "https://example.com", "#a-nas-text-handle/", "#a-nas-text-handle/1a2b3c/../x"])
      expect(textHandleOf(href)).toBeUndefined();
  });
});
