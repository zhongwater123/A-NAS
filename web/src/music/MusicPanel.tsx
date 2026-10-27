import { errorText } from "../errorMessages";
import { ArrowLeft, CircleAlert } from "lucide-react";
import {
  KeyboardEvent,
  useCallback,
  useDeferredValue,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";

import { formatBytes } from "../bytes";
import { useDialogs, useToasts } from "../photos/feedback";
import { type Music, MusicContext, type Place } from "./context";
import {
  buildIndex,
  formatDuration,
  type PinyinMatch,
  qualityOf,
  search,
  type SongSort,
  unknownAlbum,
  unknownArtist,
} from "./library";
import { type MenuState, TrackMenu } from "./Menu";
import {
  addToPlaylist,
  clearRecent,
  createPlaylist,
  deletePlaylist,
  type Favorite,
  getMusicStatus,
  listFavorites,
  listPlaylists,
  listRecent,
  listSources,
  listTracks,
  type MusicLibrary,
  type MusicSources,
  type MusicStatus,
  type Play as PlayRecord,
  type Playlist,
  recordPlay,
  scanMusic,
  setFavorite,
  setSources,
  type Track,
  updatePlaylist,
} from "./musicApi";
import { NowPlaying } from "./NowPlaying";
import { loadPinyin } from "./pinyin";
import { usePlayer } from "./player";
import { PlayerBar } from "./PlayerBar";
import { QueuePanel } from "./QueuePanel";
import { Sidebar } from "./Sidebar";
import { SourcesDialog } from "./SourcesDialog";
import {
  AlbumsView,
  AlbumView,
  ArtistsView,
  ArtistView,
  FavoritesView,
  FoldersView,
  GenresView,
  GenreView,
  LibraryEmpty,
  PlaylistView,
  RecentView,
  SearchView,
  SongsView,
} from "./views";
import "../photos/photos.css";
import "./music.css";

interface Props {
  userId: string;
  onOpenFiles?: () => void;
}

const statusPoll = 1500;
// While a scan runs the library reloads at most this often.
const reloadInterval = 5000;

const topLevel = new Set<Place["kind"]>([
  "songs",
  "albums",
  "genres",
  "artists",
  "recent",
  "favorites",
  "playlist",
  "search",
]);
const isTopLevel = (place: Place) => topLevel.has(place.kind) || (place.kind === "folders" && !place.key);

const placeKey = (place: Place) => {
  switch (place.kind) {
    case "album":
    case "playlist":
      return `${place.kind}:${place.id}`;
    case "artist":
    case "genre":
      return `${place.kind}:${place.key}`;
    case "folders":
      return `folders:${place.key ?? ""}`;
    default:
      return place.kind;
  }
};

function messageOf(error: unknown): string {
  return errorText(error, "music");
}

function useStored<T>(key: string, initial: T): [T, (value: T | ((current: T) => T)) => void] {
  const [value, setValue] = useState<T>(() => {
    try {
      const stored = window.localStorage.getItem(key);
      return stored === null ? initial : (JSON.parse(stored) as T);
    } catch {
      return initial;
    }
  });
  const set = useCallback(
    (next: T | ((current: T) => T)) =>
      setValue((current) => {
        const resolved = typeof next === "function" ? (next as (current: T) => T)(current) : next;
        try {
          window.localStorage.setItem(key, JSON.stringify(resolved));
        } catch {
          /* storage is optional */
        }
        return resolved;
      }),
    [key],
  );
  return [value, set];
}

// moveRows moves the entries at positions to just before target.
export function moveRows<T>(items: T[], positions: number[], target: number): T[] {
  const moving = new Set(positions);
  const kept = items.filter((_, position) => !moving.has(position));
  const at = target - positions.filter((position) => position < target).length;
  return [...kept.slice(0, at), ...positions.map((position) => items[position]), ...kept.slice(at)];
}

// MusicPanel is the music window: navigation on the left, the library in
// the middle and the player console along the bottom. The library is
// indexed on the NAS; this window loads it once and derives every view.
export function MusicPanel({ userId, onOpenFiles }: Props) {
  const [library, setLibrary] = useState<MusicLibrary>();
  const [status, setStatus] = useState<MusicStatus>();
  const [favorites, setFavorites] = useState<Favorite[]>([]);
  const [recent, setRecent] = useState<PlayRecord[]>([]);
  const [playlists, setPlaylists] = useState<Playlist[]>([]);
  const [sources, setSourcesState] = useState<MusicSources>();
  const [failure, setFailure] = useState("");
  const [history, setHistory] = useState<Place[]>([{ kind: "songs" }]);
  const place = history[history.length - 1];
  const [query, setQuery] = useState("");
  const deferredQuery = useDeferredValue(query);
  const [overlay, setOverlay] = useState<"lyrics" | "queue">();
  const [menu, setMenu] = useState<MenuState>();
  const [info, setInfo] = useState<string>();
  const [sourcesOpen, setSourcesOpen] = useState(false);
  const [songSort, setSongSort] = useStored<{ by: SongSort; descending: boolean }>("a-nas.music.songSort", {
    by: "title",
    descending: false,
  });
  const [albumSort, setAlbumSort] = useStored<"title" | "artist" | "year" | "added">("a-nas.music.albumSort", "title");
  const [searches, setSearches] = useStored<string[]>(`a-nas.music.${userId}.searches`, []);
  const [pinyin, setPinyin] = useState<PinyinMatch>();
  const { prompt, confirm, dialog } = useDialogs();
  const { toast, element: toasts } = useToasts();
  const appRef = useRef<HTMLDivElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const index = useMemo(() => buildIndex(library?.items ?? []), [library]);

  // Load everything once, then bring the library up to date in the background.
  useEffect(() => {
    let live = true;
    (async () => {
      try {
        const [tracks, favoriteItems, plays, lists, folders] = await Promise.all([
          listTracks(),
          listFavorites(),
          listRecent(),
          listPlaylists(),
          listSources(),
        ]);
        if (!live) return;
        setLibrary(tracks);
        setFavorites(favoriteItems);
        setRecent(plays);
        setPlaylists(lists);
        setSourcesState(folders);
        const started = await scanMusic();
        if (live) setStatus(started);
      } catch (caught) {
        if (live) setFailure(messageOf(caught));
      }
    })();
    return () => {
      live = false;
    };
  }, []);

  const scanning = status?.scan.state === "scanning";
  useEffect(() => {
    if (!scanning) return;
    let live = true;
    const timer = window.setInterval(() => {
      getMusicStatus().then(
        (next) => {
          if (live) setStatus(next);
        },
        () => undefined,
      );
    }, statusPoll);
    return () => {
      live = false;
      window.clearInterval(timer);
    };
  }, [scanning]);
  const lastReload = useRef(0);
  useEffect(() => {
    if (!status || !library || status.version === library.version) return;
    const wait = scanning ? Math.max(0, reloadInterval - (Date.now() - lastReload.current)) : 0;
    const timer = window.setTimeout(() => {
      lastReload.current = Date.now();
      listTracks().then(setLibrary, () => undefined);
    }, wait);
    return () => window.clearTimeout(timer);
  }, [status, library, scanning]);

  const rescan = useCallback(() => {
    scanMusic().then(setStatus, (caught: unknown) => toast({ tone: "error", text: messageOf(caught) }));
  }, [toast]);

  const onPlayed = useCallback((trackId: string) => {
    setRecent((current) => [
      {
        trackId,
        playedAt: new Date().toISOString(),
        plays: (current.find((play) => play.trackId === trackId)?.plays ?? 0) + 1,
      },
      ...current.filter((play) => play.trackId !== trackId),
    ]);
    recordPlay(trackId).catch(() => undefined);
  }, []);
  const onError = useCallback(
    (track: Track) => {
      toast({
        tone: "error",
        text: track.transcode
          ? `无法播放“${track.title}”：设备暂时无法转码这个文件`
          : `无法播放“${track.title}”，已跳过`,
      });
    },
    [toast],
  );
  const player = usePlayer({
    storageKey: `a-nas.music.${userId}.player`,
    tracks: index.byId,
    ready: Boolean(library),
    onPlayed,
    onError,
  });

  const favoriteSet = useMemo(() => new Set(favorites.map((item) => item.trackId)), [favorites]);
  // Quick toggles see each other's changes before the next render.
  const favoritesRef = useRef(favoriteSet);
  useLayoutEffect(() => {
    favoritesRef.current = favoriteSet;
  });
  const toggleFavorite = useCallback(
    (trackId: string) => {
      const on = !favoritesRef.current.has(trackId);
      favoritesRef.current = new Set(favoritesRef.current);
      if (on) favoritesRef.current.add(trackId);
      else favoritesRef.current.delete(trackId);
      setFavorites((current) =>
        on
          ? [{ trackId, addedAt: new Date().toISOString() }, ...current]
          : current.filter((item) => item.trackId !== trackId),
      );
      setFavorite(trackId, on).catch((caught: unknown) => {
        setFavorites((current) =>
          on
            ? current.filter((item) => item.trackId !== trackId)
            : [{ trackId, addedAt: new Date().toISOString() }, ...current],
        );
        toast({ tone: "error", text: messageOf(caught) });
      });
    },
    [toast],
  );

  const open = useCallback((next: Place) => {
    setMenu(undefined);
    setOverlay((current) => (current === "lyrics" ? undefined : current));
    setHistory((current) => {
      if (placeKey(current[current.length - 1]) === placeKey(next)) return current;
      return isTopLevel(next) ? [next] : [...current, next];
    });
  }, []);
  const back = () => setHistory((current) => (current.length > 1 ? current.slice(0, -1) : current));

  const replacePlaylist = (list: Playlist) =>
    setPlaylists((current) => current.map((item) => (item.id === list.id ? list : item)));
  const createPlaylistFlow = useCallback(
    async (ids: string[] = []) => {
      const name = await prompt({ title: "新建歌单", label: "歌单名称", confirm: "创建", maxLength: 80 });
      if (!name) return;
      try {
        const list = await createPlaylist(name, ids);
        setPlaylists((current) => [...current, list]);
        toast({
          tone: "done",
          text: ids.length ? `已创建“${list.name}”，加入了 ${ids.length} 首` : `已创建歌单“${list.name}”`,
          action: { label: "查看", run: () => open({ kind: "playlist", id: list.id }) },
        });
      } catch (caught) {
        toast({ tone: "error", text: messageOf(caught) });
      }
    },
    [open, prompt, toast],
  );
  const addToPlaylistFlow = useCallback(
    (playlistId: string | undefined, ids: string[]) => {
      if (!ids.length) return;
      if (!playlistId) {
        void createPlaylistFlow(ids);
        return;
      }
      addToPlaylist(playlistId, ids).then(
        (list) => {
          replacePlaylist(list);
          toast({ tone: "done", text: `已加入歌单“${list.name}”${ids.length > 1 ? `（${ids.length} 首）` : ""}` });
        },
        (caught: unknown) => toast({ tone: "error", text: messageOf(caught) }),
      );
    },
    [createPlaylistFlow, toast],
  );
  const changePlaylist = (list: Playlist, trackIds: string[]) => {
    replacePlaylist({ ...list, trackIds });
    updatePlaylist(list.id, { trackIds }).then(replacePlaylist, (caught: unknown) => {
      replacePlaylist(list);
      toast({ tone: "error", text: messageOf(caught) });
    });
  };
  const renamePlaylist = async (list: Playlist) => {
    const name = await prompt({
      title: "重命名歌单",
      label: "歌单名称",
      value: list.name,
      confirm: "保存",
      maxLength: 80,
    });
    if (!name || name === list.name) return;
    updatePlaylist(list.id, { name }).then(replacePlaylist, (caught: unknown) =>
      toast({ tone: "error", text: messageOf(caught) }),
    );
  };
  const removePlaylist = async (list: Playlist) => {
    const ok = await confirm({
      title: "删除歌单",
      message: <>删除“{list.name}”后无法恢复。歌单里的歌曲文件不会被删除。</>,
      confirm: "删除",
      danger: true,
    });
    if (!ok) return;
    try {
      await deletePlaylist(list.id);
      setPlaylists((current) => current.filter((item) => item.id !== list.id));
      setHistory([{ kind: "songs" }]);
      toast({ tone: "done", text: `已删除歌单“${list.name}”` });
    } catch (caught) {
      toast({ tone: "error", text: messageOf(caught) });
    }
  };
  const removeFromPlaylist = (playlistId: string, positions: number[]) => {
    const list = playlists.find((item) => item.id === playlistId);
    if (!list) return;
    const drop = new Set(positions);
    changePlaylist(
      list,
      list.trackIds.filter((_, position) => !drop.has(position)),
    );
    toast({
      tone: "done",
      text: `已从“${list.name}”移除 ${positions.length} 首`,
      action: { label: "撤销", run: () => changePlaylist(list, list.trackIds) },
    });
  };

  const clearHistory = async () => {
    const ok = await confirm({
      title: "清空播放记录",
      message: "最近播放的列表会被清空，不影响歌曲和收藏。",
      confirm: "清空",
      danger: true,
    });
    if (!ok) return;
    clearRecent().then(
      () => setRecent([]),
      (caught: unknown) => toast({ tone: "error", text: messageOf(caught) }),
    );
  };

  const saveSources = async (custom: boolean, items: Array<{ spaceId: string; folderId?: string }>) => {
    const saved = await setSources(
      custom,
      items.map(({ spaceId, folderId }) => ({ spaceId, folderId })),
    );
    setSourcesState(saved);
    setSourcesOpen(false);
    setHistory([{ kind: "songs" }]);
    rescan();
    listTracks().then(setLibrary, () => undefined);
  };

  // Search loads pinyin matching the first time it is used.
  const searching = place.kind === "search";
  useEffect(() => {
    if (!searching || pinyin) return;
    let live = true;
    loadPinyin().then(
      (match) => {
        if (live) setPinyin(() => match);
      },
      () => undefined,
    );
    return () => {
      live = false;
    };
  }, [searching, pinyin]);
  const results = useMemo(() => search(index, deferredQuery, pinyin), [index, deferredQuery, pinyin]);
  useEffect(() => {
    const term = query.trim();
    if (!term || !searching) return;
    const timer = window.setTimeout(
      () => setSearches((current) => [term, ...current.filter((item) => item !== term)].slice(0, 8)),
      1500,
    );
    return () => window.clearTimeout(timer);
  }, [query, searching, setSearches]);

  const music: Music = useMemo(
    () => ({
      index,
      player,
      favorites: favoriteSet,
      playlists,
      open,
      toggleFavorite,
      addToPlaylist: addToPlaylistFlow,
      showMenu: (point, target) => {
        const box = appRef.current?.getBoundingClientRect();
        setMenu({ x: point.clientX - (box?.left ?? 0), y: point.clientY - (box?.top ?? 0), target });
      },
    }),
    [index, player, favoriteSet, playlists, open, toggleFavorite, addToPlaylistFlow],
  );

  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    const target = event.target as HTMLElement;
    const typing = Boolean(target.closest("input, textarea, select, [contenteditable=true]"));
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "f") {
      event.preventDefault();
      searchRef.current?.focus();
      return;
    }
    if (typing) return;
    if (event.key === " " && !target.closest("button, [role=slider]")) {
      event.preventDefault();
      player.toggle();
    } else if ((event.ctrlKey || event.metaKey) && event.key === "ArrowRight") {
      event.preventDefault();
      player.next();
    } else if ((event.ctrlKey || event.metaKey) && event.key === "ArrowLeft") {
      event.preventDefault();
      player.previous();
    } else if (event.key === "Escape" && overlay === "queue") setOverlay(undefined);
  };

  const plays = useMemo(() => new Map(recent.map((play) => [play.trackId, play.plays])), [recent]);
  const counts = {
    tracks: index.tracks.length,
    albums: index.albums.filter((album) => album.id !== "album:unknown").length,
    artists: index.artists.length,
    genres: index.genres.length,
    favorites: favorites.filter((item) => index.byId.has(item.trackId)).length,
  };

  let content;
  if (failure) {
    content = (
      <div className="mu-view mu-welcome">
        <span className="mu-welcome-art offline">
          <CircleAlert />
        </span>
        <h1>音乐库暂时打不开</h1>
        <p>{failure}</p>
        <div className="mu-hero-actions">
          <button type="button" className="mu-primary" onClick={() => window.location.reload()}>
            重新载入
          </button>
        </div>
      </div>
    );
  } else if (!library) {
    content = (
      <div className="mu-view mu-loading" aria-busy="true">
        <span className="mu-skeleton title" />
        {Array.from({ length: 8 }, (_, row) => (
          <span key={row} className="mu-skeleton row" />
        ))}
      </div>
    );
  } else if (!library.items.length && place.kind !== "playlist" && place.kind !== "search") {
    const progress = status?.scan.total ? `（${status.scan.done} / ${status.scan.total}）` : "";
    content = (
      <LibraryEmpty
        scanning={scanning || !status}
        progress={progress}
        volumeOffline={status?.scan.error === "volume_unavailable"}
        onOpenFiles={onOpenFiles}
        onSources={() => setSourcesOpen(true)}
        onRescan={rescan}
      />
    );
  } else {
    switch (place.kind) {
      case "songs":
        content = <SongsView sort={songSort} onSort={setSongSort} />;
        break;
      case "albums":
        content = <AlbumsView sort={albumSort} onSort={setAlbumSort} />;
        break;
      case "album":
        content = <AlbumView album={index.albumsById.get(place.id)} />;
        break;
      case "artists":
        content = <ArtistsView />;
        break;
      case "artist":
        content = <ArtistView artist={index.artistsByKey.get(place.key)} plays={plays} />;
        break;
      case "genres":
        content = <GenresView />;
        break;
      case "genre":
        content = <GenreView genre={index.genresByKey.get(place.key)} />;
        break;
      case "folders":
        content = <FoldersView folderKey={place.key} sources={sources} />;
        break;
      case "recent":
        content = <RecentView recent={recent} onClear={() => void clearHistory()} />;
        break;
      case "favorites":
        content = <FavoritesView order={favorites.map((item) => item.trackId)} />;
        break;
      case "playlist": {
        const list = playlists.find((item) => item.id === place.id);
        content = (
          <PlaylistView
            playlist={list}
            onRename={() => list && void renamePlaylist(list)}
            onDelete={() => list && void removePlaylist(list)}
            onReorder={(positions, to) => list && changePlaylist(list, moveRows(list.trackIds, positions, to))}
          />
        );
        break;
      }
      case "search":
        content = (
          <SearchView
            query={deferredQuery}
            input={query}
            onInput={setQuery}
            results={results}
            history={searches}
            pinyinReady={Boolean(pinyin)}
            onPick={(term) => {
              setQuery(term);
              searchRef.current?.focus();
            }}
          />
        );
        break;
    }
  }

  const infoTrack = info ? index.byId.get(info) : undefined;
  return (
    <MusicContext.Provider value={music}>
      <div
        className={`mu-app${overlay === "lyrics" && player.current ? " staged" : ""}`}
        ref={appRef}
        tabIndex={-1}
        onKeyDown={onKeyDown}
      >
        <Sidebar
          place={place}
          query={query}
          searchRef={searchRef}
          status={status}
          counts={counts}
          onPlace={open}
          onQuery={setQuery}
          onCreatePlaylist={(ids) => void createPlaylistFlow(ids)}
          onRescan={rescan}
          onSources={() => setSourcesOpen(true)}
        />
        <main className={`mu-main${history.length > 1 ? " has-back" : ""}`}>
          <div className="mu-topbar">
            {history.length > 1 && (
              <button type="button" className="mu-back" aria-label="返回" onClick={back}>
                <ArrowLeft />
              </button>
            )}
          </div>
          <div className="mu-content" key={placeKey(place)}>
            {content}
          </div>
          {overlay === "queue" && <QueuePanel onClose={() => setOverlay(undefined)} />}
        </main>
        {overlay === "lyrics" && player.current && <NowPlaying onClose={() => setOverlay(undefined)} />}
        <PlayerBar view={overlay} onView={setOverlay} />
        {menu && (
          <TrackMenu
            menu={menu}
            onClose={() => setMenu(undefined)}
            onRemoveFromPlaylist={removeFromPlaylist}
            onInfo={setInfo}
          />
        )}
        {infoTrack && <TrackInfo track={infoTrack} sources={sources} onClose={() => setInfo(undefined)} />}
        {sourcesOpen && sources && (
          <SourcesDialog sources={sources} onSave={saveSources} onClose={() => setSourcesOpen(false)} />
        )}
        {dialog}
        {toasts}
      </div>
    </MusicContext.Provider>
  );
}

function TrackInfo({ track, sources, onClose }: { track: Track; sources?: MusicSources; onClose: () => void }) {
  useEffect(() => {
    const escape = (event: globalThis.KeyboardEvent) => {
      if (event.key === "Escape") onClose();
    };
    window.addEventListener("keydown", escape);
    return () => window.removeEventListener("keydown", escape);
  }, [onClose]);
  const kind = sources?.items.find((source) => source.spaceId === track.spaceId)?.spaceKind;
  const location = [kind === "shared" ? "共享空间" : "个人空间", track.folder, track.name].filter(Boolean).join(" / ");
  const rows: Array<[string, string]> = [
    ["标题", track.title],
    ["歌手", track.artists?.join(" / ") || track.artist || unknownArtist],
    ["专辑", track.album || unknownAlbum],
    ...(track.albumArtist ? [["专辑歌手", track.albumArtist] as [string, string]] : []),
    ...(track.genres?.length ? [["类型", track.genres.join(" / ")] as [string, string]] : []),
    ...(track.year ? [["年份", String(track.year)] as [string, string]] : []),
    ...(track.trackNo
      ? [
          ["曲目", track.discNo ? `第 ${track.discNo} 碟 · 第 ${track.trackNo} 首` : `第 ${track.trackNo} 首`] as [
            string,
            string,
          ],
        ]
      : []),
    ["时长", formatDuration(track.durationMs)],
    ["格式", `${qualityOf(track)} · ${formatBytes(track.sizeBytes)}`],
    ["位置", location],
    ["加入音乐库", new Date(track.addedAt).toLocaleString("zh-CN")],
  ];
  return (
    <div
      className="ph-dialog-backdrop"
      onPointerDown={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <div className="ph-dialog mu-info" role="dialog" aria-modal="true" aria-label="歌曲信息">
        <h3>歌曲信息</h3>
        <dl>
          {rows.map(([label, value]) => (
            <div key={label}>
              <dt>{label}</dt>
              <dd>{value}</dd>
            </div>
          ))}
        </dl>
        <div className="ph-dialog-actions">
          <button type="button" className="primary" onClick={onClose}>
            完成
          </button>
        </div>
      </div>
    </div>
  );
}
