// Photos: the libraries with their timelines, albums and trash, local AI's
// status and groups, people and pets, and each photo's details. Searches,
// groups' photos and changes are left to the tests, which see what they ask for.
import type {
  PhotoAiStatus,
  PhotoAlbum,
  PhotoAsset,
  PhotoLabelCounts,
  PhotoLibrary,
  PhotoPerson,
  PhotoTagCounts,
  PhotoTimelineMonths,
} from "../../api.generated";
import { apiError, http } from "../server";

// alice's own library and the shared one.
export const privateLibrary: PhotoLibrary = {
  id: "library:mine",
  kind: "private",
  ownerUserId: "user:alice",
  createdAt: "2026-10-08T00:00:00Z",
};
export const sharedLibrary: PhotoLibrary = { id: "library:shared", kind: "shared", createdAt: "2026-10-08T00:00:00Z" };

// asset is a JPEG photo in alice's library named after its ID, taken in May
// and imported in October, with its thumbnail ready.
export function asset(id: string, overrides: Partial<PhotoAsset> = {}): PhotoAsset {
  return {
    id,
    libraryId: privateLibrary.id,
    name: `${id}.jpg`,
    mediaType: "image/jpeg",
    sizeBytes: 2_400_000,
    uploadedBy: "user:alice",
    importedAt: "2026-10-08T09:00:00Z",
    takenAt: "2026-05-01T08:00:00Z",
    width: 4000,
    height: 3000,
    thumbnail: "ready",
    ...overrides,
  };
}

// album is an album of alice's library holding one photo.
export function album(id: string, name: string, overrides: Partial<PhotoAlbum> = {}): PhotoAlbum {
  return {
    id,
    libraryId: privateLibrary.id,
    name,
    createdBy: "user:alice",
    createdAt: "2026-10-09T00:00:00Z",
    photos: 1,
    ...overrides,
  };
}

// person is someone local AI found in alice's library, not named yet.
export function person(id: string, overrides: Partial<PhotoPerson> = {}): PhotoPerson {
  return {
    id,
    libraryId: privateLibrary.id,
    kind: "person",
    name: "",
    hidden: false,
    photos: 1,
    coverId: `face:${id}`,
    canChange: true,
    ...overrides,
  };
}

// A photo's month on the timeline: when it was taken, else when imported.
const monthOf = (photo: PhotoAsset) => (photo.takenAt ?? photo.importedAt).slice(0, 7);

// timelineMonths counts photos by month, newest first, as the photo service does.
export function timelineMonths(photos: PhotoAsset[]): PhotoTimelineMonths {
  const months = new Map<string, { photos: number; videos: number }>();
  for (const photo of photos) {
    const month = months.get(monthOf(photo)) ?? { photos: 0, videos: 0 };
    month.photos++;
    if (photo.video) month.videos++;
    months.set(monthOf(photo), month);
  }
  return {
    items: [...months].sort((a, b) => b[0].localeCompare(a[0])).map(([month, counts]) => ({ month, ...counts })),
  };
}

// What the photo service holds. Every function is read on each request, so a
// test changes the answers as the service would.
export interface PhotosOptions {
  libraries?: () => PhotoLibrary[];
  // Each library's photos outside the trash, by library ID; one not named has none.
  timelines?: Record<string, () => PhotoAsset[]>;
  albums?: Record<string, () => PhotoAlbum[]>;
  trash?: Record<string, () => PhotoAsset[]>;
  ai?: () => PhotoAiStatus;
  labels?: () => PhotoLabelCounts;
  tags?: () => PhotoTagCounts["items"];
  // Hidden people are listed only when the window asks for them too.
  people?: () => PhotoPerson[];
}

// photosHandlers answers what the photos window reads. By default the caller
// has an empty library of their own and the shared one, and local AI is idle
// with nothing to group. A photo's details are what its timeline or trash
// holds; any other photo is not found.
export function photosHandlers(options: PhotosOptions = {}) {
  const libraries = options.libraries ?? (() => [sharedLibrary, privateLibrary]);
  const timeline = (libraryId: string) => options.timelines?.[libraryId]?.() ?? [];
  const held = () =>
    [...Object.values(options.timelines ?? {}), ...Object.values(options.trash ?? {})].flatMap((photos) => photos());
  return [
    http.get("/api/v1/photos/libraries", ({ response }) => response(200).json({ items: libraries() })),
    http.get("/api/v1/photos/ai", ({ response }) =>
      response(200).json(options.ai?.() ?? { state: "idle", ready: 3, pending: 0, failed: 0 }),
    ),
    http.get("/api/v1/photos/labels", ({ response }) =>
      response(200).json(options.labels?.() ?? { items: [], ready: false }),
    ),
    http.get("/api/v1/photos/tags", ({ response }) => response(200).json({ items: options.tags?.() ?? [] })),
    http.get("/api/v1/photos/people", ({ query, response }) => {
      const people = options.people?.() ?? [];
      return response(200).json({ items: query.get("hidden") ? people : people.filter((person) => !person.hidden) });
    }),
    http.get("/api/v1/photos/libraries/{libraryId}/timeline/months", ({ params, response }) =>
      response(200).json(timelineMonths(timeline(params.libraryId))),
    ),
    http.get("/api/v1/photos/libraries/{libraryId}/timeline", ({ params, query, response }) => {
      const month = query.get("month");
      const items = timeline(params.libraryId).filter((photo) => !month || monthOf(photo) === month);
      return response(200).json({ items });
    }),
    http.get("/api/v1/photos/libraries/{libraryId}/albums", ({ params, response }) =>
      response(200).json({ items: options.albums?.[params.libraryId]?.() ?? [] }),
    ),
    http.get("/api/v1/photos/libraries/{libraryId}/trash", ({ params, response }) =>
      response(200).json({ items: options.trash?.[params.libraryId]?.() ?? [] }),
    ),
    http.get("/api/v1/photos/assets/{assetId}", ({ params, response }) => {
      const photo = held().find((item) => item.id === params.assetId);
      return photo ? response(200).json(photo) : response(404).json(apiError("not_found", "asset not found"));
    }),
  ];
}

// photoDetails answers the details of photos, read on each request, ahead of
// what photosHandlers holds; the details of other photos pass on to it.
export function photoDetails(photos: () => PhotoAsset[]) {
  return http.get("/api/v1/photos/assets/{assetId}", ({ params, response }) => {
    const photo = photos().find((item) => item.id === params.assetId);
    return photo ? response(200).json(photo) : undefined;
  });
}
