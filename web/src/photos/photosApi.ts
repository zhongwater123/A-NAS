import { request, uploadForm } from "../api";

export interface PhotoLibrary {
  id: string;
  kind: "private" | "shared";
  ownerUserId?: string;
  ownerName?: string;
  createdAt: string;
  viewing?: { grantId: string; expiresAt: string };
  // Whether the library recognises people and pets, and whether the caller
  // may turn that on or off.
  people?: boolean;
  canSetPeople?: boolean;
}
export interface PhotoTrash { trashedAt: string; trashedBy: string; purgeAfter: string }
export interface PhotoAsset {
  id: string;
  libraryId: string;
  directoryId?: string;
  name: string;
  mediaType: "image/jpeg" | "image/png" | "image/heic";
  motion?: { kind: "live-photo" | "motion-photo"; duration: number };
  // The still of an Apple Live Photo that came without its MOV; uploading
  // the MOV later completes it.
  liveVideoMissing?: boolean;
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
  // The faces and pets in the photo, with who each is in its library; only a
  // single photo's details carry them.
  people?: PhotoAssetPerson[];
}
// A person or pet of one library, found by local AI and named by members.
// coverId is the occurrence whose avatar stands for them; canChange says
// whether the caller may name, merge, hide and correct them.
export interface PhotoPerson {
  id: string;
  libraryId: string;
  kind: "person" | "cat" | "dog";
  name: string;
  hidden: boolean;
  photos: number;
  coverId: string;
  canChange: boolean;
}
// A face or pet in a photo; personId is absent while it is nobody yet.
export interface PhotoAssetPerson { occurrenceId: string; kind: "face" | "cat" | "dog"; personId?: string; name?: string }
export interface PhotoAlbum { id: string; libraryId: string; name: string; createdBy: string; createdAt: string; photos: number; coverId?: string }
export interface PhotoPage { items: PhotoAsset[]; next?: string }
// semantic is false when local AI could not encode the query and only names
// and user tags were matched. closest counts the results, over all pages, in
// the closest group; the results after them are less related.
// people are the person names in the query; the results keep to their photos.
export interface PhotoSearchPage extends PhotoPage { semantic: boolean; closest: number; objects?: PhotoObjectsFound; people?: string[] }
// For a query about things the object detector finds reliably, such as 手机:
// the closest group is the photos it found them in. undetected counts the
// photos in scope it has not looked at yet.
export interface PhotoObjectsFound { words: string[]; detected: number; undetected: number }
// ready is false while local AI has not organised the photo; there are no
// results then.
export interface PhotoSimilarPage extends PhotoPage { ready: boolean }
// A month of a library's timeline in the device's time zone (YYYY-MM).
export interface PhotoMonth { month: string; photos: number }
// An AI cluster: the photos local AI is sure show a label. Clusters are shown
// on their own and never take part in search.
export interface PhotoLabelCount { id: string; name: string; category: string; photos: number; coverId: string }
// A user tag as a group: the photos people tagged by hand. photos is 0 when
// a group is opened without its count, such as from a photo's details.
export interface PhotoTagCount { name: string; photos: number; coverId: string }
export interface PhotoAIStatus {
  state: "unavailable" | "paused" | "working" | "idle";
  // Why work is paused: foreground, or a resource under pressure.
  reason?: string;
  model?: string;
  objectModel?: string;
  faceModel?: string;
  animalModel?: string;
  ready: number;
  pending: number;
  failed: number;
  // Whether AI 聚合 can list its groups; absent when the model has none.
  clusters?: "ready" | "preparing";
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
// viewing adds a member library the caller is viewing read-only; library
// narrows the search to one library of that scope.
export const searchPhotos = (query: string, viewing = "", cursor = "", library = "", limit = 120) =>
  request<PhotoSearchPage>(`${base}/search?q=${id(query)}&${page(cursor, limit)}${viewing ? `&viewing=${id(viewing)}` : ""}${library ? `&library=${id(library)}` : ""}`);
// Photos that look like this one, in the same scope as a search.
export const listSimilarPhotos = (assetId: string, viewing = "", cursor = "", limit = 120) =>
  request<PhotoSimilarPage>(`${base}/assets/${id(assetId)}/similar?${page(cursor, limit)}${viewing ? `&viewing=${id(viewing)}` : ""}`);
// Photos that look like an image from outside the library. The photo
// service encodes the image at once, never stores it, and lists the results
// under the returned ID for a while; once it answers search_expired, the
// image is sent again.
export function createImageSearch(image: Blob) {
  const body = new FormData();
  body.set("file", image, "image.jpg");
  return request<{ id: string }>(`${base}/image-searches`, { method: "POST", body }, true);
}
export const listImageSearchResults = (searchId: string, viewing = "", cursor = "", limit = 120) =>
  request<PhotoPage>(`${base}/image-searches/${id(searchId)}/assets?${page(cursor, limit)}${viewing ? `&viewing=${id(viewing)}` : ""}`);
export const listPhotoLabels = (viewing = "") =>
  request<{ items: PhotoLabelCount[]; ready: boolean }>(`${base}/labels${viewing ? `?viewing=${id(viewing)}` : ""}`);
export const listLabelPhotos = (labelId: string, viewing = "", cursor = "", limit = 120) =>
  request<PhotoPage>(`${base}/labels/${id(labelId)}/assets?${page(cursor, limit)}${viewing ? `&viewing=${id(viewing)}` : ""}`);
export const listTagGroups = async (viewing = "") =>
  (await request<{ items: PhotoTagCount[] }>(`${base}/tags${viewing ? `?viewing=${id(viewing)}` : ""}`)).items;
export const listTagPhotos = (name: string, viewing = "", cursor = "", limit = 120) =>
  request<PhotoPage>(`${base}/tags/${id(name)}/assets?${page(cursor, limit)}${viewing ? `&viewing=${id(viewing)}` : ""}`);
// Takes a photo out of an AI cluster, for that photo only.
export const hideAILabel = (assetId: string, labelId: string) => request<PhotoAsset>(`${base}/assets/${id(assetId)}/ai-labels/${id(labelId)}`, { method: "DELETE" }, true);
export const getAIStatus = () => request<PhotoAIStatus>(`${base}/ai`);
export const listPeople = async (viewing = "", hidden = false) =>
  (await request<{ items: PhotoPerson[] }>(`${base}/people${hidden || viewing ? "?" : ""}${[viewing && `viewing=${id(viewing)}`, hidden && "hidden=1"].filter(Boolean).join("&")}`)).items;
export const listPersonPhotos = (personId: string, cursor = "", limit = 120) =>
  request<PhotoPage>(`${base}/people/${id(personId)}/assets?${page(cursor, limit)}`);
// setLibraryPeople turns a library's people and pets on or off; off clears
// what local AI recognised there and keeps names and corrections.
export const setLibraryPeople = (libraryId: string, people: boolean) =>
  request<PhotoLibrary>(`${base}/libraries/${id(libraryId)}`, json("PATCH", { people }), true);
export const updatePerson = (personId: string, change: { name?: string; hidden?: boolean; coverId?: string }) =>
  request<PhotoPerson>(`${base}/people/${id(personId)}`, json("PATCH", change), true);
// Every face of personId moves into the other person, which keeps its name
// (or takes this one's); personId is gone afterwards.
export const mergePeople = (personId: string, into: string) => request<PhotoPerson>(`${base}/people/${id(personId)}/merge`, json("POST", { into }), true);
// Takes a photo out of a person for good; local AI never puts it back.
export const removeFromPerson = (personId: string, assetId: string) => request<void>(`${base}/people/${id(personId)}/assets/${id(assetId)}`, { method: "DELETE" }, true);
// Says who a face or pet in a photo is: an existing person, or a new one by name.
export const assignFace = (assetId: string, occurrenceId: string, who: { personId: string } | { name: string }) =>
  request<PhotoAsset>(`${base}/assets/${id(assetId)}/people/${id(occurrenceId)}`, json("PUT", who), true);
export const avatarURL = (occurrenceId: string) => `${base}/occurrences/${id(occurrenceId)}/avatar`;
export function uploadPhoto(libraryId: string, file: File, onProgress: (fraction: number) => void = () => undefined, signal?: AbortSignal, motion?: File) {
  const body = new FormData();
  body.set("file", file);
  if (motion) body.set("motion", motion);
  return uploadForm<PhotoAsset>(`${base}/libraries/${id(libraryId)}/${motion ? "live-photos" : "uploads"}`, body, onProgress, signal);
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
export const displayURL = (asset: PhotoAsset) => asset.mediaType === "image/heic" ? `${base}/assets/${id(asset.id)}/preview` : originalURL(asset.id);
// A Live Photo's MOV uploaded on its own completes the photos of the library
// whose still came without it, matched by Apple's identifier; the result
// lists them.
export async function attachLiveVideo(libraryId: string, file: File, onProgress: (fraction: number) => void = () => undefined) {
  const body = new FormData();
  body.set("file", file);
  return (await uploadForm<{ items: PhotoAsset[] }>(`${base}/libraries/${id(libraryId)}/live-videos`, body, onProgress)).items;
}
export const motionURL = (assetId: string) => `${base}/assets/${id(assetId)}/motion/play`;
export const downloadURL = (assetId: string) => `${base}/assets/${id(assetId)}/download`;
export const previewURL = (asset: PhotoAsset) => asset.thumbnail === "ready" ? thumbnailURL(asset.id) : displayURL(asset);
