import { afterEach, describe, expect, it, vi } from "vitest";

import { animationFrames, drawableCanvas } from "../test/canvas";
import {
  BOOT_TIMELINE,
  crtParams,
  lockupTime,
  startBootIdent,
  startIdentLockup,
  TAGLINE,
  taglineGlyphs,
} from "./bootIdent";

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("boot ident timeline", () => {
  it("powers on from a beam through static to a steady picture", () => {
    const warming = crtParams(0.05, 0);
    expect(warming.open).toBeLessThan(0.01);
    expect(warming.beam).toBeGreaterThan(0);
    expect(crtParams(0.3, 0).stat).toBeGreaterThan(0.5);
    expect(crtParams(4.5, 0)).toMatchObject({ open: 1, openX: 1, beam: 0, stat: 0, roll: -2 });
  });

  it("switches off to a line, then a dot, and is dark before the overlay fades", () => {
    const line = crtParams(BOOT_TIMELINE.exit + 0.16, 0);
    expect(line.open).toBeLessThan(0.01);
    expect(line.openX).toBe(1);
    expect(line.beam).toBe(1);
    expect(crtParams(BOOT_TIMELINE.exit + 0.4, 0).openX).toBeLessThan(0.01);
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

describe("sign-in lockup", () => {
  it("locks the name in once, then rests and ends each idle cycle with a glint", () => {
    expect(lockupTime(0)).toBeLessThan(2.25);
    expect(lockupTime(1)).toBeCloseTo(3.2);
    expect(lockupTime(5)).toBe(4.7);
    expect(lockupTime(2.5 + 11 - 0.5)).toBeCloseTo(4.2);
    expect(lockupTime(2.5 + 11 + 1)).toBe(4.7);
  });

  it("does not start when the browser cannot draw on a canvas", () => {
    vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockImplementation(() => null);
    expect(startIdentLockup(document.createElement("canvas"), () => false)).toBeNull();
  });

  it("draws nothing while paused and carries on when resumed", () => {
    const clears = drawableCanvas();
    const frames = animationFrames();
    const lockup = startIdentLockup(document.createElement("canvas"), () => false);
    expect(lockup).not.toBeNull();
    expect(frames.queued).toHaveLength(1);

    lockup?.pause();
    expect(frames.cancelled).toHaveBeenCalledWith(1);
    // A frame the browser had already handed out draws nothing and asks for no other.
    const drawn = clears.mock.calls.length;
    frames.queued[0](16);
    expect(clears.mock.calls.length).toBe(drawn);
    expect(frames.queued).toHaveLength(1);

    lockup?.resume();
    frames.queued[1](32);
    expect(clears.mock.calls.length).toBeGreaterThan(drawn);
    expect(frames.queued).toHaveLength(3);
    lockup?.stop();
  });
});
