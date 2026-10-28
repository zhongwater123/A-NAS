import { errorCodeText } from "../api/errorMessages";
import { formatRate } from "../lib/bytes";
import type { Transfer, TransferDirection, TransferLocation } from "./api";
import type { ExternalUpload, Hold, LocalUpload, TransferRates, TransferSnapshot, UploadPhase } from "./store";

export type ItemStatus =
  | "queued"
  | "pending"
  | "running"
  | "paused"
  | "waiting"
  | "conflict"
  | "interrupted"
  | "completed"
  | "failed"
  | "canceled"
  | "skipped";

// TransferItem is one row: this browser's upload, a transfer the NAS
// reports, or another app's upload.
export interface TransferItem {
  id: string;
  direction: TransferDirection;
  kind: "file" | "archive";
  name: string;
  location?: TransferLocation;
  // label replaces the location for another app's upload.
  label?: string;
  relativePath?: string;
  total: number;
  transferred: number;
  speed: number;
  status: ItemStatus;
  failure?: string;
  hold?: Hold;
  savedName?: string;
  replaced?: boolean;
  restarted?: boolean;
  skipped?: number;
  createdAt: number;
  finishedAt?: number;
  batch?: string;
  local?: LocalUpload;
  server?: Transfer;
  external?: ExternalUpload;
}

export interface TransferGroup {
  key: string;
  direction: TransferDirection;
  // batch is set for the uploads one action started.
  batch?: string;
  items: TransferItem[];
  location?: TransferLocation;
  // label names the place instead of location, for other apps' uploads.
  label?: string;
  total: number;
  transferred: number;
  speed: number;
  createdAt: number;
  finishedAt?: number;
  counts: Partial<Record<ItemStatus, number>>;
}

const activeStatuses: ItemStatus[] = ["queued", "pending", "running", "paused", "waiting", "conflict", "interrupted"];
export const isActive = (status: ItemStatus) => activeStatuses.includes(status);
// Failed uploads of this browser stay with the active ones until retried or
// dismissed; the NAS's history keeps the rest.
export const isCurrent = (item: TransferItem) =>
  isActive(item.status) || (item.status === "failed" && Boolean(item.local || item.external));

const phaseStatus: Record<UploadPhase, ItemStatus> = {
  queued: "queued",
  running: "running",
  paused: "paused",
  waiting: "waiting",
  conflict: "conflict",
  completed: "completed",
  failed: "failed",
  canceled: "canceled",
  skipped: "skipped",
};

export function transferItems(snapshot: TransferSnapshot): TransferItem[] {
  const server = new Map(snapshot.server.map((transfer) => [transfer.id, transfer]));
  const owned = new Set<string>();
  const items: TransferItem[] = snapshot.uploads.map((upload) => {
    const remote = upload.transferId ? server.get(upload.transferId) : undefined;
    for (const id of [upload.transferId, ...(upload.superseded ?? [])]) if (id) owned.add(id);
    return {
      // The NAS knows the folders a folder upload created; before it answers,
      // the target stands in.
      id: upload.key,
      direction: "upload",
      kind: "file",
      name: upload.name,
      location: remote?.location ?? upload.target,
      label: remote ? undefined : upload.target.label,
      relativePath: upload.directories.join("/") || undefined,
      total: upload.size,
      transferred: upload.transferred,
      speed: upload.speed,
      status: phaseStatus[upload.phase],
      failure: upload.error ? failureText(upload.error.code) : undefined,
      hold: upload.hold,
      savedName: upload.savedName ?? remote?.savedName,
      replaced: upload.replaced ?? remote?.replaced,
      restarted: upload.restarted,
      createdAt: upload.createdAt,
      finishedAt: upload.finishedAt,
      batch: upload.batch,
      local: upload,
      server: remote,
    };
  });
  for (const transfer of snapshot.server) {
    if (owned.has(transfer.id)) continue;
    items.push({
      id: transfer.id,
      direction: transfer.direction,
      kind: transfer.kind,
      name: transfer.name,
      location: transfer.location,
      relativePath: transfer.relativePath,
      total: transfer.totalBytes,
      transferred: transfer.transferredBytes,
      speed: transfer.bytesPerSecond,
      status: transfer.state,
      failure: transfer.error ? failureText(transfer.error.code) : undefined,
      savedName: transfer.savedName,
      replaced: transfer.replaced,
      skipped: transfer.skipped,
      createdAt: Date.parse(transfer.createdAt) || 0,
      finishedAt: transfer.finishedAt ? Date.parse(transfer.finishedAt) : undefined,
      batch: transfer.batch,
      server: transfer,
    });
  }
  for (const upload of snapshot.external) {
    items.push({
      id: upload.key,
      direction: "upload",
      kind: "file",
      name: upload.name,
      label: upload.label,
      total: upload.size,
      transferred: Math.round(upload.size * upload.progress),
      speed: 0,
      status: upload.phase,
      failure: upload.error,
      createdAt: upload.createdAt,
      finishedAt: upload.finishedAt,
      batch: upload.batch,
      external: upload,
    });
  }
  return items.sort((left, right) => right.createdAt - left.createdAt);
}

// groupItems keeps each batch together, at the place of its newest item. A
// batch this browser uploads takes its speed from batchRates, which also
// counts files too short to have a speed of their own.
export function groupItems(items: TransferItem[], batchRates: Record<string, number> = {}): TransferGroup[] {
  const groups: TransferGroup[] = [];
  const byBatch = new Map<string, TransferGroup>();
  for (const item of items) {
    const key = item.batch && item.direction === "upload" ? `batch:${item.batch}` : `item:${item.id}`;
    let group = byBatch.get(key);
    if (!group) {
      group = {
        key,
        direction: item.direction,
        batch: key.startsWith("batch:") ? item.batch : undefined,
        items: [],
        location: item.location,
        label: item.label,
        total: 0,
        transferred: 0,
        speed: 0,
        createdAt: item.createdAt,
        counts: {},
      };
      byBatch.set(key, group);
      groups.push(group);
    }
    group.items.push(item);
    group.total += item.total;
    group.transferred += item.status === "completed" ? item.total : item.transferred;
    group.speed += item.status === "running" ? item.speed : 0;
    group.counts[item.status] = (group.counts[item.status] ?? 0) + 1;
    group.createdAt = Math.min(group.createdAt, item.createdAt);
    if (item.finishedAt) group.finishedAt = Math.max(group.finishedAt ?? 0, item.finishedAt);
  }
  for (const group of groups) {
    if (group.batch && group.items.some((item) => item.local || item.external))
      group.speed = batchRates[group.batch] ?? 0;
    // Once the NAS reports a batch member, its location replaces the label.
    const located = group.items.find((item) => item.location && !item.label);
    if (located && !group.items.every((item) => item.external)) {
      group.location = located.location;
      group.label = undefined;
    }
  }
  return groups;
}

// groupStatus summarises a group for its badge.
export function groupStatus(group: TransferGroup): ItemStatus {
  for (const status of [
    "conflict",
    "running",
    "waiting",
    "queued",
    "pending",
    "interrupted",
    "paused",
    "failed",
  ] as ItemStatus[]) {
    if (group.counts[status]) return status;
  }
  if (group.counts.completed) return "completed";
  return group.items[0]?.status ?? "completed";
}

export interface TransferTotals {
  uploads: number;
  downloads: number;
  attention: number;
  uploadSpeed: number;
  downloadSpeed: number;
  remainingBytes: number;
  progress: number;
}

// transferTotals sums the transfers; the speeds come from rates, measured
// over all bytes moved, so many small files still show a speed.
export function transferTotals(
  items: TransferItem[],
  rates: TransferRates = { upload: 0, download: 0, batches: {} },
): TransferTotals {
  const current = items.filter(isCurrent);
  let total = 0;
  let done = 0;
  const totals: TransferTotals = {
    uploads: 0,
    downloads: 0,
    attention: 0,
    uploadSpeed: 0,
    downloadSpeed: 0,
    remainingBytes: 0,
    progress: 0,
  };
  for (const item of current) {
    if (item.direction === "upload") totals.uploads += 1;
    else totals.downloads += 1;
    if (
      item.status === "conflict" ||
      item.status === "failed" ||
      item.status === "waiting" ||
      (item.status === "interrupted" && !item.local)
    )
      totals.attention += 1;
  }
  totals.uploadSpeed = rates.upload;
  totals.downloadSpeed = rates.download;
  for (const item of current) {
    if (item.status === "failed" || item.status === "skipped") continue;
    total += item.total;
    done += item.transferred;
  }
  totals.remainingBytes = Math.max(0, total - done);
  totals.progress = total > 0 ? Math.min(1, done / total) : current.length ? 0 : 1;
  return totals;
}

export function locationParts(location?: TransferLocation): string[] {
  if (!location) return [];
  return [location.spaceKind === "shared" ? "共享空间" : "个人空间", ...location.folders.map((folder) => folder.name)];
}

export function formatRemaining(bytes: number, bytesPerSecond: number): string {
  if (bytesPerSecond < 1 || bytes <= 0) return "";
  const seconds = bytes / bytesPerSecond;
  if (seconds < 60) return "不到 1 分钟";
  if (seconds < 3600) return `约 ${Math.ceil(seconds / 60)} 分钟`;
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.round((seconds % 3600) / 60);
  return minutes ? `约 ${hours} 小时 ${minutes} 分钟` : `约 ${hours} 小时`;
}

// speedText is a speed and, while bytes are left, about how long they take.
export function speedText(bytesPerSecond: number, remainingBytes: number): string {
  return [formatRate(bytesPerSecond), formatRemaining(remainingBytes, bytesPerSecond)].filter(Boolean).join(" · ");
}

export function formatClock(value: number): string {
  return new Intl.DateTimeFormat("zh-CN", { hour: "2-digit", minute: "2-digit", hour12: false }).format(value);
}

// dayLabel names the day a transfer finished for the history's headings.
export function dayLabel(value: number, now = Date.now()): string {
  const day = new Date(value);
  const today = new Date(now);
  const start = (date: Date) => new Date(date.getFullYear(), date.getMonth(), date.getDate()).getTime();
  const difference = Math.round((start(today) - start(day)) / 86_400_000);
  if (difference === 0) return "今天";
  if (difference === 1) return "昨天";
  return new Intl.DateTimeFormat("zh-CN", {
    month: "long",
    day: "numeric",
    weekday: "short",
    year: day.getFullYear() === today.getFullYear() ? undefined : "numeric",
  }).format(day);
}

export function failureText(code: string): string {
  return errorCodeText(code, "transfers") ?? "传输失败";
}

export const statusText: Record<ItemStatus, string> = {
  queued: "等待中",
  pending: "等待浏览器",
  running: "传输中",
  paused: "已暂停",
  waiting: "等待恢复",
  conflict: "同名冲突",
  interrupted: "已中断",
  completed: "已完成",
  failed: "失败",
  canceled: "已取消",
  skipped: "已跳过",
};

export const holdText: Record<Hold, { title: string; detail: string }> = {
  offline: { title: "与 NAS 的连接中断", detail: "上传已暂停，网络恢复后会从中断处自动继续。" },
  session: { title: "登录已过期", detail: "重新登录同一账号后，上传会从中断处继续。" },
  volume: { title: "数据卷离线或只读", detail: "上传已暂停，每 30 秒自动重试一次。" },
  space: { title: "数据卷空间不足", detail: "上传已暂停。清理空间后继续，已上传的部分会保留。" },
};
