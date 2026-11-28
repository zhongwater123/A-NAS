import { describe, expect, it } from "vitest";

import { clock, duration } from "./time";

describe("times in the film", () => {
  it("shows minutes and seconds, and lengths in seconds", () => {
    expect([clock(0), clock(4), clock(75.9)]).toEqual(["0:00", "0:04", "1:15"]);
    expect([duration(2), duration(1.5)]).toEqual(["2秒", "1.5秒"]);
  });
});
