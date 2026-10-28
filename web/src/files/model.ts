import { errorText } from "../api/errorMessages";
import type { FileEntry, Space, Volume } from "../api.generated";
import { fileTypeLabel, isPDFPreviewable, isPreviewableImage } from "../lib/fileTypes";
import { FileOrder, FileSort, isPinned } from "./useFileOrder";

export type FileView = "list" | "icons";
export type FileResource = "photos" | "music" | "movies";
export type FileBrowserItem = {
  id: string;
  name: string;
  kind: "directory" | "file" | "resource";
  type: string;
  detail: string;
  modifiedAt: string;
  sizeBytes: number;
  entry?: FileEntry;
  resource?: FileResource;
  planned?: boolean;
};
export type FileClipboard = { entries: FileEntry[]; mode: "copy" | "move" };
export type FileFolder = { id: string; name: string };

// What VNAS never reads or acts on (assistant/webosControl.ts): another member's
// space open under a viewing grant is beyond what the assistant may read.
export const othersSpace = "他人的个人空间（只读查看）";

export const personalFileResources: FileBrowserItem[] = [
  {
    id: "resource:photos",
    name: "图库",
    kind: "resource",
    type: "图库投影",
    detail: "个人图库",
    modifiedAt: "",
    sizeBytes: 0,
    resource: "photos",
  },
  {
    id: "resource:music",
    name: "音乐",
    kind: "resource",
    type: "功能入口",
    detail: "在音乐中打开",
    modifiedAt: "",
    sizeBytes: 0,
    resource: "music",
  },
  {
    id: "resource:movies",
    name: "影视",
    kind: "resource",
    type: "功能入口",
    detail: "影视中心",
    modifiedAt: "",
    sizeBytes: 0,
    resource: "movies",
  },
];

export function browserItem(entry: FileEntry): FileBrowserItem {
  return {
    id: entry.id,
    name: entry.name,
    kind: entry.kind,
    type: entry.kind === "directory" ? "文件夹" : fileTypeLabel(entry.name),
    detail: entry.kind === "directory" ? "文件夹" : isViewable(entry.name) ? "双击预览" : "双击下载",
    modifiedAt: entry.modifiedAt,
    sizeBytes: entry.sizeBytes,
    entry,
  };
}

// spaceLabel names a space in the tree and the breadcrumb; a viewed private
// space is named by its owner, the way they go by.
export function spaceLabel(space: Space, ownerName: string): string {
  return space.kind === "shared" ? "共享空间" : space.viewing ? `只读查看 · ${ownerName}` : "个人空间";
}

// volumeUsage reads the data volume's usage; without capacity or free space
// it is unknown rather than full.
export function volumeUsage(volume?: Volume) {
  const capacityBytes = volume?.capacityBytes ?? 0;
  const known = capacityBytes > 0 && volume?.availableBytes !== undefined;
  const availableBytes = known ? Math.min(capacityBytes, Math.max(0, volume.availableBytes!)) : 0;
  const usedBytes = known ? capacityBytes - availableBytes : 0;
  const usedPercent = known ? Math.min(100, Math.round((usedBytes / capacityBytes) * 100)) : 0;
  return { known, capacityBytes, usedBytes, usedPercent };
}

export interface SelectionState {
  // The items selected, and the one last clicked, which may have been
  // clicked out of the selection with Ctrl.
  items: FileBrowserItem[];
  item?: FileBrowserItem;
  order: FileOrder;
  clipboard?: FileClipboard;
  spaceID: string;
  parentID: string;
  // Another member's space open read-only.
  viewing: boolean;
  loading: boolean;
  // An operation is running.
  busy: boolean;
}

// selectionCommands says which commands the selection allows; the toolbar and
// the context menu offer the same ones.
export function selectionCommands({
  items,
  item,
  order,
  clipboard,
  spaceID,
  parentID,
  viewing,
  loading,
  busy,
}: SelectionState) {
  const entries = items.flatMap((selected) => (selected.entry ? [selected.entry] : []));
  const onlyEntries = entries.length > 0 && entries.length === items.length;
  const folders = items.filter((selected) => selected.kind === "directory");
  const mutate = onlyEntries && !viewing && !busy;
  return {
    entries,
    folders,
    mutate,
    rename: mutate && entries.length === 1,
    // Snapshot versions are for the user's own spaces; viewing a member's is for browsing files.
    versions: entries.length === 1 && items.length === 1 && item?.entry?.kind === "file" && !viewing,
    pin: folders.length > 0 && folders.length === items.length && !loading && !busy,
    allPinned: folders.length > 0 && folders.every((folder) => isPinned(folder, order)),
    download: onlyEntries,
    // A folder or several items arrive as one ZIP archive.
    archive: entries.length > 1 || entries[0]?.kind === "directory",
    paste: Boolean(
      clipboard?.entries.length &&
        !viewing &&
        !busy &&
        !(
          clipboard.mode === "move" &&
          clipboard.entries.every((entry) => entry.spaceId === spaceID && (entry.parentId ?? "") === parentID)
        ),
    ),
  };
}

export type SelectionCommands = ReturnType<typeof selectionCommands>;

// sortedBy sorts by another column, or turns the direction around when the
// column is already the sort. Dates and sizes start with the newest and largest;
// manual order starts from what is shown.
export function sortedBy(order: FileOrder, sort: FileSort, shown: FileBrowserItem[]): FileOrder {
  return {
    ...order,
    sortBy: sort,
    direction:
      order.sortBy === sort
        ? order.direction === "ascending"
          ? "descending"
          : "ascending"
        : sort === "modifiedAt" || sort === "sizeBytes"
          ? "descending"
          : "ascending",
    manual: sort === "manual" && !order.manual.length ? shown.map((item) => item.id) : order.manual,
  };
}

// withPins pins the folders, or unpins them when all of them are pinned.
export function withPins(order: FileOrder, folders: FileBrowserItem[]): FileOrder {
  const ids = folders.map((item) => item.id);
  const allPinned = folders.length > 0 && folders.every((item) => isPinned(item, order));
  return {
    ...order,
    pinned: allPinned ? order.pinned.filter((id) => !ids.includes(id)) : [...new Set([...order.pinned, ...ids])],
  };
}

export function messageOf(error: unknown): string {
  return errorText(error);
}

export function formatFileDate(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "—";
  const today = new Date();
  const sameDay =
    date.getFullYear() === today.getFullYear() &&
    date.getMonth() === today.getMonth() &&
    date.getDate() === today.getDate();
  return new Intl.DateTimeFormat(
    "zh-CN",
    sameDay
      ? { hour: "2-digit", minute: "2-digit" }
      : {
          year: date.getFullYear() === today.getFullYear() ? undefined : "numeric",
          month: "2-digit",
          day: "2-digit",
          hour: "2-digit",
          minute: "2-digit",
        },
  ).format(date);
}

// isViewable says whether the file manager's viewer can show a file.
export function isViewable(name: string): boolean {
  return isPreviewableImage(name) || isPDFPreviewable(name);
}

export function fileSortLabel(sort: FileSort): string {
  return { manual: "手动排序", name: "名称", modifiedAt: "修改时间", type: "类型", sizeBytes: "大小" }[sort];
}

export function duplicateFileName(name: string): string {
  const dot = name.lastIndexOf(".");
  if (dot <= 0) return `${name} - 副本`;
  return `${name.slice(0, dot)} - 副本${name.slice(dot)}`;
}
