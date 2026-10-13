import { APIError, type Space, type TrashItem } from "../api";

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

// expiryOf words when an item is deleted for good and how urgent that is.
export function expiryOf(item: Pick<TrashItem, "purgeAt">, now = Date.now()): { label: string; tone: "calm" | "soon" | "urgent" } {
  const left = Date.parse(item.purgeAt) - now;
  if (!(left > 0)) return { label: "即将清理", tone: "urgent" };
  const days = Math.ceil(left / day);
  if (days === 1) return { label: "1 天内清理", tone: "urgent" };
  return { label: `${days} 天后清理`, tone: days <= 7 ? "soon" : "calm" };
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
