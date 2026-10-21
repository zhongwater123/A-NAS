import { useSyncExternalStore } from "react";

import { createDirectory, type FileEntry, listEntries, listSpaces, type Space } from "../api";
import { failureText } from "../transfers/model";
import { type LocalUpload, transferStore } from "../transfers/store";
import { getTransfer } from "../transfers/transfersApi";
import { importRecording, type ImportOptions } from "./voiceApi";

// The folder of the personal space uploads from the computer go to.
export const uploadFolder = "录音";

// The extensions the pickers offer; the NAS accepts any file it finds audio in.
export const audioExtensions = [".mp3", ".m4a", ".aac", ".wav", ".flac", ".ogg", ".opus", ".oga", ".amr", ".wma", ".webm", ".mp4", ".mov", ".mkv", ".3gp", ".aiff", ".aif", ".caf", ".ape", ".m4b"];
export const isAudioName = (name: string) => audioExtensions.some((extension) => name.toLowerCase().endsWith(extension));

export type PendingState = "uploading" | "importing" | "failed";
export interface PendingImport { key: string; name: string; size: number; progress: number; state: PendingState; error?: string }

type Options = Omit<ImportOptions, "fileId">;
interface Tracked { upload: string; name: string; size: number; options: Options; state: PendingState; error?: string }

// ImportQueue sends files from the computer through the transfer center's
// resumable queue into 个人空间/录音 and imports each as soon as it lands.
// It lives outside the window, so closing 语音处理 does not stop it.
class ImportQueue {
  private tracked = new Map<string, Tracked>();
  private listeners = new Set<() => void>();
  private snapshot: PendingImport[] = [];
  private imported = new Set<() => void>();
  private unsubscribe?: () => void;

  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => { this.listeners.delete(listener); };
  };

  getSnapshot = () => this.snapshot;

  // onImported is told whenever a new recording was imported.
  onImported(listener: () => void) {
    this.imported.add(listener);
    return () => { this.imported.delete(listener); };
  }

  async upload(files: File[], options: Options, userID: string) {
    const target = await uploadTarget(userID);
    const batch = transferStore.enqueue(files.map((file) => ({ file })), target, { choice: "rename" });
    const queued = transferStore.getSnapshot().uploads.filter((upload) => upload.batch === batch);
    queued.forEach((upload, index) => {
      this.tracked.set(upload.key, { upload: upload.key, name: files[index].name, size: files[index].size, options, state: "uploading" });
    });
    this.follow();
    this.publish();
  }

  dismiss(key: string) {
    this.tracked.delete(key);
    this.publish();
  }

  private follow() {
    this.unsubscribe ??= transferStore.subscribe(() => this.publish());
  }

  private publish() {
    const uploads = new Map(transferStore.getSnapshot().uploads.map((upload) => [upload.key, upload]));
    for (const [key, entry] of this.tracked) {
      const upload = uploads.get(key);
      if (entry.state !== "uploading") continue;
      if (!upload || upload.phase === "canceled" || upload.phase === "skipped") this.tracked.delete(key);
      else if (upload.phase === "failed") Object.assign(entry, { state: "failed", error: failureText(upload.error?.code ?? "") });
      else if (upload.phase === "completed") {
        entry.state = "importing";
        void this.finish(key, upload);
      }
    }
    this.snapshot = [...this.tracked.entries()].map(([key, entry]) => {
      const upload = uploads.get(key);
      const progress = entry.state === "uploading" && upload?.size ? Math.min(1, upload.transferred / upload.size) : entry.state === "importing" ? 1 : 0;
      return { key, name: entry.name, size: entry.size, progress, state: entry.state, error: entry.error };
    });
    if (!this.tracked.size && this.unsubscribe) {
      this.unsubscribe();
      this.unsubscribe = undefined;
    }
    for (const listener of this.listeners) listener();
  }

  private async finish(key: string, upload: LocalUpload) {
    const entry = this.tracked.get(key);
    if (!entry) return;
    try {
      if (!upload.transferId) throw new Error("上传记录丢失");
      const transfer = await getTransfer(upload.transferId);
      if (!transfer.entryId) throw new Error("上传的文件没有找到");
      await importRecording({ ...entry.options, fileId: transfer.entryId });
      this.tracked.delete(key);
      for (const listener of this.imported) listener();
    } catch (caught) {
      Object.assign(entry, { state: "failed", error: caught instanceof Error ? caught.message : "导入失败" });
    }
    this.publish();
  }
}

export const importQueue = new ImportQueue();

export function usePendingImports() {
  return useSyncExternalStore(importQueue.subscribe, importQueue.getSnapshot);
}

// personalSpace is the signed-in member's own space.
export function personalSpace(spaces: Space[], userID: string) {
  return spaces.find((space) => space.kind === "private" && space.ownerUserId === userID && !space.viewing)
    ?? spaces.find((space) => space.kind === "private" && !space.viewing);
}

async function uploadTarget(userID: string) {
  const space = personalSpace(await listSpaces(), userID);
  if (!space) throw new Error("找不到你的个人空间");
  const find = async () => (await listEntries(space.id)).find((entry) => entry.kind === "directory" && entry.name === uploadFolder);
  let folder: FileEntry | undefined = await find();
  if (!folder) {
    // Another window may create it at the same moment.
    try { folder = await createDirectory(space.id, "", uploadFolder); } catch (caught) { folder = await find(); if (!folder) throw caught; }
  }
  return { spaceId: space.id, spaceKind: "private" as const, parentId: folder.id, folders: [{ id: folder.id, name: uploadFolder }], label: `语音处理 · ${uploadFolder}` };
}
