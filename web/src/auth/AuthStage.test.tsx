import { act, render } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { animationFrames, drawableCanvas } from "../test/canvas";
import { AuthStage } from "./AuthStage";

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("sign-in stage", () => {
  it("stops both canvases while the stage is not rendered", () => {
    drawableCanvas();
    const frames = animationFrames();
    let shown: (visible: boolean) => void = () => undefined;
    vi.stubGlobal(
      "IntersectionObserver",
      class {
        constructor(report: IntersectionObserverCallback) {
          shown = (visible) =>
            report([{ isIntersecting: visible } as IntersectionObserverEntry], this as unknown as IntersectionObserver);
        }
        observe() {}
        disconnect() {}
      },
    );
    render(<AuthStage>form</AuthStage>);
    // The meteor sky and the ident lockup.
    expect(frames.queued).toHaveLength(2);

    // Under the local console's screen saver or black screen.
    act(() => shown(false));
    expect(frames.cancelled).toHaveBeenCalledTimes(2);
    for (const frame of frames.queued.splice(0)) frame(16);
    expect(frames.queued).toHaveLength(0);

    act(() => shown(true));
    expect(frames.queued).toHaveLength(2);
  });
});
