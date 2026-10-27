import { useVirtualizer } from "@tanstack/react-virtual";
import { Clock3, Heart, Play } from "lucide-react";
import {
  DragEvent,
  KeyboardEvent,
  MouseEvent,
  ReactNode,
  RefObject,
  useCallback,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";

import { Cover, carriesTracks, dragType, Equalizer, readDrag, useMusic } from "./context";
import { artistLine, artistKey, artistsOf, formatDuration, lossless, qualityOf, unknownAlbum } from "./library";
import type { Track } from "./api";

export interface TrackListProps {
  tracks: Track[];
  scrollRef: RefObject<HTMLDivElement | null>;
  // "number" shows each track's number on its album instead of its row.
  numbering?: "row" | "album";
  showAlbum?: boolean;
  // An extra column, such as when a track was played.
  extra?: { label: string; render: (track: Track, position: number) => ReactNode };
  onPlay: (position: number) => void;
  // Rows of a playlist can be dragged into a new order.
  playlist?: { id: string; reorder: (from: number[], to: number) => void };
}

const rowHeight = 54;

// dragLabel is the small pill that follows the pointer while dragging songs.
function dragLabel(count: number): HTMLElement {
  const label = document.createElement("div");
  label.className = "mu-drag-label";
  label.textContent = count === 1 ? "1 首歌曲" : `${count} 首歌曲`;
  document.body.appendChild(label);
  window.setTimeout(() => label.remove(), 0);
  return label;
}

// TrackList is a virtualized song table that shares its scroll container with
// the header above it. Click selects, Ctrl/Shift extend the selection, a
// double click or Enter plays, and the context menu acts on the selection.
export function TrackList({
  tracks,
  scrollRef,
  numbering = "row",
  showAlbum = true,
  extra,
  onPlay,
  playlist,
}: TrackListProps) {
  const music = useMusic();
  const listRef = useRef<HTMLDivElement>(null);
  const [selected, setSelected] = useState<ReadonlySet<number>>(new Set());
  const [anchor, setAnchor] = useState(-1);
  const [dropAt, setDropAt] = useState<number>();
  const [margin, setMargin] = useState(0);
  useLayoutEffect(() => {
    const list = listRef.current;
    const scroller = scrollRef.current;
    if (!list || !scroller) return;
    const measure = () =>
      setMargin(list.getBoundingClientRect().top - scroller.getBoundingClientRect().top + scroller.scrollTop);
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(scroller.firstElementChild ?? scroller);
    return () => observer.disconnect();
  }, [scrollRef]);
  // Selection is by row; a new list starts without one.
  const identity = useMemo(() => tracks.map((track) => track.id).join("\n"), [tracks]);
  const [selectedFor, setSelectedFor] = useState(identity);
  if (selectedFor !== identity) {
    setSelectedFor(identity);
    setSelected(new Set());
    setAnchor(-1);
  }

  const virtualizer = useVirtualizer({
    count: tracks.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => rowHeight,
    overscan: 12,
    scrollMargin: margin,
    initialRect: { width: 0, height: 900 },
  });

  const select = useCallback(
    (position: number, event: Pick<MouseEvent, "ctrlKey" | "metaKey" | "shiftKey">) => {
      if (event.shiftKey && anchor >= 0) {
        const range = new Set<number>();
        for (let index = Math.min(anchor, position); index <= Math.max(anchor, position); index++) range.add(index);
        setSelected(range);
        return;
      }
      if (event.ctrlKey || event.metaKey) {
        setSelected((current) => {
          const next = new Set(current);
          if (next.has(position)) next.delete(position);
          else next.add(position);
          return next;
        });
      } else setSelected(new Set([position]));
      setAnchor(position);
    },
    [anchor],
  );

  const chosen = (position: number) => {
    const rows = selected.has(position) ? [...selected].sort((a, b) => a - b) : [position];
    return { rows, ids: rows.map((row) => tracks[row].id) };
  };

  const openMenu = (event: MouseEvent, position: number) => {
    event.preventDefault();
    if (!selected.has(position)) {
      setSelected(new Set([position]));
      setAnchor(position);
    }
    const { rows, ids } = chosen(position);
    music.showMenu(event, { ids, playlist: playlist ? { id: playlist.id, positions: rows } : undefined });
  };

  const startDrag = (event: DragEvent, position: number) => {
    if (!selected.has(position)) {
      setSelected(new Set([position]));
      setAnchor(position);
    }
    const { rows, ids } = chosen(position);
    event.dataTransfer.effectAllowed = playlist ? "copyMove" : "copy";
    event.dataTransfer.setData(
      dragType,
      JSON.stringify({ ids, playlist: playlist ? { id: playlist.id, positions: rows } : undefined }),
    );
    event.dataTransfer.setDragImage(dragLabel(ids.length), 16, 16);
  };

  const dropProps = (position: number) =>
    playlist
      ? {
          onDragOver: (event: DragEvent) => {
            if (!carriesTracks(event.dataTransfer)) return;
            event.preventDefault();
            const box = (event.currentTarget as HTMLElement).getBoundingClientRect();
            setDropAt(event.clientY > box.top + box.height / 2 ? position + 1 : position);
          },
          onDrop: (event: DragEvent) => {
            const payload = readDrag(event.dataTransfer);
            const target = dropAt ?? position;
            setDropAt(undefined);
            if (!payload || payload.playlist?.id !== playlist.id) return;
            event.preventDefault();
            event.stopPropagation();
            playlist.reorder(payload.playlist.positions, target);
            setSelected(new Set());
          },
        }
      : {};

  const onKey = (event: KeyboardEvent) => {
    if (event.key === "Enter" && selected.size) {
      event.preventDefault();
      onPlay(Math.min(...selected));
    }
    if ((event.key === "a" || event.key === "A") && (event.ctrlKey || event.metaKey)) {
      event.preventDefault();
      setSelected(new Set(tracks.map((_, position) => position)));
    }
  };

  const items = virtualizer.getVirtualItems();
  const currentId = music.player.current?.id;
  return (
    <div
      ref={listRef}
      className={`mu-tracks${showAlbum ? "" : " no-album"}${extra ? " with-extra" : ""}`}
      role="grid"
      aria-label="歌曲"
      aria-rowcount={tracks.length}
      tabIndex={0}
      onKeyDown={onKey}
      onDragLeave={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget as Node)) setDropAt(undefined);
      }}
    >
      <div className="mu-track-head" role="row">
        <span>#</span>
        <span>标题</span>
        <span className="mu-col-artist">歌手</span>
        {showAlbum && <span className="mu-col-album">专辑</span>}
        {extra && <span className="mu-col-extra">{extra.label}</span>}
        <span className="mu-col-time" aria-label="时长">
          <Clock3 />
        </span>
        <span />
      </div>
      <div className="mu-track-body" style={{ height: virtualizer.getTotalSize() }}>
        {items.map((item) => {
          const track = tracks[item.index];
          return (
            <TrackRow
              key={`${track.id}:${item.index}`}
              track={track}
              position={item.index}
              top={item.start - virtualizer.options.scrollMargin}
              numbering={numbering}
              showAlbum={showAlbum}
              extra={extra?.render(track, item.index)}
              selected={selected.has(item.index)}
              current={track.id === currentId}
              paused={!music.player.playing}
              favorite={music.favorites.has(track.id)}
              dropBefore={dropAt === item.index}
              dropAfter={dropAt === tracks.length && item.index === tracks.length - 1}
              onSelect={select}
              onPlay={onPlay}
              onMenu={openMenu}
              onDragStart={startDrag}
              dropProps={dropProps(item.index)}
            />
          );
        })}
      </div>
    </div>
  );
}

interface RowProps {
  track: Track;
  position: number;
  top: number;
  numbering: "row" | "album";
  showAlbum: boolean;
  extra?: ReactNode;
  selected: boolean;
  current: boolean;
  paused: boolean;
  favorite: boolean;
  dropBefore: boolean;
  dropAfter: boolean;
  onSelect: (position: number, event: MouseEvent) => void;
  onPlay: (position: number) => void;
  onMenu: (event: MouseEvent, position: number) => void;
  onDragStart: (event: DragEvent, position: number) => void;
  dropProps: object;
}

function TrackRow({
  track,
  position,
  top,
  numbering,
  showAlbum,
  extra,
  selected,
  current,
  paused,
  favorite,
  dropBefore,
  dropAfter,
  onSelect,
  onPlay,
  onMenu,
  onDragStart,
  dropProps,
}: RowProps) {
  const music = useMusic();
  const number = numbering === "album" ? track.trackNo || position + 1 : position + 1;
  const artists = artistsOf(track);
  return (
    <div
      className={`mu-track${selected ? " selected" : ""}${current ? " current" : ""}${dropBefore ? " drop-before" : ""}${dropAfter ? " drop-after" : ""}`}
      role="row"
      aria-selected={selected}
      aria-rowindex={position + 1}
      style={{ transform: `translateY(${top}px)` }}
      draggable
      onClick={(event) => onSelect(position, event)}
      onDoubleClick={() => onPlay(position)}
      onContextMenu={(event) => onMenu(event, position)}
      onDragStart={(event) => onDragStart(event, position)}
      {...dropProps}
    >
      <span className="mu-track-index">
        {current ? <Equalizer paused={paused} /> : <span className="mu-track-number">{number}</span>}
        <button
          type="button"
          className="mu-track-play"
          aria-label={`播放 ${track.title}`}
          onClick={(event) => {
            event.stopPropagation();
            onPlay(position);
          }}
        >
          <Play />
        </button>
      </span>
      <span className="mu-track-title">
        <Cover id={track.coverId} name={track.album || track.title} kind="track" />
        <span className="mu-track-name">
          <strong title={track.title}>
            {track.title}
            {lossless(track) && <em className="mu-badge">{qualityOf(track)}</em>}
          </strong>
          <small className="mu-track-sub">{artistLine(track)}</small>
        </span>
      </span>
      <span className="mu-col-artist mu-links">
        {artists.map((name, at) => (
          <span key={name}>
            {at > 0 && " / "}
            <button
              type="button"
              onClick={(event) => {
                event.stopPropagation();
                music.open({ kind: "artist", key: artistKey(name) });
              }}
            >
              {name}
            </button>
          </span>
        ))}
      </span>
      {showAlbum && (
        <span className="mu-col-album mu-links">
          <button
            type="button"
            onClick={(event) => {
              event.stopPropagation();
              music.open({ kind: "album", id: track.albumId });
            }}
          >
            {track.album || unknownAlbum}
          </button>
        </span>
      )}
      {extra !== undefined && <span className="mu-col-extra">{extra}</span>}
      <span className="mu-col-time">{formatDuration(track.durationMs)}</span>
      <span className="mu-track-like">
        <button
          type="button"
          className={favorite ? "on" : ""}
          aria-label={favorite ? "取消收藏" : "收藏"}
          aria-pressed={favorite}
          onClick={(event) => {
            event.stopPropagation();
            music.toggleFavorite(track.id);
          }}
        >
          <Heart />
        </button>
      </span>
    </div>
  );
}
