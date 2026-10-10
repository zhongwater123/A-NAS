import { request, uploadForm } from "../api";

export interface PhotoLibrary { id: string; kind: "private" | "shared"; ownerUserId?: string; ownerName?: string; createdAt: string; viewing?: { grantId: string; expiresAt: string } }
export interface PhotoTrash { trashedAt: string; trashedBy: string; purgeAfter: string }
export interface PhotoAsset {
  id: string;
  libraryId: string;
  directoryId?: string;
  name: string;
  mediaType: "image/jpeg" | "image/png";
  sizeBytes: number;
  uploadedBy: string;
  importedAt: string;
  takenAt?: string;
  width: number;
  height: number;
  thumbnail: "ready" | "pending" | "failed";
  duplicate?: "first" | "duplicate";
  // Other members whose private libraries hold the same original.
  alsoKeptBy?: string[];
  trash?: PhotoTrash;
  // User tags and albums; only a single photo's details carry them.
  tags?: string[];
  albums?: { id: string; name: string }[];
}
export interface PhotoAlbum { id: string; libraryId: string; name: string; createdBy: string; createdAt: string; photos: number; coverId?: string }
export interface PhotoPage { items: PhotoAsset[]; next?: string }
// semantic is false when local AI could not encode the query and only names
// and user tags were matched. closest counts the results, over all pages, in
// the closest group; the results after them are less related.
export interface PhotoSearchPage extends PhotoPage { semantic: boolean; closest: number }
// A month of a library's timeline in the device's time zone (YYYY-MM).
export interface PhotoMonth { month: string; photos: number }
export interface PhotoAIStatus {
  state: "unavailable" | "paused" | "working" | "idle";
  // Why work is paused: foreground, or a resource under pressure.
  reason?: string;
  model?: string;
  ready: number;
  pending: number;
  failed: number;
}

const base = "/api/v1/photos";
const id = encodeURIComponent;
const json = (method: string, value: unknown): RequestInit => ({ method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(value) });
const page = (cursor: string, limit: number) => `limit=${limit}${cursor ? `&cursor=${id(cursor)}` : ""}`;

export const listPhotoLibraries = async () => (await request<{ items: PhotoLibrary[] }>(`${base}/libraries`)).items;
export const listTimeline = (libraryId: string, cursor = "", limit = 120) =>
  request<PhotoPage>(`${base}/libraries/${id(libraryId)}/timeline?${page(cursor, limit)}`);
export const listTimelineMonths = async (libraryId: string) =>
  (await request<{ items: PhotoMonth[] }>(`${base}/libraries/${id(libraryId)}/timeline/months`)).items;
export const listTimelineMonth = (libraryId: string, month: string, cursor = "", limit = 500) =>
  request<PhotoPage>(`${base}/libraries/${id(libraryId)}/timeline?month=${id(month)}&${page(cursor, limit)}`);
// viewing adds a member library the caller is viewing read-only.
export const searchPhotos = (query: string, viewing = "", cursor = "", limit = 120) =>
  request<PhotoSearchPage>(`${base}/search?q=${id(query)}&${page(cursor, limit)}${viewing ? `&viewing=${id(viewing)}` : ""}`);
export const getAIStatus = () => request<PhotoAIStatus>(`${base}/ai`);
export function uploadPhoto(libraryId: string, file: File, onProgress: (fraction: number) => void = () => undefined, signal?: AbortSignal) {
  const body = new FormData();
  body.set("file", file);
  return uploadForm<PhotoAsset>(`${base}/libraries/${id(libraryId)}/uploads`, body, onProgress, signal);
}
export const getPhoto = (assetId: string) => request<PhotoAsset>(`${base}/assets/${id(assetId)}`);
export const renamePhoto = (assetId: string, name: string) => request<PhotoAsset>(`${base}/assets/${id(assetId)}`, json("PATCH", { name }), true);
export const copyPhoto = (assetId: string, libraryId: string) => request<PhotoAsset>(`${base}/assets/${id(assetId)}/copies`, json("POST", { libraryId }), true);
export const trashPhoto = (assetId: string) => request<PhotoAsset>(`${base}/assets/${id(assetId)}`, { method: "DELETE" }, true);
export const restorePhoto = (assetId: string) => request<PhotoAsset>(`${base}/assets/${id(assetId)}/restore`, { method: "POST" }, true);
export const purgePhoto = (assetId: string) => request<void>(`${base}/trash/${id(assetId)}`, { method: "DELETE" }, true);
export const listPhotoTrash = async (libraryId: string) => (await request<{ items: PhotoAsset[] }>(`${base}/libraries/${id(libraryId)}/trash`)).items;
export const emptyPhotoTrash = async (libraryId: string) => (await request<{ purged: number }>(`${base}/libraries/${id(libraryId)}/trash`, { method: "DELETE" }, true)).purged;

export const listAlbums = async (libraryId: string) => (await request<{ items: PhotoAlbum[] }>(`${base}/libraries/${id(libraryId)}/albums`)).items;
export const createAlbum = (libraryId: string, name: string) => request<PhotoAlbum>(`${base}/libraries/${id(libraryId)}/albums`, json("POST", { name }), true);
export const renameAlbum = (albumId: string, name: string) => request<PhotoAlbum>(`${base}/albums/${id(albumId)}`, json("PATCH", { name }), true);
export const deleteAlbum = (albumId: string) => request<void>(`${base}/albums/${id(albumId)}`, { method: "DELETE" }, true);
export const listAlbumPhotos = (albumId: string, cursor = "", limit = 200) =>
  request<PhotoPage>(`${base}/albums/${id(albumId)}/assets?${page(cursor, limit)}`);
// Adding a photo of another library copies it into the album's library; the
// result is the photo the album holds.
export const addToAlbum = (albumId: string, assetId: string) => request<PhotoAsset>(`${base}/albums/${id(albumId)}/assets`, json("POST", { assetId }), true);
export const removeFromAlbum = (albumId: string, assetId: string) => request<void>(`${base}/albums/${id(albumId)}/assets/${id(assetId)}`, { method: "DELETE" }, true);
export const addPhotoTag = (assetId: string, name: string) => request<PhotoAsset>(`${base}/assets/${id(assetId)}/tags`, json("POST", { name }), true);
export const removePhotoTag = (assetId: string, name: string) => request<PhotoAsset>(`${base}/assets/${id(assetId)}/tags/${id(name)}`, { method: "DELETE" }, true);
export const thumbnailURL = (assetId: string) => `${base}/assets/${id(assetId)}/thumbnail`;

export const originalURL = (assetId: string, download = false) => `${base}/assets/${id(assetId)}/original${download ? "?download=1" : ""}`;
// The grid shows the original until its thumbnail is ready; JPEG and PNG
// originals display directly.
export const previewURL = (asset: PhotoAsset) => asset.thumbnail === "ready" ? thumbnailURL(asset.id) : originalURL(asset.id);
