import { afterEach, describe, expect, it, vi } from "vitest";

import { animationFrames, drawableCanvas } from "../test/canvas";
import { startMeteorShower } from "./meteorShower";

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("sign-in meteor shower", () => {
  it("does not start when the browser cannot draw on a canvas", () => {
    vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockImplementation(() => null);
    expect(startMeteorShower(document.createElement("canvas"))).toBeNull();
  });

  it("gives up quietly when drawing fails instead of breaking the sign-in screen", () => {
    vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue({ fillRect: vi.fn() } as never);
    const frame = vi.spyOn(window, "requestAnimationFrame");
    expect(startMeteorShower(document.createElement("canvas"))).toBeNull();
    expect(frame).not.toHaveBeenCalled();
  });

  it("draws nothing while paused and carries on when resumed", () => {
    const clears = drawableCanvas();
    const frames = animationFrames();
    const shower = startMeteorShower(document.createElement("canvas"));
    expect(shower).not.toBeNull();
    expect(frames.queued).toHaveLength(1);

    shower?.pause();
    expect(frames.cancelled).toHaveBeenCalledWith(1);
    // A frame the browser had already handed out draws nothing and asks for no other.
    const drawn = clears.mock.calls.length;
    frames.queued[0](16);
    expect(clears.mock.calls.length).toBe(drawn);
    expect(frames.queued).toHaveLength(1);

    shower?.resume();
    frames.queued[1](32);
    expect(clears.mock.calls.length).toBeGreaterThan(drawn);
    expect(frames.queued).toHaveLength(3);
    shower?.stop();
  });
});
