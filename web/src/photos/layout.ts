import justifiedLayout from "justified-layout";

import { dayKey, dayLabel, fullMonthLabel, momentOf } from "./model";
import type { PhotoAsset, PhotoMonth } from "./photosApi";

// Grid sizes are target row heights; the grid keeps every photo's aspect
// ratio, so rows come out a little taller or shorter.
export const minRowHeight = 96;
export const maxRowHeight = 340;
export const defaultRowHeight = 180;
export const tileGap = 4;
export const clampRowHeight = (value: number) => Math.round(Math.min(maxRowHeight, Math.max(minRowHeight, value)));

export const sizes = { month: 64, day: 44, heading: 52, more: 72, end: 64 };

export interface Box<T> { item: T; left: number; width: number; height: number }

// A pending upload shows in the grid before the photo service has it.
export interface PendingPhoto { key: string; name: string; url: string; aspect: number; progress: number; state: "waiting" | "uploading" | "done" | "failed"; error?: string }

export type Entry =
  | { kind: "month"; key: string; size: number; label: string; month: string; photos: number }
  | { kind: "day"; key: string; size: number; label: string; ids: string[] }
  | { kind: "heading"; key: string; size: number; label: string; detail?: string }
  | { kind: "row"; key: string; size: number; label: string; boxes: Box<PhotoAsset>[] }
  | { kind: "pending"; key: string; size: number; label: string; boxes: Box<PendingPhoto>[] }
  | { kind: "placeholder"; key: string; size: number; label: string; month: string; photos: number; failed: boolean }
  | { kind: "more"; key: string; size: number; label: string }
  | { kind: "end"; key: string; size: number; label: string; text: string };

// Very wide panoramas and tall strips would make a row a sliver; their tiles
// crop a little instead.
const aspectOf = (width: number, height: number) => width > 0 && height > 0 ? Math.min(3, Math.max(0.42, width / height)) : 1;
export const assetAspect = (asset: PhotoAsset) => aspectOf(asset.width, asset.height);

// justify lays items out in rows of about rowHeight that fill width.
export function justify<T>(items: T[], aspect: (item: T) => number, width: number, rowHeight: number): Array<{ height: number; boxes: Box<T>[] }> {
  if (!items.length || width <= 0) return [];
  const layout = justifiedLayout(items.map(aspect), {
    containerWidth: width, containerPadding: 0, boxSpacing: tileGap, targetRowHeight: rowHeight, targetRowHeightTolerance: 0.3,
  });
  const rows: Array<{ top: number; height: number; boxes: Box<T>[] }> = [];
  layout.boxes.forEach((box, index) => {
    const placed = { item: items[index], left: box.left, width: box.width, height: box.height };
    const last = rows.at(-1);
    if (last && Math.abs(last.top - box.top) < 1) last.boxes.push(placed);
    else rows.push({ top: box.top, height: box.height, boxes: [placed] });
  });
  return rows;
}

function rowEntries(assets: PhotoAsset[], width: number, rowHeight: number, label: string, keyPrefix: string): Entry[] {
  return justify(assets, assetAspect, width, rowHeight).map((row) => ({
    kind: "row", key: `${keyPrefix}:${row.boxes[0].item.id}`, size: Math.round(row.height) + tileGap, label, boxes: row.boxes,
  }));
}

// groupByDay keeps the order and starts a group when the local day changes.
export function groupByDay(assets: PhotoAsset[]) {
  const groups: Array<{ key: string; date: Date; assets: PhotoAsset[] }> = [];
  for (const asset of assets) {
    const date = momentOf(asset);
    const key = dayKey(date);
    const last = groups.at(-1);
    if (last?.key === key) last.assets.push(asset);
    else groups.push({ key, date, assets: [asset] });
  }
  return groups;
}

export interface MonthState { assets?: PhotoAsset[]; failed?: boolean }

// estimateMonth guesses an unloaded month's height from its photo count, so
// the scrollbar and the scrubber are about right before it loads.
function estimateMonth(photos: number, width: number, rowHeight: number) {
  const perRow = Math.max(1, Math.floor((width + tileGap) / (rowHeight * 1.3 + tileGap)));
  const rows = Math.ceil(photos / perRow);
  const days = Math.min(photos, Math.max(1, Math.round(photos / 7)));
  return rows * (rowHeight + tileGap) + days * sizes.day;
}

export function timelineEntries(months: PhotoMonth[], loaded: Map<string, MonthState>, pending: PendingPhoto[], width: number, rowHeight: number, now = new Date()): Entry[] {
  const entries: Entry[] = [];
  if (pending.length) {
    const uploading = pending.filter((item) => item.state !== "done").length;
    entries.push({ kind: "heading", key: "uploads", size: sizes.heading, label: "正在上传", detail: uploading ? `正在上传 ${uploading} 张` : "上传完成" });
    for (const row of justify(pending, (item) => item.aspect, width, rowHeight)) {
      entries.push({ kind: "pending", key: `pending:${row.boxes[0].item.key}`, size: Math.round(row.height) + tileGap, label: "正在上传", boxes: row.boxes });
    }
  }
  for (const month of months) {
    const state = loaded.get(month.month);
    const label = fullMonthLabel(month.month);
    const photos = state?.assets ? state.assets.length : month.photos;
    if (state?.assets && !photos) continue;
    entries.push({ kind: "month", key: `month:${month.month}`, size: sizes.month, label, month: month.month, photos });
    if (!state?.assets) {
      entries.push({ kind: "placeholder", key: `placeholder:${month.month}`, size: Math.round(estimateMonth(month.photos, width, rowHeight)), label, month: month.month, photos: month.photos, failed: Boolean(state?.failed) });
      continue;
    }
    for (const day of groupByDay(state.assets)) {
      const dayName = dayLabel(day.date, now);
      // Months follow the device's time zone and days the browser's; the
      // month keeps keys unique should the two differ.
      entries.push({ kind: "day", key: `day:${month.month}:${day.key}`, size: sizes.day, label: dayName, ids: day.assets.map((asset) => asset.id) });
      entries.push(...rowEntries(day.assets, width, rowHeight, dayName, `row:${month.month}:${day.key}`));
    }
  }
  return entries;
}

// flatEntries lays out a list without dates, such as an album or search
// results; "more" asks for the next page when it scrolls into view.
export function flatEntries(assets: PhotoAsset[], width: number, rowHeight: number, more: boolean, end = ""): Entry[] {
  const entries = rowEntries(assets, width, rowHeight, "", "row");
  if (more) entries.push({ kind: "more", key: "more", size: sizes.more, label: "" });
  else if (end && assets.length) entries.push({ kind: "end", key: "end", size: sizes.end, label: "", text: end });
  return entries;
}
