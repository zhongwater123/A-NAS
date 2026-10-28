import { useState } from "react";

export type FileSort = "name" | "modifiedAt" | "type" | "sizeBytes" | "manual";
export type FileOrder = {
  sortBy: FileSort;
  direction: "ascending" | "descending";
  pinned: string[];
  manual: string[];
};
export type OrderableFile = {
  id: string;
  name: string;
  kind: "directory" | "resource" | "file";
  modifiedAt: string;
  type: string;
  sizeBytes: number;
};
const defaults: FileOrder = { sortBy: "name", direction: "ascending", pinned: [], manual: [] };

export function fileOrderKey(userID: string, spaceID: string, parentID: string) {
  return `a-nas.file-order.v1:${JSON.stringify([userID, spaceID, parentID])}`;
}

function readOrder(key: string): FileOrder {
  try {
    const value = JSON.parse(localStorage.getItem(key) ?? "null");
    if (
      !value ||
      !["name", "modifiedAt", "type", "sizeBytes", "manual"].includes(value.sortBy) ||
      !["ascending", "descending"].includes(value.direction) ||
      ![value.pinned, value.manual].every((ids) => Array.isArray(ids) && ids.every((id) => typeof id === "string"))
    )
      return defaults;
    return {
      sortBy: value.sortBy,
      direction: value.direction,
      pinned: [...new Set<string>(value.pinned)],
      manual: [...new Set<string>(value.manual)],
    };
  } catch {
    return defaults;
  }
}

// Store only opaque IDs, scoped to the signed-in user and the directory. A
// failed read/listing never prunes preferences, so offline data keeps its order.
export function useFileOrder(userID: string, spaceID: string, parentID: string) {
  const key = fileOrderKey(userID, spaceID, parentID);
  const [cache, setCache] = useState<Record<string, FileOrder>>({});
  const [storageError, setStorageError] = useState("");
  const order = cache[key] ?? readOrder(key);
  const save = (next: FileOrder) => {
    setCache((current) => ({ ...current, [key]: next }));
    try {
      localStorage.setItem(key, JSON.stringify(next));
      setStorageError("");
    } catch {
      setStorageError("浏览器无法保存排序偏好；本次打开期间仍然有效。");
    }
  };
  return { order, save, storageError, scope: key };
}

export function isPinned(item: OrderableFile, order: FileOrder) {
  return item.kind === "directory" && order.pinned.includes(item.id);
}

export function sortFiles<T extends OrderableFile>(items: T[], order: FileOrder): T[] {
  const ranks = new Map(order.manual.map((id, index) => [id, index]));
  const nameCompare = (a: T, b: T) =>
    a.name.localeCompare(b.name, "zh-CN", { numeric: true, sensitivity: "base" }) || a.id.localeCompare(b.id);
  return [...items].sort((a, b) => {
    const pin = Number(isPinned(b, order)) - Number(isPinned(a, order));
    if (pin) return pin;
    if (order.sortBy === "manual") {
      return (
        (ranks.get(a.id) ?? Number.MAX_SAFE_INTEGER) - (ranks.get(b.id) ?? Number.MAX_SAFE_INTEGER) || nameCompare(a, b)
      );
    }
    const groups = { directory: 0, resource: 1, file: 2 };
    const group = groups[a.kind] - groups[b.kind];
    if (group) return group;
    let result = 0;
    if (order.sortBy === "modifiedAt") result = (Date.parse(a.modifiedAt) || 0) - (Date.parse(b.modifiedAt) || 0);
    if (order.sortBy === "sizeBytes") result = a.sizeBytes - b.sizeBytes;
    if (order.sortBy === "type") result = a.type.localeCompare(b.type, "zh-CN", { numeric: true, sensitivity: "base" });
    return (result || nameCompare(a, b)) * (order.direction === "ascending" ? 1 : -1);
  });
}

export function reorderFiles(items: OrderableFile[], order: FileOrder, sourceID: string, targetID: string): FileOrder {
  const sorted = sortFiles(items, order);
  const from = sorted.findIndex((item) => item.id === sourceID);
  const to = sorted.findIndex((item) => item.id === targetID);
  if (
    order.sortBy !== "manual" ||
    from < 0 ||
    to < 0 ||
    from === to ||
    isPinned(sorted[from], order) !== isPinned(sorted[to], order)
  )
    return order;
  const [source] = sorted.splice(from, 1);
  sorted.splice(to, 0, source);
  return { ...order, manual: sorted.map((item) => item.id) };
}
