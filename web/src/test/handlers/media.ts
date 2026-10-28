// The media center (影视中心): libraries, the home rows, title lists, a film's
// or a show's page, how a video plays and the viewer's own lists. The spaces
// and folders its library dialog picks from are the files handlers'.
import type {
  MediaCollection,
  MediaHome,
  MediaLibrary,
  MediaPlayback,
  MediaShowDetail,
  MediaTitle,
  MediaTitleResults,
  MediaVideoDetail,
} from "../../api.generated";
import { apiError, http } from "../server";

// mediaLibrary is the admin's mixed library of the shared space's 影视 folder,
// scanned and idle.
export function mediaLibrary(overrides: Partial<MediaLibrary> = {}): MediaLibrary {
  return {
    id: "library:films",
    name: "家庭影院",
    kind: "mixed",
    spaceId: "space:shared",
    spaceKind: "shared",
    createdBy: "user:admin",
    createdAt: "2026-10-01T00:00:00Z",
    folders: [{ entryId: "file:films", name: "影视", path: "影视" }],
    counts: { movies: 0, shows: 0, episodes: 0, others: 0, sizeBytes: 0 },
    scan: { state: "idle", pending: 0, scannedAt: "2026-10-10T07:00:00Z" },
    canManage: true,
    covers: [],
    ...overrides,
  };
}

// mediaHome is a start page with empty rows.
export function mediaHome(overrides: Partial<MediaHome> = {}): MediaHome {
  return {
    featured: [],
    continue: [],
    recent: [],
    movies: [],
    shows: [],
    others: [],
    favorites: [],
    libraries: 0,
    scanning: false,
    ...overrides,
  };
}

// playback plays a video's file as it is.
export function playback(url: string, overrides: Partial<MediaPlayback> = {}): MediaPlayback {
  return { mode: "direct", url, audio: 0, quality: "original", qualities: ["original"], ...overrides };
}

export interface MediaOptions {
  // libraries is read on every request, so a test changes what the next poll sees.
  libraries: MediaLibrary[] | (() => MediaLibrary[]);
  home?: MediaHome;
  // titles answers every title list; a function also sees the list's query.
  titles?: MediaTitleResults | ((query: URLSearchParams) => MediaTitleResults);
  // videos, shows and playback are served only when given; an ID they do not
  // know is not found.
  videos?: MediaVideoDetail[] | ((videoId: string) => MediaVideoDetail | undefined);
  shows?: MediaShowDetail[];
  playback?: (videoId: string, query: URLSearchParams) => MediaPlayback | undefined;
  // history and collections are the viewer's own lists, served when given.
  history?: MediaTitle[];
  collections?: MediaCollection[];
}

const noTitles: MediaTitleResults = { items: [], total: 0, genres: [] };

// mediaHandlers answers the media center's reads: the libraries (FFmpeg
// available), the home rows and the title lists, and the videos, shows and
// playback decisions a test gives.
export function mediaHandlers(options: MediaOptions) {
  const given = options.libraries;
  const libraries = typeof given === "function" ? given : () => given;
  const handlers = [
    http.get("/api/v1/media/libraries", ({ response }) => response(200).json({ items: libraries(), processing: true })),
    http.get("/api/v1/media/home", ({ response }) => response(200).json(options.home ?? mediaHome())),
    http.get("/api/v1/media/titles", ({ request, response }) => {
      const titles = options.titles ?? noTitles;
      return response(200).json(typeof titles === "function" ? titles(new URL(request.url).searchParams) : titles);
    }),
  ];
  const { videos, shows, playback: decide, history, collections } = options;
  if (videos)
    handlers.push(
      http.get("/api/v1/media/videos/{videoId}", ({ params, response }) => {
        const video =
          typeof videos === "function" ? videos(params.videoId) : videos.find((item) => item.id === params.videoId);
        return video ? response(200).json(video) : response(404).json(apiError("not_found"));
      }),
    );
  if (shows)
    handlers.push(
      http.get("/api/v1/media/shows/{showId}", ({ params, response }) => {
        const show = shows.find((item) => item.id === params.showId);
        return show ? response(200).json(show) : response(404).json(apiError("not_found"));
      }),
    );
  if (decide)
    handlers.push(
      http.get("/api/v1/media/videos/{videoId}/playback", ({ params, request, response }) => {
        const decision = decide(params.videoId, new URL(request.url).searchParams);
        return decision ? response(200).json(decision) : response(404).json(apiError("not_found"));
      }),
    );
  if (history)
    handlers.push(
      http.get("/api/v1/media/history", ({ response }) =>
        response(200).json({ items: history, total: history.length }),
      ),
    );
  if (collections)
    handlers.push(http.get("/api/v1/media/collections", ({ response }) => response(200).json({ items: collections })));
  return handlers;
}
