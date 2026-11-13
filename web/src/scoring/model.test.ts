import { describe, expect, it } from "vitest";

import { APIError } from "../api/client";
import { retryable } from "./api";
import { formatCorrelation, formatScore, formatSeconds, lanes, parseScore, share, stageAction } from "./model";

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

describe("retryable", () => {
  it("sends an answer again after a network failure or a service outage, never after a refusal", () => {
    expect(retryable(new TypeError("offline"))).toBe(true);
    expect(retryable(new APIError(0, "network_error"))).toBe(true);
    expect(retryable(new APIError(503, "module_unavailable"))).toBe(true);
    expect(retryable(new APIError(409, "already_rated"))).toBe(false);
    expect(retryable(new APIError(403, "not_rater"))).toBe(false);
  });
});
