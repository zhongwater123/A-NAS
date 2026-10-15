import { describe, expect, it } from "vitest";

import { toCSV } from "./common";

describe("activity export", () => {
  it("keeps cells a spreadsheet would run as formulas as text", () => {
    expect(toCSV([["=HYPERLINK(\"x\")", "+1", "-1", "@SUM(A1)", "小爱", "a=b"]]))
      .toBe("﻿\"'=HYPERLINK(\"\"x\"\")\",\"'+1\",\"'-1\",\"'@SUM(A1)\",\"小爱\",\"a=b\"\r\n");
  });
});
