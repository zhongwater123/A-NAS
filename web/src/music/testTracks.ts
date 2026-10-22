import type { Track } from "./musicApi";

// track builds a library track for tests.
export function track(id: string, overrides: Partial<Track> = {}): Track {
  return {
    id,
    spaceId: "space:shared",
    folder: "",
    name: `${id}.flac`,
    sizeBytes: 1000,
    format: "flac",
    title: id,
    albumId: "album:unknown",
    addedAt: "2026-10-10T00:00:00Z",
    ...overrides,
  };
}
