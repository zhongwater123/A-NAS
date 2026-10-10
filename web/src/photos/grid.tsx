import { Check } from "lucide-react";
import { MouseEvent, PointerEvent, ReactNode, RefObject, useCallback, useEffect, useMemo, useRef } from "react";

import type { Box, Entry } from "./layout";
import { getPhoto, type PhotoAsset } from "./photosApi";
import type { ScrollerHandle } from "./PhotoScroller";
import { Tile } from "./Tile";

// GridProps is what every photo grid view gets from the panel: size,
// selection and what opening or dragging a photo does.
export interface GridProps {
  rowHeight: number;
  onZoom: (factor: number) => void;
  selected: ReadonlySet<string>;
  onOpen: (asset: PhotoAsset, image: HTMLImageElement | null) => void;
  onToggle: (asset: PhotoAsset, range: boolean) => void;
  onBeginSwipe: (asset: PhotoAsset) => (to: string) => void;
  onMarquee: (ids: string[], additive: boolean) => void;
  onGestureEnd: () => void;
  onBackgroundClick: () => void;
  onSelectGroup: (ids: string[]) => void;
  // Absent where photos cannot be moved into albums, as when viewing.
  onDragIds?: (asset: PhotoAsset) => string[];
  onAssets: (assets: PhotoAsset[]) => void;
  hidden: ReadonlySet<string>;
  refreshKey: number;
  scroller: RefObject<ScrollerHandle | null>;
  topInset: number;
}

export const gridInset = 22;
// The timeline's right side leaves room for its month scrubber.
export const scrubberInset = 40;

// useTileRenderer renders a grid's tiles with stable callbacks, so tiles
// that did not change are not rendered again.
export function useTileRenderer(grid: GridProps, badge?: (asset: PhotoAsset) => ReactNode) {
  const selecting = grid.selected.size > 0;
  const gridRef = useRef(grid);
  gridRef.current = grid;
  const onToggle = useCallback((asset: PhotoAsset, event: MouseEvent) => gridRef.current.onToggle(asset, event.shiftKey), []);
  const onOpen = useCallback((asset: PhotoAsset, image: HTMLImageElement | null) => gridRef.current.onOpen(asset, image), []);
  const onCheckDown = useCallback((asset: PhotoAsset, event: PointerEvent) => {
    const swipe = gridRef.current.onBeginSwipe(asset);
    gridRef.current.scroller.current?.beginSwipe(asset.id, event, swipe);
  }, []);
  const onDragIds = useCallback((asset: PhotoAsset) => gridRef.current.onDragIds?.(asset) ?? [asset.id], []);
  const draggable = Boolean(grid.onDragIds);
  return useCallback((box: Box<PhotoAsset>) => (
    <Tile
      key={box.item.id}
      box={box}
      selected={grid.selected.has(box.item.id)}
      selecting={selecting}
      onOpen={onOpen}
      onToggle={onToggle}
      onCheckDown={onCheckDown}
      onDragIds={draggable ? onDragIds : undefined}
      badge={badge?.(box.item)}
    />
  ), [grid.selected, selecting, onOpen, onToggle, onCheckDown, onDragIds, draggable, badge]);
}

// DayHeader names a day and selects all of it.
export function DayHeader({ entry, selected, onSelect }: { entry: Extract<Entry, { kind: "day" }>; selected: ReadonlySet<string>; onSelect: (ids: string[]) => void }) {
  const all = entry.ids.every((id) => selected.has(id));
  return (
    <div className={`ph-day${selected.size ? " selecting" : ""}`}>
      <button type="button" role="checkbox" aria-checked={all} aria-label={`选择${entry.label}的 ${entry.ids.length} 张照片`} className={`ph-day-check${all ? " on" : ""}`} onClick={() => onSelect(entry.ids)}><Check strokeWidth={3} /></button>
      <h3>{entry.label}</h3>
      <small>{entry.ids.length} 张</small>
    </div>
  );
}

const thumbnailCheckInterval = 3000;
const thumbnailChecks = 20;

// usePendingThumbnails rechecks photos whose thumbnails are still being
// rendered for about a minute, so the grid stops loading their full-size
// originals once the thumbnails exist.
export function usePendingThumbnails(assets: PhotoAsset[] | undefined, onReady: (ready: Map<string, PhotoAsset>) => void) {
  const pending = useMemo(() => (assets ?? []).filter((asset) => asset.thumbnail === "pending").map((asset) => asset.id).join(","), [assets]);
  const onReadyRef = useRef(onReady);
  onReadyRef.current = onReady;
  useEffect(() => {
    if (!pending) return;
    let attempts = 0;
    const timer = window.setInterval(() => {
      if (++attempts >= thumbnailChecks) window.clearInterval(timer);
      void Promise.all(pending.split(",").map((id) => getPhoto(id).catch(() => undefined))).then((checked) => {
        const ready = new Map(checked.filter((asset): asset is PhotoAsset => asset !== undefined && asset.thumbnail !== "pending").map((asset) => [asset.id, asset]));
        if (ready.size) onReadyRef.current(ready);
      });
    }, thumbnailCheckInterval);
    return () => window.clearInterval(timer);
  }, [pending]);
}
