import { describe, expect, it } from "vitest";

import { album, privateLibrary } from "../test/handlers/photos";
import { placeTabs } from "./Sidebar";

const current = (tabs: ReturnType<typeof placeTabs>) => tabs.filter((tab) => tab.on).map((tab) => tab.key);

describe("a library's places", () => {
  it("are the same for the sidebar and a phone's row, but for an album's own page", () => {
    const trip = { kind: "album" as const, libraryId: privateLibrary.id, album: album("album:trip", "旅行") };
    // The sidebar marks the album in its list; a phone's row, with no list, marks 相册.
    expect(current(placeTabs(privateLibrary, trip, { albumIsAlbums: false }))).toEqual([]);
    expect(current(placeTabs(privateLibrary, trip, { albumIsAlbums: true }))).toEqual(["albums"]);
    const search = { kind: "search" as const, viewing: "", query: "海边" };
    expect(current(placeTabs(privateLibrary, search, { albumIsAlbums: true }))).toEqual(["ai"]);
  });

  it("leave out 回收站 for a library viewed under a grant", () => {
    const viewed = {
      ...privateLibrary,
      id: "library:bob",
      viewing: { grantId: "grant:1", expiresAt: "2026-12-01T00:00:00Z" },
    };
    const tabs = placeTabs(viewed, { kind: "timeline", libraryId: viewed.id }, { albumIsAlbums: true });
    expect(tabs.map((tab) => tab.key)).toEqual(["timeline", "albums", "ai"]);
    expect(tabs.find((tab) => tab.key === "ai")?.place).toEqual({ kind: "search", viewing: "library:bob", query: "" });
  });
});
