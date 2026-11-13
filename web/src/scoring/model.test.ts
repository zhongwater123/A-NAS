import { describe, expect, it } from "vitest";

import { APIError } from "../api/client";
import { retryable } from "./api";
import {
  exportColumns,
  formatCorrelation,
  formatScore,
  formatSeconds,
  lanes,
  orientationMatrix,
  parseScore,
  Stopwatch,
  share,
  stageAction,
  uprightSize,
} from "./model";

const key = (value: string, modifiers: Partial<Record<"ctrlKey" | "altKey" | "metaKey" | "shiftKey", boolean>> = {}) =>
  stageAction({ key: value, ctrlKey: false, altKey: false, metaKey: false, shiftKey: false, ...modifiers });

describe("stageAction", () => {
  it("moves to whole scores with 0 to 5, by 0.1 with the arrows and 0.01 with Shift, and confirms with Enter", () => {
    expect(key("0")).toEqual({ kind: "set", score: 0 });
    expect(key("5")).toEqual({ kind: "set", score: 500 });
    expect(key("6")).toBeUndefined();
    expect(key("ArrowRight")).toEqual({ kind: "nudge", by: 10 });
    expect(key("ArrowLeft", { shiftKey: true })).toEqual({ kind: "nudge", by: -1 });
    expect(key("Enter")).toEqual({ kind: "confirm" });
  });

  it("skips, marks, goes back and shows the guide", () => {
    expect(key("s")).toEqual({ kind: "skip" });
    expect(key("X")).toEqual({ kind: "flag" });
    expect(key("Backspace")).toEqual({ kind: "undo" });
    expect(key("?")).toEqual({ kind: "guide" });
    expect(key("Escape")).toEqual({ kind: "cancel" });
  });

  it("moves between 打分项 with ↑ and ↓ and looks at 100% with Z", () => {
    expect(key("ArrowUp")).toEqual({ kind: "item", by: -1 });
    expect(key("ArrowDown")).toEqual({ kind: "item", by: 1 });
    expect(key("z")).toEqual({ kind: "zoom" });
    expect(key("Z", { shiftKey: true })).toEqual({ kind: "zoom" });
  });

  it("leaves the browser's shortcuts and other keys alone", () => {
    expect(key("5", { ctrlKey: true })).toBeUndefined();
    expect(key("s", { metaKey: true })).toBeUndefined();
    expect(key("a")).toBeUndefined();
  });
});

describe("scores", () => {
  it("reads 0 to 5 with at most two decimals as hundredths", () => {
    expect(parseScore("3.47")).toBe(347);
    expect(parseScore(" 5 ")).toBe(500);
    expect(parseScore("0.1")).toBe(10);
    expect(parseScore("5.01")).toBeUndefined();
    expect(parseScore("2.345")).toBeUndefined();
    expect(parseScore("-1")).toBeUndefined();
    expect(formatScore(347)).toBe("3.47");
    expect(formatScore(0)).toBe("0.00");
  });

  it("stacks thumbnails that would overlap and keeps apart ones that do not", () => {
    const width = 1000;
    expect(
      lanes(
        [
          { at: 0.1, width: 60 },
          { at: 0.9, width: 60 },
        ],
        width,
      ),
    ).toEqual([0, 0]);
    expect(
      lanes(
        [
          { at: 0.5, width: 60 },
          { at: 0.52, width: 60 },
          { at: 0.51, width: 60 },
        ],
        width,
      ),
    ).toEqual([0, 1, 2]);
    expect(
      lanes(
        Array.from({ length: 4 }, () => ({ at: 0.5, width: 60 })),
        width,
      ),
    ).toHaveLength(4);
  });
});

describe("numbers", () => {
  it("shows what is missing as a dash and rounds the rest", () => {
    expect(formatCorrelation(null)).toBe("—");
    expect(formatCorrelation(0.7149)).toBe("0.71");
    expect(formatSeconds(3420)).toBe("3.4 秒");
    expect(share(3, 0)).toBe(0);
    expect(share(5, 4)).toBe(1);
  });
});

describe("the time on a photo", () => {
  it("runs while the photo is in view and stands still while the page is hidden or a sheet covers it", () => {
    const watch = new Stopwatch();
    watch.start(1_000, true);
    watch.pause(4_000);
    watch.pause(5_000);
    expect(watch.elapsed(9_000)).toBe(3_000);
    watch.resume(9_000);
    watch.resume(9_500);
    expect(watch.elapsed(10_000)).toBe(4_000);
    watch.start(20_000, false);
    expect(watch.elapsed(30_000)).toBe(0);
  });
});

describe("the photo at 100%", () => {
  // Where a CSS matrix sends a point of the stored picture.
  const apply = ([a, b, c, d, e, f]: number[], x: number, y: number) => [a * x + c * y + e, b * x + d * y + f];

  it("turns every EXIF orientation upright, as browsers do", () => {
    const width = 4;
    const height = 3;
    // The stored picture's top-left corner, upright: where each orientation's
    // row 0 and column 0 belong.
    const topLeft: Record<number, number[]> = {
      1: [0, 0],
      2: [4, 0],
      3: [4, 3],
      4: [0, 3],
      5: [0, 0],
      6: [3, 0],
      7: [3, 4],
      8: [0, 4],
    };
    for (let orientation = 1; orientation <= 8; orientation++) {
      const matrix = orientationMatrix(orientation, width, height);
      const upright = uprightSize(orientation, width, height);
      expect(apply(matrix, 0, 0)).toEqual(topLeft[orientation]);
      // The whole picture lands inside the upright box.
      for (const [x, y] of [
        [0, 0],
        [width, 0],
        [0, height],
        [width, height],
      ]) {
        const [ux, uy] = apply(matrix, x, y);
        expect(ux).toBeGreaterThanOrEqual(0);
        expect(ux).toBeLessThanOrEqual(upright.width);
        expect(uy).toBeGreaterThanOrEqual(0);
        expect(uy).toBeLessThanOrEqual(upright.height);
      }
    }
    expect(uprightSize(6, 4000, 3000)).toEqual({ width: 3000, height: 4000 });
  });
});

describe("the export's columns", () => {
  it("has the photo and the rater first, then who, the 打分项 and the rest in a fixed order", () => {
    expect(
      exportColumns(["duration", "composition", "overall", "nickname", "image_size"], ["overall", "composition"]),
    ).toEqual([
      "image_id",
      "image_path",
      "rater",
      "nickname",
      "image_width",
      "image_height",
      "overall",
      "composition",
      "duration_s",
    ]);
    expect(exportColumns([], ["overall"])).toEqual(["image_id", "image_path", "rater"]);
  });
});

describe("retryable", () => {
  it("sends an answer again after a network failure or a service outage, never after a refusal", () => {
    expect(retryable(new TypeError("offline"))).toBe(true);
    expect(retryable(new APIError(0, "network_error"))).toBe(true);
    expect(retryable(new APIError(503, "module_unavailable"))).toBe(true);
    expect(retryable(new APIError(409, "already_rated"))).toBe(false);
    expect(retryable(new APIError(403, "not_rater"))).toBe(false);
  });
});
