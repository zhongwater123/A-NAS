import { calendarMoment } from "../lib/time";

// Shared by task cards and desktop notices, without loading cronstrue.
export function whenText(seconds: number, now = new Date()): string {
  return calendarMoment(seconds * 1000, now);
}
