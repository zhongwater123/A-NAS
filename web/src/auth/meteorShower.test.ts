import { afterEach, describe, expect, it, vi } from "vitest";

import { startMeteorShower } from "./meteorShower";

afterEach(() => vi.restoreAllMocks());

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
});
