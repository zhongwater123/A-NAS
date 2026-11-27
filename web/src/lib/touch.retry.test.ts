import { afterEach, expect, it, vi } from "vitest";

import { installTouchInput } from "./touch";

// The long press's chunk fails to download the first time, as on a phone
// whose network drops, and arrives the second.
let attempts = 0;
vi.mock("./longPress", async (importOriginal) => {
  attempts += 1;
  if (attempts === 1) throw new Error("network down");
  return importOriginal();
});

// A one-finger touchstart; jsdom has no Touch constructor.
const touchstart = () => {
  const event = new Event("touchstart", { bubbles: true });
  Object.defineProperty(event, "touches", { value: [{ clientX: 0, clientY: 0 }] });
  document.body.dispatchEvent(event);
};

afterEach(() => {
  Reflect.deleteProperty(navigator, "userAgent");
});

it("asks for the long press again on the next touch when it did not load", async () => {
  vi.spyOn(console, "warn").mockImplementation(() => {});
  Object.defineProperty(navigator, "userAgent", {
    value: "Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X)",
    configurable: true,
  });
  const uninstall = installTouchInput();
  await vi.dynamicImportSettled();
  expect(attempts).toBe(1);
  expect(console.warn).toHaveBeenCalledTimes(1);

  touchstart();
  await vi.dynamicImportSettled();
  expect(attempts).toBe(2);

  // Loaded now: later touches ask for nothing more.
  touchstart();
  await vi.dynamicImportSettled();
  expect(attempts).toBe(2);
  uninstall();
});
