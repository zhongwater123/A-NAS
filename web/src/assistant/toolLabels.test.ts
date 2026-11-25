import { describe, expect, it } from "vitest";

import { friendlyPath, short, toolLabel } from "./toolLabels";

describe("what the assistant's tools are called for the member", () => {
  it("names a call and what it works on, in the member's own paths", () => {
    expect(toolLabel("read_file", { path: "/nas/照片/2026/a.jpg" })).toEqual({
      label: "读取",
      detail: "照片/2026/a.jpg",
    });
    expect(toolLabel("write_file", { path: "/nas/助手/待确认/计划.md" })).toEqual({
      label: "写入草稿",
      detail: "草稿/计划.md",
    });
    expect(toolLabel("search_files", { pattern: "发票", path: "/nas/文档" })).toEqual({
      label: "搜索",
      detail: "“发票” 在 文档",
    });
    expect(toolLabel("browser_scroll", { direction: "up" })).toEqual({ label: "滚动", detail: "向上" });
    expect(toolLabel("mcp__desktop__scroll", { direction: "up" })).toEqual({ label: "滚动", detail: "向上" });
    expect(toolLabel("mcp__desktop__click", { ref: "e12" })).toEqual({ label: "点击", detail: "e12" });
    expect(toolLabel("mcp__desktop__look", {})).toEqual({ label: "看桌面画面", detail: "" });
  });

  it("keeps the first line of a long command and names an unknown tool by itself", () => {
    const command = `${"x".repeat(70)}\nsecond line`;
    expect(toolLabel("terminal", { command }).detail).toBe(`${"x".repeat(60)}…`);
    expect(toolLabel("some_new_tool", { path: "/nas/a" })).toEqual({ label: "some_new_tool", detail: "" });
    // Arguments of the wrong type are left out rather than shown.
    expect(toolLabel("read_file", { path: 42 })).toEqual({ label: "读取", detail: "" });
  });

  it("shortens text and paths the same way everywhere", () => {
    expect(short("一行\n两行")).toBe("一行");
    expect(friendlyPath("/nas/家庭/a.txt")).toBe("家庭/a.txt");
  });
});
