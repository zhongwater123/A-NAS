import { call, client } from "../api";
import type { MusicSources, MusicStatus, MusicTrack } from "../api.generated";

export type {
  MusicFavorite as Favorite,
  MusicLibrary,
  MusicPlay as Play,
  MusicPlaylist as Playlist,
  MusicSources,
  MusicStatus,
  MusicTrack as Track,
} from "../api.generated";
export type TrackFormat = MusicTrack["format"];
export type ScanStatus = MusicStatus["scan"];
export type MusicSource = MusicSources["items"][number];

const id = encodeURIComponent;
const track = (trackId: string) => ({ params: { path: { trackId } } });
const playlist = (playlistId: string) => ({ params: { path: { playlistId } } });

export const getMusicStatus = () => call(client.GET("/api/v1/music/library"));
export const scanMusic = () => call(client.POST("/api/v1/music/library/scan"));
export const listTracks = () => call(client.GET("/api/v1/music/tracks"));
export const getLyrics = async (trackId: string) =>
  (await call(client.GET("/api/v1/music/tracks/{trackId}/lyrics", track(trackId)))).text;
export const streamURL = (trackId: string, start = 0) =>
  `/api/v1/music/tracks/${id(trackId)}/stream${start > 0 ? `?start=${start.toFixed(3)}` : ""}`;
export const coverURL = (coverId: string, size: "small" | "large" = "small") =>
  `/api/v1/music/covers/${id(coverId)}?size=${size}`;

export const listSources = () => call(client.GET("/api/v1/music/sources"));
export const setSources = (custom: boolean, items: Array<{ spaceId: string; folderId?: string }>) =>
  call(client.PUT("/api/v1/music/sources", { body: { custom, items } }));

export const listFavorites = async () => (await call(client.GET("/api/v1/music/favorites"))).items;
export const setFavorite = (trackId: string, favorite: boolean) =>
  call(
    favorite
      ? client.PUT("/api/v1/music/favorites/{trackId}", track(trackId))
      : client.DELETE("/api/v1/music/favorites/{trackId}", track(trackId)),
  );

export const listRecent = async () => (await call(client.GET("/api/v1/music/recent"))).items;
export const clearRecent = () => call(client.DELETE("/api/v1/music/recent"));
export const recordPlay = (trackId: string) => call(client.POST("/api/v1/music/plays", { body: { trackId } }));

export const listPlaylists = async () => (await call(client.GET("/api/v1/music/playlists"))).items;
export const createPlaylist = (name: string, trackIds: string[] = []) =>
  call(client.POST("/api/v1/music/playlists", { body: { name, trackIds } }));
export const updatePlaylist = (playlistId: string, change: { name?: string; trackIds?: string[] }) =>
  call(client.PATCH("/api/v1/music/playlists/{playlistId}", { ...playlist(playlistId), body: change }));
export const addToPlaylist = (playlistId: string, trackIds: string[]) =>
  call(client.POST("/api/v1/music/playlists/{playlistId}/tracks", { ...playlist(playlistId), body: { trackIds } }));
export const deletePlaylist = (playlistId: string) =>
  call(client.DELETE("/api/v1/music/playlists/{playlistId}", playlist(playlistId)));
