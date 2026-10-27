import { calendarDays, calendarDate, calendarMoment, formatDate, relativeTime } from "../format/time";

export function describeTime(iso: string, now = new Date()): string {
  const elapsed = Math.abs(now.getTime() - new Date(iso).getTime());
  if (elapsed < 3_600_000) return relativeTime(iso, now);
  if (Math.abs(calendarDays(iso, now)) <= 1) return calendarMoment(iso, now);
  return calendarDate(iso, now);
}
export const fullTime = (iso: string) => formatDate(iso, { dateStyle: "short", timeStyle: "medium", hourCycle: "h23" });
