import type { ExternalUpload, Hold, LocalUpload, TransferSnapshot, UploadPhase } from "./store";
import type { Transfer, TransferDirection, TransferLocation } from "./transfersApi";

export type ItemStatus = "queued" | "pending" | "running" | "paused" | "waiting" | "conflict" | "interrupted" | "completed" | "failed" | "canceled" | "skipped";

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
export const isCurrent = (item: TransferItem) => isActive(item.status) || (item.status === "failed" && Boolean(item.local || item.external));

const phaseStatus: Record<UploadPhase, ItemStatus> = {
  queued: "queued", running: "running", paused: "paused", waiting: "waiting", conflict: "conflict",
  completed: "completed", failed: "failed", canceled: "canceled", skipped: "skipped",
};

export function transferItems(snapshot: TransferSnapshot): TransferItem[] {
  const server = new Map(snapshot.server.map((transfer) => [transfer.id, transfer]));
  const owned = new Set<string>();
  const items: TransferItem[] = snapshot.uploads.map((upload) => {
    const remote = upload.transferId ? server.get(upload.transferId) : undefined;
    for (const id of [upload.transferId, ...(upload.superseded ?? [])]) if (id) owned.add(id);
    return {
      id: upload.key, direction: "upload", kind: "file", name: upload.name, location: upload.target,
      relativePath: upload.directories.join("/") || undefined, total: upload.size, transferred: upload.transferred, speed: upload.speed,
      status: phaseStatus[upload.phase], failure: upload.error ? failureText(upload.error.code) : undefined, hold: upload.hold,
      savedName: upload.savedName ?? remote?.savedName, replaced: upload.replaced ?? remote?.replaced, restarted: upload.restarted,
      createdAt: upload.createdAt, finishedAt: upload.finishedAt, batch: upload.batch, local: upload, server: remote,
    };
  });
  for (const transfer of snapshot.server) {
    if (owned.has(transfer.id)) continue;
    items.push({
      id: transfer.id, direction: transfer.direction, kind: transfer.kind, name: transfer.name, location: transfer.location,
      relativePath: transfer.relativePath, total: transfer.totalBytes, transferred: transfer.transferredBytes, speed: transfer.bytesPerSecond,
      status: transfer.state, failure: transfer.error ? failureText(transfer.error.code) : undefined, savedName: transfer.savedName,
      replaced: transfer.replaced, skipped: transfer.skipped, createdAt: Date.parse(transfer.createdAt) || 0,
      finishedAt: transfer.finishedAt ? Date.parse(transfer.finishedAt) : undefined, batch: transfer.batch, server: transfer,
    });
  }
  for (const upload of snapshot.external) {
    items.push({
      id: upload.key, direction: "upload", kind: "file", name: upload.name, label: upload.label, total: upload.size,
      transferred: Math.round(upload.size * upload.progress), speed: 0, status: upload.phase, failure: upload.error,
      createdAt: upload.createdAt, finishedAt: upload.finishedAt, external: upload,
    });
  }
  return items.sort((left, right) => right.createdAt - left.createdAt);
}

// groupItems keeps each batch together, at the place of its newest item.
export function groupItems(items: TransferItem[]): TransferGroup[] {
  const groups: TransferGroup[] = [];
  const byBatch = new Map<string, TransferGroup>();
  for (const item of items) {
    const key = item.batch && item.direction === "upload" ? `batch:${item.batch}` : `item:${item.id}`;
    let group = byBatch.get(key);
    if (!group) {
      group = { key, direction: item.direction, batch: key.startsWith("batch:") ? item.batch : undefined, items: [], location: item.location, total: 0, transferred: 0, speed: 0, createdAt: item.createdAt, counts: {} };
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
  return groups;
}

// groupStatus summarises a group for its badge.
export function groupStatus(group: TransferGroup): ItemStatus {
  for (const status of ["conflict", "running", "waiting", "queued", "pending", "interrupted", "paused", "failed"] as ItemStatus[]) {
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

export function transferTotals(items: TransferItem[]): TransferTotals {
  const current = items.filter(isCurrent);
  let total = 0;
  let done = 0;
  const totals: TransferTotals = { uploads: 0, downloads: 0, attention: 0, uploadSpeed: 0, downloadSpeed: 0, remainingBytes: 0, progress: 0 };
  for (const item of current) {
    if (item.direction === "upload") totals.uploads += 1;
    else totals.downloads += 1;
    if (item.status === "conflict" || item.status === "failed" || item.status === "waiting" || (item.status === "interrupted" && !item.local)) totals.attention += 1;
  }
  for (const item of current) {
    if (item.status === "running") {
      if (item.direction === "upload") totals.uploadSpeed += item.speed;
      else totals.downloadSpeed += item.speed;
    }
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

export function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${Math.max(0, Math.round(bytes))} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let value = bytes / 1024;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value < 10 ? value.toFixed(1) : Math.round(value)} ${units[unit]}`;
}

export function formatSpeed(bytesPerSecond: number): string {
  return `${formatBytes(bytesPerSecond)}/s`;
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
  return new Intl.DateTimeFormat("zh-CN", { month: "long", day: "numeric", weekday: "short", year: day.getFullYear() === today.getFullYear() ? undefined : "numeric" }).format(day);
}

const failures: Record<string, string> = {
  network_error: "与 NAS 的连接中断",
  file_unreadable: "无法读取本地文件，可能已被移动或删除",
  authentication_required: "登录已过期",
  csrf_failed: "登录状态已变化",
  forbidden: "没有写入这个位置的权限",
  not_found: "目标文件夹已不存在",
  name_conflict: "目标位置已有同名文件",
  folder_blocked: "已有同名文件，无法创建文件夹",
  validation_failed: "文件名无效，或 NAS 不接受这个请求",
  volume_unavailable: "数据卷离线或只读",
  insufficient_storage: "数据卷空间不足",
  too_many_uploads: "未完成的上传过多",
  upload_lost: "NAS 上的上传进度已丢失",
  service_restarted: "NAS 服务重启，上传需要重新开始",
  expired: "超过一天没有继续，已自动清除",
  not_started: "浏览器没有开始下载，可能被拦截",
  canceled: "已在其他设备上取消",
  interrupted: "连接中断",
  transfer_state: "传输状态已变化",
  internal_error: "NAS 处理失败",
};

export function failureText(code: string): string {
  return failures[code] ?? "传输失败";
}

export const statusText: Record<ItemStatus, string> = {
  queued: "等待中", pending: "等待浏览器", running: "传输中", paused: "已暂停", waiting: "等待恢复", conflict: "同名冲突",
  interrupted: "已中断", completed: "已完成", failed: "失败", canceled: "已取消", skipped: "已跳过",
};

export const holdText: Record<Hold, { title: string; detail: string }> = {
  offline: { title: "与 NAS 的连接中断", detail: "上传已暂停，网络恢复后会从中断处自动继续。" },
  session: { title: "登录已过期", detail: "重新登录同一账号后，上传会从中断处继续。" },
  volume: { title: "数据卷离线或只读", detail: "上传已暂停，每 30 秒自动重试一次。" },
  space: { title: "数据卷空间不足", detail: "上传已暂停。清理空间后继续，已上传的部分会保留。" },
};
