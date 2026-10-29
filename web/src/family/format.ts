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

// addMonths moves a date-only value by whole months, keeping the day but
// clamping it to the month's end, as the server counts months of life.
export function addMonths(value: string, months: number): Date {
  const date = dateOnly(value);
  const first = new Date(date.getFullYear(), date.getMonth() + months, 1);
  const last = new Date(first.getFullYear(), first.getMonth() + 1, 0).getDate();
  return new Date(first.getFullYear(), first.getMonth(), Math.min(date.getDate(), last));
}

// monthRange is the first and last day of a month of life: “10月15日” and
// “11月14日”.
export function monthRange(birthDate: string, monthOfLife: number): [string, string] {
  const start = addMonths(birthDate, monthOfLife);
  const end = addMonths(birthDate, monthOfLife + 1);
  end.setDate(end.getDate() - 1);
  const day = (date: Date) => formatDate(date, { month: "long", day: "numeric" });
  return [day(start), day(end)];
}

export interface Band {
  lower?: string;
  upper?: string;
}

// assess places a value among a growth standard's percentile curves at an
// age in months, as the server does; undefined outside the standard's ages.
export function assess(
  curve: Array<{ ageMonths: number; values: number[] }>,
  percentiles: string[],
  ageMonths: number,
  value: number,
): Band | undefined {
  if (curve.length === 0 || ageMonths < curve[0].ageMonths || ageMonths > curve[curve.length - 1].ageMonths)
    return undefined;
  const after = curve.findIndex((point) => point.ageMonths >= ageMonths);
  const right = curve[after];
  const left = curve[Math.max(0, after - 1)];
  const share =
    right.ageMonths === left.ageMonths ? 0 : (ageMonths - left.ageMonths) / (right.ageMonths - left.ageMonths);
  const values = left.values.map((low, index) => low + share * (right.values[index] - low));
  const above = values.findIndex((reference) => value < reference);
  if (above === -1) return { lower: percentiles[percentiles.length - 1] };
  return { lower: above > 0 ? percentiles[above - 1] : undefined, upper: percentiles[above] };
}

// bandShort is the band in a few characters: “P25—P50”, “< P3”, “> P97”;
// empty outside the standard's ages.
export function bandShort(band: Band): string {
  if (band.lower && band.upper) return `${band.lower}—${band.upper}`;
  if (band.upper) return `< ${band.upper}`;
  return band.lower ? `> ${band.lower}` : "";
}

// sealText fits an automatic milestone's title into its round seal: “百天”,
// “周岁”, “2岁”.
export function sealText(title: string): string {
  return title.replace(/\s*岁生日$/, "岁").slice(0, 3);
}

// verbs say what an author did, after their name: “外婆 写了日志”.
export const verbs = {
  photos: "添加了照片",
  recording: "录了一段",
  journal: "写了日志",
  measurement: "量了身高体重",
  vaccine: "记下接种",
  milestone: "记下大事",
} as const;
