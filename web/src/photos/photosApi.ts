import { call, client, rawBody, uploadForm } from "../api";
import type {
  operations,
  PhotoAsset,
  PhotoLabelCounts,
  PhotoSearchResults,
  PhotoTagCounts,
  PhotoTimelineMonths,
} from "../api.generated";

export type {
  PhotoAiStatus as PhotoAIStatus,
  PhotoAlbum,
  PhotoAsset,
  PhotoLibrary,
  PhotoPerson,
  PhotoTimeline as PhotoPage,
  PhotoSearchResults as PhotoSearchPage,
  PhotoSimilarResults as PhotoSimilarPage,
} from "../api.generated";
export type PhotoTrash = NonNullable<PhotoAsset["trash"]>;
// A face or pet in a photo; personId is absent while it is nobody yet.
export type PhotoAssetPerson = NonNullable<PhotoAsset["people"]>[number];
// For a query about things the object detector finds reliably, such as 手机:
// the closest group is the photos it found them in. undetected counts the
// photos in scope it has not looked at yet.
export type PhotoObjectsFound = NonNullable<PhotoSearchResults["objects"]>;
// A month of a library's timeline in the device's time zone (YYYY-MM).
// photos counts the month's photos and videos; videos the videos among them.
export type PhotoMonth = PhotoTimelineMonths["items"][number];
// An AI cluster: the photos local AI is sure show a label. Clusters are shown
// on their own and never take part in search.
export type PhotoLabelCount = PhotoLabelCounts["items"][number];
// A user tag as a group: the photos people tagged by hand. photos is 0 when
// a group is opened without its count, such as from a photo's details.
export type PhotoTagCount = PhotoTagCounts["items"][number];
// An original uploaded on its own becomes a photo or a video; a Live Photo's
// MOV whose stills in the library wait for it completes them instead.
export type PhotoUpload = operations["uploadPhoto"]["responses"][200 | 201]["content"]["application/json"];
// How this browser plays a video: the original itself, or a stream the NAS
// converts it into, whose type is mime.
export type VideoPlayback = operations["getPhotoVideoPlayback"]["responses"][200]["content"]["application/json"];

const id = encodeURIComponent;
// Empty parameters are left out of the query.
const page = (cursor: string, limit: number) => ({ cursor: cursor || undefined, limit });
const scope = (viewing: string) => viewing || undefined;
const asset = (assetId: string) => ({ params: { path: { assetId } } });
const album = (albumId: string) => ({ params: { path: { albumId } } });
const library = (libraryId: string) => ({ params: { path: { libraryId } } });
const person = (personId: string) => ({ params: { path: { personId } } });

export const listPhotoLibraries = async () => (await call(client.GET("/api/v1/photos/libraries"))).items;
export const listTimeline = (libraryId: string, cursor = "", limit = 120) =>
  call(
    client.GET("/api/v1/photos/libraries/{libraryId}/timeline", {
      params: { path: { libraryId }, query: page(cursor, limit) },
    }),
  );
export const listTimelineMonths = async (libraryId: string) =>
  (await call(client.GET("/api/v1/photos/libraries/{libraryId}/timeline/months", library(libraryId)))).items;
export const listTimelineMonth = (libraryId: string, month: string, cursor = "", limit = 500) =>
  call(
    client.GET("/api/v1/photos/libraries/{libraryId}/timeline", {
      params: { path: { libraryId }, query: { month, ...page(cursor, limit) } },
    }),
  );
// viewing adds a member library the caller is viewing read-only; library
// narrows the search to one library of that scope. semantic is false when
// local AI could not encode the query and only names and user tags were
// matched; closest counts the results, over all pages, in the closest group.
// people are the person names in the query; the results keep to their photos.
export const searchPhotos = (query: string, viewing = "", cursor = "", library = "", limit = 120) =>
  call(
    client.GET("/api/v1/photos/search", {
      params: { query: { q: query, ...page(cursor, limit), viewing: scope(viewing), library: library || undefined } },
    }),
  );
// Photos that look like this one, in the same scope as a search; ready is
// false while local AI has not organised the photo, and there are no results then.
export const listSimilarPhotos = (assetId: string, viewing = "", cursor = "", limit = 120) =>
  call(
    client.GET("/api/v1/photos/assets/{assetId}/similar", {
      params: { path: { assetId }, query: { ...page(cursor, limit), viewing: scope(viewing) } },
    }),
  );
// Photos that look like an image from outside the library. The photo
// service encodes the image at once, never stores it, and lists the results
// under the returned ID for a while; once it answers search_expired, the
// image is sent again.
export function createImageSearch(image: Blob) {
  const body = new FormData();
  body.set("file", image, "image.jpg");
  return call(client.POST("/api/v1/photos/image-searches", rawBody(body)));
}
export const listImageSearchResults = (searchId: string, viewing = "", cursor = "", limit = 120) =>
  call(
    client.GET("/api/v1/photos/image-searches/{searchId}/assets", {
      params: { path: { searchId }, query: { ...page(cursor, limit), viewing: scope(viewing) } },
    }),
  );
export const listPhotoLabels = (viewing = "") =>
  call(client.GET("/api/v1/photos/labels", { params: { query: { viewing: scope(viewing) } } }));
export const listLabelPhotos = (labelId: string, viewing = "", cursor = "", limit = 120) =>
  call(
    client.GET("/api/v1/photos/labels/{labelId}/assets", {
      params: { path: { labelId }, query: { ...page(cursor, limit), viewing: scope(viewing) } },
    }),
  );
export const listTagGroups = async (viewing = "") =>
  (await call(client.GET("/api/v1/photos/tags", { params: { query: { viewing: scope(viewing) } } }))).items;
export const listTagPhotos = (name: string, viewing = "", cursor = "", limit = 120) =>
  call(
    client.GET("/api/v1/photos/tags/{name}/assets", {
      params: { path: { name }, query: { ...page(cursor, limit), viewing: scope(viewing) } },
    }),
  );
// Takes a photo out of an AI cluster, for that photo only.
export const hideAILabel = (assetId: string, labelId: string) =>
  call(
    client.DELETE("/api/v1/photos/assets/{assetId}/ai-labels/{labelId}", { params: { path: { assetId, labelId } } }),
  );
export const getAIStatus = () => call(client.GET("/api/v1/photos/ai"));
export const listPeople = async (viewing = "", hidden = false) =>
  (
    await call(
      client.GET("/api/v1/photos/people", {
        params: { query: { viewing: scope(viewing), hidden: hidden ? "1" : undefined } },
      }),
    )
  ).items;
export const listPersonPhotos = (personId: string, cursor = "", limit = 120) =>
  call(
    client.GET("/api/v1/photos/people/{personId}/assets", {
      params: { path: { personId }, query: page(cursor, limit) },
    }),
  );
// setLibraryPeople turns a library's people and pets on or off; off clears
// what local AI recognised there and keeps names and corrections.
export const setLibraryPeople = (libraryId: string, people: boolean) =>
  call(client.PATCH("/api/v1/photos/libraries/{libraryId}", { ...library(libraryId), body: { people } }));
export const updatePerson = (personId: string, change: { name?: string; hidden?: boolean; coverId?: string }) =>
  call(client.PATCH("/api/v1/photos/people/{personId}", { ...person(personId), body: change }));
// Every face of personId moves into the other person, which keeps its name
// (or takes this one's); personId is gone afterwards.
export const mergePeople = (personId: string, into: string) =>
  call(client.POST("/api/v1/photos/people/{personId}/merge", { ...person(personId), body: { into } }));
// Takes a photo out of a person for good; local AI never puts it back.
export const removeFromPerson = (personId: string, assetId: string) =>
  call(client.DELETE("/api/v1/photos/people/{personId}/assets/{assetId}", { params: { path: { personId, assetId } } }));
// Says who a face or pet in a photo is: an existing person, or a new one by name.
export const assignFace = (assetId: string, occurrenceId: string, who: { personId: string } | { name: string }) =>
  call(
    client.PUT("/api/v1/photos/assets/{assetId}/people/{occurrenceId}", {
      params: { path: { assetId, occurrenceId } },
      body: who,
    }),
  );
export const avatarURL = (occurrenceId: string) => `/api/v1/photos/occurrences/${id(occurrenceId)}/avatar`;
export function uploadPhoto(
  libraryId: string,
  file: File,
  onProgress: (fraction: number) => void = () => undefined,
  signal?: AbortSignal,
  motion?: File,
) {
  const body = new FormData();
  body.set("file", file);
  if (motion) body.set("motion", motion);
  return uploadForm<PhotoUpload>(
    `/api/v1/photos/libraries/${id(libraryId)}/${motion ? "live-photos" : "uploads"}`,
    body,
    onProgress,
    signal,
  );
}
export const getPhoto = (assetId: string) => call(client.GET("/api/v1/photos/assets/{assetId}", asset(assetId)));
export const renamePhoto = (assetId: string, name: string) =>
  call(client.PATCH("/api/v1/photos/assets/{assetId}", { ...asset(assetId), body: { name } }));
export const copyPhoto = (assetId: string, libraryId: string) =>
  call(client.POST("/api/v1/photos/assets/{assetId}/copies", { ...asset(assetId), body: { libraryId } }));
export const trashPhoto = (assetId: string) => call(client.DELETE("/api/v1/photos/assets/{assetId}", asset(assetId)));
export const restorePhoto = (assetId: string) =>
  call(client.POST("/api/v1/photos/assets/{assetId}/restore", asset(assetId)));
export const purgePhoto = (assetId: string) => call(client.DELETE("/api/v1/photos/trash/{assetId}", asset(assetId)));
export const listPhotoTrash = async (libraryId: string) =>
  (await call(client.GET("/api/v1/photos/libraries/{libraryId}/trash", library(libraryId)))).items;
export const emptyPhotoTrash = async (libraryId: string) =>
  (await call(client.DELETE("/api/v1/photos/libraries/{libraryId}/trash", library(libraryId)))).purged;

export const listAlbums = async (libraryId: string) =>
  (await call(client.GET("/api/v1/photos/libraries/{libraryId}/albums", library(libraryId)))).items;
export const createAlbum = (libraryId: string, name: string) =>
  call(client.POST("/api/v1/photos/libraries/{libraryId}/albums", { ...library(libraryId), body: { name } }));
export const renameAlbum = (albumId: string, name: string) =>
  call(client.PATCH("/api/v1/photos/albums/{albumId}", { ...album(albumId), body: { name } }));
export const deleteAlbum = (albumId: string) => call(client.DELETE("/api/v1/photos/albums/{albumId}", album(albumId)));
export const listAlbumPhotos = (albumId: string, cursor = "", limit = 200) =>
  call(
    client.GET("/api/v1/photos/albums/{albumId}/assets", { params: { path: { albumId }, query: page(cursor, limit) } }),
  );
// Adding a photo of another library copies it into the album's library; the
// result is the photo the album holds.
export const addToAlbum = (albumId: string, assetId: string) =>
  call(client.POST("/api/v1/photos/albums/{albumId}/assets", { ...album(albumId), body: { assetId } }));
export const removeFromAlbum = (albumId: string, assetId: string) =>
  call(client.DELETE("/api/v1/photos/albums/{albumId}/assets/{assetId}", { params: { path: { albumId, assetId } } }));
export const addPhotoTag = (assetId: string, name: string) =>
  call(client.POST("/api/v1/photos/assets/{assetId}/tags", { ...asset(assetId), body: { name } }));
export const removePhotoTag = (assetId: string, name: string) =>
  call(client.DELETE("/api/v1/photos/assets/{assetId}/tags/{name}", { params: { path: { assetId, name } } }));
export const thumbnailURL = (assetId: string) => `/api/v1/photos/assets/${id(assetId)}/thumbnail`;

export const originalURL = (assetId: string, download = false) =>
  `/api/v1/photos/assets/${id(assetId)}/original${download ? "?download=1" : ""}`;
// The grid shows the original until its thumbnail is ready; JPEG and PNG
// originals display directly, HEIC as a compatible JPEG and a video as a
// frame of it.
export const displayURL = (asset: PhotoAsset) =>
  asset.mediaType === "image/heic" || asset.video
    ? `/api/v1/photos/assets/${id(asset.id)}/preview`
    : originalURL(asset.id);
// A Live Photo's MOV uploaded on its own completes the photos of the library
// whose still came without it, matched by Apple's identifier; the result
// lists them.
export async function attachLiveVideo(
  libraryId: string,
  file: File,
  onProgress: (fraction: number) => void = () => undefined,
) {
  const body = new FormData();
  body.set("file", file);
  type Attached = operations["attachLivePhotoVideo"]["responses"][200]["content"]["application/json"];
  return (await uploadForm<Attached>(`/api/v1/photos/libraries/${id(libraryId)}/live-videos`, body, onProgress)).items;
}
export const motionURL = (assetId: string) => `/api/v1/photos/assets/${id(assetId)}/motion/play`;
export const getVideoPlayback = (assetId: string, caps: string) =>
  call(
    client.GET("/api/v1/photos/assets/{assetId}/video/playback", { params: { path: { assetId }, query: { caps } } }),
  );
export const videoOriginalURL = (assetId: string) => `/api/v1/photos/assets/${id(assetId)}/video/original`;
// A converted stream cannot seek; a player asks for it again from start.
export const videoStreamURL = (assetId: string, start: number, caps: string, transcode: boolean) =>
  `/api/v1/photos/assets/${id(assetId)}/video/stream?${new URLSearchParams({ start: start.toFixed(3), caps, ...(transcode ? { transcode: "1" } : {}) })}`;
export const downloadURL = (assetId: string) => `/api/v1/photos/assets/${id(assetId)}/download`;
export const previewURL = (asset: PhotoAsset) =>
  asset.thumbnail === "ready" ? thumbnailURL(asset.id) : displayURL(asset);
