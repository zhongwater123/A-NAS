import { APIError } from "../api";
import type { PhotoAlbum, PhotoAsset, PhotoLabelCount, PhotoLibrary } from "./photosApi";

// A place is what the main area shows. AI 聚合 (a search with an empty query
// is its start page) and its clusters cover the caller's and the shared
// library; viewing adds the member library it was started from, so ordinary
// searches never mix it in.
export type Place =
  | { kind: "timeline"; libraryId: string }
  | { kind: "albums"; libraryId: string }
  | { kind: "album"; libraryId: string; album: PhotoAlbum }
  | { kind: "trash"; libraryId: string }
  | { kind: "search"; viewing: string; query: string }
  | { kind: "cluster"; viewing: string; label: PhotoLabelCount };

export interface Caller { userId: string; isAdmin: boolean; libraries: PhotoLibrary[] }

export const libraryOf = (caller: Caller, libraryId: string) => caller.libraries.find((item) => item.id === libraryId);
export const ownLibrary = (caller: Caller) => caller.libraries.find((item) => item.kind === "private" && item.ownerUserId === caller.userId && !item.viewing);
export const sharedLibrary = (caller: Caller) => caller.libraries.find((item) => item.kind === "shared");
export const viewingLibrary = (caller: Caller) => caller.libraries.find((item) => item.viewing);

// The panel only hides what the photo service would refuse anyway.
export function canChange(caller: Caller, asset: PhotoAsset) {
  const owner = libraryOf(caller, asset.libraryId);
  return Boolean(owner && !owner.viewing && (owner.kind === "private" || asset.uploadedBy === caller.userId || caller.isAdmin));
}
export function canEditAlbum(caller: Caller, album: PhotoAlbum) {
  const owner = libraryOf(caller, album.libraryId);
  return Boolean(owner && !owner.viewing && (owner.kind === "private" || album.createdBy === caller.userId || caller.isAdmin));
}
export const canWrite = (caller: Caller, libraryId: string) => {
  const library = libraryOf(caller, libraryId);
  return Boolean(library && !library.viewing);
};

export function libraryName(caller: Caller, library?: PhotoLibrary) {
  if (!library) return "图库";
  if (library.kind === "shared") return "共享图库";
  if (library.ownerUserId === caller.userId && !library.viewing) return "我的图库";
  return `${library.ownerName ?? "成员"}的图库`;
}

export const placeLibrary = (place: Place) => "libraryId" in place ? place.libraryId : "";

// Label categories of the vocabulary, in the order AI 聚合 shows them.
export const categoryNames: Record<string, string> = {
  animal: "动物", nature: "自然", food: "食物", vehicle: "交通工具", activity: "运动", object: "物品", scene: "场景", people: "人物", document: "文档",
};
// The member library a search or a cluster started from covers.
export const placeViewing = (caller: Caller, place: Place) => {
  if ("viewing" in place) return place.viewing;
  return libraryOf(caller, placeLibrary(place))?.viewing ? placeLibrary(place) : "";
};

// The moment a photo sorts by on the timeline.
export const momentOf = (asset: PhotoAsset) => new Date(asset.takenAt ?? asset.importedAt);

export function dayKey(date: Date) {
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`;
}

const weekdays = ["周日", "周一", "周二", "周三", "周四", "周五", "周六"];

// dayLabel names a day the way people say it: today, yesterday, this year's
// days without the year.
export function dayLabel(date: Date, now = new Date()) {
  const today = dayKey(now);
  const yesterday = new Date(now);
  yesterday.setDate(now.getDate() - 1);
  if (dayKey(date) === today) return "今天";
  if (dayKey(date) === dayKey(yesterday)) return "昨天";
  const day = `${date.getMonth() + 1}月${date.getDate()}日 ${weekdays[date.getDay()]}`;
  return date.getFullYear() === now.getFullYear() ? day : `${date.getFullYear()}年${day}`;
}

export function monthLabel(month: string, now = new Date()) {
  const [year, value] = month.split("-").map(Number);
  return year === now.getFullYear() ? `${value}月` : `${year}年${value}月`;
}

export const fullMonthLabel = (month: string) => {
  const [year, value] = month.split("-").map(Number);
  return `${year}年${value}月`;
};

export function formatDateTime(value: string) {
  return new Date(value).toLocaleString("zh-CN", { year: "numeric", month: "long", day: "numeric", weekday: "short", hour: "2-digit", minute: "2-digit" });
}

export function formatSize(bytes: number) {
  if (bytes >= 1024 ** 3) return `${(bytes / 1024 ** 3).toFixed(1)} GB`;
  if (bytes >= 1024 ** 2) return `${(bytes / 1024 ** 2).toFixed(1)} MB`;
  return `${Math.max(1, Math.round(bytes / 1024))} KB`;
}

export function daysLeft(until: string, now = Date.now()) {
  return Math.max(0, Math.ceil((new Date(until).getTime() - now) / 86_400_000));
}

const photoErrors: Record<string, string> = {
  unsupported_media_type: "只支持 JPEG 和 PNG 照片",
  too_large: "照片超过导入大小上限",
  insufficient_storage: "数据卷剩余空间不足",
  forbidden: "你不能修改这张照片",
  conflict: "名称已存在，或照片状态已经变化",
  validation_failed: "名称无效",
  not_found: "照片或相册已不存在",
  photos_unavailable: "相册暂时不可用：数据卷未就绪，或此设备尚未启用相册",
  network_error: "网络连接中断",
};

export function messageOf(error: unknown) {
  if (error instanceof APIError) return photoErrors[error.code] ?? error.message;
  return error instanceof Error ? error.message : "请求失败";
}

export const isUnavailable = (error: unknown) => error instanceof APIError && error.code === "photos_unavailable";

// runEach applies an action to every item, a few at a time, and reports
// progress; a failure does not stop the others or undo earlier successes.
export async function runEach<T, R>(items: T[], action: (item: T) => Promise<R>, onProgress?: (done: number) => void, concurrency = 3) {
  const results: Array<{ item: T; value?: R; error?: unknown }> = [];
  let next = 0;
  let done = 0;
  const worker = async () => {
    while (next < items.length) {
      const item = items[next++];
      try { results.push({ item, value: await action(item) }); }
      catch (error) { results.push({ item, error }); }
      onProgress?.(++done);
    }
  };
  await Promise.all(Array.from({ length: Math.min(concurrency, items.length) }, worker));
  return { succeeded: results.filter((result) => !("error" in result)), failed: results.filter((result) => "error" in result) };
}

export const reducedMotion = () => typeof window.matchMedia === "function" && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
