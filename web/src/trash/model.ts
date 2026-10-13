import { APIError, type Space, type TrashItem } from "../api";
import { formatDay } from "../backup/format";

export type TrashSort = "deleted" | "expiry";

const day = 86_400_000;

// retentionDays reads how long the recycle bin keeps items from the items
// themselves, falling back to the default of 30 days.
export function retentionDays(items: TrashItem[]): number {
  const item = items[0];
  if (!item) return 30;
  const days = Math.round((Date.parse(item.purgeAt) - Date.parse(item.deletedAt)) / day);
  return Number.isFinite(days) && days > 0 ? days : 30;
}

export type ExpiryTone = "calm" | "soon" | "urgent";

// expiryOf words when an item is deleted for good, how urgent that is and
// how much of its time in the bin is left (0 to 1).
export function expiryOf(item: Pick<TrashItem, "deletedAt" | "purgeAt">, now = Date.now()): { label: string; short: string; tone: ExpiryTone; left: number } {
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

export function sortItems(items: TrashItem[], sort: TrashSort): TrashItem[] {
  return [...items].sort((left, right) => sort === "expiry"
    ? Date.parse(left.purgeAt) - Date.parse(right.purgeAt)
    : Date.parse(right.deletedAt) - Date.parse(left.deletedAt));
}

export interface TrashGroup { key: string; label: string; items: TrashItem[] }

// groupItems groups sorted items by the day they were deleted, or, sorted
// by expiry, into those that go within a week and the rest.
export function groupItems(items: TrashItem[], sort: TrashSort, now = new Date()): TrashGroup[] {
  const groups: TrashGroup[] = [];
  for (const item of items) {
    const key = sort === "expiry"
      ? (expiryOf(item, now.getTime()).tone === "calm" ? "later" : "week")
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

// matches filters by space and by a part of the name, ignoring case.
export function matches(item: TrashItem, spaceId: string, query: string): boolean {
  if (spaceId && item.spaceId !== spaceId) return false;
  const needle = query.trim().toLocaleLowerCase("zh-CN");
  return !needle || item.name.toLocaleLowerCase("zh-CN").includes(needle);
}

export interface BatchOutcome { done: TrashItem[]; conflicts: TrashItem[]; gone: TrashItem[]; failed: string[] }

// restoreSummary words what restoring several items did.
export function restoreSummary(outcome: BatchOutcome): { text: string; tone: "success" | "error" } {
  const parts = [outcome.done.length ? `已恢复 ${outcome.done.length} 项` : "没有恢复任何项目"];
  if (outcome.conflicts.length) parts.push(`${outcome.conflicts.length} 项的原位置已有同名项目，仍留在回收站并保持选中，可逐个改名恢复`);
  if (outcome.gone.length) parts.push(`${outcome.gone.length} 项已不在回收站`);
  if (outcome.failed.length) parts.push(`${outcome.failed.length} 项失败：${outcome.failed[0]}`);
  return { text: parts.join("；"), tone: outcome.conflicts.length || outcome.failed.length ? "error" : "success" };
}

export function messageOf(error: unknown): string {
  if (error instanceof APIError) {
    switch (error.code) {
      case "forbidden": return "只有删除它的人可以恢复或永久删除";
      case "not_found": return "这个项目已不在回收站，请刷新后重试";
      case "name_conflict": return "目标位置已有同名项目";
      case "volume_unavailable": return "数据卷当前不可用，请检查存储状态";
      case "validation_failed": return "名称不符合要求";
      case "network_error": return "网络连接中断";
    }
    return error.status >= 500 ? "服务暂时不可用，请稍后重试" : error.message;
  }
  return "请求失败，请稍后重试";
}
