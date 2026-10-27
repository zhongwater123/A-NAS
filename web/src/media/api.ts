import { call, client } from "../api/client";
import type {
  MediaFolderListing,
  MediaLibrary,
  MediaLibraryInput,
  MediaPlayback,
  MediaTitle,
  MediaVideoDetail,
  operations,
} from "../api.generated";

export type {
  MediaLibrary,
  MediaArtwork as Artwork,
  MediaCollection as Collection,
  MediaCollectionDetail as CollectionDetail,
  MediaCollectionRef as CollectionRef,
  MediaEpisode as Episode,
  MediaFolderListing as FolderListing,
  MediaFolderRoot as FolderRoot,
  MediaHome as Home,
  MediaPlayback as Playback,
  MediaProgress as Progress,
  MediaShowDetail as ShowDetail,
  MediaTitle as Title,
  MediaTitlePage as TitlePage,
  MediaTitleResults as TitleResults,
  MediaVideoDetail as VideoDetail,
} from "../api.generated";
export type LibraryInput = MediaLibraryInput;
export type LibraryKind = MediaLibrary["kind"];
export type LibraryFolder = MediaLibrary["folders"][number];
export type TitleType = MediaTitle["type"];
export type TitleQuery = NonNullable<operations["listMediaTitles"]["parameters"]["query"]>;
export type Category = NonNullable<TitleQuery["category"]>;
export type SortOrder = NonNullable<TitleQuery["sort"]>;
export type Quality = MediaPlayback["quality"];
export type AudioTrack = MediaVideoDetail["audio"][number];
export type SubtitleTrack = MediaVideoDetail["subtitles"][number];
export type Folder = MediaFolderListing["folders"][number];
export type ArtworkKind = operations["getMediaArtwork"]["parameters"]["path"]["kind"];

const id = encodeURIComponent;
const library = (libraryId: string) => ({ params: { path: { libraryId } } });
const video = (videoId: string) => ({ params: { path: { videoId } } });
const title = (titleId: string) => ({ params: { path: { titleId } } });
const collection = (collectionId: string) => ({ params: { path: { collectionId } } });

export const listLibraries = () => call(client.GET("/api/v1/media/libraries"));
export const createLibrary = (input: LibraryInput) => call(client.POST("/api/v1/media/libraries", { body: input }));
export const updateLibrary = (libraryId: string, input: LibraryInput) =>
  call(client.PATCH("/api/v1/media/libraries/{libraryId}", { ...library(libraryId), body: input }));
export const deleteLibrary = (libraryId: string) =>
  call(client.DELETE("/api/v1/media/libraries/{libraryId}", library(libraryId)));
// scanLibrary starts a scan and returns the library as it reads while scanning.
export const scanLibrary = (libraryId: string) =>
  call(client.POST("/api/v1/media/libraries/{libraryId}/scan", library(libraryId)));

export const getHome = () => call(client.GET("/api/v1/media/home"));
// listTitles leaves out the empty parts of the query.
export function listTitles(query: TitleQuery) {
  const given = Object.fromEntries(Object.entries(query).filter(([, value]) => value !== "")) as TitleQuery;
  return call(client.GET("/api/v1/media/titles", { params: { query: given } }));
}
export const getShow = (showId: string) =>
  call(client.GET("/api/v1/media/shows/{showId}", { params: { path: { showId } } }));
export const getVideo = (videoId: string) => call(client.GET("/api/v1/media/videos/{videoId}", video(videoId)));
// renameVideoFile renames the 片源, the video's file, with the subtitles, NFO
// and artwork named after it.
export const renameVideoFile = (videoId: string, fileName: string) =>
  call(client.PATCH("/api/v1/media/videos/{videoId}", { ...video(videoId), body: { fileName } }));
// renameShow renames a show's own folder and the episode files carrying its title.
export const renameShow = (showId: string, name: string) =>
  call(client.PATCH("/api/v1/media/shows/{showId}", { params: { path: { showId } }, body: { name } }));
// A playback's mime names a converted stream's codecs, so the player can buffer it itself.
export const getPlayback = (videoId: string, options: { caps: string; audio?: number; quality?: Quality }) =>
  call(
    client.GET("/api/v1/media/videos/{videoId}/playback", {
      params: {
        path: { videoId },
        query: { caps: options.caps, audio: options.audio ?? -1, quality: options.quality ?? "original" },
      },
    }),
  );
// streamURL asks for a converted stream directly, for when the original
// file turned out not to play.
export function streamURL(videoId: string, mode: "remux" | "transcode", audio: number, quality: Quality, caps: string) {
  return `/api/v1/media/videos/${id(videoId)}/stream?${new URLSearchParams({ mode, audio: String(audio), quality, caps })}`;
}
// Progress is saved while the page may be closing, so it keeps the request alive.
export const saveProgress = (videoId: string, position: number, duration: number) =>
  call(
    client.PUT("/api/v1/media/videos/{videoId}/progress", {
      ...video(videoId),
      body: { position: Math.max(0, position), duration: Math.max(0, duration) },
      keepalive: true,
    }),
  );
export const setWatched = (titleId: string, watched: boolean) =>
  call(client.PUT("/api/v1/media/titles/{titleId}/watched", { ...title(titleId), body: { watched } }));
export const setFavorite = (titleId: string, favorite: boolean) =>
  call(
    favorite
      ? client.PUT("/api/v1/media/favorites/{titleId}", title(titleId))
      : client.DELETE("/api/v1/media/favorites/{titleId}", title(titleId)),
  );
export const listFavorites = () => call(client.GET("/api/v1/media/favorites"));
export const listHistory = (offset = 0, limit = 200) =>
  call(client.GET("/api/v1/media/history", { params: { query: { offset, limit } } }));
export const removeHistory = (videoId: string) =>
  call(client.DELETE("/api/v1/media/history/{videoId}", video(videoId)));
export const clearHistory = () => call(client.DELETE("/api/v1/media/history"));
export const listCollections = async () => (await call(client.GET("/api/v1/media/collections"))).items;
export const createCollection = (name: string, items: string[] = []) =>
  call(client.POST("/api/v1/media/collections", { body: { name, items } }));
export const getCollection = (collectionId: string) =>
  call(client.GET("/api/v1/media/collections/{collectionId}", collection(collectionId)));
export const renameCollection = (collectionId: string, name: string) =>
  call(client.PATCH("/api/v1/media/collections/{collectionId}", { ...collection(collectionId), body: { name } }));
export const deleteCollection = (collectionId: string) =>
  call(client.DELETE("/api/v1/media/collections/{collectionId}", collection(collectionId)));
export const addToCollection = (collectionId: string, items: string[]) =>
  call(client.POST("/api/v1/media/collections/{collectionId}/items", { ...collection(collectionId), body: { items } }));
export const removeFromCollection = (collectionId: string, titleId: string) =>
  call(
    client.DELETE("/api/v1/media/collections/{collectionId}/items/{titleId}", {
      params: { path: { collectionId, titleId } },
    }),
  );
export const listFolderRoots = async () => (await call(client.GET("/api/v1/media/folders"))).items;
export const getFolder = (entryId: string, path = "") =>
  call(client.GET("/api/v1/media/folders/{entryId}", { params: { path: { entryId }, query: { path } } }));

export const artworkURL = (titleId: string, kind: ArtworkKind, version = "") =>
  `/api/v1/media/artwork/${id(titleId)}/${kind}${version ? `?v=${id(version)}` : ""}`;
export const subtitleURL = (videoId: string, trackId: string) =>
  `/api/v1/media/videos/${id(videoId)}/subtitles/${id(trackId)}`;

// browserCapabilities lists what this browser decodes beyond what every
// browser plays, so the server can play more files without converting them.
export function browserCapabilities(): string {
  if (typeof document === "undefined") return "";
  const video = document.createElement("video");
  const can = (type: string) => {
    try {
      return video.canPlayType(type) !== "";
    } catch {
      return false;
    }
  };
  const source = (type: string) => {
    try {
      return typeof MediaSource !== "undefined" && MediaSource.isTypeSupported(type);
    } catch {
      return false;
    }
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
