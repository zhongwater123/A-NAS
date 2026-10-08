import { afterEach, describe, expect, it, vi } from "vitest";

import { BOOT_TIMELINE, TAGLINE, crtParams, startBootIdent, taglineGlyphs } from "./bootIdent";

afterEach(() => vi.restoreAllMocks());

describe("boot ident timeline", () => {
  it("powers on from a beam through static to a steady picture", () => {
    const warming = crtParams(.05, 0);
    expect(warming.open).toBeLessThan(.01);
    expect(warming.beam).toBeGreaterThan(0);
    expect(crtParams(.3, 0).stat).toBeGreaterThan(.5);
    expect(crtParams(4.5, 0)).toMatchObject({ open: 1, openX: 1, beam: 0, stat: 0, roll: -2 });
  });

  it("switches off to a line, then a dot, and is dark before the overlay fades", () => {
    const line = crtParams(BOOT_TIMELINE.exit + .16, 0);
    expect(line.open).toBeLessThan(.01);
    expect(line.openX).toBe(1);
    expect(line.beam).toBe(1);
    expect(crtParams(BOOT_TIMELINE.exit + .4, 0).openX).toBeLessThan(.01);
    expect(crtParams(BOOT_TIMELINE.fadeStart, 0).beam).toBe(0);
  });

  it("has a glyph for every tagline character", () => {
    for (const ch of TAGLINE) expect(taglineGlyphs[ch]).toBeDefined();
  });

  it("does not start when the browser cannot draw on a canvas", () => {
    vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockImplementation(() => null);
    expect(startBootIdent(document.createElement("canvas"), { onFade: vi.fn(), onDone: vi.fn() })).toBeNull();
  });
});
