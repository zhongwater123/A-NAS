import { calendarLabel, formatDate } from "../lib/time";
import { errorText } from "../api/errorMessages";
import { APIError } from "../api/client";
import type { ImageQuery } from "./imageSearch";
import type { PhotoAlbum, PhotoAsset, PhotoLabelCount, PhotoLibrary, PhotoPerson, PhotoTagCount } from "./api";

// A place is what the main area shows. AI 聚合 (a search with an empty query
// is its start page), its clusters and the user tag groups cover the caller's and the shared
// library; viewing adds the member library it was started from, so ordinary
// searches never mix it in. Similar photos, and the photos like an image
// from outside the library, cover what a search would and go back to where
// they were asked for.
export type Place =
  | { kind: "timeline"; libraryId: string }
  | { kind: "albums"; libraryId: string }
  | { kind: "album"; libraryId: string; album: PhotoAlbum }
  | { kind: "trash"; libraryId: string }
  | { kind: "search"; viewing: string; query: string }
  | { kind: "cluster"; viewing: string; label: PhotoLabelCount }
  | { kind: "tag"; viewing: string; tag: PhotoTagCount }
  | { kind: "person"; viewing: string; person: PhotoPerson }
  | { kind: "similar"; viewing: string; asset: PhotoAsset; from: Place }
  | { kind: "image"; viewing: string; query: ImageQuery; from: Place };

export interface Caller {
  userId: string;
  isAdmin: boolean;
  libraries: PhotoLibrary[];
}

export const libraryOf = (caller: Caller, libraryId: string) => caller.libraries.find((item) => item.id === libraryId);
export const ownLibrary = (caller: Caller) =>
  caller.libraries.find((item) => item.kind === "private" && item.ownerUserId === caller.userId && !item.viewing);
export const sharedLibrary = (caller: Caller) => caller.libraries.find((item) => item.kind === "shared");
export const viewingLibrary = (caller: Caller) => caller.libraries.find((item) => item.viewing);

// Who may name and correct a library's people: its owner, and every member in
// the shared library; never an administrator who only views it.
export function canChangePeople(caller: Caller, libraryId: string) {
  const library = libraryOf(caller, libraryId);
  if (!library || library.viewing) return false;
  return library.kind === "shared" || library.ownerUserId === caller.userId;
}

// The panel only hides what the photo service would refuse anyway.
export function canChange(caller: Caller, asset: PhotoAsset) {
  const owner = libraryOf(caller, asset.libraryId);
  return Boolean(
    owner && !owner.viewing && (owner.kind === "private" || asset.uploadedBy === caller.userId || caller.isAdmin),
  );
}
export function canEditAlbum(caller: Caller, album: PhotoAlbum) {
  const owner = libraryOf(caller, album.libraryId);
  return Boolean(
    owner && !owner.viewing && (owner.kind === "private" || album.createdBy === caller.userId || caller.isAdmin),
  );
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

export const placeLibrary = (place: Place) => ("libraryId" in place ? place.libraryId : "");

// Label categories of the vocabulary, in the order AI 聚合 shows them.
export const categoryNames: Record<string, string> = {
  animal: "动物",
  nature: "自然",
  scene: "场景",
  food: "食物",
  vehicle: "交通工具",
  activity: "运动",
  object: "物品",
  people: "人物",
  document: "文档",
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

// dayLabel names a day the way people say it: today, yesterday, this year's
// days without the year.
export function dayLabel(date: Date, now = new Date()) {
  return calendarLabel(date, now, "short");
}

export function monthLabel(month: string, now = new Date()) {
  const [year, value] = month.split("-").map(Number);
  return formatDate(new Date(year, value - 1), {
    month: "long",
    ...(year !== now.getFullYear() ? { year: "numeric" } : {}),
  });
}

export const fullMonthLabel = (month: string) => {
  const [year, value] = month.split("-").map(Number);
  return formatDate(new Date(year, value - 1), { year: "numeric", month: "long" });
};

export function formatDateTime(value: string) {
  return new Date(value).toLocaleString("zh-CN", {
    year: "numeric",
    month: "long",
    day: "numeric",
    weekday: "short",
    hour: "2-digit",
    minute: "2-digit",
  });
}

// countLabel says how many photos and videos there are: 3 张照片, 2 个视频,
// 3 张照片和 2 个视频, or with another join in a heading.
export function countLabel(total: number, videos = 0, join = "和") {
  const photos = total - videos;
  if (!videos) return `${total} 张照片`;
  if (!photos) return `${videos} 个视频`;
  return join === "和" ? `${photos} 张照片和 ${videos} 个视频` : `${photos} 张照片${join}${videos} 个视频`;
}
export const assetsLabel = (assets: PhotoAsset[]) =>
  countLabel(assets.length, assets.filter((asset) => asset.video).length);

// clock writes a video position the way players do: 0:42, 12:05, 1:02:05.
export function clock(seconds: number) {
  const whole = Math.max(0, Math.floor(seconds));
  const hours = Math.floor(whole / 3600);
  const minutes = Math.floor(whole / 60) % 60;
  const rest = String(whole % 60).padStart(2, "0");
  return hours ? `${hours}:${String(minutes).padStart(2, "0")}:${rest}` : `${minutes}:${rest}`;
}

// durationLabel says how long a video plays: 42 秒, 3 分 05 秒, 1 小时 2 分;
// whole seconds, as the player's clock counts them.
export function durationLabel(seconds: number) {
  const whole = Math.max(1, Math.floor(seconds));
  if (whole < 60) return `${whole} 秒`;
  if (whole < 3600) return `${Math.floor(whole / 60)} 分 ${String(whole % 60).padStart(2, "0")} 秒`;
  return `${Math.floor(whole / 3600)} 小时 ${Math.floor(whole / 60) % 60} 分`;
}

// videoLines names a video's resolution by its shorter side: 1080p, 4K.
export function videoLines(width: number, height: number) {
  const lines = Math.min(width, height);
  if (lines >= 2160) return "4K";
  return lines > 0 ? `${lines}p` : "";
}

export function daysLeft(until: string, now = Date.now()) {
  return Math.max(0, Math.ceil((new Date(until).getTime() - now) / 86_400_000));
}

// How to send the MOV a Live Photo still came without: Photos on a Mac
// hands a browser only the still when dragged or chosen directly.
export const liveVideoHint =
  "在 Mac 的“照片”App 中选中这张照片，用“文件 → 导出 → 导出未修改的原件”得到同名 MOV，上传后就会补上动态效果。";

export function messageOf(error: unknown): string {
  return errorText(error, "photos");
}

export const isUnavailable = (error: unknown) => error instanceof APIError && error.code === "photos_unavailable";

// runEach applies an action to every item, a few at a time, and reports
// progress; a failure does not stop the others or undo earlier successes.
export async function runEach<T, R>(
  items: T[],
  action: (item: T) => Promise<R>,
  onProgress?: (done: number) => void,
  concurrency = 3,
) {
  const results: Array<{ item: T; value?: R; error?: unknown }> = [];
  let next = 0;
  let done = 0;
  const worker = async () => {
    while (next < items.length) {
      const item = items[next++];
      try {
        results.push({ item, value: await action(item) });
      } catch (error) {
        results.push({ item, error });
      }
      onProgress?.(++done);
    }
  };
  await Promise.all(Array.from({ length: Math.min(concurrency, items.length) }, worker));
  return {
    succeeded: results.filter((result) => !("error" in result)),
    failed: results.filter((result) => "error" in result),
  };
}

export const reducedMotion = () =>
  typeof window.matchMedia === "function" && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
