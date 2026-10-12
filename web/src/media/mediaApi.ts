import { request } from "../api";

export type LibraryKind = "movies" | "shows" | "mixed" | "other";
export type TitleType = "movie" | "show" | "episode" | "other";
export type Category = "all" | "movie" | "show" | "other";
export type SortOrder = "added" | "name" | "year" | "rating";
export type Quality = "original" | "1080p" | "720p" | "480p";

export interface LibraryFolder { entryId: string; name: string; path: string; missing?: boolean }
export interface MediaLibrary {
  id: string;
  name: string;
  kind: LibraryKind;
  spaceId: string;
  spaceKind: "private" | "shared";
  ownerUserId?: string;
  createdBy: string;
  createdAt: string;
  folders: LibraryFolder[];
  counts: { movies: number; shows: number; episodes: number; others: number; sizeBytes: number };
  scan: { state: "idle" | "scanning" | "processing"; pending: number; scannedAt?: string; error?: string };
  canManage: boolean;
  covers: string[];
}
export interface LibraryInput { name: string; kind?: LibraryKind; spaceId?: string; folderIds?: string[] }

export interface Artwork { poster: boolean; backdrop: boolean; thumb: boolean; version?: string }
export interface Progress { position: number; duration: number; watched: boolean; playedAt?: string }
export interface Title {
  id: string;
  type: TitleType;
  libraryId: string;
  title: string;
  originalTitle?: string;
  year?: number;
  genres?: string[];
  rating?: number;
  addedAt: string;
  duration?: number;
  resolution?: "4K" | "1080p" | "720p" | "SD";
  hdr?: string;
  artwork: Artwork;
  favorite: boolean;
  progress?: Progress;
  seasons?: number;
  episodes?: number;
  watchedEpisodes?: number;
  showId?: string;
  showTitle?: string;
  season?: number;
  episode?: number;
}
export interface TitlePage { items: Title[]; total: number }
export interface TitleResults extends TitlePage { genres: string[] }
export interface Home {
  featured: Title[];
  continue: Title[];
  recent: Title[];
  movies: Title[];
  shows: Title[];
  others: Title[];
  favorites: Title[];
  libraries: number;
  scanning: boolean;
}
export interface CollectionRef { id: string; name: string }
export interface Episode { id: string; season: number; episode: number; title: string; plot?: string; duration?: number; addedAt: string; artwork: Artwork; progress?: Progress }
export interface ShowDetail extends Title { plot?: string; next?: Episode; seasonList: { number: number; episodes: Episode[] }[]; collections: CollectionRef[] }
export interface AudioTrack { index: number; label: string; language?: string; codec: string; channels?: number; default?: boolean }
export interface SubtitleTrack { id: string; label: string; language?: string; external: boolean; default?: boolean }
export interface VideoDetail extends Title {
  plot?: string;
  episodeTitle?: string;
  file: { name: string; folder: string; sizeBytes: number; modifiedAt: string };
  media?: { container: string; videoCodec?: string; audioCodec?: string; width?: number; height?: number; frameRate?: number; bitrate?: number };
  probeState: "pending" | "ready" | "failed";
  audio: AudioTrack[];
  subtitles: SubtitleTrack[];
  previousId?: string;
  nextId?: string;
  collections: CollectionRef[];
  collection?: string;
}
export interface Playback { mode: "direct" | "remux" | "transcode"; url: string; duration?: number; reason?: string; audio: number; quality: Quality; qualities: Quality[] }
export interface Collection { id: string; name: string; automatic: boolean; count: number; updatedAt: string; covers: string[] }
export interface CollectionDetail extends Collection { items: Title[] }
export interface FolderRoot { libraryId: string; libraryName: string; entryId: string; name: string; path: string; videos: number; cover?: string; missing?: boolean }
export interface Folder { name: string; path: string; videos: number; cover?: string }
export interface FolderListing { root: FolderRoot; path: string; folders: Folder[]; videos: Title[] }

const base = "/api/v1/media";
const id = encodeURIComponent;
const body = (method: string, value: unknown): RequestInit => ({ method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(value) });

export const listLibraries = () => request<{ items: MediaLibrary[]; processing: boolean }>(`${base}/libraries`);
export const createLibrary = (input: LibraryInput) => request<MediaLibrary>(`${base}/libraries`, body("POST", input), true);
export const updateLibrary = (libraryId: string, input: LibraryInput) => request<MediaLibrary>(`${base}/libraries/${id(libraryId)}`, body("PATCH", input), true);
export const deleteLibrary = (libraryId: string) => request<void>(`${base}/libraries/${id(libraryId)}`, { method: "DELETE" }, true);
export const scanLibrary = (libraryId: string) => request<void>(`${base}/libraries/${id(libraryId)}/scan`, { method: "POST" }, true);

export const getHome = () => request<Home>(`${base}/home`);
export interface TitleQuery { category?: Category; library?: string; genre?: string; sort?: SortOrder; q?: string; offset?: number; limit?: number }
export function listTitles(query: TitleQuery) {
  const parameters = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) if (value !== undefined && value !== "") parameters.set(key, String(value));
  return request<TitleResults>(`${base}/titles?${parameters}`);
}
export const getShow = (showId: string) => request<ShowDetail>(`${base}/shows/${id(showId)}`);
export const getVideo = (videoId: string) => request<VideoDetail>(`${base}/videos/${id(videoId)}`);
export function getPlayback(videoId: string, options: { caps: string; audio?: number; quality?: Quality }) {
  const parameters = new URLSearchParams({ caps: options.caps, audio: String(options.audio ?? -1), quality: options.quality ?? "original" });
  return request<Playback>(`${base}/videos/${id(videoId)}/playback?${parameters}`);
}
// streamURL asks for a converted stream directly, for when the original
// file turned out not to play.
export function streamURL(videoId: string, mode: "remux" | "transcode", audio: number, quality: Quality, caps: string) {
  return `${base}/videos/${id(videoId)}/stream?${new URLSearchParams({ mode, audio: String(audio), quality, caps })}`;
}
// Progress is saved while the page may be closing, so it keeps the request alive.
export const saveProgress = (videoId: string, position: number, duration: number) =>
  request<Progress>(`${base}/videos/${id(videoId)}/progress`, { ...body("PUT", { position: Math.max(0, position), duration: Math.max(0, duration) }), keepalive: true }, true);
export const setWatched = (titleId: string, watched: boolean) => request<void>(`${base}/titles/${id(titleId)}/watched`, body("PUT", { watched }), true);
export const setFavorite = (titleId: string, favorite: boolean) => request<void>(`${base}/favorites/${id(titleId)}`, { method: favorite ? "PUT" : "DELETE" }, true);
export const listFavorites = () => request<TitlePage>(`${base}/favorites`);
export const listHistory = (offset = 0, limit = 200) => request<TitlePage>(`${base}/history?offset=${offset}&limit=${limit}`);
export const removeHistory = (videoId: string) => request<void>(`${base}/history/${id(videoId)}`, { method: "DELETE" }, true);
export const clearHistory = () => request<void>(`${base}/history`, { method: "DELETE" }, true);
export const listCollections = async () => (await request<{ items: Collection[] }>(`${base}/collections`)).items;
export const createCollection = (name: string, items: string[] = []) => request<Collection>(`${base}/collections`, body("POST", { name, items }), true);
export const getCollection = (collectionId: string) => request<CollectionDetail>(`${base}/collections/${id(collectionId)}`);
export const renameCollection = (collectionId: string, name: string) => request<void>(`${base}/collections/${id(collectionId)}`, body("PATCH", { name }), true);
export const deleteCollection = (collectionId: string) => request<void>(`${base}/collections/${id(collectionId)}`, { method: "DELETE" }, true);
export const addToCollection = (collectionId: string, items: string[]) => request<void>(`${base}/collections/${id(collectionId)}/items`, body("POST", { items }), true);
export const removeFromCollection = (collectionId: string, titleId: string) =>
  request<void>(`${base}/collections/${id(collectionId)}/items/${id(titleId)}`, { method: "DELETE" }, true);
export const listFolderRoots = async () => (await request<{ items: FolderRoot[] }>(`${base}/folders`)).items;
export const getFolder = (entryId: string, path = "") => request<FolderListing>(`${base}/folders/${id(entryId)}?path=${id(path)}`);

export type ArtworkKind = "poster" | "backdrop" | "thumb";
export const artworkURL = (titleId: string, kind: ArtworkKind, version = "") => `${base}/artwork/${id(titleId)}/${kind}${version ? `?v=${id(version)}` : ""}`;
export const subtitleURL = (videoId: string, trackId: string) => `${base}/videos/${id(videoId)}/subtitles/${id(trackId)}`;

// browserCapabilities lists what this browser decodes beyond what every
// browser plays, so the server can play more files without converting them.
export function browserCapabilities(): string {
  if (typeof document === "undefined") return "";
  const video = document.createElement("video");
  const can = (type: string) => {
    try { return video.canPlayType(type) !== ""; } catch { return false; }
  };
  const source = (type: string) => {
    try { return typeof MediaSource !== "undefined" && MediaSource.isTypeSupported(type); } catch { return false; }
  };
  const caps: string[] = [];
  if (can('video/mp4; codecs="hvc1.1.6.L93.B0"') || source('video/mp4; codecs="hvc1.1.6.L93.B0"')) caps.push("hevc");
  if (can('video/mp4; codecs="av01.0.05M.08"')) caps.push("av1");
  if (can('video/webm; codecs="vp9"')) caps.push("vp9", "webm");
  if (can('audio/mp4; codecs="ac-3"')) caps.push("ac3");
  if (can('audio/mp4; codecs="ec-3"')) caps.push("eac3");
  // Chromium opens Matroska files with codecs it knows but does not say so.
  const chromium = typeof navigator !== "undefined" && /Chrom(e|ium)\//.test(navigator.userAgent);
  if (chromium || can('video/x-matroska; codecs="avc1.64001F"')) caps.push("mkv");
  return caps.join(",");
}
