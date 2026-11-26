import { describe, expect, it } from "vitest";

import { withinArea } from "./placement";

const page = { width: 1366, height: 820 };

describe("a remembered place, brought back on the page", () => {
  it("keeps it where it was when it is on the page, and brings in what hangs off it or lies under the rail", () => {
    expect(withinArea({ rx: 0.5, ry: 0.5 }, 240, page)).toEqual({ rx: 0.5, ry: 0.5 });
    // From the bottom right corner: the hit area's right and bottom 8 px inside the page.
    const corner = withinArea({ rx: 1, ry: 1 }, 240, page);
    expect(corner.rx * page.width - 120 + 165).toBeCloseTo(page.width - 8, 6);
    // From the top left, under the rail: out to its right.
    const rail = withinArea({ rx: 0, ry: 0 }, 240, page);
    expect(rail.rx * page.width - 120 + 75).toBeCloseTo(88, 6);
  });
});
