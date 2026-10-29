import { errorCodeText } from "../api/errorMessages";
import type { Space } from "../api.generated";
import { calendarLabel, calendarMoment, relativeTime, relativeUnit } from "../lib/time";
import type { Frequency, RetentionTiers, ScheduleSettings, Snapshot, SnapshotSchedule } from "./api";

const weekdays = ["周日", "周一", "周二", "周三", "周四", "周五", "周六"];
export const weekdayShort = ["日", "一", "二", "三", "四", "五", "六"];

export const formatCount = (count: number) => count.toLocaleString("zh-CN");

export { formatClock } from "../lib/time";

// formatDay names a calendar day: 今天, 昨天, 10月10日 周六, or with the year
// when it is not this year.
export function formatDay(value: string | Date, now = new Date()): string {
  return calendarLabel(value, now, new Date(value).getFullYear() === now.getFullYear() ? "short" : undefined);
}

// formatShortDay names a day without its weekday, for narrow places.
export function formatShortDay(value: string | Date, now = new Date()): string {
  return calendarLabel(value, now);
}

export function formatDateTime(value: string | Date, now = new Date()): string {
  return calendarMoment(value, now);
}

export function formatRelative(value: string | Date, now = new Date()): string {
  return relativeTime(value, now, { withTime: true });
}

// formatUntil words a future time: 12 分钟后, 今天 15:00, 明天 03:00 …
export function formatUntil(value: string | Date, now = new Date()): string {
  const seconds = (new Date(value).getTime() - now.getTime()) / 1000;
  if (seconds < 90) return "即将开始";
  if (seconds < 3600) return relativeUnit(Math.round(seconds / 60), "minute");
  return formatDateTime(value, now);
}

export function describeSchedule(settings: Pick<ScheduleSettings, "frequency" | "time" | "weekday">): string {
  const minute = settings.time.slice(3);
  switch (settings.frequency) {
    case "hourly":
      return minute === "00" ? "每小时整点" : `每小时第 ${Number(minute)} 分`;
    case "weekly":
      return `每${weekdays[settings.weekday]} ${settings.time}`;
    default:
      return `每天 ${settings.time}`;
  }
}

const spanUnit: Record<Frequency, string> = { hourly: "小时", daily: "天", weekly: "周" };

export type Tier = keyof RetentionTiers;
export const tierOrder: Tier[] = ["hourly", "daily", "weekly", "monthly"];
export const tierLabel: Record<Tier, { every: string; span: string }> = {
  hourly: { every: "每小时", span: "小时" },
  daily: { every: "每天", span: "天" },
  weekly: { every: "每周", span: "周" },
  monthly: { every: "每月", span: "个月" },
};
export const noTiers: RetentionTiers = { hourly: 0, daily: 0, weekly: 0, monthly: 0 };

// tiersFor names the tiers a frequency can use: none finer than it.
export const tiersFor = (frequency: Frequency): Tier[] =>
  frequency === "hourly" ? tierOrder : frequency === "daily" ? tierOrder.slice(1) : tierOrder.slice(2);

// What tiered retention suggests: a day of hours, two weeks of days, two
// months of weeks and a year of months, from the frequency down.
export function suggestedTiers(frequency: Frequency): RetentionTiers {
  const all: RetentionTiers = { hourly: 24, daily: 14, weekly: 8, monthly: 12 };
  const usable = tiersFor(frequency);
  return { hourly: 0, daily: 0, weekly: 0, monthly: 0, ...Object.fromEntries(usable.map((tier) => [tier, all[tier]])) };
}

export const tierTotal = (tiers: RetentionTiers) => tiers.hourly + tiers.daily + tiers.weekly + tiers.monthly;

// describeRetention says how far back the kept scheduled snapshots reach.
export function describeRetention(
  settings: Pick<ScheduleSettings, "frequency" | "keep" | "retention" | "tiers">,
): string {
  if (settings.retention !== "tiered") return `可回到最近 ${settings.keep} ${spanUnit[settings.frequency]}内的任一份`;
  const widest = [...tierOrder].reverse().find((tier) => settings.tiers[tier] > 0);
  if (!widest) return "没有保留任何定时快照";
  return `越近越密，最远可回到约 ${settings.tiers[widest]} ${tierLabel[widest].span}前，最多 ${tierTotal(settings.tiers)} 份`;
}

export const spaceLabel = (space: Pick<Space, "kind">) => (space.kind === "shared" ? "共享空间" : "个人空间");
export const spaceAudience = (space: Pick<Space, "kind">) => (space.kind === "shared" ? "所有成员可见" : "只有你可见");

export const originLabel = (snapshot: Pick<Snapshot, "origin">) => (snapshot.origin === "scheduled" ? "定时" : "手动");

export function runErrorText(schedule: SnapshotSchedule): string {
  switch (schedule.lastRun?.error) {
    case "volume_unavailable":
      return errorCodeText("volume_unavailable")!;
    case "space_unavailable":
      return "空间已不存在";
    default:
      return "快照创建失败";
  }
}

// defaultSnapshotName names a manual snapshot after the minute it is taken.
export function defaultSnapshotName(now = new Date()): string {
  const pad = (value: number) => String(value).padStart(2, "0");
  return `手动快照 ${now.getFullYear()}-${pad(now.getMonth() + 1)}-${pad(now.getDate())} ${pad(now.getHours())}:${pad(now.getMinutes())}`;
}

const stamp = (value: string | Date) => {
  const date = new Date(value);
  const pad = (number: number) => String(number).padStart(2, "0");
  return `${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}.${pad(date.getMinutes())}`;
};

// restoredSnapshotName names the folder a whole snapshot is restored into:
// 定时快照 10-12 03.00. Like restoredName it avoids ":".
export const restoredSnapshotName = (title: string, createdAt: string) => `${title} ${stamp(createdAt)}`;

// restoredName suggests a name beside an existing one: 报告 (恢复 10-12 14.30).docx.
// It avoids ":", which Windows cannot show over SMB.
export function restoredName(name: string, kind: "file" | "directory", now = new Date()): string {
  const suffix = ` (恢复 ${stamp(now)})`;
  const dot = name.lastIndexOf(".");
  if (kind === "directory" || dot <= 0) return `${name}${suffix}`;
  return `${name.slice(0, dot)}${suffix}${name.slice(dot)}`;
}
