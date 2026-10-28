import {
  Clock3,
  Disc3,
  FolderCog,
  FolderOpen,
  Heart,
  Library,
  Loader2,
  Mic2,
  Plus,
  RefreshCw,
  Search,
  Tags,
  X,
} from "lucide-react";
import { DragEvent, ReactNode, RefObject, useState } from "react";

import { errorCodeText } from "../api/errorMessages";
import type { MusicStatus } from "./api";
import { carriesTracks, Mosaic, type Place, readDrag, useMusic } from "./context";
import styles from "./music.module.css";
import { playlistCovers } from "./views";

interface Props {
  place: Place;
  query: string;
  searchRef: RefObject<HTMLInputElement | null>;
  status?: MusicStatus;
  counts: { tracks: number; albums: number; artists: number; genres: number; favorites: number };
  onPlace: (place: Place) => void;
  onQuery: (query: string) => void;
  onCreatePlaylist: (ids?: string[]) => void;
  onRescan: () => void;
  onSources: () => void;
}

// Sidebar is the left column: search, the library's ways in, the member's
// own collections and playlists, and how far the library is organised.
export function Sidebar({
  place,
  query,
  searchRef,
  status,
  counts,
  onPlace,
  onQuery,
  onCreatePlaylist,
  onRescan,
  onSources,
}: Props) {
  const music = useMusic();
  const is = (kind: Place["kind"], ...also: Place["kind"][]) => place.kind === kind || also.includes(place.kind);
  const link = (target: Place, icon: ReactNode, label: string, active: boolean, count?: number) => (
    <button
      type="button"
      className={active ? styles.active : ""}
      aria-current={active ? "page" : undefined}
      onClick={() => onPlace(target)}
    >
      {icon}
      <span>{label}</span>
      {count !== undefined && count > 0 && <small>{count.toLocaleString("zh-CN")}</small>}
    </button>
  );
  return (
    <nav className={styles.muSidebar} aria-label="音乐导航">
      <button
        type="button"
        className={`${styles.muSearchIcon}${place.kind === "search" ? ` ${styles.active}` : ""}`}
        aria-label="搜索"
        onClick={() => onPlace({ kind: "search" })}
      >
        <Search />
      </button>
      <label className={`${styles.muSearch}${place.kind === "search" ? ` ${styles.active}` : ""}`}>
        <Search />
        <input
          ref={searchRef}
          type="search"
          placeholder="搜索歌曲、歌手、专辑"
          value={query}
          onChange={(event) => {
            onQuery(event.target.value);
            if (place.kind !== "search") onPlace({ kind: "search" });
          }}
          onFocus={() => {
            if (place.kind !== "search") onPlace({ kind: "search" });
          }}
          onKeyDown={(event) => {
            if (event.key === "Escape") {
              onQuery("");
              event.currentTarget.blur();
            }
          }}
        />
        {query && (
          <button type="button" aria-label="清除搜索" onClick={() => onQuery("")}>
            <X />
          </button>
        )}
      </label>

      <div className={styles.muNavScroll}>
        <div className={styles.muNav}>
          {link({ kind: "songs" }, <Library />, "音乐库", is("songs"), counts.tracks)}
          {link({ kind: "folders" }, <FolderOpen />, "文件夹", is("folders"))}
          {link({ kind: "albums" }, <Disc3 />, "专辑", is("albums", "album"), counts.albums)}
          {link({ kind: "genres" }, <Tags />, "类型", is("genres", "genre"), counts.genres)}
          {link({ kind: "artists" }, <Mic2 />, "歌手", is("artists", "artist"), counts.artists)}
        </div>

        <p className={styles.muNavLabel}>我的</p>
        <div className={styles.muNav}>
          {link({ kind: "recent" }, <Clock3 />, "最近播放", is("recent"))}
          <DropLink
            active={is("favorites")}
            onDrop={(ids) => {
              for (const id of ids) if (!music.favorites.has(id)) music.toggleFavorite(id);
            }}
            onClick={() => onPlace({ kind: "favorites" })}
          >
            <Heart />
            <span>我的收藏</span>
            {counts.favorites > 0 && <small>{counts.favorites}</small>}
          </DropLink>
        </div>

        <div className={`${styles.muNavLabel} ${styles.withAction}`}>
          <span>歌单</span>
          <button type="button" aria-label="新建歌单" title="新建歌单" onClick={() => onCreatePlaylist()}>
            <Plus />
          </button>
        </div>
        <div className={`${styles.muNav} ${styles.muPlaylists}`}>
          {music.playlists.map((playlist) => {
            const active = place.kind === "playlist" && place.id === playlist.id;
            return (
              <DropLink
                key={playlist.id}
                active={active}
                onDrop={(ids) => music.addToPlaylist(playlist.id, ids)}
                onClick={() => onPlace({ kind: "playlist", id: playlist.id })}
              >
                <span className={styles.muNavThumb}>
                  <Mosaic coverIds={playlistCovers(playlist, music.index)} name={playlist.name} />
                </span>
                <span>{playlist.name}</span>
                <small>{playlist.trackIds.length || ""}</small>
              </DropLink>
            );
          })}
          <DropLink
            active={false}
            className={styles.muNewPlaylist}
            onDrop={(ids) => onCreatePlaylist(ids)}
            onClick={() => onCreatePlaylist()}
          >
            <span className={`${styles.muNavThumb} ${styles.add}`}>
              <Plus />
            </span>
            <span>{music.playlists.length ? "新建歌单" : "新建第一个歌单"}</span>
          </DropLink>
        </div>
      </div>

      <LibraryCard status={status} onRescan={onRescan} onSources={onSources} />
    </nav>
  );
}

// DropLink is a sidebar entry that takes songs dragged from a list.
function DropLink({
  active,
  className = "",
  onDrop,
  onClick,
  children,
}: {
  active: boolean;
  className?: string;
  onDrop: (ids: string[]) => void;
  onClick: () => void;
  children: ReactNode;
}) {
  const [over, setOver] = useState(false);
  return (
    <button
      type="button"
      className={`${className}${active ? ` ${styles.active}` : ""}${over ? ` ${styles.drop}` : ""}`}
      aria-current={active ? "page" : undefined}
      onClick={onClick}
      onDragOver={(event: DragEvent) => {
        if (carriesTracks(event.dataTransfer)) {
          event.preventDefault();
          event.dataTransfer.dropEffect = "copy";
          setOver(true);
        }
      }}
      onDragLeave={() => setOver(false)}
      onDrop={(event: DragEvent) => {
        setOver(false);
        const payload = readDrag(event.dataTransfer);
        if (!payload) return;
        event.preventDefault();
        onDrop(payload.ids);
      }}
    >
      {children}
    </button>
  );
}

const phaseLabels = { listing: "正在查找音乐文件", reading: "正在读取歌曲信息", measuring: "正在计算歌曲时长" };

function LibraryCard({
  status,
  onRescan,
  onSources,
}: {
  status?: MusicStatus;
  onRescan: () => void;
  onSources: () => void;
}) {
  const scanning = status?.scan.state === "scanning";
  const scan = status?.scan;
  const share = scan && scan.total ? scan.done / scan.total : 0;
  let title = status ? `${status.tracks.toLocaleString("zh-CN")} 首歌曲` : "正在打开音乐库";
  let detail = "音乐库已是最新";
  if (scanning) {
    title = phaseLabels[scan!.phase ?? "listing"];
    detail = scan!.total
      ? `${scan!.done.toLocaleString("zh-CN")} / ${scan!.total.toLocaleString("zh-CN")}`
      : `已有 ${status!.tracks.toLocaleString("zh-CN")} 首`;
  } else if (scan?.error === "volume_unavailable") detail = errorCodeText("volume_unavailable")!;
  else if (scan?.error) detail = "上次整理没有完成";
  return (
    <div
      className={`${styles.muLibraryCard}${scanning ? ` ${styles.scanning}` : ""}${scan?.error ? ` ${styles.problem}` : ""}`}
      role="status"
      aria-label="音乐库状态"
    >
      <div className={styles.muLibraryText}>
        <strong>{title}</strong>
        <small>{detail}</small>
        {scanning && (
          <span className={styles.muLibraryBar}>
            <span
              style={{ width: `${scan!.total ? Math.max(4, share * 100) : 12}%` }}
              className={scan!.total ? "" : styles.indeterminate}
            />
          </span>
        )}
      </div>
      <button
        type="button"
        className={`${styles.muIconButton} ${styles.small}`}
        aria-label="重新扫描音乐库"
        title="重新扫描"
        disabled={scanning}
        onClick={onRescan}
      >
        {scanning ? <Loader2 className={styles.spin} /> : <RefreshCw />}
      </button>
      <button
        type="button"
        className={`${styles.muIconButton} ${styles.small}`}
        aria-label="音乐文件夹"
        title="音乐文件夹"
        onClick={onSources}
      >
        <FolderCog />
      </button>
    </div>
  );
}
