import { errorText } from "../api/errorMessages";
import type { RestoredEntry, Space, TrashItem } from "../api.generated";
import { formatDay } from "../backup/format";
import { calendarDays } from "../lib/time";
import type { RestoreConflict } from "./api";

export type TrashSort = "deleted" | "expiry" | "size";

export const sortLabels: Record<TrashSort, string> = {
  deleted: "最近删除",
  expiry: "即将清理",
  size: "占用空间",
};

const day = 86_400_000;

export type ExpiryTone = "calm" | "soon" | "urgent";

// expiryOf words when an item is deleted for good, how urgent that is and
// how much of its time in the bin is left (0 to 1).
export function expiryOf(
  item: Pick<TrashItem, "deletedAt" | "purgeAt">,
  now = Date.now(),
): { label: string; short: string; tone: ExpiryTone; left: number } {
  const purge = Date.parse(item.purgeAt);
  const span = purge - Date.parse(item.deletedAt);
  const remaining = purge - now;
  const left = span > 0 ? Math.min(1, Math.max(0, remaining / span)) : 0;
  if (!(remaining > 0)) return { label: "即将清理", short: "即将清理", tone: "urgent", left: 0 };
  const days = Math.ceil(remaining / day);
  if (days === 1) return { label: "1 天内清理", short: "1 天内", tone: "urgent", left };
  return { label: `${days} 天后清理`, short: `${days} 天`, tone: days <= 7 ? "soon" : "calm", left };
}

export const spaceName = (space?: Pick<Space, "kind">) => (space?.kind === "shared" ? "共享空间" : "个人空间");

// locationOf is where restoring puts an item: its original folder, or the
// space root when that folder is gone.
export function locationOf(item: TrashItem, space?: Pick<Space, "kind">): { folders: string[]; text: string } {
  const folders = item.originalGone || !item.originalPath ? [] : item.originalPath.split("/");
  return { folders, text: [spaceName(space), ...folders].join(" / ") };
}

// restoredName suggests a free name beside a taken one: 计划 (恢复).txt,
// then 计划 (恢复 2).txt.
export function restoredName(name: string, kind: TrashItem["kind"], attempt = 1): string {
  const suffix = attempt <= 1 ? " (恢复)" : ` (恢复 ${attempt})`;
  const dot = name.lastIndexOf(".");
  if (kind === "directory" || dot <= 0) return `${name}${suffix}`;
  return `${name.slice(0, dot)}${suffix}${name.slice(dot)}`;
}

// splitName parts a file's name before its extension, so a long name can be
// cut short and still show what kind of file it is.
export function splitName(name: string, kind: TrashItem["kind"]): { stem: string; extension: string } {
  const dot = name.lastIndexOf(".");
  if (kind === "directory" || dot <= 0 || name.length - dot > 8) return { stem: name, extension: "" };
  return { stem: name.slice(0, dot), extension: name.slice(dot) };
}

export function sortItems(items: TrashItem[], sort: TrashSort): TrashItem[] {
  const newest = (left: TrashItem, right: TrashItem) => Date.parse(right.deletedAt) - Date.parse(left.deletedAt);
  return [...items].sort((left, right) =>
    sort === "expiry"
      ? Date.parse(left.purgeAt) - Date.parse(right.purgeAt)
      : sort === "size"
        ? right.sizeBytes - left.sizeBytes || newest(left, right)
        : newest(left, right),
  );
}

export interface TrashGroup {
  key: string;
  label: string;
  items: TrashItem[];
}

// groupItems groups sorted items by the day they were deleted, or, sorted
// by expiry, into those that go within a week and the rest. Sorted by size
// they are one group without a name.
export function groupItems(items: TrashItem[], sort: TrashSort, now = new Date()): TrashGroup[] {
  if (sort === "size") return items.length ? [{ key: "size", label: "", items }] : [];
  const groups: TrashGroup[] = [];
  for (const item of items) {
    const key =
      sort === "expiry"
        ? expiryOf(item, now.getTime()).tone === "calm"
          ? "later"
          : "week"
        : new Date(item.deletedAt).toDateString();
    let group = groups.at(-1);
    if (group?.key !== key) {
      const label = sort === "expiry" ? (key === "week" ? "7 天内清理" : "更晚清理") : formatDay(item.deletedAt, now);
      group = { key, label, items: [] };
      groups.push(group);
    }
    group.items.push(item);
  }
  return groups;
}

// purgeOffset counts the calendar days from today to the day an item is
// deleted for good; an item already due counts as today's.
export function purgeOffset(item: Pick<TrashItem, "purgeAt">, now: Date | number = Date.now()): number {
  return Math.max(0, calendarDays(item.purgeAt, now));
}

export interface PurgeDay {
  offset: number;
  date: Date;
  count: number;
  bytes: number;
  tone: ExpiryTone;
}

const scheduleDays = 30;
const scheduleLimit = 90;

const urgency: ExpiryTone[] = ["calm", "soon", "urgent"];

// purgeSchedule is what the recycle bin deletes on each of the coming days,
// from today to the last day anything is due and for at least 30 days. A
// day is as urgent as the most urgent of its items.
export function purgeSchedule(items: TrashItem[], now = new Date()): PurgeDay[] {
  const offsets = items.map((item) => Math.min(purgeOffset(item, now), scheduleLimit));
  const days: PurgeDay[] = Array.from({ length: Math.max(scheduleDays, ...offsets) + 1 }, (_, offset) => ({
    offset,
    date: new Date(now.getFullYear(), now.getMonth(), now.getDate() + offset),
    count: 0,
    bytes: 0,
    tone: "calm",
  }));
  for (const [index, item] of items.entries()) {
    const day = days[offsets[index]];
    const tone = expiryOf(item, now.getTime()).tone;
    day.count += 1;
    day.bytes += item.sizeBytes;
    if (urgency.indexOf(tone) > urgency.indexOf(day.tone)) day.tone = tone;
  }
  return days;
}

// matches filters by space, by a part of the name, ignoring case, and by the
// day the item is deleted for good.
export function matches(item: TrashItem, spaceId: string, query: string, purgeDay?: number, now = new Date()): boolean {
  if (spaceId && item.spaceId !== spaceId) return false;
  if (purgeDay !== undefined && Math.min(purgeOffset(item, now), scheduleLimit) !== purgeDay) return false;
  const needle = query.trim().toLocaleLowerCase("zh-CN");
  return !needle || item.name.toLocaleLowerCase("zh-CN").includes(needle);
}

// BatchOutcome is what restoring a selection did. renamed and replaced count
// the restored items that came back beside, or in place of, one of their
// name; skipped ones stay in the recycle bin.
export interface BatchOutcome {
  done: TrashItem[];
  renamed: number;
  replaced: number;
  skipped: TrashItem[];
  gone: TrashItem[];
  failed: string[];
}

export const emptyOutcome = (): BatchOutcome => ({
  done: [],
  renamed: 0,
  replaced: 0,
  skipped: [],
  gone: [],
  failed: [],
});

// restoreSummary words what restoring several items did.
export function restoreSummary(outcome: BatchOutcome): { text: string; tone: "success" | "error" } {
  let restored = outcome.done.length ? `已恢复 ${outcome.done.length} 项` : "没有恢复任何项目";
  const how = [];
  if (outcome.renamed) how.push(`${outcome.renamed} 项改用了新名称`);
  if (outcome.replaced) how.push(`${outcome.replaced} 项替换了同名文件，原来的文件已移到回收站`);
  if (how.length) restored += `，其中 ${how.join("、")}`;
  const parts = [restored];
  if (outcome.skipped.length) parts.push(`跳过 ${outcome.skipped.length} 项，仍在回收站中`);
  if (outcome.gone.length) parts.push(`${outcome.gone.length} 项已不在回收站`);
  if (outcome.failed.length) parts.push(`${outcome.failed.length} 项失败：${outcome.failed[0]}`);
  return { text: parts.join("；"), tone: outcome.failed.length ? "error" : "success" };
}

// restoredText words where a single restored item went and under what name.
export function restoredText(
  restored: Pick<RestoredEntry, "name" | "replaced">,
  asked: string,
  location: string,
  choice: RestoreConflict,
): string {
  if (restored.replaced) return `已恢复“${restored.name}”到 ${location}，原来的同名文件已移到回收站`;
  if (choice === "replace" && restored.name !== asked)
    return `那里的“${asked}”是文件夹，不会被替换；已恢复为“${restored.name}”`;
  return `已恢复“${restored.name}”到 ${location}`;
}

export function messageOf(error: unknown): string {
  return errorText(error, "trash");
}
