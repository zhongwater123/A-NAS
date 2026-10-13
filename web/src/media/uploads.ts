import { useCallback, useEffect, useRef, useState } from "react";

import { APIError, createDirectory, listEntries, sendFile } from "../api";
import { transferStore } from "../transfers/store";
import { messageOf } from "./Browse";

// The files a library scan reads: the scanner's video extensions plus the
// subtitles, NFO documents and artwork it picks up beside them.
const videoTypes = [".mp4", ".m4v", ".mkv", ".webm", ".mov", ".avi", ".wmv", ".flv", ".ts", ".m2ts", ".mts", ".mpg", ".mpeg", ".rmvb", ".rm", ".3gp", ".vob", ".ogv", ".asf", ".divx"];
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
export interface Picked { file: File; folder: string }

export function pickedFromList(files: ArrayLike<File>): Picked[] {
  return Array.from(files).map((file) => ({ file, folder: (file.webkitRelativePath ?? "").split("/").filter(Boolean).slice(0, -1).join("/") }));
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
export interface Destination { libraryId: string; libraryName: string; spaceId: string; parentId: string; place: string }

export type UploadState = "ready" | "waiting" | "uploading" | "done" | "exists" | "skipped" | "failed" | "canceled";
export interface UploadItem { id: number; file: File; folder: string; role: FileRole; state: UploadState; progress: number; error?: string; to?: Destination }

const active = (item: UploadItem) => item.state === "waiting" || item.state === "uploading";
const hidden = (picked: Picked) => [...picked.folder.split("/"), picked.file.name].some((part) => part.startsWith(".")) || /^(thumbs\.db|desktop\.ini)$/i.test(picked.file.name);

// ensureFolder returns the ID of a folder, creating it when it is missing.
async function ensureFolder(spaceId: string, parentId: string, name: string): Promise<string> {
  try {
    return (await createDirectory(spaceId, parentId, name)).id;
  } catch (caught) {
    if (!(caught instanceof APIError && caught.status === 409)) throw caught;
    const existing = (await listEntries(spaceId, parentId)).find((entry) => entry.kind === "directory" && entry.name === name);
    if (!existing) throw caught;
    return existing.id;
  }
}

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

// useUploads uploads one file at a time through the file service, as the
// signed-in user, so the space's permissions decide and the media center
// rescans the library on its own once a file lands.
export function useUploads({ onUploaded, onSettled }: { onUploaded: (item: UploadItem) => void; onSettled: (items: UploadItem[]) => void }): Uploads {
  const list = useRef<UploadItem[]>([]);
  const [, setVersion] = useState(0);
  const next = useRef(0);
  const run = useRef({ running: false, live: true, controller: undefined as AbortController | undefined, current: -1, folders: new Map<string, Promise<string>>() });
  const callbacks = useRef({ onUploaded, onSettled });
  callbacks.current = { onUploaded, onSettled };

  const commit = useCallback((items: UploadItem[]) => { list.current = items; setVersion((value) => value + 1); }, []);
  const patch = useCallback((id: number, change: Partial<UploadItem>) => commit(list.current.map((item) => (item.id === id ? { ...item, ...change } : item))), [commit]);

  const folderFor = useCallback((to: Destination, folder: string): Promise<string> => {
    if (!folder) return Promise.resolve(to.parentId);
    const key = `${to.spaceId}\n${to.parentId}\n${folder}`;
    const known = run.current.folders.get(key);
    if (known) return known;
    const slash = folder.lastIndexOf("/");
    const made = folderFor(to, slash < 0 ? "" : folder.slice(0, slash)).then((parent) => ensureFolder(to.spaceId, parent, folder.slice(slash + 1)));
    run.current.folders.set(key, made);
    made.catch(() => run.current.folders.delete(key));
    return made;
  }, []);

  const drain = useCallback(async () => {
    const state = run.current;
    if (state.running) return;
    state.running = true;
    setVersion((value) => value + 1);
    const settled: UploadItem[] = [];
    for (;;) {
      const item = list.current.find((candidate) => candidate.state === "waiting");
      if (!item || !item.to || !state.live) break;
      const controller = new AbortController();
      state.controller = controller;
      state.current = item.id;
      patch(item.id, { state: "uploading", progress: 0, error: undefined });
      // The transfer center lists media uploads beside file uploads.
      const tracked = transferStore.trackExternal({
        key: `media-${item.id}`, app: "media", name: item.file.name, size: item.file.size,
        label: `影视中心 · ${item.to.libraryName}${item.folder ? ` · ${item.folder}` : ""}`,
      });
      let result: Partial<UploadItem>;
      try {
        const parent = await folderFor(item.to, item.folder);
        if (controller.signal.aborted) throw new DOMException("upload aborted", "AbortError");
        await sendFile(item.to.spaceId, parent, item.file, (fraction) => { patch(item.id, { progress: fraction }); tracked.progress(fraction); }, controller.signal);
        result = { state: "done", progress: 1 };
        tracked.done();
      } catch (caught) {
        if (controller.signal.aborted) result = { state: "canceled", progress: 0 };
        else if (caught instanceof APIError && caught.status === 409) result = { state: "exists", progress: 0 };
        else result = { state: "failed", progress: 0, error: caught instanceof APIError && caught.status === 403 ? "没有权限上传到这个文件夹" : messageOf(caught) };
        if (result.state === "canceled") tracked.cancel();
        else tracked.fail(result.state === "exists" ? "同名文件已存在，未覆盖" : result.error ?? "上传失败");
      }
      if (!state.live) break;
      patch(item.id, result);
      const finished = list.current.find((candidate) => candidate.id === item.id)!;
      settled.push(finished);
      if (finished.state === "done") callbacks.current.onUploaded(finished);
    }
    state.running = false;
    state.controller = undefined;
    state.current = -1;
    state.folders.clear();
    if (!state.live) return;
    setVersion((value) => value + 1);
    if (settled.length) callbacks.current.onSettled(settled);
  }, [folderFor, patch]);

  useEffect(() => {
    const state = run.current;
    state.live = true;
    return () => { state.live = false; state.controller?.abort(); };
  }, []);

  // Leaving the page would drop what is still uploading.
  const running = run.current.running;
  useEffect(() => {
    if (!running) return;
    const warn = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ""; };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [running]);

  const add = useCallback((picked: Picked[]) => {
    const known = new Set(list.current.filter((item) => item.state === "ready" || active(item)).map((item) => `${item.folder}/${item.file.name}/${item.file.size}`));
    const added: UploadItem[] = [];
    for (const entry of picked) {
      const key = `${entry.folder}/${entry.file.name}/${entry.file.size}`;
      if (hidden(entry) || known.has(key)) continue;
      known.add(key);
      const role = roleOf(entry.file.name);
      added.push({ id: ++next.current, file: entry.file, folder: entry.folder, role, state: role === "other" ? "skipped" : "ready", progress: 0 });
    }
    if (added.length) commit([...list.current, ...added]);
  }, [commit]);

  const start = useCallback((to: Destination) => {
    commit(list.current.map((item) => (item.state === "ready" ? { ...item, state: "waiting", to } : item)));
    void drain();
  }, [commit, drain]);

  const cancel = useCallback((id: number) => {
    const item = list.current.find((candidate) => candidate.id === id);
    if (item?.state === "uploading" && run.current.current === id) run.current.controller?.abort();
    else if (item?.state === "waiting") patch(id, { state: "canceled" });
  }, [patch]);

  return {
    items: list.current,
    running,
    add,
    start,
    remove: useCallback((id: number) => commit(list.current.filter((item) => item.id !== id || active(item))), [commit]),
    cancel,
    cancelAll: useCallback(() => {
      commit(list.current.map((item) => (item.state === "waiting" ? { ...item, state: "canceled" } : item)));
      run.current.controller?.abort();
    }, [commit]),
    retry: useCallback((id: number) => {
      commit(list.current.map((item) => (item.id === id && item.to && (item.state === "failed" || item.state === "canceled") ? { ...item, state: "waiting", progress: 0, error: undefined } : item)));
      void drain();
    }, [commit, drain]),
    clear: useCallback(() => commit(list.current.filter(active)), [commit]),
  };
}
