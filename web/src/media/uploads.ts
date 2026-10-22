import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { failureText } from "../transfers/model";
import { type LocalUpload, transferStore, useTransfers } from "../transfers/store";

// The files a library scan reads: the scanner's video extensions plus the
// subtitles, NFO documents and artwork it picks up beside them.
const videoTypes = [
  ".mp4",
  ".m4v",
  ".mkv",
  ".webm",
  ".mov",
  ".avi",
  ".wmv",
  ".flv",
  ".f4v",
  ".ts",
  ".m2ts",
  ".mts",
  ".mpg",
  ".mpeg",
  ".rmvb",
  ".rm",
  ".3gp",
  ".vob",
  ".ogv",
  ".asf",
  ".divx",
];
const subtitleTypes = [".srt", ".ass", ".ssa", ".vtt"];
const imageTypes = [".jpg", ".jpeg", ".png", ".webp"];
export const acceptedTypes = [...videoTypes, ...subtitleTypes, ".nfo", ...imageTypes].join(",");

export type FileRole = "video" | "subtitle" | "image" | "nfo" | "other";
export function roleOf(name: string): FileRole {
  const dot = name.lastIndexOf(".");
  const extension = dot < 0 ? "" : name.slice(dot).toLowerCase();
  if (videoTypes.includes(extension)) return "video";
  if (subtitleTypes.includes(extension)) return "subtitle";
  if (imageTypes.includes(extension)) return "image";
  return extension === ".nfo" ? "nfo" : "other";
}

// Picked is a file with the folders it sat in when a whole folder was
// chosen or dropped ("" for a single file).
export interface Picked {
  file: File;
  folder: string;
}

export function pickedFromList(files: ArrayLike<File>): Picked[] {
  return Array.from(files).map((file) => ({
    file,
    folder: (file.webkitRelativePath ?? "").split("/").filter(Boolean).slice(0, -1).join("/"),
  }));
}

// pickedFromDrop reads dropped files and folders. The entries must be taken
// while the drop event runs; reading them may finish later.
export function pickedFromDrop(transfer: DataTransfer): Promise<Picked[]> {
  const items = Array.from(transfer.items ?? []).filter((item) => item.kind === "file");
  const entries = items.map((item) => item.webkitGetAsEntry?.() ?? null);
  if (!entries.length || entries.some((entry) => !entry)) return Promise.resolve(pickedFromList(transfer.files));
  return Promise.all(entries.map((entry) => walk(entry!, ""))).then((lists) => lists.flat());
}

async function walk(entry: FileSystemEntry, folder: string): Promise<Picked[]> {
  if (entry.isFile) {
    const file = await new Promise<File>((resolve, reject) => (entry as FileSystemFileEntry).file(resolve, reject));
    return [{ file, folder }];
  }
  if (!entry.isDirectory) return [];
  // A directory reader returns its entries in batches until an empty one.
  const reader = (entry as FileSystemDirectoryEntry).createReader();
  const children: FileSystemEntry[] = [];
  for (;;) {
    const batch = await new Promise<FileSystemEntry[]>((resolve, reject) => reader.readEntries(resolve, reject));
    if (!batch.length) break;
    children.push(...batch);
  }
  const inside = folder ? `${folder}/${entry.name}` : entry.name;
  return (await Promise.all(children.map((child) => walk(child, inside)))).flat();
}

// Where a batch goes: a folder of a library, by its file entry.
export interface Destination {
  libraryId: string;
  libraryName: string;
  spaceId: string;
  spaceKind: "private" | "shared";
  parentId: string;
  place: string;
}

export type UploadState = "ready" | "waiting" | "uploading" | "done" | "exists" | "skipped" | "failed" | "canceled";
export interface UploadItem {
  id: number;
  file: File;
  folder: string;
  role: FileRole;
  state: UploadState;
  progress: number;
  error?: string;
  to?: Destination;
}

const active = (item: UploadItem) => item.state === "waiting" || item.state === "uploading";
const hidden = (picked: Picked) =>
  [...picked.folder.split("/"), picked.file.name].some((part) => part.startsWith(".")) ||
  /^(thumbs\.db|desktop\.ini)$/i.test(picked.file.name);

export interface Uploads {
  items: UploadItem[];
  running: boolean;
  add: (picked: Picked[]) => void;
  start: (to: Destination) => void;
  remove: (id: number) => void;
  cancel: (id: number) => void;
  cancelAll: () => void;
  retry: (id: number) => void;
  // clear drops everything that is not uploading or waiting to.
  clear: () => void;
}

// Staged is a picked file: ready until the user chooses where it goes, then
// a file of the transfer center's queue under key.
interface Staged {
  id: number;
  file: File;
  folder: string;
  role: FileRole;
  key?: string;
  to?: Destination;
}

// stateOf tells a queued upload's state in the media center's words.
function stateOf(upload: LocalUpload): Pick<UploadItem, "state" | "progress" | "error"> {
  const progress = upload.size ? Math.min(1, upload.transferred / upload.size) : 0;
  switch (upload.phase) {
    case "running":
      return { state: "uploading", progress };
    case "completed":
      return { state: "done", progress: 1 };
    case "skipped":
      return { state: "exists", progress: 0 };
    case "failed":
      return {
        state: "failed",
        progress: 0,
        error: upload.error?.code === "forbidden" ? "没有权限上传到这个文件夹" : failureText(upload.error?.code ?? ""),
      };
    case "canceled":
      return { state: "canceled", progress: 0 };
    default:
      return { state: "waiting", progress };
  }
}

// useUploads sends the chosen files through the transfer center's queue: the
// whole batch shows there at once, three files go in parallel in resumable
// parts, and folders of a folder upload are created by the NAS as the
// signed-in user. Files that already exist are left alone, never replaced
// or duplicated; the media center rescans the library once a file lands.
export function useUploads({
  onUploaded,
  onSettled,
}: {
  onUploaded: (item: UploadItem) => void;
  onSettled: (items: UploadItem[]) => void;
}): Uploads {
  const snapshot = useTransfers();
  const [staged, setStaged] = useState<Staged[]>([]);
  const next = useRef(0);
  const callbacks = useRef({ onUploaded, onSettled });
  callbacks.current = { onUploaded, onSettled };

  const items = useMemo(() => {
    const queued = new Map(snapshot.uploads.map((upload) => [upload.key, upload]));
    return staged.map((entry): UploadItem => {
      const base = { id: entry.id, file: entry.file, folder: entry.folder, role: entry.role, to: entry.to };
      if (!entry.key) return { ...base, state: entry.role === "other" ? "skipped" : "ready", progress: 0 };
      const upload = queued.get(entry.key);
      // Signing out empties the queue.
      return upload ? { ...base, ...stateOf(upload) } : { ...base, state: "canceled", progress: 0 };
    });
  }, [staged, snapshot.uploads]);
  const running = items.some(active);

  // Each finished file is announced once; the batch when nothing is left.
  const announced = useRef(new Set<number>());
  const settled = useRef<UploadItem[]>([]);
  useEffect(() => {
    for (const item of items) {
      if (!item.to || active(item) || announced.current.has(item.id)) continue;
      announced.current.add(item.id);
      settled.current.push(item);
      if (item.state === "done") callbacks.current.onUploaded(item);
    }
    if (!running && settled.current.length) {
      const batch = settled.current;
      settled.current = [];
      callbacks.current.onSettled(batch);
    }
  }, [items, running]);

  const add = useCallback(
    (picked: Picked[]) =>
      setStaged((current) => {
        const known = new Set(
          current
            .filter((entry) => !entry.key || items.some((item) => item.id === entry.id && active(item)))
            .map((entry) => `${entry.folder}/${entry.file.name}/${entry.file.size}`),
        );
        const added: Staged[] = [];
        for (const entry of picked) {
          const key = `${entry.folder}/${entry.file.name}/${entry.file.size}`;
          if (hidden(entry) || known.has(key)) continue;
          known.add(key);
          added.push({ id: ++next.current, file: entry.file, folder: entry.folder, role: roleOf(entry.file.name) });
        }
        return added.length ? [...current, ...added] : current;
      }),
    [items],
  );

  const latest = useRef(staged);
  latest.current = staged;
  const start = useCallback((to: Destination) => {
    const ready = latest.current.filter((entry) => !entry.key && entry.role !== "other");
    if (!ready.length) return;
    const batch = transferStore.enqueue(
      ready.map((entry) => ({ file: entry.file, directories: entry.folder ? entry.folder.split("/") : [] })),
      {
        spaceId: to.spaceId,
        spaceKind: to.spaceKind,
        parentId: to.parentId,
        folders: [],
        label: `影视中心 · ${to.place}`,
      },
      { choice: "skip" },
    );
    // The queue keeps the batch in the order it was given.
    const keys = new Map(
      transferStore
        .getSnapshot()
        .uploads.filter((upload) => upload.batch === batch)
        .map((upload, index) => [ready[index].id, upload.key]),
    );
    setStaged((current) =>
      current.map((entry) => (keys.has(entry.id) ? { ...entry, key: keys.get(entry.id), to } : entry)),
    );
  }, []);

  const keyOf = (id: number) => staged.find((entry) => entry.id === id)?.key;
  return {
    items,
    running,
    add,
    start,
    remove: (id) =>
      setStaged((current) =>
        current.filter((entry) => entry.id !== id || items.some((item) => item.id === id && active(item))),
      ),
    cancel: (id) => {
      const key = keyOf(id);
      if (key) transferStore.cancel(key);
    },
    cancelAll: () => {
      for (const item of items)
        if (active(item)) {
          const key = keyOf(item.id);
          if (key) transferStore.cancel(key);
        }
    },
    retry: (id) => {
      const key = keyOf(id);
      if (key) transferStore.retry(key);
    },
    clear: () =>
      setStaged((current) => current.filter((entry) => items.some((item) => item.id === entry.id && active(item)))),
  };
}
