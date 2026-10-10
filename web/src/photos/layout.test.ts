import { describe, expect, it } from "vitest";

import { assetAspect, flatEntries, justify, timelineEntries, tileGap } from "./layout";
import { dayLabel } from "./model";
import type { PhotoAsset } from "./photosApi";

const photo = (id: string, takenAt: string, width = 4000, height = 3000): PhotoAsset => ({
  id, libraryId: "library:mine", name: `${id}.jpg`, mediaType: "image/jpeg", sizeBytes: 1, uploadedBy: "user:alice",
  importedAt: "2026-10-08T09:00:00Z", takenAt, width, height, thumbnail: "ready",
});

describe("justify", () => {
  it("fills each row to the width and keeps every photo's shape", () => {
    const photos = Array.from({ length: 9 }, (_, index) => photo(`p${index}`, "2026-05-01T08:00:00Z", index % 3 ? 4000 : 3000, index % 3 ? 3000 : 4000));
    const rows = justify(photos, assetAspect, 900, 180);
    expect(rows.flatMap((row) => row.boxes).map((box) => box.item.id)).toEqual(photos.map((item) => item.id));
    for (const row of rows.slice(0, -1)) {
      const last = row.boxes.at(-1)!;
      expect(Math.round(last.left + last.width)).toBe(900);
      for (const box of row.boxes) expect(box.width / box.height).toBeCloseTo(assetAspect(box.item), 1);
    }
    expect(rows[1].boxes[0].left).toBe(0);
    expect(rows[0].boxes[1].left - rows[0].boxes[0].width).toBeCloseTo(tileGap, 0);
  });

  it("keeps panoramas from turning a row into a sliver", () => {
    expect(assetAspect(photo("wide", "", 12000, 1000))).toBe(3);
    expect(assetAspect(photo("unknown", "", 0, 0))).toBe(1);
  });
});

describe("timelineEntries", () => {
  it("heads months and days, and holds unloaded months open by their size", () => {
    const may = [photo("a", "2026-05-02T08:00:00Z"), photo("b", "2026-05-02T07:00:00Z"), photo("c", "2026-05-01T08:00:00Z")];
    const now = new Date("2026-10-10T08:00:00Z");
    const entries = timelineEntries([{ month: "2026-05", photos: 3 }, { month: "2025-12", photos: 40 }], new Map([["2026-05", { assets: may }]]), [], 900, 180, now);
    expect(entries.map((entry) => entry.kind)).toEqual(["month", "day", "row", "day", "row", "month", "placeholder"]);
    expect(entries[0].label).toBe("2026年5月");
    expect(entries[1].kind === "day" && entries[1].ids).toEqual(["a", "b"]);
    expect(entries[1].label).toBe(dayLabel(new Date("2026-05-02T08:00:00Z"), now));
    // The floating date of a row is its day.
    expect(entries[2].label).toBe(entries[1].label);
    const placeholder = entries.at(-1)!;
    expect(placeholder.kind === "placeholder" && placeholder.month).toBe("2025-12");
    expect(placeholder.size).toBeGreaterThan(180 * 3);
  });

  it("puts uploads first and leaves out months that emptied", () => {
    const entries = timelineEntries([{ month: "2026-05", photos: 1 }], new Map([["2026-05", { assets: [] }]]),
      [{ key: "upload-1", name: "new.jpg", url: "blob:1", aspect: 1.5, progress: 0.5, state: "uploading" }], 900, 180);
    expect(entries.map((entry) => entry.kind)).toEqual(["heading", "pending"]);
    expect(entries[0].kind === "heading" && entries[0].detail).toBe("正在上传 1 张");
  });
});

describe("flatEntries", () => {
  it("asks for more while pages remain and ends with a note otherwise", () => {
    const photos = [photo("a", "2026-05-02T08:00:00Z")];
    expect(flatEntries(photos, 900, 180, true).at(-1)?.kind).toBe("more");
    expect(flatEntries(photos, 900, 180, false, "以上是相关度较高的照片").at(-1)?.kind).toBe("end");
    expect(flatEntries([], 900, 180, false, "以上是相关度较高的照片")).toEqual([]);
  });
});
