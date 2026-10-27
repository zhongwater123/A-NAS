import {
  ArrowDownUp,
  ChevronRight,
  Clock3,
  Disc3,
  Folder as FolderIcon,
  FolderOpen,
  Heart,
  ListMusic,
  Mic2,
  Music2,
  Pencil,
  Play,
  Search,
  Shuffle,
  Tags,
  Trash2,
} from "lucide-react";
import { MouseEvent, ReactNode, useMemo, useRef } from "react";

import { Cover, Mosaic, useMusic } from "./context";
import {
  type Album,
  type Artist,
  artistKey,
  folderTracks,
  type Genre,
  hue,
  type LibraryIndex,
  relativeTime,
  type SearchResults,
  type SongSort,
  sortTracks,
  sourceFolder,
  summary,
} from "./library";
import { coverURL, type MusicSources, type Play as PlayRecord, type Playlist, type Track } from "./api";
import { TrackList, type TrackListProps } from "./TrackList";

const spaceName = (kind: string) => (kind === "shared" ? "共享空间" : "个人空间");

function Actions({ tracks, children }: { tracks: Track[]; children?: ReactNode }) {
  const { player } = useMusic();
  const ids = tracks.map((track) => track.id);
  return (
    <div className="mu-hero-actions">
      <button type="button" className="mu-primary" disabled={!ids.length} onClick={() => player.play(ids, 0)}>
        <Play />
        <span>播放全部</span>
      </button>
      <button type="button" className="mu-secondary" disabled={!ids.length} onClick={() => player.playShuffled(ids)}>
        <Shuffle />
        <span>随机播放</span>
      </button>
      {children}
    </div>
  );
}

interface HeroProps {
  art: ReactNode;
  label: string;
  title: ReactNode;
  meta?: ReactNode;
  tracks: Track[];
  actions?: ReactNode;
  backdrop?: string;
  tone?: number;
}

// Hero heads a collection: its art, name, what it holds and how to play it.
function Hero({ art, label, title, meta, tracks, actions, backdrop, tone }: HeroProps) {
  return (
    <header
      className={`mu-hero${backdrop || tone !== undefined ? " tinted" : ""}`}
      style={tone !== undefined ? { ["--mu-tone" as string]: tone } : undefined}
    >
      {backdrop && (
        <span className="mu-hero-backdrop" style={{ backgroundImage: `url("${backdrop}")` }} aria-hidden="true" />
      )}
      <span className="mu-hero-art">{art}</span>
      <div className="mu-hero-text">
        <small>{label}</small>
        <h1>{title}</h1>
        <p>{meta ?? summary(tracks)}</p>
        <Actions tracks={tracks}>{actions}</Actions>
      </div>
    </header>
  );
}

function Heading({ title, meta, children }: { title: string; meta?: ReactNode; children?: ReactNode }) {
  return (
    <header className="mu-heading">
      <div>
        <h1>{title}</h1>
        {meta && <p>{meta}</p>}
      </div>
      {children && <div className="mu-heading-tools">{children}</div>}
    </header>
  );
}

function Empty({ icon, title, children }: { icon: ReactNode; title: string; children?: ReactNode }) {
  return (
    <div className="mu-empty">
      <span className="mu-empty-icon">{icon}</span>
      <h2>{title}</h2>
      {children && <p>{children}</p>}
    </div>
  );
}

function useList() {
  const scrollRef = useRef<HTMLDivElement>(null);
  const { player } = useMusic();
  const play = (tracks: Track[]) => (position: number) =>
    player.play(
      tracks.map((track) => track.id),
      position,
    );
  return { scrollRef, play };
}

function List(
  props: Omit<TrackListProps, "scrollRef" | "onPlay"> & {
    scrollRef: TrackListProps["scrollRef"];
    onPlay?: TrackListProps["onPlay"];
  },
) {
  const { player } = useMusic();
  return (
    <TrackList
      {...props}
      onPlay={
        props.onPlay ??
        ((position) =>
          player.play(
            props.tracks.map((track) => track.id),
            position,
          ))
      }
    />
  );
}

// --- Songs ---------------------------------------------------------------

const sortLabels: Record<SongSort, string> = {
  title: "歌名",
  artist: "歌手",
  album: "专辑",
  added: "最近添加",
  duration: "时长",
};

export function SongsView({
  sort,
  onSort,
}: {
  sort: { by: SongSort; descending: boolean };
  onSort: (sort: { by: SongSort; descending: boolean }) => void;
}) {
  const { index } = useMusic();
  const { scrollRef } = useList();
  const tracks = useMemo(() => sortTracks(index.tracks, sort.by, sort.descending), [index.tracks, sort]);
  return (
    <div className="mu-view" ref={scrollRef}>
      <Heading title="音乐库" meta={`全部歌曲 · ${summary(tracks)}`}>
        <label className="mu-sort">
          <ArrowDownUp />
          <select
            aria-label="排序方式"
            value={`${sort.by}:${sort.descending ? "desc" : "asc"}`}
            onChange={(event) => {
              const [by, direction] = event.target.value.split(":");
              onSort({ by: by as SongSort, descending: direction === "desc" });
            }}
          >
            {(Object.keys(sortLabels) as SongSort[]).map((by) => (
              <option key={by} value={`${by}:${by === "added" ? "desc" : "asc"}`}>
                {sortLabels[by]}
              </option>
            ))}
          </select>
        </label>
        <Actions tracks={tracks} />
      </Heading>
      <List tracks={tracks} scrollRef={scrollRef} />
    </div>
  );
}

// --- Albums ----------------------------------------------------------------

function albumMenu(music: ReturnType<typeof useMusic>, event: MouseEvent, tracks: Track[]) {
  event.preventDefault();
  music.showMenu(event, { ids: tracks.map((track) => track.id) });
}

export function AlbumCard({ album }: { album: Album }) {
  const music = useMusic();
  const ids = album.tracks.map((track) => track.id);
  const playing = music.player.current?.albumId === album.id;
  return (
    <article
      className={`mu-card${playing ? " playing" : ""}`}
      onContextMenu={(event) => albumMenu(music, event, album.tracks)}
    >
      <div className="mu-card-media">
        <button
          type="button"
          className="mu-card-art"
          aria-label={`打开专辑 ${album.title}`}
          onClick={() => music.open({ kind: "album", id: album.id })}
        >
          <Cover id={album.coverId} name={album.title} size="large" />
        </button>
        <button
          type="button"
          className="mu-card-play"
          aria-label={`播放专辑 ${album.title}`}
          onClick={() => music.player.play(ids, 0)}
        >
          <Play />
        </button>
      </div>
      <button type="button" className="mu-card-title" onClick={() => music.open({ kind: "album", id: album.id })}>
        {album.title}
      </button>
      <p className="mu-card-meta">
        {album.artist}
        {album.year ? ` · ${album.year}` : ""}
      </p>
    </article>
  );
}

type AlbumSort = "title" | "artist" | "year" | "added";

export function AlbumsView({ sort, onSort }: { sort: AlbumSort; onSort: (sort: AlbumSort) => void }) {
  const { index } = useMusic();
  const albums = useMemo(() => {
    const list = [...index.albums];
    if (sort === "artist")
      list.sort(
        (left, right) =>
          left.artist.localeCompare(right.artist, "zh-CN") || left.title.localeCompare(right.title, "zh-CN"),
      );
    if (sort === "year") list.sort((left, right) => (right.year ?? 0) - (left.year ?? 0));
    if (sort === "added") list.sort((left, right) => right.addedAt.localeCompare(left.addedAt));
    return list;
  }, [index.albums, sort]);
  return (
    <div className="mu-view">
      <Heading title="专辑" meta={`${albums.filter((album) => album.id !== "album:unknown").length} 张专辑`}>
        <label className="mu-sort">
          <ArrowDownUp />
          <select aria-label="排序方式" value={sort} onChange={(event) => onSort(event.target.value as AlbumSort)}>
            <option value="title">专辑名</option>
            <option value="artist">歌手</option>
            <option value="year">发行年份</option>
            <option value="added">最近添加</option>
          </select>
        </label>
      </Heading>
      {albums.length ? (
        <div className="mu-grid">
          {albums.map((album) => (
            <AlbumCard key={album.id} album={album} />
          ))}
        </div>
      ) : (
        <Empty icon={<Disc3 />} title="还没有专辑">
          带有专辑信息的歌曲会自动归入专辑。
        </Empty>
      )}
    </div>
  );
}

export function AlbumView({ album }: { album?: Album }) {
  const music = useMusic();
  const { scrollRef } = useList();
  if (!album)
    return (
      <div className="mu-view">
        <Empty icon={<Disc3 />} title="专辑不存在">
          它的歌曲可能已被移动或删除。
        </Empty>
      </div>
    );
  const artist = music.index.artistsByKey.get(artistKey(album.artist));
  const more = artist?.albums.filter((other) => other.id !== album.id) ?? [];
  const multiDisc = new Set(album.tracks.map((track) => track.discNo ?? 1)).size > 1;
  return (
    <div className="mu-view" ref={scrollRef}>
      <Hero
        art={<Cover id={album.coverId} name={album.title} size="large" />}
        backdrop={album.coverId ? coverURL(album.coverId) : undefined}
        label={album.id === "album:unknown" ? "未归入专辑的歌曲" : "专辑"}
        title={album.title}
        tracks={album.tracks}
        meta={
          <>
            {artist ? (
              <button
                type="button"
                className="mu-inline-link"
                onClick={() => music.open({ kind: "artist", key: artist.key })}
              >
                {album.artist}
              </button>
            ) : (
              album.artist
            )}
            {album.year ? ` · ${album.year}` : ""} · {summary(album.tracks)}
            {multiDisc ? ` · ${new Set(album.tracks.map((track) => track.discNo ?? 1)).size} 张碟` : ""}
          </>
        }
      />
      <List tracks={album.tracks} scrollRef={scrollRef} numbering="album" showAlbum={false} />
      {more.length > 0 && (
        <section className="mu-section">
          <h2>更多来自 {album.artist}</h2>
          <div className="mu-grid">
            {more.slice(0, 12).map((other) => (
              <AlbumCard key={other.id} album={other} />
            ))}
          </div>
        </section>
      )}
    </div>
  );
}

// --- Artists ----------------------------------------------------------------

export function ArtistCard({ artist }: { artist: Artist }) {
  const music = useMusic();
  return (
    <article className="mu-card artist" onContextMenu={(event) => albumMenu(music, event, artist.tracks)}>
      <div className="mu-card-media">
        <button
          type="button"
          className="mu-card-art"
          aria-label={`打开歌手 ${artist.name}`}
          onClick={() => music.open({ kind: "artist", key: artist.key })}
        >
          <Cover id={artist.coverId} name={artist.name} kind="artist" size="large" />
        </button>
        <button
          type="button"
          className="mu-card-play"
          aria-label={`播放 ${artist.name} 的歌曲`}
          onClick={() =>
            music.player.play(
              artist.tracks.map((track) => track.id),
              0,
            )
          }
        >
          <Play />
        </button>
      </div>
      <button type="button" className="mu-card-title" onClick={() => music.open({ kind: "artist", key: artist.key })}>
        {artist.name}
      </button>
      <p className="mu-card-meta">
        {artist.albums.length ? `${artist.albums.length} 张专辑 · ` : ""}
        {artist.tracks.length} 首
      </p>
    </article>
  );
}

export function ArtistsView() {
  const { index } = useMusic();
  return (
    <div className="mu-view">
      <Heading title="歌手" meta={`${index.artists.length} 位歌手`} />
      {index.artists.length ? (
        <div className="mu-grid artists">
          {index.artists.map((artist) => (
            <ArtistCard key={artist.key} artist={artist} />
          ))}
        </div>
      ) : (
        <Empty icon={<Mic2 />} title="还没有歌手" />
      )}
    </div>
  );
}

export function ArtistView({ artist, plays }: { artist?: Artist; plays: Map<string, number> }) {
  const { scrollRef } = useList();
  // Songs the member plays most come first.
  const tracks = useMemo(
    () =>
      artist ? [...artist.tracks].sort((left, right) => (plays.get(right.id) ?? 0) - (plays.get(left.id) ?? 0)) : [],
    [artist, plays],
  );
  if (!artist)
    return (
      <div className="mu-view">
        <Empty icon={<Mic2 />} title="歌手不存在" />
      </div>
    );
  return (
    <div className="mu-view" ref={scrollRef}>
      <Hero
        art={<Cover id={artist.coverId} name={artist.name} kind="artist" size="large" />}
        label="歌手"
        title={artist.name}
        tracks={tracks}
        backdrop={artist.coverId ? coverURL(artist.coverId) : undefined}
        meta={`${artist.albums.length ? `${artist.albums.length} 张专辑 · ` : ""}${summary(artist.tracks)}`}
      />
      {artist.albums.length > 0 && (
        <section className="mu-section">
          <h2>专辑</h2>
          <div className="mu-grid">
            {artist.albums.map((album) => (
              <AlbumCard key={album.id} album={album} />
            ))}
          </div>
        </section>
      )}
      <section className="mu-section">
        <h2>歌曲</h2>
      </section>
      <List tracks={tracks} scrollRef={scrollRef} />
    </div>
  );
}

// --- Genres ----------------------------------------------------------------

export function GenresView() {
  const music = useMusic();
  const genres = music.index.genres;
  return (
    <div className="mu-view">
      <Heading title="类型" meta={`${genres.length} 种类型`} />
      {genres.length ? (
        <div className="mu-genres">
          {genres.map((genre) => (
            <GenreCard key={genre.key} genre={genre} />
          ))}
        </div>
      ) : (
        <Empty icon={<Tags />} title="还没有类型">
          歌曲的“流派”标签会出现在这里。
        </Empty>
      )}
    </div>
  );
}

function GenreCard({ genre }: { genre: Genre }) {
  const music = useMusic();
  const tone = hue(genre.name);
  return (
    <button
      type="button"
      className="mu-genre"
      style={{ ["--mu-tone" as string]: tone }}
      onClick={() => music.open({ kind: "genre", key: genre.key })}
      onContextMenu={(event) => albumMenu(music, event, genre.tracks)}
    >
      <strong>{genre.name}</strong>
      <small>{genre.tracks.length} 首</small>
      {genre.coverIds[0] && (
        <span className="mu-genre-art">
          <Cover id={genre.coverIds[0]} name={genre.name} />
        </span>
      )}
    </button>
  );
}

export function GenreView({ genre }: { genre?: Genre }) {
  const { scrollRef } = useList();
  const tracks = useMemo(() => (genre ? sortTracks(genre.tracks, "artist") : []), [genre]);
  if (!genre)
    return (
      <div className="mu-view">
        <Empty icon={<Tags />} title="类型不存在" />
      </div>
    );
  return (
    <div className="mu-view" ref={scrollRef}>
      <Hero
        art={
          <span className="mu-icon-art genre">
            <Tags />
          </span>
        }
        tone={hue(genre.name)}
        label="类型"
        title={genre.name}
        tracks={tracks}
      />
      <List tracks={tracks} scrollRef={scrollRef} />
    </div>
  );
}

// --- Folders ----------------------------------------------------------------

export function FoldersView({ folderKey, sources }: { folderKey?: string; sources?: MusicSources }) {
  const music = useMusic();
  const { scrollRef } = useList();
  const folder = folderKey ? music.index.folders.get(folderKey) : undefined;
  const roots = useMemo(
    () => (sources?.items ?? []).map((source) => ({ source, folder: sourceFolder(music.index, source) })),
    [sources, music.index],
  );
  const all = useMemo(() => (folder ? folderTracks(folder) : []), [folder]);
  if (!folderKey) {
    return (
      <div className="mu-view">
        <Heading title="文件夹" meta="按音乐所在的文件夹浏览" />
        <div className="mu-folders">
          {roots.map(({ source, folder: root }) => (
            <button
              key={`${source.spaceId}:${source.path}`}
              type="button"
              className="mu-folder"
              disabled={!root}
              onClick={() => root && music.open({ kind: "folders", key: root.key })}
            >
              <span className="mu-folder-icon">
                <FolderOpen />
              </span>
              <span>
                <strong>{source.path ? source.path.split("/").at(-1) : spaceName(source.spaceKind)}</strong>
                <small>{source.path ? `${spaceName(source.spaceKind)} / ${source.path}` : "整个空间"}</small>
              </span>
              <em>{root ? `${root.total} 首` : "没有音乐"}</em>
              <ChevronRight />
            </button>
          ))}
        </div>
      </div>
    );
  }
  if (!folder)
    return (
      <div className="mu-view">
        <Empty icon={<FolderIcon />} title="文件夹里没有音乐" />
      </div>
    );
  const kind = sources?.items.find((source) => source.spaceId === folder.spaceId)?.spaceKind ?? "private";
  const trail = folder.path ? folder.path.split("/") : [];
  return (
    <div className="mu-view" ref={scrollRef}>
      <nav className="mu-crumbs" aria-label="文件夹路径">
        <button type="button" onClick={() => music.open({ kind: "folders" })}>
          文件夹
        </button>
        <ChevronRight />
        <button type="button" onClick={() => music.open({ kind: "folders", key: `${folder.spaceId}\u0000` })}>
          {spaceName(kind)}
        </button>
        {trail.map((name, at) => (
          <span key={at}>
            <ChevronRight />
            <button
              type="button"
              onClick={() =>
                music.open({ kind: "folders", key: `${folder.spaceId}\u0000${trail.slice(0, at + 1).join("/")}` })
              }
            >
              {name}
            </button>
          </span>
        ))}
      </nav>
      <Heading title={folder.name || spaceName(kind)} meta={summary(all)}>
        <Actions tracks={all} />
      </Heading>
      {folder.folders.length > 0 && (
        <div className="mu-folders compact">
          {folder.folders.map((child) => (
            <button
              key={child.key}
              type="button"
              className="mu-folder"
              onClick={() => music.open({ kind: "folders", key: child.key })}
              onContextMenu={(event) => albumMenu(music, event, folderTracks(child))}
            >
              <span className="mu-folder-icon">
                <FolderIcon />
              </span>
              <span>
                <strong>{child.name}</strong>
              </span>
              <em>{child.total} 首</em>
              <ChevronRight />
            </button>
          ))}
        </div>
      )}
      {folder.tracks.length > 0 && <List tracks={folder.tracks} scrollRef={scrollRef} />}
    </div>
  );
}

// --- Recent, favorites and playlists ------------------------------------------

export function RecentView({ recent, onClear }: { recent: PlayRecord[]; onClear: () => void }) {
  const { index } = useMusic();
  const { scrollRef } = useList();
  const rows = useMemo(
    () =>
      recent.flatMap((play) => {
        const track = index.byId.get(play.trackId);
        return track ? [{ track, play }] : [];
      }),
    [recent, index.byId],
  );
  const tracks = rows.map((row) => row.track);
  return (
    <div className="mu-view" ref={scrollRef}>
      <Hero
        art={
          <span className="mu-icon-art recent">
            <Clock3 />
          </span>
        }
        label="我的"
        title="最近播放"
        tracks={tracks}
        actions={
          rows.length > 0 && (
            <button type="button" className="mu-ghost" onClick={onClear}>
              <Trash2 />
              <span>清空记录</span>
            </button>
          )
        }
      />
      {rows.length ? (
        <List
          tracks={tracks}
          scrollRef={scrollRef}
          extra={{ label: "播放时间", render: (_, position) => relativeTime(rows[position].play.playedAt) }}
        />
      ) : (
        <Empty icon={<Clock3 />} title="还没有播放记录">
          听过的歌曲会按时间出现在这里。
        </Empty>
      )}
    </div>
  );
}

export function FavoritesView({ order }: { order: string[] }) {
  const { index } = useMusic();
  const { scrollRef } = useList();
  const tracks = useMemo(() => order.flatMap((id) => index.byId.get(id) ?? []), [order, index.byId]);
  return (
    <div className="mu-view" ref={scrollRef}>
      <Hero
        art={
          <span className="mu-icon-art favorite">
            <Heart />
          </span>
        }
        label="我的"
        title="我的收藏"
        tracks={tracks}
      />
      {tracks.length ? (
        <List tracks={tracks} scrollRef={scrollRef} />
      ) : (
        <Empty icon={<Heart />} title="还没有收藏">
          点歌曲右侧的 ♡，喜欢的歌就会收进这里。
        </Empty>
      )}
    </div>
  );
}

export function playlistCovers(playlist: Playlist, index: LibraryIndex): string[] {
  const covers: string[] = [];
  for (const id of playlist.trackIds) {
    const cover = index.byId.get(id)?.coverId;
    if (cover && !covers.includes(cover)) covers.push(cover);
    if (covers.length === 4) break;
  }
  return covers;
}

export function PlaylistView({
  playlist,
  onRename,
  onDelete,
  onReorder,
}: {
  playlist?: Playlist;
  onRename: () => void;
  onDelete: () => void;
  onReorder: (positions: number[], to: number) => void;
}) {
  const music = useMusic();
  const { scrollRef } = useList();
  // Rows keep their playlist positions so edits address the right entry
  // even when tracks missing from the library are hidden.
  const rows = useMemo(
    () =>
      (playlist?.trackIds ?? []).flatMap((id, position) => {
        const track = music.index.byId.get(id);
        return track ? [{ track, position }] : [];
      }),
    [playlist, music.index.byId],
  );
  if (!playlist)
    return (
      <div className="mu-view">
        <Empty icon={<ListMusic />} title="歌单不存在" />
      </div>
    );
  const tracks = rows.map((row) => row.track);
  const missing = playlist.trackIds.length - rows.length;
  return (
    <div className="mu-view" ref={scrollRef}>
      <Hero
        art={<Mosaic coverIds={playlistCovers(playlist, music.index)} name={playlist.name} />}
        label="歌单"
        title={playlist.name}
        tracks={tracks}
        meta={`${summary(tracks)}${missing ? ` · ${missing} 首暂不可用` : ""} · 更新于 ${relativeTime(playlist.updatedAt)}`}
        actions={
          <>
            <button type="button" className="mu-ghost icon" aria-label="重命名歌单" title="重命名" onClick={onRename}>
              <Pencil />
            </button>
            <button
              type="button"
              className="mu-ghost icon danger"
              aria-label="删除歌单"
              title="删除歌单"
              onClick={onDelete}
            >
              <Trash2 />
            </button>
          </>
        }
      />
      {rows.length ? (
        <TrackList
          tracks={tracks}
          scrollRef={scrollRef}
          onPlay={(position) =>
            music.player.play(
              tracks.map((track) => track.id),
              position,
            )
          }
          playlist={{
            id: playlist.id,
            reorder: (from, to) =>
              onReorder(
                from.map((row) => rows[row].position),
                to < rows.length ? rows[to].position : playlist.trackIds.length,
              ),
          }}
        />
      ) : (
        <Empty icon={<ListMusic />} title="歌单还是空的">
          在歌曲上点右键选择“添加到歌单”，或把歌曲拖到左侧的歌单。
        </Empty>
      )}
    </div>
  );
}

// --- Search ------------------------------------------------------------------

// SearchField is the search page's own field, shown when a narrow window
// folds the sidebar and its search box away.
function SearchField({ value, onChange }: { value: string; onChange: (value: string) => void }) {
  return (
    <label className="mu-search mu-search-inline">
      <Search />
      <input
        type="search"
        placeholder="搜索歌曲、歌手、专辑"
        value={value}
        onChange={(event) => onChange(event.target.value)}
      />
    </label>
  );
}

export function SearchView({
  query,
  input,
  onInput,
  results,
  history,
  onPick,
  pinyinReady,
}: {
  query: string;
  input: string;
  onInput: (value: string) => void;
  results: SearchResults;
  history: string[];
  onPick: (query: string) => void;
  pinyinReady: boolean;
}) {
  const { scrollRef } = useList();
  const trimmed = query.trim();
  if (!trimmed) {
    return (
      <div className="mu-view">
        <SearchField value={input} onChange={onInput} />
        <Heading title="搜索" meta="歌名、歌手和专辑都能搜，也支持拼音和首字母" />
        <div className="mu-search-tips">
          <p>
            试试输入 <kbd>zjl</kbd> 找周杰伦，或 <kbd>qingtian</kbd> 找《晴天》。
          </p>
          {history.length > 0 && (
            <>
              <h2>最近搜索</h2>
              <div className="mu-chips">
                {history.map((item) => (
                  <button key={item} type="button" onClick={() => onPick(item)}>
                    <Search />
                    {item}
                  </button>
                ))}
              </div>
            </>
          )}
        </div>
      </div>
    );
  }
  const total = results.tracks.length + results.albums.length + results.artists.length;
  return (
    <div className="mu-view" ref={scrollRef}>
      <SearchField value={input} onChange={onInput} />
      <Heading
        title={`“${trimmed}”`}
        meta={
          total
            ? `${results.tracks.length} 首歌曲 · ${results.albums.length} 张专辑 · ${results.artists.length} 位歌手`
            : undefined
        }
      />
      {!total && (
        <Empty icon={<Search />} title="没有找到">
          {pinyinReady ? "换个关键词，或检查是否拼写正确。" : "正在加载拼音搜索…"}
        </Empty>
      )}
      {results.artists.length > 0 && (
        <section className="mu-section">
          <h2>歌手</h2>
          <div className="mu-grid artists single-row">
            {results.artists.slice(0, 6).map((artist) => (
              <ArtistCard key={artist.key} artist={artist} />
            ))}
          </div>
        </section>
      )}
      {results.albums.length > 0 && (
        <section className="mu-section">
          <h2>专辑</h2>
          <div className="mu-grid single-row">
            {results.albums.slice(0, 6).map((album) => (
              <AlbumCard key={album.id} album={album} />
            ))}
          </div>
        </section>
      )}
      {results.tracks.length > 0 && (
        <>
          <section className="mu-section">
            <h2>歌曲</h2>
          </section>
          <List tracks={results.tracks} scrollRef={scrollRef} />
        </>
      )}
    </div>
  );
}

// --- Library states ----------------------------------------------------------

export function LibraryEmpty({
  scanning,
  progress,
  volumeOffline,
  onOpenFiles,
  onSources,
  onRescan,
}: {
  scanning: boolean;
  progress?: string;
  volumeOffline: boolean;
  onOpenFiles?: () => void;
  onSources: () => void;
  onRescan: () => void;
}) {
  if (scanning) {
    return (
      <div className="mu-view mu-welcome">
        <span className="mu-welcome-art scanning">
          <Music2 />
        </span>
        <h1>正在整理你的音乐</h1>
        <p>A-NAS 正在读取个人空间和共享空间里的音乐文件，整理成专辑、歌手和类型。{progress}</p>
      </div>
    );
  }
  if (volumeOffline) {
    return (
      <div className="mu-view mu-welcome">
        <span className="mu-welcome-art offline">
          <Music2 />
        </span>
        <h1>数据卷暂时不可用</h1>
        <p>音乐存放在数据卷上。数据卷恢复在线后，点“重新扫描”即可继续。</p>
        <div className="mu-hero-actions">
          <button type="button" className="mu-primary" onClick={onRescan}>
            重新扫描
          </button>
        </div>
      </div>
    );
  }
  return (
    <div className="mu-view mu-welcome">
      <span className="mu-welcome-art">
        <Music2 />
      </span>
      <h1>还没有音乐</h1>
      <p>
        把音乐文件放进个人空间或共享空间的任意文件夹（例如新建一个“音乐”文件夹），A-NAS
        会自动整理成专辑、歌手和类型，并读取封面与歌词。
      </p>
      <p className="mu-welcome-formats">
        支持 MP3 · FLAC · AAC (M4A) · OGG · Opus · WAV，以及由设备转码播放的 ALAC · APE · WMA · WavPack · AIFF · DSD
      </p>
      <div className="mu-hero-actions">
        {onOpenFiles && (
          <button type="button" className="mu-primary" onClick={onOpenFiles}>
            打开文件管理
          </button>
        )}
        <button type="button" className="mu-secondary" onClick={onSources}>
          选择音乐文件夹
        </button>
        <button type="button" className="mu-ghost" onClick={onRescan}>
          重新扫描
        </button>
      </div>
    </div>
  );
}
