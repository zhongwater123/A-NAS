import type { Space } from "../api";

import type { Frequency, ScheduleSettings, Snapshot, SnapshotSchedule } from "./backupApi";

const weekdays = ["周日", "周一", "周二", "周三", "周四", "周五", "周六"];
export const weekdayShort = ["日", "一", "二", "三", "四", "五", "六"];

export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return "—";
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let value = bytes / 1024;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value < 10 ? value.toFixed(1) : Math.round(value)} ${units[unit]}`;
}

export const formatCount = (count: number) => count.toLocaleString("zh-CN");

const startOfDay = (date: Date) => new Date(date.getFullYear(), date.getMonth(), date.getDate());
const dayDifference = (date: Date, now: Date) => Math.round((startOfDay(now).getTime() - startOfDay(date).getTime()) / 86_400_000);

export const formatClock = (value: string | Date) =>
  new Date(value).toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit", hour12: false });

// formatDay names a calendar day: 今天, 昨天, 10月10日 周六, or with the year
// when it is not this year.
export function formatDay(value: string | Date, now = new Date()): string {
  const date = new Date(value);
  const days = dayDifference(date, now);
  if (days === 0) return "今天";
  if (days === 1) return "昨天";
  if (days === -1) return "明天";
  const month = `${date.getMonth() + 1}月${date.getDate()}日`;
  return date.getFullYear() === now.getFullYear() ? `${month} ${weekdays[date.getDay()]}` : `${date.getFullYear()}年${month}`;
}

export function formatDateTime(value: string | Date, now = new Date()): string {
  const date = new Date(value);
  const days = dayDifference(date, now);
  const day = Math.abs(days) <= 1 ? formatDay(date, now) : date.getFullYear() === now.getFullYear()
    ? `${date.getMonth() + 1}月${date.getDate()}日` : `${date.getFullYear()}年${date.getMonth() + 1}月${date.getDate()}日`;
  return `${day} ${formatClock(date)}`;
}

export function formatRelative(value: string | Date, now = new Date()): string {
  const seconds = (now.getTime() - new Date(value).getTime()) / 1000;
  if (seconds < 60) return "刚刚";
  if (seconds < 3600) return `${Math.floor(seconds / 60)} 分钟前`;
  if (seconds < 86_400) return `${Math.floor(seconds / 3600)} 小时前`;
  const days = dayDifference(new Date(value), now);
  if (days < 30) return `${days} 天前`;
  return formatDateTime(value, now);
}

// formatUntil words a future time: 12 分钟后, 今天 15:00, 明天 03:00 …
export function formatUntil(value: string | Date, now = new Date()): string {
  const seconds = (new Date(value).getTime() - now.getTime()) / 1000;
  if (seconds < 90) return "即将开始";
  if (seconds < 3600) return `${Math.round(seconds / 60)} 分钟后`;
  return formatDateTime(value, now);
}

export function describeSchedule(settings: Pick<ScheduleSettings, "frequency" | "time" | "weekday">): string {
  const minute = settings.time.slice(3);
  switch (settings.frequency) {
    case "hourly": return minute === "00" ? "每小时整点" : `每小时第 ${Number(minute)} 分`;
    case "weekly": return `每${weekdays[settings.weekday]} ${settings.time}`;
    default: return `每天 ${settings.time}`;
  }
}

const spanUnit: Record<Frequency, string> = { hourly: "小时", daily: "天", weekly: "周" };

// describeRetention says how far back the kept scheduled snapshots reach.
export const describeRetention = (frequency: Frequency, keep: number) => `可回到最近 ${keep} ${spanUnit[frequency]}内的任一份`;

export const spaceLabel = (space: Pick<Space, "kind">) => (space.kind === "shared" ? "共享空间" : "个人空间");
export const spaceAudience = (space: Pick<Space, "kind">) => (space.kind === "shared" ? "所有成员可见" : "只有你可见");

export const originLabel = (snapshot: Pick<Snapshot, "origin">) => (snapshot.origin === "scheduled" ? "定时" : "手动");

export function runErrorText(schedule: SnapshotSchedule): string {
  switch (schedule.lastRun?.error) {
    case "volume_unavailable": return "数据卷不可用";
    case "space_unavailable": return "空间已不存在";
    default: return "快照创建失败";
  }
}

// defaultSnapshotName names a manual snapshot after the minute it is taken.
export function defaultSnapshotName(now = new Date()): string {
  const pad = (value: number) => String(value).padStart(2, "0");
  return `手动快照 ${now.getFullYear()}-${pad(now.getMonth() + 1)}-${pad(now.getDate())} ${pad(now.getHours())}:${pad(now.getMinutes())}`;
}

// restoredName suggests a name beside an existing one: 报告 (恢复 10-12 14.30).docx.
// It avoids ":", which Windows cannot show over SMB.
export function restoredName(name: string, kind: "file" | "directory", now = new Date()): string {
  const pad = (value: number) => String(value).padStart(2, "0");
  const suffix = ` (恢复 ${pad(now.getMonth() + 1)}-${pad(now.getDate())} ${pad(now.getHours())}.${pad(now.getMinutes())})`;
  const dot = name.lastIndexOf(".");
  if (kind === "directory" || dot <= 0) return `${name}${suffix}`;
  return `${name.slice(0, dot)}${suffix}${name.slice(dot)}`;
}
