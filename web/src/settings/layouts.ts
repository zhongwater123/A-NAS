import type { Volume, VolumeConversion, VolumeLayout } from "../api";

// What each data-volume layout means for the person choosing it (ADR 0025).
// The server decides; these numbers only let the choice show its effect.

export interface LayoutInfo {
  name: string;
  // tag is the RAID level people know, or what the layout gives up.
  tag: string;
  summary: string;
  // loss says what one failed disk costs.
  loss: string;
  tone: "success" | "warning" | "danger";
}

export const layouts: Record<VolumeLayout, LayoutInfo> = {
  single: { name: "单盘", tag: "", summary: "数据卷只在一块硬盘上", loss: "硬盘损坏会丢失全部文件", tone: "danger" },
  mirror: { name: "镜像", tag: "RAID 1", summary: "每个文件在两块硬盘上各存一份", loss: "坏一块硬盘，文件不丢，换盘后恢复", tone: "success" },
  span: { name: "扩容", tag: "不冗余", summary: "容量相加，每个文件只存一份", loss: "坏一块硬盘，那块盘上的文件会丢失", tone: "warning" },
  stripe: { name: "条带", tag: "RAID 0", summary: "容量相加，文件分散写到每块硬盘，读写更快", loss: "坏一块硬盘，几乎所有文件都会丢失", tone: "danger" },
};

// The layouts a volume of two disks or more can have, most protective first.
export const multiDiskLayouts: VolumeLayout[] = ["mirror", "span", "stripe"];

export function layoutLabel(layout: VolumeLayout): string {
  const { name, tag } = layouts[layout] ?? layouts.single;
  return tag ? `${name}（${tag}）` : name;
}

// usableBytes is storage.UsableBytes: the space for files on disks of the
// sizes, before filesystem overhead. A mirror holds half the total and never
// more than the other disks can copy of the largest.
export function usableBytes(layout: VolumeLayout, sizes: number[]): number {
  const total = sizes.reduce((sum, size) => sum + size, 0);
  if (layout === "mirror") return Math.min(total / 2, total - Math.max(0, ...sizes));
  return total;
}

// reserveBytes is internal/capacity.Reserve: writers leave 5% of a volume,
// and at least 10 GiB, free.
export function reserveBytes(total: number): number {
  return Math.max(total / 20, 10 * 1024 ** 3);
}

// usedBytes is the space files take on the volume, when it is known.
export function usedBytes(volume?: Volume): number | undefined {
  if (!volume?.capacityBytes || volume.availableBytes === undefined) return undefined;
  return volume.capacityBytes - Math.min(volume.capacityBytes, Math.max(0, volume.availableBytes));
}

// fits reports whether the files already on the volume fit a layout of the
// usable size with the reserve left free, as the server checks.
export function fits(after: number, used: number | undefined): boolean {
  return used === undefined || used + reserveBytes(after) <= after;
}

// conversionPercent stays below 100 until Btrfs reports the volume converted.
export function conversionPercent(conversion: VolumeConversion): number {
  if (conversion.totalBytes <= 0) return 0;
  return Math.min(99, Math.floor((conversion.convertedBytes / conversion.totalBytes) * 100));
}

export interface ProgressSample { at: number; bytes: number }

// remainingSeconds estimates how long a running conversion still takes from
// its speed since first seen; unknown until it has run for a while.
export function remainingSeconds(first: ProgressSample, now: number, conversion: VolumeConversion): number | undefined {
  const elapsed = (now - first.at) / 1000;
  const moved = conversion.convertedBytes - first.bytes;
  if (conversion.state !== "running" || elapsed < 15 || moved <= 0) return undefined;
  return (conversion.totalBytes - conversion.convertedBytes) / (moved / elapsed);
}

export function formatRemaining(seconds: number): string {
  const minutes = Math.max(1, Math.ceil(seconds / 60));
  if (minutes < 60) return `约 ${minutes} 分钟`;
  const hours = Math.floor(minutes / 60);
  const rest = minutes % 60;
  return rest ? `约 ${hours} 小时 ${rest} 分钟` : `约 ${hours} 小时`;
}
