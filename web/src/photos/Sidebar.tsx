import { Images, Library, type LucideIcon, Plus, Shapes, Sparkles, Trash2 } from "lucide-react";
import { DragEvent, useEffect, useState } from "react";

import { listAlbums, type PhotoAIStatus, type PhotoAlbum, type PhotoLibrary, thumbnailURL } from "./api";
import { type Caller, libraryName, type Place } from "./model";
import styles from "./photos.module.css";
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
  try {
    return JSON.parse(event.dataTransfer.getData(dragType));
  } catch {
    return [];
  }
};

// dropTarget makes an element accept photos dragged from the grid.
function useDropTarget(onDrop: (ids: string[]) => void, enabled: boolean) {
  const [over, setOver] = useState(false);
  if (!enabled) return { over: false, props: {} };
  return {
    over,
    props: {
      onDragOver: (event: DragEvent) => {
        if (carriesPhotos(event)) {
          event.preventDefault();
          event.dataTransfer.dropEffect = "copy";
          setOver(true);
        }
      },
      onDragLeave: () => setOver(false),
      onDrop: (event: DragEvent) => {
        if (!carriesPhotos(event)) return;
        event.preventDefault();
        event.stopPropagation();
        setOver(false);
        const ids = droppedIds(event);
        if (ids.length) onDrop(ids);
      },
    },
  };
}

export function Sidebar({
  caller,
  library,
  place,
  albumsKey,
  count,
  ai,
  onPlace,
  onLibrary,
  onCreateAlbum,
  onDropOnAlbum,
  onDropOnLibrary,
}: Props) {
  const libraryId = library?.id ?? "";
  // Another library starts with no albums until they are listed; a refresh
  // keeps the library's albums until the new list arrives.
  const [albums, setAlbums] = useState<PhotoAlbum[]>([]);
  const [albumsOf, setAlbumsOf] = useState(libraryId);
  if (albumsOf !== libraryId) {
    setAlbumsOf(libraryId);
    setAlbums([]);
  }
  useEffect(() => {
    if (!libraryId) return;
    let live = true;
    listAlbums(libraryId).then(
      (items) => {
        if (live) setAlbums(items);
      },
      () => undefined,
    );
    return () => {
      live = false;
    };
  }, [libraryId, albumsKey]);
  const readOnly = Boolean(library?.viewing);
  // The album list marks an album's own page; 相册 is current on the list of albums.
  const tabs = placeTabs(library, place, { albumIsAlbums: false });
  const albumsTab = tabs.find((tab) => tab.key === "albums")!;

  return (
    <nav className={styles.phSidebar} aria-label="相册导航">
      <LibraryTabs caller={caller} library={library} onLibrary={onLibrary} onDropOnLibrary={onDropOnLibrary} />

      <div className={styles.phNav}>
        {tabs
          .filter((tab) => tab.key !== "albums")
          .map((tab) => (
            <button
              key={tab.key}
              type="button"
              className={tab.on ? styles.active : ""}
              aria-current={tab.on ? "page" : undefined}
              title={tab.title}
              onClick={() => onPlace(tab.place)}
            >
              <tab.icon />
              <span>{tab.label}</span>
              {tab.key === "timeline" && tab.on && count !== undefined && <small>{count}</small>}
            </button>
          ))}
      </div>

      <div className={styles.phNavSection}>
        <button
          type="button"
          className={`${styles.phNavHeading}${albumsTab.on ? ` ${styles.active}` : ""}`}
          aria-current={albumsTab.on ? "page" : undefined}
          onClick={() => onPlace(albumsTab.place)}
        >
          <albumsTab.icon />
          <span>{albumsTab.label}</span>
          <small>{albums.length || ""}</small>
        </button>
        {!readOnly && (
          <button
            type="button"
            className={styles.phNavAdd}
            aria-label="新建相册"
            title="新建相册"
            onClick={onCreateAlbum}
          >
            <Plus />
          </button>
        )}
      </div>
      <div className={styles.phAlbumList}>
        {albums.map((album) => (
          <AlbumLink
            key={album.id}
            album={album}
            active={place.kind === "album" && place.album.id === album.id}
            droppable={!readOnly}
            onOpen={() => onPlace({ kind: "album", libraryId, album })}
            onDrop={(ids) => onDropOnAlbum(album, ids)}
          />
        ))}
        {!albums.length && (
          <p className={styles.phNavEmpty}>{readOnly ? "没有相册" : "把照片拖到这里的相册，或新建一个"}</p>
        )}
      </div>

      <AICard ai={ai} />
    </nav>
  );
}

// PlacesBar is the sidebar's navigation as one row under a phone's toolbar:
// the libraries as a switch, then the library's places as tabs. Albums open
// on their own page, where a new one is made; the AI card stays on a computer.
export function PlacesBar({
  caller,
  library,
  place,
  onPlace,
  onLibrary,
  onDropOnLibrary,
}: Pick<Props, "caller" | "library" | "place" | "onPlace" | "onLibrary" | "onDropOnLibrary">) {
  // With no album list to mark it in, an album's own page counts as 相册.
  const tabs = placeTabs(library, place, { albumIsAlbums: true });
  return (
    <nav className={styles.phPlaces} aria-label="相册导航">
      {caller.libraries.length > 1 && (
        <LibraryTabs caller={caller} library={library} onLibrary={onLibrary} onDropOnLibrary={onDropOnLibrary} />
      )}
      <div className={styles.phPlaceTabs}>
        {tabs.map((tab) => (
          <button
            key={tab.key}
            type="button"
            className={tab.on ? styles.active : ""}
            aria-current={tab.on ? "page" : undefined}
            onClick={() => onPlace(tab.place)}
          >
            {tab.label}
          </button>
        ))}
      </div>
    </nav>
  );
}

// placeTabs is a library's places, in the order a phone's row shows them;
// the sidebar lists 相册 apart, with its albums under it. Searches, AI
// clusters and tag groups all belong to AI 聚合; a library viewed under a
// grant has no 回收站.
export function placeTabs(
  library: PhotoLibrary | undefined,
  place: Place,
  { albumIsAlbums }: { albumIsAlbums: boolean },
) {
  const libraryId = library?.id ?? "";
  const readOnly = Boolean(library?.viewing);
  const at = (kind: Place["kind"]) =>
    place.kind === kind && ("libraryId" in place ? place.libraryId === libraryId : true);
  const tabs: Array<{
    key: "timeline" | "albums" | "ai" | "trash";
    label: string;
    title?: string;
    icon: LucideIcon;
    on: boolean;
    place: Place;
  }> = [
    // The library's photos and videos together, as in Apple's Photos.
    {
      key: "timeline",
      label: "图库",
      title: "图库中的全部照片和视频",
      icon: Images,
      on: at("timeline"),
      place: { kind: "timeline", libraryId },
    },
    {
      key: "albums",
      label: "相册",
      icon: Library,
      on: at("albums") || (albumIsAlbums && at("album")),
      place: { kind: "albums", libraryId },
    },
    {
      key: "ai",
      label: "AI 聚合",
      icon: Shapes,
      on: place.kind === "search" || place.kind === "cluster" || place.kind === "tag" || place.kind === "image",
      place: { kind: "search", viewing: readOnly ? libraryId : "", query: "" },
    },
  ];
  if (!readOnly)
    tabs.push({ key: "trash", label: "回收站", icon: Trash2, on: at("trash"), place: { kind: "trash", libraryId } });
  return tabs;
}

// LibraryTabs switches between the caller's libraries; photos dropped on one
// are copied there.
function LibraryTabs({
  caller,
  library,
  onLibrary,
  onDropOnLibrary,
}: Pick<Props, "caller" | "library" | "onLibrary" | "onDropOnLibrary">) {
  return (
    <div className={styles.phLibraries} role="tablist" aria-label="图库">
      {caller.libraries.map((item) => (
        <LibraryTab
          key={item.id}
          caller={caller}
          library={item}
          active={item.id === library?.id}
          onSelect={() => onLibrary(item)}
          onDrop={(ids) => onDropOnLibrary(item, ids)}
        />
      ))}
    </div>
  );
}

function LibraryTab({
  caller,
  library,
  active,
  onSelect,
  onDrop,
}: {
  caller: Caller;
  library: PhotoLibrary;
  active: boolean;
  onSelect: () => void;
  onDrop: (ids: string[]) => void;
}) {
  const target = useDropTarget(onDrop, !library.viewing);
  const name = library.kind === "shared" ? "共享" : library.viewing ? `${library.ownerName ?? "成员"}` : "我的";
  return (
    <button
      type="button"
      role="tab"
      aria-selected={active}
      title={library.viewing ? `只读查看 ${libraryName(caller, library)}` : libraryName(caller, library)}
      className={`${active ? styles.active : ""}${library.viewing ? ` ${styles.viewing}` : ""}${target.over ? ` ${styles.drop}` : ""}`}
      onClick={onSelect}
      {...target.props}
    >
      {name}
    </button>
  );
}

function AlbumLink({
  album,
  active,
  droppable,
  onOpen,
  onDrop,
}: {
  album: PhotoAlbum;
  active: boolean;
  droppable: boolean;
  onOpen: () => void;
  onDrop: (ids: string[]) => void;
}) {
  const target = useDropTarget(onDrop, droppable);
  return (
    <button
      type="button"
      className={`${styles.phAlbumLink}${active ? ` ${styles.active}` : ""}${target.over ? ` ${styles.drop}` : ""}`}
      aria-current={active ? "page" : undefined}
      onClick={onOpen}
      {...target.props}
    >
      <span className={styles.phAlbumThumb}>
        {album.coverId ? <img src={thumbnailURL(album.coverId)} alt="" loading="lazy" /> : <Images />}
      </span>
      <span>{album.name}</span>
      <small>{album.photos}</small>
    </button>
  );
}

// AICard shows how far the built-in AI has organised the caller's photos.
// AI is part of the product, not something users manage, so while it cannot
// run the card is simply absent.
function AICard({ ai }: { ai?: PhotoAIStatus }) {
  if (!ai || ai.state === "unavailable") return null;
  let total = ai.ready + ai.pending + ai.failed;
  let share = total ? ai.ready / total : 1;
  let title: string;
  let detail: string;
  let failed = ai.failed ? `；${ai.failed} 张无法识别` : "";
  // Local AI reads the text in photos once it has organised them all.
  const texts = !ai.pending && ai.texts?.queued ? ai.texts : undefined;
  if (texts) {
    const read = texts.read + texts.failed;
    total = read + texts.queued;
    share = read / total;
    title = `${ai.state === "working" ? "正在读取文字" : "已读取文字"} ${read} / ${total}`;
    detail =
      ai.state === "working"
        ? "读完后可以搜索照片中的文字"
        : ai.state === "paused" && ai.reason !== "foreground"
          ? "设备正忙，稍后继续读取"
          : "相册空闲 5 分钟后继续读取";
    if (texts.failed) failed += `；${texts.failed} 张没能读出`;
  } else if (!ai.pending) {
    title = `已整理全部 ${ai.ready} 张`;
    detail = "可以用自然语言搜索照片";
  } else if (ai.state === "working") {
    title = `正在整理 ${ai.ready} / ${total}`;
    detail = "完成后可以按内容搜索";
  } else {
    title = `已整理 ${ai.ready} / ${total}`;
    detail =
      ai.state === "paused" && ai.reason !== "foreground" ? "设备正忙，稍后继续整理" : "相册空闲 5 分钟后继续整理";
  }
  return (
    <div className={`${styles.phAi} ${styles[ai.state] ?? ""}`} role="status" aria-label="本地 AI 状态">
      <Sparkles />
      <div>
        <strong>{title}</strong>
        <small>
          {detail}
          {failed}
        </small>
        {total > 0 && (
          <span className={styles.phAiBar}>
            <span style={{ width: `${Math.round(share * 100)}%` }} />
          </span>
        )}
      </div>
    </div>
  );
}
