import { type Mock, vi } from "vitest";

// accepting answers any method call with a mock, so drawing code can run
// against it; a gradient it hands out takes colour stops.
const accepting = () =>
  new Proxy({} as Record<PropertyKey, unknown>, {
    get: (target, key) => (key in target ? target[key] : (target[key] = vi.fn(() => ({ addColorStop: vi.fn() })))),
  });

// drawableCanvas gives every canvas a 2D context that accepts any drawing
// call, and stubs jsdom's missing Path2D, so animation loops still run. It
// returns the context's clearRect, which each frame of those loops calls.
// Undo the stub with vi.unstubAllGlobals.
export function drawableCanvas(): Mock {
  const context = accepting();
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue(context as never);
  vi.stubGlobal("Path2D", function Path2D() {
    return accepting();
  });
  return context.clearRect as Mock;
}

// animationFrames holds requestAnimationFrame callbacks for the test to run.
export function animationFrames() {
  const queued: FrameRequestCallback[] = [];
  vi.spyOn(window, "requestAnimationFrame").mockImplementation((callback) => queued.push(callback));
  const cancelled = vi.spyOn(window, "cancelAnimationFrame").mockImplementation(() => undefined);
  return { queued, cancelled };
}
