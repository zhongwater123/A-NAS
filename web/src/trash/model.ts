import { APIError, type Space, type TrashItem } from "../api";
import { formatDay } from "../backup/format";

export type SortKey = "name" | "size" | "deleted" | "expiry";
export interface TrashSort { key: SortKey; ascending: boolean }

// The direction a column sorts in first: names A to Z, the biggest, the
// latest deleted and the soonest to go first.
export const firstDirection: Record<SortKey, boolean> = { name: true, size: false, deleted: false, expiry: true };

const day = 86_400_000;

export type ExpiryTone = "calm" | "soon" | "urgent";

// expiryOf words how long an item has left in the bin and how urgent that is.
export function expiryOf(item: Pick<TrashItem, "purgeAt">, now = Date.now()): { label: string; tone: ExpiryTone } {
  const remaining = Date.parse(item.purgeAt) - now;
  if (!(remaining > 0)) return { label: "即将清理", tone: "urgent" };
  const days = Math.ceil(remaining / day);
  if (days === 1) return { label: "不到 1 天", tone: "urgent" };
  return { label: `${days} 天`, tone: days <= 7 ? "soon" : "calm" };
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
  const compare = (left: TrashItem, right: TrashItem) => {
    switch (sort.key) {
      case "name": return left.name.localeCompare(right.name, "zh-CN", { numeric: true, sensitivity: "base" });
      case "size": return left.sizeBytes - right.sizeBytes;
      case "deleted": return Date.parse(left.deletedAt) - Date.parse(right.deletedAt);
      case "expiry": return Date.parse(left.purgeAt) - Date.parse(right.purgeAt);
    }
  };
  return [...items].sort((left, right) => (sort.ascending ? 1 : -1) * compare(left, right));
}

export interface TrashGroup { key: string; label?: string; items: TrashItem[] }

// groupItems groups items sorted by deletion time by the day they were
// deleted; any other order is one group without a heading.
export function groupItems(items: TrashItem[], sort: TrashSort, now = new Date()): TrashGroup[] {
  if (sort.key !== "deleted") return items.length ? [{ key: "all", items }] : [];
  const groups: TrashGroup[] = [];
  for (const item of items) {
    const key = new Date(item.deletedAt).toDateString();
    let group = groups.at(-1);
    if (group?.key !== key) {
      group = { key, label: formatDay(item.deletedAt, now), items: [] };
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
