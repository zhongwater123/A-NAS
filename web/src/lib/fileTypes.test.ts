import { describe, expect, it } from "vitest";

import { fileTypeLabel, isTextPreviewable, isVideoFile } from "./fileTypes";

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
