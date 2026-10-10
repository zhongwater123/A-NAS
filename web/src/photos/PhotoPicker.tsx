import { X } from "lucide-react";
import { useCallback, useRef, useState } from "react";

import { AssetGrid } from "./AssetGrid";
import { Empty } from "./Collections";
import { clampRowHeight } from "./layout";
import { listTimeline, type PhotoAlbum, type PhotoAsset } from "./photosApi";
import type { ScrollerHandle } from "./PhotoScroller";
import { useSelection } from "./selection";

interface Props {
  album: PhotoAlbum;
  onAdd: (assets: PhotoAsset[]) => void;
  onClose: () => void;
}

const noHidden: ReadonlySet<string> = new Set();

// PhotoPicker picks photos of an album's library to add to it; clicking a
// photo selects it.
export function PhotoPicker({ album, onAdd, onClose }: Props) {
  const [assets, setAssets] = useState<PhotoAsset[]>([]);
  const selection = useSelection(assets);
  const [rowHeight, setRowHeight] = useState(140);
  const [error, setError] = useState("");
  const scroller = useRef<ScrollerHandle>(null);
  const load = useCallback((cursor: string) => listTimeline(album.libraryId, cursor, 200), [album.libraryId]);
  const chosen = assets.filter((asset) => selection.selected.has(asset.id));
  return (
    <div className="ph-dialog-backdrop" onPointerDown={(event) => { if (event.target === event.currentTarget) onClose(); }}>
      <div className="ph-dialog ph-photo-picker" role="dialog" aria-modal="true" aria-label={`添加照片到 ${album.name}`} onKeyDown={(event) => { if (event.key === "Escape") { event.stopPropagation(); onClose(); } }}>
        <header>
          <h3>添加到“{album.name}”</h3>
          <small>{chosen.length ? `已选 ${chosen.length} 张` : "点选照片，可以按住勾选圈拖动或框选"}</small>
          <button type="button" className="ph-dialog-close" aria-label="关闭" onClick={onClose}><X /></button>
        </header>
        <div className="ph-photo-picker-grid">
          <AssetGrid
            listKey={`picker:${album.libraryId}`}
            load={load}
            onError={setError}
            empty={<Empty icon={<X />} title="没有照片" text={error || "这个图库还没有照片。"} />}
            rowHeight={rowHeight}
            onZoom={(factor) => setRowHeight((value) => clampRowHeight(value * factor))}
            selected={selection.selected}
            onOpen={(asset) => selection.toggle(asset, false)}
            onToggle={selection.toggle}
            onBeginSwipe={selection.beginSwipe}
            onMarquee={selection.marquee}
            onGestureEnd={selection.endGesture}
            onBackgroundClick={() => undefined}
            onSelectGroup={selection.setGroup}
            onAssets={setAssets}
            hidden={noHidden}
            refreshKey={0}
            scroller={scroller}
            topInset={8}
          />
        </div>
        <footer className="ph-dialog-actions">
          <button type="button" onClick={onClose}>取消</button>
          <button type="button" className="primary" disabled={!chosen.length} onClick={() => onAdd(chosen)}>{chosen.length ? `添加 ${chosen.length} 张` : "添加"}</button>
        </footer>
      </div>
    </div>
  );
}
