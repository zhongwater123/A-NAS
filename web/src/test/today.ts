import { afterAll, beforeEach, vi } from "vitest";

/**
 * The day a test file that calls fixToday() runs on. Noon in local time, so
 * the calendar date is the same in every time zone and an hour or two either
 * way stays on that day.
 */
export const TODAY = new Date(2026, 9, 12, 12, 0, 0);

/**
 * fixToday makes `new Date()` and `Date.now()` answer TODAY for the rest of a
 * test file, so a test that reads "3 天前" or "今天 23:30" off the page gives
 * the same result whenever it runs (#426). Timers stay real: only the date is
 * set, and it does not advance.
 *
 * Call it at the top of the file, before any fixture that reads the clock. A
 * file that uses fake timers sets its own time instead.
 */
export function fixToday(): void {
  vi.setSystemTime(TODAY);
  beforeEach(() => {
    vi.setSystemTime(TODAY);
  });
  afterAll(() => {
    vi.useRealTimers();
  });
}
