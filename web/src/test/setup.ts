import "@testing-library/jest-dom/vitest";
import { cleanup, configure } from "@testing-library/react";
import { afterAll, afterEach, beforeAll, vi } from "vitest";

import { pathOf, server } from "./server";

// findBy… and waitFor wait 1 s by default, which a loaded machine misses while
// the element is still on its way. An element that never comes still fails
// the test, 5 s later.
configure({ asyncUtilTimeout: 5_000 });

// A request that no handler answers fails the test that sent it. MSW's "error"
// strategy (onUnhandledFrame in MSW 3, onUnhandledRequest before) fails only
// the request, which the page may catch and show as any other failure.
const unhandled: string[] = [];
beforeAll(() =>
  server.listen({
    onUnhandledFrame({ frame, defaults }) {
      const { request } = frame.data as { request?: Request };
      unhandled.push(request ? `${request.method} ${pathOf(request)}` : frame.protocol);
      return defaults.error();
    },
  }),
);

// Requests MSW has not yet matched to a handler. A page still sends a few as
// a test ends (a refresh after its last step, a player saving as it closes);
// they are the test's own and must reach its handlers, not the next test's.
const resolving = new Set<string>();
server.events.on("request:start", ({ requestId }) => resolving.add(requestId));
server.events.on("request:match", ({ requestId }) => resolving.delete(requestId));
server.events.on("request:unhandled", ({ requestId }) => resolving.delete(requestId));
server.events.on("request:end", ({ requestId }) => resolving.delete(requestId));

afterEach(async () => {
  cleanup();
  // MSW answers on real timers; a test's fake ones end with the test.
  if (vi.isFakeTimers()) vi.useRealTimers();
  // Let the requests already sent reach MSW and find their handlers, for at most a second.
  for (let waited = 0; waited === 0 || (resolving.size > 0 && waited < 100); waited++) {
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  resolving.clear();
  server.resetHandlers();
  const missed = unhandled.splice(0);
  if (missed.length) throw new Error(`No handler answered ${missed.join(", ")}`);
});
afterAll(() => server.close());

Object.defineProperty(document, "hidden", {
  configurable: true,
  value: false,
});

// jsdom captures no pointer (jsdom 30), and windows follow a drag by pointer
// capture (desktop/AppWindow.tsx). Here a capture ends as a browser ends it:
// the press is released or cancelled anywhere, then the element hears that it
// lost the pointer. Moves are not redirected; tests send them to the element
// they pressed.
if (!Element.prototype.setPointerCapture) {
  Element.prototype.setPointerCapture = function (this: Element) {
    const end = () => {
      document.removeEventListener("pointerup", end, true);
      document.removeEventListener("pointercancel", end, true);
      // After the release has reached every listener.
      queueMicrotask(() => this.dispatchEvent(new Event("lostpointercapture")));
    };
    document.addEventListener("pointerup", end, true);
    document.addEventListener("pointercancel", end, true);
  };
}
