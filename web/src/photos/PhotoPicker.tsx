import { X } from "lucide-react";
import { useCallback, useRef, useState } from "react";

import feedbackStyles from "../ui/feedback.module.css";
import { AssetGrid } from "./AssetGrid";
import { listTimeline, type PhotoAlbum, type PhotoAsset } from "./api";
import { Empty } from "./Collections";
import { clampRowHeight } from "./layout";
import type { ScrollerHandle } from "./PhotoScroller";
import styles from "./photos.module.css";
import { useSelection } from "./selection";

interface Props {
  // The library picked from and the name it goes by: an album to add to, or
  // just a library.
  album: Pick<PhotoAlbum, "libraryId" | "name">;
  // title replaces "添加到“<album>”" for a caller that adds elsewhere.
  title?: string;
  // videos lists the library's videos alone, for a caller that takes no photos.
  videos?: boolean;
  onAdd: (assets: PhotoAsset[]) => void;
  onClose: () => void;
}

const noHidden: ReadonlySet<string> = new Set();
// How many pages without a video one load passes over before it hands the
// grid what it has; the grid asks for the next as its end comes into view.
const PAGES_PASSED = 5;

// PhotoPicker picks photos of an album's library to add to it; clicking a
// photo selects it. With videos it lists and picks videos only.
export function PhotoPicker({ album, title, videos = false, onAdd, onClose }: Props) {
  const [assets, setAssets] = useState<PhotoAsset[]>([]);
  const selection = useSelection(assets);
  const [rowHeight, setRowHeight] = useState(140);
  const [error, setError] = useState("");
  const scroller = useRef<ScrollerHandle>(null);
  const load = useCallback(
    async (cursor: string) => {
      let page = await listTimeline(album.libraryId, cursor, 200);
      if (!videos) return page;
      let items = page.items.filter((asset) => asset.video);
      for (let passed = 0; !items.length && page.next && passed < PAGES_PASSED; passed++) {
        page = await listTimeline(album.libraryId, page.next, 200);
        items = page.items.filter((asset) => asset.video);
      }
      return { ...page, items };
    },
    [album.libraryId, videos],
  );
  const heading = title ?? `添加到“${album.name}”`;
  const unit = videos ? "个视频" : "张";
  const chosen = assets.filter((asset) => selection.selected.has(asset.id));
  return (
    <div
      className={feedbackStyles.phDialogBackdrop}
      onPointerDown={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <div
        className={`${feedbackStyles.phDialog} ${styles.phPhotoPicker}`}
        role="dialog"
        aria-modal="true"
        aria-label={title ?? `添加照片到 ${album.name}`}
        onKeyDown={(event) => {
          if (event.key === "Escape") {
            event.stopPropagation();
            onClose();
          }
        }}
      >
        <header>
          <h3>{heading}</h3>
          <small>
            {chosen.length
              ? `已选 ${chosen.length} ${unit}`
              : `点选${videos ? "视频" : "照片"}，可以按住勾选圈拖动或框选`}
          </small>
          <button type="button" className={feedbackStyles.phDialogClose} aria-label="关闭" onClick={onClose}>
            <X />
          </button>
        </header>
        <div className={styles.phPhotoPickerGrid}>
          <AssetGrid
            listKey={`picker:${album.libraryId}:${videos ? "videos" : "all"}`}
            load={load}
            onError={setError}
            empty={
              videos ? (
                <Empty icon={<X />} title="没有视频" text={error || "这个图库还没有视频。"} />
              ) : (
                <Empty icon={<X />} title="没有照片" text={error || "这个图库还没有照片。"} />
              )
            }
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
        <footer className={feedbackStyles.phDialogActions}>
          <button type="button" onClick={onClose}>
            取消
          </button>
          <button type="button" className="primary" disabled={!chosen.length} onClick={() => onAdd(chosen)}>
            {chosen.length ? `添加 ${chosen.length} ${unit}` : "添加"}
          </button>
        </footer>
      </div>
    </div>
  );
}
