import { APIError, type RestoreConflict, type RestoredEntry, type Space, type TrashItem } from "../api";
import { formatDay } from "../backup/format";
import { nameNotPortableText, nameTooLongText } from "../fileNames";

export type TrashSort = "deleted" | "expiry";

const day = 86_400_000;

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

// BatchOutcome is what restoring a selection did. renamed and replaced count
// the restored items that came back beside, or in place of, one of their
// name; skipped ones stay in the recycle bin.
export interface BatchOutcome { done: TrashItem[]; renamed: number; replaced: number; skipped: TrashItem[]; gone: TrashItem[]; failed: string[] }

export const emptyOutcome = (): BatchOutcome => ({ done: [], renamed: 0, replaced: 0, skipped: [], gone: [], failed: [] });

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
export function restoredText(restored: Pick<RestoredEntry, "name" | "replaced">, asked: string, location: string, choice: RestoreConflict): string {
  if (restored.replaced) return `已恢复“${restored.name}”到 ${location}，原来的同名文件已移到回收站`;
  if (choice === "replace" && restored.name !== asked) return `那里的“${asked}”是文件夹，不会被替换；已恢复为“${restored.name}”`;
  return `已恢复“${restored.name}”到 ${location}`;
}

export function messageOf(error: unknown): string {
  if (error instanceof APIError) {
    switch (error.code) {
      case "forbidden": return "只有删除它的人可以恢复或永久删除";
      case "not_found": return "这个项目已不在回收站，请刷新后重试";
      case "name_conflict": return "目标位置已有同名项目";
      case "volume_unavailable": return "数据卷当前不可用，请检查存储状态";
      case "validation_failed": return "名称不符合要求";
      case "name_too_long": return nameTooLongText;
      case "name_not_portable": return nameNotPortableText;
      case "network_error": return "网络连接中断";
    }
    return error.status >= 500 ? "服务暂时不可用，请稍后重试" : error.message;
  }
  return "请求失败，请稍后重试";
}
