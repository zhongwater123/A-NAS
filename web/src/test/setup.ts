import "@testing-library/jest-dom/vitest";
import { cleanup, configure } from "@testing-library/react";
import { afterEach } from "vitest";

// findBy… and waitFor wait 1 s by default, which a loaded machine misses while
// the element is still on its way. An element that never comes still fails
// the test, 5 s later.
configure({ asyncUtilTimeout: 5_000 });

afterEach(cleanup);

Object.defineProperty(document, "hidden", {
  configurable: true,
  value: false,
});
