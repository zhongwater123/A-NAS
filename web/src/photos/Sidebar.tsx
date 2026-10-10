import { Images, Library, Plus, ScanEye, Sparkles, Trash2 } from "lucide-react";
import { DragEvent, useEffect, useState } from "react";

import { type Caller, libraryName, type Place } from "./model";
import { listAlbums, thumbnailURL, type PhotoAIStatus, type PhotoAlbum, type PhotoLibrary } from "./photosApi";
import { dragType } from "./Tile";

interface Props {
  caller: Caller;
  // The library the sidebar shows, which the switch at its top changes.
  library?: PhotoLibrary;
  place: Place;
  albumsKey: number;
  count?: number;
  ai?: PhotoAIStatus;
  onPlace: (place: Place) => void;
  onLibrary: (library: PhotoLibrary) => void;
  onCreateAlbum: () => void;
  // Photos dropped on an album join it; dropped on a library they are copied there.
  onDropOnAlbum: (album: PhotoAlbum, ids: string[]) => void;
  onDropOnLibrary: (library: PhotoLibrary, ids: string[]) => void;
}

const carriesPhotos = (event: DragEvent) => Array.from(event.dataTransfer.types).includes(dragType);
const droppedIds = (event: DragEvent): string[] => {
  try { return JSON.parse(event.dataTransfer.getData(dragType)); } catch { return []; }
};

// dropTarget makes an element accept photos dragged from the grid.
function useDropTarget(onDrop: (ids: string[]) => void, enabled: boolean) {
  const [over, setOver] = useState(false);
  if (!enabled) return { over: false, props: {} };
  return {
    over,
    props: {
      onDragOver: (event: DragEvent) => { if (carriesPhotos(event)) { event.preventDefault(); event.dataTransfer.dropEffect = "copy"; setOver(true); } },
      onDragLeave: () => setOver(false),
      onDrop: (event: DragEvent) => { if (!carriesPhotos(event)) return; event.preventDefault(); event.stopPropagation(); setOver(false); const ids = droppedIds(event); if (ids.length) onDrop(ids); },
    },
  };
}

export function Sidebar({ caller, library, place, albumsKey, count, ai, onPlace, onLibrary, onCreateAlbum, onDropOnAlbum, onDropOnLibrary }: Props) {
  const [albums, setAlbums] = useState<PhotoAlbum[]>([]);
  const libraryId = library?.id ?? "";
  useEffect(() => {
    setAlbums([]);
    if (!libraryId) return;
    let live = true;
    listAlbums(libraryId).then((items) => { if (live) setAlbums(items); }, () => undefined);
    return () => { live = false; };
  }, [libraryId, albumsKey]);
  const readOnly = Boolean(library?.viewing);
  const at = (kind: Place["kind"]) => place.kind === kind && ("libraryId" in place ? place.libraryId === libraryId : true);
  const viewing = readOnly ? libraryId : "";

  return (
    <nav className="ph-sidebar" aria-label="相册导航">
      <div className="ph-libraries" role="tablist" aria-label="图库">
        {caller.libraries.map((item) => <LibraryTab key={item.id} caller={caller} library={item} active={item.id === libraryId} onSelect={() => onLibrary(item)} onDrop={(ids) => onDropOnLibrary(item, ids)} />)}
      </div>

      <div className="ph-nav">
        <button type="button" className={at("timeline") ? "active" : ""} aria-current={at("timeline") ? "page" : undefined} onClick={() => onPlace({ kind: "timeline", libraryId })}>
          <Images /><span>照片</span>{count !== undefined && at("timeline") && <small>{count}</small>}
        </button>
        <button type="button" className={place.kind === "things" || place.kind === "label" ? "active" : ""} aria-current={place.kind === "things" ? "page" : undefined} onClick={() => onPlace({ kind: "things", viewing })}>
          <ScanEye /><span>识别的事物</span>
        </button>
        {!readOnly && (
          <button type="button" className={at("trash") ? "active" : ""} aria-current={at("trash") ? "page" : undefined} onClick={() => onPlace({ kind: "trash", libraryId })}>
            <Trash2 /><span>回收站</span>
          </button>
        )}
      </div>

      <div className="ph-nav-section">
        <button type="button" className={`ph-nav-heading${at("albums") ? " active" : ""}`} aria-current={at("albums") ? "page" : undefined} onClick={() => onPlace({ kind: "albums", libraryId })}><Library /><span>相册</span><small>{albums.length || ""}</small></button>
        {!readOnly && <button type="button" className="ph-nav-add" aria-label="新建相册" title="新建相册" onClick={onCreateAlbum}><Plus /></button>}
      </div>
      <div className="ph-album-list">
        {albums.map((album) => <AlbumLink key={album.id} album={album} active={place.kind === "album" && place.album.id === album.id} droppable={!readOnly} onOpen={() => onPlace({ kind: "album", libraryId, album })} onDrop={(ids) => onDropOnAlbum(album, ids)} />)}
        {!albums.length && <p className="ph-nav-empty">{readOnly ? "没有相册" : "把照片拖到这里的相册，或新建一个"}</p>}
      </div>

      <AICard ai={ai} />
    </nav>
  );
}

function LibraryTab({ caller, library, active, onSelect, onDrop }: { caller: Caller; library: PhotoLibrary; active: boolean; onSelect: () => void; onDrop: (ids: string[]) => void }) {
  const target = useDropTarget(onDrop, !library.viewing);
  const name = library.kind === "shared" ? "共享" : library.viewing ? `${library.ownerName ?? "成员"}` : "我的";
  return (
    <button
      type="button"
      role="tab"
      aria-selected={active}
      title={library.viewing ? `只读查看 ${libraryName(caller, library)}` : libraryName(caller, library)}
      className={`${active ? "active" : ""}${library.viewing ? " viewing" : ""}${target.over ? " drop" : ""}`}
      onClick={onSelect}
      {...target.props}
    >
      {name}
    </button>
  );
}

function AlbumLink({ album, active, droppable, onOpen, onDrop }: { album: PhotoAlbum; active: boolean; droppable: boolean; onOpen: () => void; onDrop: (ids: string[]) => void }) {
  const target = useDropTarget(onDrop, droppable);
  return (
    <button type="button" className={`ph-album-link${active ? " active" : ""}${target.over ? " drop" : ""}`} aria-current={active ? "page" : undefined} onClick={onOpen} {...target.props}>
      <span className="ph-album-thumb">{album.coverId ? <img src={thumbnailURL(album.coverId)} alt="" loading="lazy" /> : <Images />}</span>
      <span>{album.name}</span>
      <small>{album.photos}</small>
    </button>
  );
}

// AICard says what local AI is doing with the caller's photos.
function AICard({ ai }: { ai?: PhotoAIStatus }) {
  if (!ai) return null;
  const total = ai.ready + ai.pending + ai.failed;
  const share = total ? ai.ready / total : 1;
  let title = "本地 AI";
  let detail = "";
  if (ai.state === "unavailable") {
    title = "本地 AI 未启用";
    detail = "搜索只按照片名称和标签匹配";
  } else if (!ai.pending) {
    title = `已整理全部 ${ai.ready} 张`;
    detail = "可以用自然语言搜索照片";
  } else if (ai.state === "working") {
    title = `正在整理 ${ai.ready} / ${total}`;
    detail = "完成后可以按内容搜索";
  } else {
    title = `已整理 ${ai.ready} / ${total}`;
    detail = ai.state === "paused" && ai.reason !== "foreground" ? "设备正忙，稍后继续整理" : "相册空闲 5 分钟后继续整理";
  }
  return (
    <div className={`ph-ai ${ai.state}`} role="status" aria-label="本地 AI 状态">
      <Sparkles />
      <div>
        <strong>{title}</strong>
        <small>{detail}{ai.failed ? `；${ai.failed} 张无法识别` : ""}</small>
        {ai.state !== "unavailable" && total > 0 && <span className="ph-ai-bar"><span style={{ width: `${Math.round(share * 100)}%` }} /></span>}
      </div>
    </div>
  );
}
