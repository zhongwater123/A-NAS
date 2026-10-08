import { request } from "./api";

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
}
export interface PhotoPage { items: PhotoAsset[]; next?: string }

const base = "/api/v1/photos";
const id = encodeURIComponent;
const json = (method: string, value: unknown): RequestInit => ({ method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(value) });

export const listPhotoLibraries = async () => (await request<{ items: PhotoLibrary[] }>(`${base}/libraries`)).items;
export const listTimeline = (libraryId: string, cursor = "", limit = 120) =>
  request<PhotoPage>(`${base}/libraries/${id(libraryId)}/timeline?limit=${limit}${cursor ? `&cursor=${id(cursor)}` : ""}`);
export function uploadPhoto(libraryId: string, file: File) {
  const body = new FormData();
  body.set("file", file);
  return request<PhotoAsset>(`${base}/libraries/${id(libraryId)}/uploads`, { method: "POST", body }, true);
}
export const renamePhoto = (assetId: string, name: string) => request<PhotoAsset>(`${base}/assets/${id(assetId)}`, json("PATCH", { name }), true);
export const copyPhoto = (assetId: string, libraryId: string) => request<PhotoAsset>(`${base}/assets/${id(assetId)}/copies`, json("POST", { libraryId }), true);
export const trashPhoto = (assetId: string) => request<PhotoAsset>(`${base}/assets/${id(assetId)}`, { method: "DELETE" }, true);
export const restorePhoto = (assetId: string) => request<PhotoAsset>(`${base}/assets/${id(assetId)}/restore`, { method: "POST" }, true);
export const purgePhoto = (assetId: string) => request<void>(`${base}/trash/${id(assetId)}`, { method: "DELETE" }, true);
export const listPhotoTrash = async (libraryId: string) => (await request<{ items: PhotoAsset[] }>(`${base}/libraries/${id(libraryId)}/trash`)).items;
export const emptyPhotoTrash = async (libraryId: string) => (await request<{ purged: number }>(`${base}/libraries/${id(libraryId)}/trash`, { method: "DELETE" }, true)).purged;

export const originalURL = (assetId: string, download = false) => `${base}/assets/${id(assetId)}/original${download ? "?download=1" : ""}`;
// The grid shows the original until its thumbnail is ready; JPEG and PNG
// originals display directly.
export const previewURL = (asset: PhotoAsset) => asset.thumbnail === "ready" ? `${base}/assets/${id(asset.id)}/thumbnail` : originalURL(asset.id);
