// How 家庭中心 says ages, dates and growth, the way Chinese families do.
import type { FamilyAge } from "../api.generated";
import { formatDate } from "../lib/time";

// ageText is “3 个月 12 天”, “1 岁 2 个月”, “出生当天”.
export function ageText(age: FamilyAge, withDays = true): string {
  const parts: string[] = [];
  if (age.years) parts.push(`${age.years} 岁`);
  if (age.months) parts.push(`${age.months} 个月`);
  if (withDays && age.days && age.years < 3) parts.push(`${age.days} 天`);
  if (parts.length === 0) return age.days ? `${age.days} 天` : "出生当天";
  return parts.join(" ");
}

// monthLabel names a group of the cloud album by the month of life.
export function monthLabel(monthOfLife: number): string {
  if (monthOfLife < 0) return "出生前";
  if (monthOfLife === 0) return "第一个月";
  const years = Math.floor(monthOfLife / 12);
  const months = monthOfLife % 12;
  if (!years) return `${months} 个月`;
  return months ? `${years} 岁 ${months} 个月` : `${years} 岁`;
}

// The local calendar date of a date-only value, without a time zone shift.
export function dateOnly(value: string): Date {
  const [year, month, day] = value.split("-").map(Number);
  return new Date(year, month - 1, day);
}

export function today(): string {
  return isoDate(new Date());
}

export function isoDate(date: Date): string {
  const pad = (value: number) => String(value).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
}

export function longDate(value: string): string {
  return formatDate(dateOnly(value), { year: "numeric", month: "long", day: "numeric" });
}

export function shortDate(value: string): string {
  const date = dateOnly(value);
  const sameYear = date.getFullYear() === new Date().getFullYear();
  return formatDate(
    date,
    sameYear ? { month: "long", day: "numeric" } : { year: "numeric", month: "long", day: "numeric" },
  );
}

// daysUntil counts calendar days from today to a date-only value.
export function daysUntil(value: string): number {
  const now = new Date();
  const start = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  return Math.round((dateOnly(value).getTime() - start.getTime()) / 86_400_000);
}

// bandText says where a value lies among the standard's percentiles.
export function bandText(lower?: string, upper?: string): string {
  if (lower && upper) return `在 ${lower}—${upper} 之间`;
  if (upper) return `低于 ${upper}`;
  if (lower) return `高于 ${lower}`;
  return "超出标准的年龄范围";
}

// bandTone flags values outside P3—P97 for a closer look; it is not a
// diagnosis.
export function bandTone(lower?: string, upper?: string): "normal" | "watch" {
  return (!lower && upper === "P3") || (lower === "P97" && !upper) ? "watch" : "normal";
}

export const kindLabels = {
  photos: "照片视频",
  recording: "录音",
  journal: "日志",
  measurement: "身高体重",
  vaccine: "疫苗接种",
  milestone: "大事记",
} as const;

export function decimal(value: number, digits: number): string {
  return value.toFixed(digits);
}
