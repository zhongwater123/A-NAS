import { Check, CircleAlert, Copy, RotateCcw } from "lucide-react";
import { DragEvent, MouseEvent, PointerEvent, ReactNode, memo, useRef } from "react";

import type { Box, PendingPhoto } from "./layout";
import { originalURL, previewURL, type PhotoAsset } from "./photosApi";

export const dragType = "application/x-a-nas-photos";

interface Props {
  box: Box<PhotoAsset>;
  selected: boolean;
  selecting: boolean;
  onOpen: (asset: PhotoAsset, image: HTMLImageElement | null) => void;
  onToggle: (asset: PhotoAsset, event: MouseEvent) => void;
  onCheckDown?: (asset: PhotoAsset, event: PointerEvent) => void;
  // onDragIds names the photos a drag carries: the selection when the tile
  // is part of it, otherwise the tile alone.
  onDragIds?: (asset: PhotoAsset) => string[];
  badge?: ReactNode;
}

// Tile is one photo of the grid. A click opens it, or toggles it while
// photos are being selected; the check in its corner always toggles.
export const Tile = memo(function Tile({ box, selected, selecting, onOpen, onToggle, onCheckDown, onDragIds, badge }: Props) {
  const asset = box.item;
  // A press on the check swipes over other photos; it must not drag the tile.
  const fromCheck = useRef(false);
  const dragStart = (event: DragEvent<HTMLDivElement>) => {
    if (fromCheck.current) { event.preventDefault(); return; }
    const ids = onDragIds?.(asset) ?? [asset.id];
    event.dataTransfer.setData(dragType, JSON.stringify(ids));
    event.dataTransfer.effectAllowed = "copy";
    const ghost = document.createElement("div");
    ghost.className = "ph-drag-ghost";
    ghost.textContent = ids.length > 1 ? `${ids.length} 张照片` : asset.name;
    document.body.appendChild(ghost);
    event.dataTransfer.setDragImage?.(ghost, 16, 16);
    window.setTimeout(() => ghost.remove());
  };
  return (
    <div
      className={`ph-tile${selected ? " selected" : ""}${selecting ? " selecting" : ""}`}
      style={{ left: box.left, width: box.width, height: box.height }}
      data-asset-id={asset.id}
      draggable={Boolean(onDragIds)}
      onDragStart={onDragIds ? dragStart : undefined}
    >
      <button
        type="button"
        className="ph-tile-open"
        aria-label={`查看 ${asset.name}`}
        onClick={(event) => {
          if (selecting || event.shiftKey || event.ctrlKey || event.metaKey) onToggle(asset, event);
          else onOpen(asset, event.currentTarget.querySelector("img"));
        }}
      >
        <img
          src={previewURL(asset)}
          alt={asset.name}
          loading="lazy"
          decoding="async"
          draggable={false}
          onLoad={(event) => event.currentTarget.classList.add("loaded")}
          onError={(event) => {
            // A thumbnail reported ready can still be missing until the
            // photo service renders it again; the original shows instead.
            const original = originalURL(asset.id);
            if (event.currentTarget.getAttribute("src") !== original) event.currentTarget.src = original;
          }}
        />
      </button>
      <button
        type="button"
        role="checkbox"
        aria-checked={selected}
        aria-label={`选择 ${asset.name}`}
        className="ph-check"
        onPointerDown={(event) => {
          fromCheck.current = true;
          window.addEventListener("pointerup", () => { fromCheck.current = false; }, { once: true });
          onCheckDown?.(asset, event);
        }}
        onClick={(event) => onToggle(asset, event)}
      >
        <Check strokeWidth={3} />
      </button>
      {asset.duplicate === "duplicate" && <span className="ph-tile-badge" title="本图库中已有相同照片"><Copy />重复</span>}
      {badge}
    </div>
  );
});

// PendingTile is a photo being uploaded, shown from the file itself.
export function PendingTile({ box, onRetry }: { box: Box<PendingPhoto>; onRetry: (key: string) => void }) {
  const item = box.item;
  const circumference = 2 * Math.PI * 15;
  return (
    <div className={`ph-tile ph-pending ${item.state}`} style={{ left: box.left, width: box.width, height: box.height }} title={item.error ?? item.name}>
      <img src={item.url} alt={item.name} className="loaded" draggable={false} />
      {item.state === "failed" ? (
        <button type="button" className="ph-pending-failed" aria-label={`重试上传 ${item.name}`} onClick={() => onRetry(item.key)}>
          <CircleAlert /><span>{item.error ?? "上传失败"}</span><small><RotateCcw />重试</small>
        </button>
      ) : item.state !== "done" && (
        <svg className="ph-progress-ring" viewBox="0 0 36 36" role="progressbar" aria-label={`正在上传 ${item.name}`} aria-valuenow={Math.round(item.progress * 100)}>
          <circle cx="18" cy="18" r="15" className="track" />
          <circle cx="18" cy="18" r="15" className="value" strokeDasharray={circumference} strokeDashoffset={circumference * (1 - (item.state === "waiting" ? 0 : Math.max(0.04, item.progress)))} />
        </svg>
      )}
    </div>
  );
}
