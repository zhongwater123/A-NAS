import { describe, expect, it } from "vitest";

import { readFromFrame, readToFrame } from "./protocol";
import samples from "./samples.json";

// The desktop and the frame helper read the same samples (samples.json): what
// one side sends is what the other keeps, and nothing else gets through.
describe("the frame's messages", () => {
  it.each(samples.fromFrame)("the desktop reads $name", ({ data, read }) => {
    expect(readFromFrame(data)).toEqual(read ?? undefined);
  });

  it.each(samples.toFrame)("the helper reads $name", ({ data, read }) => {
    expect(readToFrame(data)).toEqual(read ?? undefined);
  });

  it("cuts what a page could stuff into a pick", () => {
    const read = readFromFrame({
      type: "anas:picked",
      at: 10,
      generated: false,
      tag: "div",
      text: "字".repeat(5000),
      dom: "<div>".repeat(5000),
      rect: { x: 0, y: 0, w: 1, h: 1 },
      style: Object.fromEntries(Array.from({ length: 50 }, (_, index) => [`p${index}`, "x"])),
      rules: Array.from({ length: 50 }, (_, index) => `.r${index}`),
    });
    expect(read?.type).toBe("anas:picked");
    if (read?.type !== "anas:picked") return;
    expect(read.text).toHaveLength(2000);
    expect(read.dom).toHaveLength(8000);
    expect(Object.keys(read.style)).toHaveLength(20);
    expect(read.rules).toHaveLength(20);
  });
});
