import { relativeTime as formatRelativeTime } from "../lib/time";
import { Lrc } from "lrc-kit";

import type { MusicSource, Track } from "./api";

export const unknownAlbum = "未知专辑";
export const unknownArtist = "未知歌手";
export const variousArtists = "群星";

// Chinese collation orders Hanzi by pinyin, so 陈奕迅 sorts beside Chen.
export const collator = new Intl.Collator("zh-CN", { numeric: true, sensitivity: "base" });
export const compareText = (left = "", right = "") => collator.compare(left, right);

export interface Album {
  id: string;
  title: string;
  artist: string;
  year?: number;
  tracks: Track[];
  coverId?: string;
  durationMs: number;
  addedAt: string;
}

export interface Artist {
  key: string;
  name: string;
  tracks: Track[];
  albums: Album[];
  coverId?: string;
}

export interface Genre {
  key: string;
  name: string;
  tracks: Track[];
  coverIds: string[];
}

export interface Folder {
  key: string;
  spaceId: string;
  path: string;
  name: string;
  folders: Folder[];
  tracks: Track[];
  // Tracks in this folder and every folder below it.
  total: number;
}

export interface LibraryIndex {
  tracks: Track[];
  byId: Map<string, Track>;
  albums: Album[];
  albumsById: Map<string, Album>;
  artists: Artist[];
  artistsByKey: Map<string, Artist>;
  genres: Genre[];
  genresByKey: Map<string, Genre>;
  folders: Map<string, Folder>;
}

export const artistKey = (name: string) => name.trim().toLocaleLowerCase("zh-CN");
export const folderKey = (spaceId: string, path: string) => `${spaceId}\u0000${path}`;
export const artistsOf = (track: Track) => (track.artists?.length ? track.artists : [track.artist || unknownArtist]);
export const artistLine = (track: Track) =>
  track.artists?.length ? track.artists.join(" / ") : track.artist || unknownArtist;

export const byAlbumOrder = (left: Track, right: Track) =>
  (left.discNo ?? 1) - (right.discNo ?? 1) ||
  (left.trackNo ?? 0) - (right.trackNo ?? 0) ||
  compareText(left.name, right.name);

export function buildIndex(tracks: Track[]): LibraryIndex {
  const byId = new Map(tracks.map((track) => [track.id, track]));
  const albumTracks = new Map<string, Track[]>();
  for (const track of tracks) {
    const list = albumTracks.get(track.albumId);
    if (list) list.push(track);
    else albumTracks.set(track.albumId, [track]);
  }
  const albums: Album[] = [];
  for (const [id, list] of albumTracks) {
    list.sort(byAlbumOrder);
    const first = list[0];
    const credited = new Set(list.map((track) => artistsOf(track)[0]));
    const unknown = id === "album:unknown";
    albums.push({
      id,
      title: unknown ? unknownAlbum : (first.album ?? unknownAlbum),
      artist: unknown
        ? variousArtists
        : first.albumArtist || (credited.size === 1 ? artistsOf(first)[0] : variousArtists),
      year: list.find((track) => track.year)?.year,
      tracks: list,
      coverId: list.find((track) => track.coverId)?.coverId,
      durationMs: list.reduce((sum, track) => sum + (track.durationMs ?? 0), 0),
      addedAt: list.reduce((latest, track) => (track.addedAt > latest ? track.addedAt : latest), ""),
    });
  }
  albums.sort(
    (left, right) =>
      Number(left.id === "album:unknown") - Number(right.id === "album:unknown") ||
      compareText(left.title, right.title),
  );
  const albumsById = new Map(albums.map((album) => [album.id, album]));

  const artistsByKey = new Map<string, Artist>();
  for (const track of tracks) {
    for (const name of artistsOf(track)) {
      const key = artistKey(name);
      let artist = artistsByKey.get(key);
      if (!artist) {
        artist = { key, name, tracks: [], albums: [] };
        artistsByKey.set(key, artist);
      }
      artist.tracks.push(track);
    }
  }
  for (const artist of artistsByKey.values()) {
    const ids = new Set(artist.tracks.map((track) => track.albumId));
    artist.albums = albums
      .filter((album) => ids.has(album.id) && album.id !== "album:unknown")
      .sort((left, right) => (right.year ?? 0) - (left.year ?? 0) || compareText(left.title, right.title));
    artist.coverId =
      artist.albums.find((album) => album.coverId)?.coverId ?? artist.tracks.find((track) => track.coverId)?.coverId;
    artist.tracks.sort((left, right) => compareText(left.title, right.title));
  }
  const artists = [...artistsByKey.values()].sort(
    (left, right) =>
      Number(left.name === unknownArtist) - Number(right.name === unknownArtist) || compareText(left.name, right.name),
  );

  const genresByKey = new Map<string, Genre>();
  for (const track of tracks) {
    for (const name of track.genres ?? []) {
      const key = artistKey(name);
      let genre = genresByKey.get(key);
      if (!genre) {
        genre = { key, name, tracks: [], coverIds: [] };
        genresByKey.set(key, genre);
      }
      genre.tracks.push(track);
      if (track.coverId && genre.coverIds.length < 4 && !genre.coverIds.includes(track.coverId))
        genre.coverIds.push(track.coverId);
    }
  }
  const genres = [...genresByKey.values()].sort(
    (left, right) => right.tracks.length - left.tracks.length || compareText(left.name, right.name),
  );

  return {
    tracks,
    byId,
    albums,
    albumsById,
    artists,
    artistsByKey,
    genres,
    genresByKey,
    folders: buildFolders(tracks),
  };
}

// buildFolders makes the folder tree of every space that holds music; the
// key "" path is each space's root.
function buildFolders(tracks: Track[]): Map<string, Folder> {
  const folders = new Map<string, Folder>();
  const ensure = (spaceId: string, path: string): Folder => {
    const key = folderKey(spaceId, path);
    let folder = folders.get(key);
    if (folder) return folder;
    folder = { key, spaceId, path, name: path.split("/").at(-1) ?? "", folders: [], tracks: [], total: 0 };
    folders.set(key, folder);
    if (path) {
      const parent = ensure(spaceId, path.includes("/") ? path.slice(0, path.lastIndexOf("/")) : "");
      parent.folders.push(folder);
    }
    return folder;
  };
  for (const track of tracks) {
    const folder = ensure(track.spaceId, track.folder);
    folder.tracks.push(track);
    for (let current: Folder | undefined = folder; current; ) {
      current.total += 1;
      const parentPath: string | undefined = current.path
        ? current.path.includes("/")
          ? current.path.slice(0, current.path.lastIndexOf("/"))
          : ""
        : undefined;
      current = parentPath === undefined ? undefined : folders.get(folderKey(track.spaceId, parentPath));
    }
  }
  for (const folder of folders.values()) {
    folder.folders.sort((left, right) => compareText(left.name, right.name));
    folder.tracks.sort(byAlbumOrder);
  }
  return folders;
}

// sourceFolder is the folder a music source starts at, if it holds music.
export const sourceFolder = (index: LibraryIndex, source: MusicSource) =>
  index.folders.get(folderKey(source.spaceId, source.path));

// folderTracks lists every track in a folder and below it, in folder order.
export function folderTracks(folder: Folder): Track[] {
  return [...folder.tracks, ...folder.folders.flatMap(folderTracks)];
}

export type SongSort = "title" | "artist" | "album" | "added" | "duration";

export function sortTracks(tracks: Track[], sort: SongSort, descending = false): Track[] {
  const compare: Record<SongSort, (left: Track, right: Track) => number> = {
    title: (left, right) => compareText(left.title, right.title),
    artist: (left, right) =>
      compareText(artistLine(left), artistLine(right)) ||
      compareText(left.album, right.album) ||
      byAlbumOrder(left, right),
    album: (left, right) => compareText(left.album, right.album) || byAlbumOrder(left, right),
    added: (left, right) => left.addedAt.localeCompare(right.addedAt),
    duration: (left, right) => (left.durationMs ?? 0) - (right.durationMs ?? 0),
  };
  const sorted = [...tracks].sort(compare[sort]);
  return descending ? sorted.reverse() : sorted;
}

// PinyinMatch tells whether Hanzi text matches a pinyin query such as "zjl"
// or "zhoujielun"; search loads it on first use.
export type PinyinMatch = (text: string, query: string) => boolean;

const normalize = (value = "") => value.normalize("NFKC").toLocaleLowerCase("zh-CN");
const hasHan = (value: string) => /\p{Script=Han}/u.test(value);

// matchScore ranks how well text matches a query: 0 is no match.
function matchScore(text: string | undefined, query: string, pinyin?: PinyinMatch): number {
  if (!text) return 0;
  const value = normalize(text);
  if (value === query) return 4;
  if (value.startsWith(query)) return 3;
  if (value.includes(query)) return 2;
  if (pinyin && /^[a-z\s]+$/.test(query) && hasHan(text) && pinyin(text, query.replace(/\s+/g, ""))) return 1;
  return 0;
}

export interface SearchResults {
  tracks: Track[];
  albums: Album[];
  artists: Artist[];
}

// search matches every word of the query against titles, artists and albums,
// by text or, for Hanzi, by pinyin, and orders closer matches first.
export function search(index: LibraryIndex, rawQuery: string, pinyin?: PinyinMatch): SearchResults {
  const query = normalize(rawQuery.trim());
  if (!query) return { tracks: [], albums: [], artists: [] };
  const words = query.split(/\s+/);
  const scoreAll = (fields: Array<string | undefined>, weights: number[]) => {
    let total = 0;
    for (const word of words) {
      let best = 0;
      fields.forEach((field, position) => {
        best = Math.max(best, matchScore(field, word, pinyin) * weights[position]);
      });
      if (!best) {
        // A whole query in pinyin may span words: "zhou jie lun".
        if (words.length > 1 && pinyin && fields.some((field) => matchScore(field, query, pinyin))) return 1;
        return 0;
      }
      total += best;
    }
    return total;
  };
  const rank = <T>(items: T[], score: (item: T) => number, name: (item: T) => string) =>
    items
      .map((item) => ({ item, score: score(item) }))
      .filter((entry) => entry.score > 0)
      .sort((left, right) => right.score - left.score || compareText(name(left.item), name(right.item)))
      .map((entry) => entry.item);
  return {
    tracks: rank(
      index.tracks,
      (track) => scoreAll([track.title, artistLine(track), track.album], [3, 2, 1]),
      (track) => track.title,
    ),
    albums: rank(
      index.albums.filter((album) => album.id !== "album:unknown"),
      (album) => scoreAll([album.title, album.artist], [3, 2]),
      (album) => album.title,
    ),
    artists: rank(
      index.artists,
      (artist) => scoreAll([artist.name], [3]),
      (artist) => artist.name,
    ),
  };
}

// formatDuration shows a length or position; undefined means unknown.
export function formatDuration(ms?: number): string {
  if (ms === undefined || !Number.isFinite(ms) || ms < 0) return "--:--";
  const total = Math.round(ms / 1000);
  const hours = Math.floor(total / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  const seconds = String(total % 60).padStart(2, "0");
  return hours ? `${hours}:${String(minutes).padStart(2, "0")}:${seconds}` : `${minutes}:${seconds}`;
}

export function formatTotal(ms: number): string {
  if (ms < 60_000) return `${Math.max(1, Math.round(ms / 1000))} 秒`;
  const minutes = Math.round(ms / 60000);
  if (minutes < 60) return `${minutes} 分钟`;
  const hours = Math.floor(minutes / 60);
  return minutes % 60 ? `${hours} 小时 ${minutes % 60} 分钟` : `${hours} 小时`;
}

export const totalDuration = (tracks: Track[]) => tracks.reduce((sum, track) => sum + (track.durationMs ?? 0), 0);

export function summary(tracks: Track[]): string {
  const total = totalDuration(tracks);
  return total ? `${tracks.length} 首 · ${formatTotal(total)}` : `${tracks.length} 首`;
}

export function relativeTime(iso: string, now = Date.now()): string {
  return formatRelativeTime(iso, now, { dateAfterDays: 7 });
}

export const formatLabel: Record<string, string> = {
  mp3: "MP3",
  flac: "FLAC",
  m4a: "AAC",
  aac: "AAC",
  ogg: "OGG",
  opus: "OPUS",
  wav: "WAV",
  ape: "APE",
  wma: "WMA",
  wv: "WavPack",
  aiff: "AIFF",
  dsf: "DSD",
  dff: "DSD",
};
export const qualityOf = (track: Track) =>
  track.codec === "alac" ? "ALAC" : (formatLabel[track.format] ?? track.format.toUpperCase());
const losslessFormats = new Set(["flac", "wav", "ape", "wv", "aiff", "dsf", "dff"]);
// WMA Lossless shares the .wma extension with lossy WMA.
export const lossless = (track: Track) =>
  losslessFormats.has(track.format) || track.codec === "alac" || track.codec === "wmalossless";

export interface LyricLine {
  time: number;
  text: string;
}
export interface Lyrics {
  synced: boolean;
  lines: LyricLine[];
}

// parseLyrics reads LRC text with lrc-kit; text without time tags is shown
// as plain lyrics. Times are in seconds.
export function parseLyrics(text: string): Lyrics {
  const parsed = Lrc.parse(text);
  const lines = parsed.lyrics
    .map((lyric) => ({ time: lyric.timestamp, text: lyric.content.trim() }))
    .sort((left, right) => left.time - right.time);
  if (lines.length) return { synced: true, lines };
  const plain = text
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter((line) => line && !/^\[[a-z]+:.*\]$/i.test(line));
  return { synced: false, lines: plain.map((line) => ({ time: -1, text: line })) };
}

// activeLine is the index of the synced line playing at time, or -1.
export function activeLine(lyrics: Lyrics, time: number): number {
  if (!lyrics.synced) return -1;
  let found = -1;
  for (let index = 0; index < lyrics.lines.length; index++) {
    if (lyrics.lines[index].time <= time + 0.15) found = index;
    else break;
  }
  return found;
}

export function shuffled<T>(items: T[]): T[] {
  const copy = [...items];
  for (let index = copy.length - 1; index > 0; index--) {
    const other = Math.floor(Math.random() * (index + 1));
    [copy[index], copy[other]] = [copy[other], copy[index]];
  }
  return copy;
}

// hue gives a name a stable colour for covers and genre cards.
export function hue(name: string): number {
  let hash = 0;
  for (const character of name) hash = (hash * 31 + character.codePointAt(0)!) % 360;
  return hash;
}
