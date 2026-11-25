import { describe, expect, it } from "vitest";

import { fileCategory, fileTypeLabel, isTextPreviewable, isVideoFile } from "./fileTypes";

describe("file categories", () => {
  it("sort a file by its name, and name it the same way", () => {
    expect(
      ["a.HEIC", "b.mkv", "c.flac", "d.pdf", "e.xlsx", "f.md", "g.tar", "h.bin", "noext"].map(fileCategory),
    ).toEqual(["image", "video", "audio", "pdf", "sheet", "document", "archive", "other", "other"]);
    expect(fileTypeLabel("e.xlsx")).toBe("电子表格");
    expect(fileTypeLabel("g.Zip")).toBe("ZIP 文件");
    expect(fileTypeLabel("noext")).toBe("文件");
  });
});

describe("video files", () => {
  it("are the types the server shows a frame of", () => {
    for (const name of ["a.mp4", "b.MKV", "c.rmvb", "d.rm", "e.vob", "f.ogv", "g.asf", "h.divx", "i.m2ts"])
      expect(isVideoFile(name), name).toBe(true);
    expect(fileTypeLabel("VTS_01_1.VOB")).toBe("视频");
  });

  it("leave out .ts, which is TypeScript shown as text", () => {
    expect(isVideoFile("main.ts")).toBe(false);
    expect(isTextPreviewable("main.ts")).toBe(true);
    expect(isVideoFile("backup.rm.txt")).toBe(false);
  });
});
