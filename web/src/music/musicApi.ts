import { request } from "../api";

export type TrackFormat =
  | "mp3"
  | "flac"
  | "m4a"
  | "aac"
  | "ogg"
  | "opus"
  | "wav"
  | "ape"
  | "wma"
  | "wv"
  | "aiff"
  | "dsf"
  | "dff";

export interface Track {
  id: string;
  spaceId: string;
  // Folder path relative to the space root; "" at the root.
  folder: string;
  name: string;
  sizeBytes: number;
  format: TrackFormat;
  codec?: string;
  title: string;
  artist?: string;
  artists?: string[];
  album?: string;
  albumArtist?: string;
  albumId: string;
  genres?: string[];
  year?: number;
  trackNo?: number;
  discNo?: number;
  durationMs?: number;
  coverId?: string;
  lyrics?: boolean;
  // The device decodes this track while it plays; its stream cannot seek, so
  // the player asks for a new start instead.
  transcode?: boolean;
  addedAt: string;
}

export interface MusicLibrary {
  version: number;
  items: Track[];
}

export interface ScanStatus {
  state: "idle" | "scanning";
  phase?: "listing" | "reading" | "measuring";
  done: number;
  total: number;
  startedAt?: string;
  finishedAt?: string;
  error?: "volume_unavailable" | "failed";
}

export interface MusicStatus {
  version: number;
  tracks: number;
  scan: ScanStatus;
}

export interface MusicSource {
  spaceId: string;
  spaceKind: "private" | "shared";
  folderId?: string;
  path: string;
}
export interface MusicSources {
  custom: boolean;
  items: MusicSource[];
}

export interface Favorite {
  trackId: string;
  addedAt: string;
}
export interface Play {
  trackId: string;
  playedAt: string;
  plays: number;
}
export interface Playlist {
  id: string;
  name: string;
  trackIds: string[];
  createdAt: string;
  updatedAt: string;
}

const base = "/api/v1/music";
const id = encodeURIComponent;
const json = (method: string, value: unknown): RequestInit => ({
  method,
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify(value),
});

export const getMusicStatus = () => request<MusicStatus>(`${base}/library`);
export const scanMusic = () => request<MusicStatus>(`${base}/library/scan`, { method: "POST" }, true);
export const listTracks = () => request<MusicLibrary>(`${base}/tracks`);
export const getLyrics = async (trackId: string) =>
  (await request<{ text: string }>(`${base}/tracks/${id(trackId)}/lyrics`)).text;
export const streamURL = (trackId: string, start = 0) =>
  `${base}/tracks/${id(trackId)}/stream${start > 0 ? `?start=${start.toFixed(3)}` : ""}`;
export const coverURL = (coverId: string, size: "small" | "large" = "small") =>
  `${base}/covers/${id(coverId)}?size=${size}`;

export const listSources = () => request<MusicSources>(`${base}/sources`);
export const setSources = (custom: boolean, items: Array<{ spaceId: string; folderId?: string }>) =>
  request<MusicSources>(`${base}/sources`, json("PUT", { custom, items }), true);

export const listFavorites = async () => (await request<{ items: Favorite[] }>(`${base}/favorites`)).items;
export const setFavorite = (trackId: string, favorite: boolean) =>
  request<void>(`${base}/favorites/${id(trackId)}`, { method: favorite ? "PUT" : "DELETE" }, true);

export const listRecent = async () => (await request<{ items: Play[] }>(`${base}/recent`)).items;
export const clearRecent = () => request<void>(`${base}/recent`, { method: "DELETE" }, true);
export const recordPlay = (trackId: string) => request<void>(`${base}/plays`, json("POST", { trackId }), true);

export const listPlaylists = async () => (await request<{ items: Playlist[] }>(`${base}/playlists`)).items;
export const createPlaylist = (name: string, trackIds: string[] = []) =>
  request<Playlist>(`${base}/playlists`, json("POST", { name, trackIds }), true);
export const updatePlaylist = (playlistId: string, change: { name?: string; trackIds?: string[] }) =>
  request<Playlist>(`${base}/playlists/${id(playlistId)}`, json("PATCH", change), true);
export const addToPlaylist = (playlistId: string, trackIds: string[]) =>
  request<Playlist>(`${base}/playlists/${id(playlistId)}/tracks`, json("POST", { trackIds }), true);
export const deletePlaylist = (playlistId: string) =>
  request<void>(`${base}/playlists/${id(playlistId)}`, { method: "DELETE" }, true);
