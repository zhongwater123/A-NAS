import { describe, expect, it } from "vitest";

import { activeLine, buildIndex, folderKey, folderTracks, formatDuration, formatTotal, parseLyrics, search, sortTracks } from "./library";
import { moveRows } from "./MusicPanel";
import { track } from "./testTracks";

const library = [
  track("a1", { title: "晴天", artist: "周杰伦", artists: ["周杰伦"], album: "叶惠美", albumId: "album:yhm", trackNo: 2, folder: "音乐/周杰伦/叶惠美", genres: ["流行"], coverId: "c1", durationMs: 269_000 }),
  track("a2", { title: "以父之名", artist: "周杰伦", artists: ["周杰伦"], album: "叶惠美", albumId: "album:yhm", trackNo: 1, folder: "音乐/周杰伦/叶惠美", genres: ["流行"], durationMs: 342_000 }),
  track("b1", { title: "Lantern Light", artist: "Mira Chen feat. Leo", artists: ["Mira Chen", "Leo"], album: "Midnight", albumId: "album:mid", folder: "音乐/Mira/Midnight/CD1", genres: ["R&B"] }),
  track("b2", { title: "Slow Tide", artist: "Mira Chen", artists: ["Mira Chen"], album: "Midnight", albumId: "album:mid", folder: "音乐/Mira/Midnight/CD2", discNo: 2 }),
  track("c1", { title: "十年", artist: "陈奕迅", artists: ["陈奕迅"], folder: "" }),
];

describe("buildIndex", () => {
  const index = buildIndex(library);

  it("groups albums in disc and track order with their art", () => {
    const album = index.albumsById.get("album:yhm")!;
    expect(album.tracks.map((item) => item.title)).toEqual(["以父之名", "晴天"]);
    expect(album.artist).toBe("周杰伦");
    expect(album.coverId).toBe("c1");
    expect(album.durationMs).toBe(611_000);
    // Tracks without an album come last as one unknown album.
    expect(index.albums.at(-1)?.id).toBe("album:unknown");
  });

  it("credits every artist of a track and gives artists their albums", () => {
    expect(index.artistsByKey.get("leo")?.tracks.map((item) => item.id)).toEqual(["b1"]);
    expect(index.artistsByKey.get("mira chen")?.albums.map((album) => album.id)).toEqual(["album:mid"]);
    expect(index.artistsByKey.get("周杰伦")?.coverId).toBe("c1");
  });

  it("counts tracks in folders and every folder below", () => {
    const music = index.folders.get(folderKey("space:shared", "音乐"))!;
    expect(music.total).toBe(4);
    // Chinese collation puts Hanzi, by pinyin, before Latin names.
    expect(music.folders.map((folder) => folder.name)).toEqual(["周杰伦", "Mira"]);
    expect(index.folders.get(folderKey("space:shared", ""))!.total).toBe(5);
    expect(folderTracks(index.folders.get(folderKey("space:shared", "音乐/Mira/Midnight"))!).map((item) => item.id)).toEqual(["b1", "b2"]);
  });

  it("collects genres", () => {
    expect(index.genres.map((genre) => [genre.name, genre.tracks.length])).toEqual([["流行", 2], ["R&B", 1]]);
  });
});

describe("search", () => {
  const index = buildIndex(library);
  const pinyin = (text: string, query: string) => ({ 周杰伦: ["zjl", "zhoujielun"], 晴天: ["qt", "qingtian"] } as Record<string, string[]>)[text]?.some((value) => value.includes(query)) ?? false;

  it("matches titles, artists and albums and ranks title matches first", () => {
    const results = search(index, "mira");
    expect(results.artists.map((artist) => artist.name)).toEqual(["Mira Chen"]);
    expect(results.tracks.map((item) => item.id)).toEqual(["b1", "b2"]);
    expect(search(index, "叶惠美").albums.map((album) => album.id)).toEqual(["album:yhm"]);
  });

  it("matches Hanzi by pinyin and initials once pinyin is loaded", () => {
    expect(search(index, "zjl").tracks).toEqual([]);
    expect(search(index, "zjl", pinyin).artists.map((artist) => artist.name)).toEqual(["周杰伦"]);
    expect(search(index, "qingtian", pinyin).tracks.map((item) => item.id)).toEqual(["a1"]);
  });

  it("requires every word to match", () => {
    expect(search(index, "slow tide").tracks.map((item) => item.id)).toEqual(["b2"]);
    expect(search(index, "slow 晴天").tracks).toEqual([]);
  });
});

describe("formatting and lyrics", () => {
  it("formats durations and totals", () => {
    expect(formatDuration(undefined)).toBe("--:--");
    expect(formatDuration(0)).toBe("0:00");
    expect(formatDuration(269_400)).toBe("4:29");
    expect(formatDuration(3_725_000)).toBe("1:02:05");
    expect(formatTotal(27_000)).toBe("27 秒");
    expect(formatTotal(611_000)).toBe("10 分钟");
    expect(formatTotal(5_400_000)).toBe("1 小时 30 分钟");
  });

  it("sorts by artist, then album order", () => {
    expect(sortTracks(library, "artist").map((item) => item.id)).toEqual(["c1", "a2", "a1", "b2", "b1"]);
    expect(sortTracks(library, "duration", true)[0].id).toBe("a2");
  });

  it("parses synced LRC and finds the line playing", () => {
    const lyrics = parseLyrics("[ti:Song]\n[00:01.00]第一句\n[00:04.50]第二句\n[00:09.00]");
    expect(lyrics.synced).toBe(true);
    expect(lyrics.lines.map((line) => line.text)).toEqual(["第一句", "第二句", ""]);
    expect(activeLine(lyrics, 0.2)).toBe(-1);
    expect(activeLine(lyrics, 5)).toBe(1);
  });

  it("keeps lyrics without time tags as plain text", () => {
    const lyrics = parseLyrics("第一句\n\n第二句");
    expect(lyrics).toEqual({ synced: false, lines: [{ time: -1, text: "第一句" }, { time: -1, text: "第二句" }] });
    expect(activeLine(lyrics, 3)).toBe(-1);
  });
});

describe("moveRows", () => {
  it("moves playlist rows before a target", () => {
    expect(moveRows(["a", "b", "c", "d", "e"], [3], 1)).toEqual(["a", "d", "b", "c", "e"]);
    expect(moveRows(["a", "b", "c", "d", "e"], [0, 1], 4)).toEqual(["c", "d", "a", "b", "e"]);
    expect(moveRows(["a", "b", "c"], [0], 3)).toEqual(["b", "c", "a"]);
  });
});
