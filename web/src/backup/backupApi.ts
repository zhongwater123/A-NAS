import { APIError, request, type FileEntry } from "../api";
import { nameNotPortableText, nameTooLongText } from "../fileNames";

export type SnapshotOrigin = "manual" | "scheduled";

export interface Snapshot {
  id: string;
  spaceId: string;
  name: string;
  origin: SnapshotOrigin;
  createdBy: string;
  createdAt: string;
  // Totals of the files held, not the space the snapshot takes on disk.
  fileCount: number;
  sizeBytes: number;
}

export interface SnapshotEntry {
  id: string;
  snapshotId: string;
  parentId?: string;
  name: string;
  kind: "file" | "directory";
  sizeBytes: number;
  modifiedAt?: string;
}

export type Frequency = "hourly" | "daily" | "weekly";

export interface ScheduleSettings {
  enabled: boolean;
  frequency: Frequency;
  // Local "HH:MM"; hourly schedules use only the minute.
  time: string;
  // 0 is Sunday, like Date#getDay.
  weekday: number;
  keep: number;
}

export interface ScheduleRun {
  at: string;
  status: "succeeded" | "failed";
  error?: "volume_unavailable" | "space_unavailable" | "snapshot_failed";
  snapshotId?: string;
}

export interface SnapshotSchedule extends ScheduleSettings {
  spaceId: string;
  updatedAt?: string;
  lastRun?: ScheduleRun;
  nextRunAt?: string;
  canManage: boolean;
}

export interface SnapshotVersion {
  snapshotId: string;
  snapshotName: string;
  origin: SnapshotOrigin;
  createdAt: string;
  entryId: string;
  sizeBytes: number;
  modifiedAt?: string;
  // How many snapshots hold this same version, and when the oldest was taken.
  snapshots: number;
  oldestAt: string;
}

export interface SnapshotMatch {
  path: string;
  name: string;
  kind: "file" | "directory";
  inSpace: boolean;
  versions: SnapshotVersion[];
}

export const maxKeep = 100;

const json = (method: string, value: unknown): RequestInit => ({ method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(value) });
const space = (id: string) => `/api/v1/spaces/${encodeURIComponent(id)}`;
const snapshot = (id: string) => `/api/v1/snapshots/${encodeURIComponent(id)}`;

export const listSnapshots = async (spaceId: string) => (await request<{ items: Snapshot[] }>(`${space(spaceId)}/snapshots`)).items;
export const createSnapshot = (spaceId: string, name: string) => request<Snapshot>(`${space(spaceId)}/snapshots`, json("POST", { name }), true);
export const deleteSnapshot = (id: string) => request<void>(snapshot(id), { method: "DELETE" }, true);
export const keepSnapshot = (id: string) => request<Snapshot>(`${snapshot(id)}/keep`, { method: "POST" }, true);
export const listSnapshotEntries = async (id: string, parentId = "") =>
  (await request<{ items: SnapshotEntry[] }>(`${snapshot(id)}/entries?parentId=${encodeURIComponent(parentId)}`)).items;
export const snapshotDownloadURL = (snapshotId: string, entryId: string) => `${snapshot(snapshotId)}/entries/${encodeURIComponent(entryId)}/content`;
// restoreEntry copies a file or a whole folder into parentId of the snapshot's space.
export const restoreEntry = (snapshotId: string, entryId: string, parentId: string, name: string) =>
  request<FileEntry>(`${snapshot(snapshotId)}/entries/${encodeURIComponent(entryId)}/restore`, json("POST", { parentId, name }), true);
export const getSchedule = (spaceId: string) => request<SnapshotSchedule>(`${space(spaceId)}/snapshot-schedule`);
export const saveSchedule = (spaceId: string, settings: ScheduleSettings) => request<SnapshotSchedule>(`${space(spaceId)}/snapshot-schedule`, json("PUT", settings), true);
export const searchSnapshots = (spaceId: string, query: string, signal?: AbortSignal) =>
  request<{ items: SnapshotMatch[]; truncated: boolean }>(`${space(spaceId)}/snapshot-search?q=${encodeURIComponent(query)}`, { signal });

// messageOf words a failed request for people; the server's messages are for logs.
export function messageOf(error: unknown): string {
  if (error instanceof APIError) {
    switch (error.code) {
      case "forbidden": return "没有权限执行这项操作";
      case "not_found": return "快照或文件已不存在，请刷新后重试";
      case "name_conflict": return "目标位置已有同名项目";
      case "volume_unavailable": return "数据卷当前不可用，请检查存储状态";
      case "insufficient_storage": return "数据卷剩余空间不足";
      case "validation_failed": return "填写的内容不符合要求";
      case "name_too_long": return nameTooLongText;
      case "name_not_portable": return nameNotPortableText;
      case "network_error": return "网络连接中断";
    }
    return error.status >= 500 ? "服务暂时不可用，请稍后重试" : error.message;
  }
  if (error instanceof DOMException && error.name === "AbortError") return "";
  return "请求失败，请稍后重试";
}

export const isVolumeOffline = (error: unknown) => error instanceof APIError && error.code === "volume_unavailable";
