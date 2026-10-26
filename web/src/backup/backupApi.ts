import { APIError, call, client } from "../api";
import type { Snapshot, SnapshotEntry, SnapshotSchedule, SnapshotScheduleSettings } from "../api.generated";
import { nameNotPortableText, nameTooLongText } from "../fileNames";

export type {
  RemovedEntry,
  RetentionTiers,
  ScheduleRun,
  Snapshot,
  SnapshotEntry,
  SnapshotMatch,
  SnapshotSchedule,
  SnapshotVersion,
} from "../api.generated";
export type SnapshotOrigin = Snapshot["origin"];
// How the space differs now at a snapshot entry's path: removed (deleted,
// moved or renamed), modified, or a folder with changes inside it.
export type SnapshotChange = NonNullable<SnapshotEntry["change"]>;
// The settings the backup center edits: those of a schedule as read, which
// it saves back whole.
export type ScheduleSettings = Pick<SnapshotSchedule, keyof SnapshotScheduleSettings>;
export type Frequency = SnapshotSchedule["frequency"];
export type Retention = SnapshotSchedule["retention"];

export const maxKeep = 100;

const space = (spaceId: string) => ({ params: { path: { spaceId } } });
const snapshot = (snapshotId: string) => ({ params: { path: { snapshotId } } });

export const listSnapshots = async (spaceId: string) =>
  (await call(client.GET("/api/v1/spaces/{spaceId}/snapshots", space(spaceId)))).items;
export const createSnapshot = (spaceId: string, name: string) =>
  call(client.POST("/api/v1/spaces/{spaceId}/snapshots", { ...space(spaceId), body: { name } }));
export const deleteSnapshot = (snapshotId: string) =>
  call(client.DELETE("/api/v1/snapshots/{snapshotId}", snapshot(snapshotId)));
export const keepSnapshot = (snapshotId: string) =>
  call(client.POST("/api/v1/snapshots/{snapshotId}/keep", snapshot(snapshotId)));
export const listSnapshotEntries = async (snapshotId: string, parentId = "") =>
  (
    await call(
      client.GET("/api/v1/snapshots/{snapshotId}/entries", { params: { path: { snapshotId }, query: { parentId } } }),
    )
  ).items;
export const snapshotDownloadURL = (snapshotId: string, entryId: string) =>
  `/api/v1/snapshots/${encodeURIComponent(snapshotId)}/entries/${encodeURIComponent(entryId)}/content`;
// restoreEntry copies a file or a whole folder into parentId of the
// snapshot's space. With replace, a file of that name goes to the recycle bin
// and the restored file takes its place.
export const restoreEntry = (snapshotId: string, entryId: string, parentId: string, name: string, replace = false) =>
  call(
    client.POST("/api/v1/snapshots/{snapshotId}/entries/{entryId}/restore", {
      params: { path: { snapshotId, entryId } },
      body: replace ? { parentId, name, replace } : { parentId, name },
    }),
  );
// restoreSnapshot copies everything a snapshot holds into a new folder.
export const restoreSnapshot = (snapshotId: string, parentId: string, name: string) =>
  call(client.POST("/api/v1/snapshots/{snapshotId}/restore", { ...snapshot(snapshotId), body: { parentId, name } }));
export const listRemovedEntries = (spaceId: string, signal?: AbortSignal) =>
  call(client.GET("/api/v1/spaces/{spaceId}/removed-entries", { ...space(spaceId), signal }));
// listVersions reads the versions snapshots hold of a file in the space.
export const listVersions = (fileId: string) =>
  call(client.GET("/api/v1/files/{fileId}/versions", { params: { path: { fileId } } }));
export const getSchedule = (spaceId: string) =>
  call(client.GET("/api/v1/spaces/{spaceId}/snapshot-schedule", space(spaceId)));
export const saveSchedule = (spaceId: string, settings: ScheduleSettings) =>
  call(client.PUT("/api/v1/spaces/{spaceId}/snapshot-schedule", { ...space(spaceId), body: settings }));
export const searchSnapshots = (spaceId: string, query: string, signal?: AbortSignal) =>
  call(
    client.GET("/api/v1/spaces/{spaceId}/snapshot-search", {
      params: { path: { spaceId }, query: { q: query } },
      signal,
    }),
  );

// messageOf words a failed request for people; the server's messages are for logs.
export function messageOf(error: unknown): string {
  if (error instanceof APIError) {
    switch (error.code) {
      case "forbidden":
        return "没有权限执行这项操作";
      case "not_found":
        return "快照或文件已不存在，请刷新后重试";
      case "name_conflict":
        return "目标位置已有同名项目";
      case "volume_unavailable":
        return "数据卷当前不可用，请检查存储状态";
      case "insufficient_storage":
        return "数据卷剩余空间不足";
      case "validation_failed":
        return "填写的内容不符合要求";
      case "name_too_long":
        return nameTooLongText;
      case "name_not_portable":
        return nameNotPortableText;
      case "network_error":
        return "网络连接中断";
    }
    return error.status >= 500 ? "服务暂时不可用，请稍后重试" : error.message;
  }
  if (error instanceof DOMException && error.name === "AbortError") return "";
  return "请求失败，请稍后重试";
}

export const isVolumeOffline = (error: unknown) => error instanceof APIError && error.code === "volume_unavailable";
