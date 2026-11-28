import { describe, expect, it } from "vitest";

import type { VideoCutProjectState } from "../../api.generated";
import { cutModeOf, pageOf, statusLabel } from "./pages";

describe("the page a project opens on", () => {
  // Every state of docs/specs/venus.md#作品状态机, with its page and view.
  const table: [VideoCutProjectState, "prepare" | "cut", string | undefined][] = [
    ["importing", "prepare", undefined],
    ["pending", "prepare", undefined],
    ["upload_interrupted", "prepare", undefined],
    ["uploading", "cut", "generating"],
    ["queued", "cut", "generating"],
    ["generating", "cut", "generating"],
    ["failed", "cut", "failed"],
    ["ready", "cut", "ready"],
    ["exporting", "cut", "exporting"],
    ["exported", "cut", "ready"],
  ];

  it.each(table)("%s opens the %s page", (state, page, mode) => {
    expect(pageOf(state)).toBe(page);
    if (mode) expect(cutModeOf(state)).toBe(mode);
  });

  it("labels the film page only while it cannot be edited", () => {
    expect([statusLabel("generating"), statusLabel("failed"), statusLabel("exporting"), statusLabel("ready")]).toEqual([
      "生成中",
      "生成失败",
      "导出中",
      undefined,
    ]);
  });
});
