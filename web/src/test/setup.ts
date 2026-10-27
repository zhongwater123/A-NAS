import "@testing-library/jest-dom/vitest";
import { cleanup, configure } from "@testing-library/react";
import { afterAll, afterEach, beforeAll } from "vitest";

import { rememberRequestBodies } from "./api";
import { pathOf, server } from "./server";

// Until every test answers through MSW, stubAPI still reads the bodies it was given.
rememberRequestBodies();

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
afterEach(() => {
  cleanup();
  server.resetHandlers();
  const missed = unhandled.splice(0);
  if (missed.length) throw new Error(`No handler answered ${missed.join(", ")}`);
});
afterAll(() => server.close());

Object.defineProperty(document, "hidden", {
  configurable: true,
  value: false,
});
