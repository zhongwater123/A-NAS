// Music (音乐): the library and its scans, the folders it covers, and the
// listener's favorites, plays and playlists.
import type {
  MusicFavorite,
  MusicPlay,
  MusicPlaylist,
  MusicSources,
  MusicStatus,
  MusicTrack,
} from "../../api.generated";
import { http } from "../server";

// musicStatus is a library of tracks at version 1 that no scan is changing.
export function musicStatus(tracks: number, overrides: Partial<MusicStatus> = {}): MusicStatus {
  return { version: 1, tracks, scan: { state: "idle", done: 0, total: 0 }, ...overrides };
}

export interface MusicOptions {
  tracks: MusicTrack[];
  favorites?: MusicFavorite[];
  recent?: MusicPlay[];
  playlists?: MusicPlaylist[];
  // sources default to the whole shared space.
  sources?: MusicSources;
}

// musicHandlers answers what the music window reads when it opens, and the
// scan it then starts: the library and its status at the same version, so
// nothing reloads.
export function musicHandlers(options: MusicOptions) {
  const status = musicStatus(options.tracks.length);
  return [
    http.get("/api/v1/music/tracks", ({ response }) => response(200).json({ version: 1, items: options.tracks })),
    http.get("/api/v1/music/library", ({ response }) => response(200).json(status)),
    http.post("/api/v1/music/library/scan", ({ response }) => response(202).json(status)),
    http.get("/api/v1/music/sources", ({ response }) =>
      response(200).json(
        options.sources ?? { custom: false, items: [{ spaceId: "space:shared", spaceKind: "shared", path: "" }] },
      ),
    ),
    http.get("/api/v1/music/favorites", ({ response }) => response(200).json({ items: options.favorites ?? [] })),
    http.get("/api/v1/music/recent", ({ response }) => response(200).json({ items: options.recent ?? [] })),
    http.get("/api/v1/music/playlists", ({ response }) => response(200).json({ items: options.playlists ?? [] })),
  ];
}

// musicFavorites records the tracks marked and unmarked as favorites, and
// accepts them.
export function musicFavorites() {
  const sent: Array<{ trackId: string; favorite: boolean }> = [];
  return {
    sent,
    handlers: [
      http.put("/api/v1/music/favorites/{trackId}", ({ params, response }) => {
        sent.push({ trackId: params.trackId, favorite: true });
        return response(204).empty();
      }),
      http.delete("/api/v1/music/favorites/{trackId}", ({ params, response }) => {
        sent.push({ trackId: params.trackId, favorite: false });
        return response(204).empty();
      }),
    ],
  };
}

// musicPlaylists records the playlists created and answers each as the server
// would create it.
export function musicPlaylists() {
  const created: Array<{ name: string; trackIds?: string[] }> = [];
  const handler = http.post("/api/v1/music/playlists", async ({ request, response }) => {
    const body = await request.json();
    created.push(body);
    return response(201).json({
      id: `playlist:${created.length}`,
      name: body.name,
      trackIds: body.trackIds ?? [],
      createdAt: "2026-10-10T00:00:00Z",
      updatedAt: "2026-10-10T00:00:00Z",
    });
  });
  return { created, handler };
}
