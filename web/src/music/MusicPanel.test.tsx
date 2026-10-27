import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setSession } from "../api/client";
import type { Playlist, Track } from "./api";
import { MusicPanel } from "./MusicPanel";
import { track } from "./testTracks";
import { json, stubAPI } from "../test/api";

const library: Track[] = [
  track("file:a1", {
    title: "晴天",
    artist: "周杰伦",
    artists: ["周杰伦"],
    album: "叶惠美",
    albumId: "album:yhm",
    trackNo: 2,
    durationMs: 269_000,
  }),
  track("file:a2", {
    title: "以父之名",
    artist: "周杰伦",
    artists: ["周杰伦"],
    album: "叶惠美",
    albumId: "album:yhm",
    trackNo: 1,
    durationMs: 342_000,
  }),
  track("file:b1", {
    title: "Lantern Light",
    artist: "Mira Chen",
    artists: ["Mira Chen"],
    album: "Midnight",
    albumId: "album:mid",
    durationMs: 200_000,
  }),
];

function serve(items: Track[] = library) {
  const playlists: Playlist[] = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url === "/api/v1/music/tracks") return json({ version: 1, items });
    if (url === "/api/v1/music/library/scan")
      return json({ version: 1, tracks: items.length, scan: { state: "idle", done: 0, total: 0 } }, 202);
    if (url === "/api/v1/music/library")
      return json({ version: 1, tracks: items.length, scan: { state: "idle", done: 0, total: 0 } });
    if (url === "/api/v1/music/sources")
      return json({ custom: false, items: [{ spaceId: "space:shared", spaceKind: "shared", path: "" }] });
    if (url === "/api/v1/music/playlists" && method === "POST") {
      const body = JSON.parse(String(init?.body));
      const playlist = {
        id: "playlist:1",
        name: body.name,
        trackIds: body.trackIds,
        createdAt: "2026-10-10T00:00:00Z",
        updatedAt: "2026-10-10T00:00:00Z",
      };
      playlists.push(playlist);
      return json(playlist, 201);
    }
    if (url === "/api/v1/music/playlists") return json({ items: playlists });
    if (method !== "GET") return new Response(null, { status: 204 });
    return json({ items: [] });
  });
  stubAPI(fetchMock);
  return fetchMock;
}

const played: string[] = [];

beforeEach(() => {
  setSession({
    csrfToken: "csrf",
    expiresAt: "2026-10-11T00:00:00Z",
    user: { id: "user:u1", username: "u1", role: "member", status: "active", createdAt: "", mustChangePassword: false },
  } as never);
  window.localStorage.clear();
  played.length = 0;
  vi.spyOn(HTMLMediaElement.prototype, "play").mockImplementation(function (this: HTMLMediaElement) {
    played.push(this.src);
    return Promise.resolve();
  });
  vi.spyOn(HTMLMediaElement.prototype, "pause").mockImplementation(() => undefined);
  vi.spyOn(HTMLMediaElement.prototype, "load").mockImplementation(() => undefined);
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const rowOf = (title: string) => screen.getAllByRole("row").find((row) => within(row).queryByText(title))!;

describe("MusicPanel", () => {
  it("loads the library, starts a scan and plays a song on double click", async () => {
    const fetchMock = serve();
    render(<MusicPanel userId="user:u1" />);
    expect(await screen.findByText("以父之名")).toBeTruthy();
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/music/library/scan", expect.objectContaining({ method: "POST" }));
    expect(screen.getByText("全部歌曲 · 3 首 · 14 分钟")).toBeTruthy();

    fireEvent.doubleClick(rowOf("晴天"));
    await waitFor(() => expect(played.at(-1)).toContain("/api/v1/music/tracks/file%3Aa1/stream"));
    const console = screen.getByRole("contentinfo", { name: "播放控制" });
    expect(within(console).getByText("晴天")).toBeTruthy();
    expect(within(console).getByRole("button", { name: "播放队列" }).textContent).toBe("3");
  });

  it("says the lyrics are loading until they arrive", async () => {
    const answer = serve([track("file:l1", { title: "有歌词的歌", lyrics: true, durationMs: 200_000 })]);
    let deliver: (response: Response) => void = () => undefined;
    const lyrics = new Promise<Response>((resolve) => {
      deliver = resolve;
    });
    stubAPI(
      vi.fn((input: RequestInfo | URL, init?: RequestInit) =>
        String(input).endsWith("/lyrics") ? lyrics : answer(input, init),
      ),
    );
    render(<MusicPanel userId="user:u1" />);
    await screen.findByText("有歌词的歌");
    fireEvent.doubleClick(rowOf("有歌词的歌"));
    fireEvent.click(await screen.findByRole("button", { name: "歌词" }));
    const stage = await screen.findByRole("region", { name: "正在播放" });
    expect(within(stage).getByText("正在载入歌词…")).toBeTruthy();
    expect(within(stage).queryByText("暂无歌词")).toBeNull();
    deliver(json({ text: "[00:01.00]第一句" }));
    expect(await within(stage).findByText("第一句")).toBeTruthy();
  });

  it("restarts a decoded track's stream from where the listener seeks", async () => {
    serve([...library, track("file:ape", { title: "无损的歌", format: "ape", transcode: true, durationMs: 300_000 })]);
    // A playing element: seeking keeps it playing from the new start.
    vi.spyOn(HTMLMediaElement.prototype, "paused", "get").mockReturnValue(false);
    render(<MusicPanel userId="user:u1" />);
    await screen.findByText("无损的歌");
    expect(within(rowOf("无损的歌")).getByText("APE")).toBeTruthy();
    fireEvent.doubleClick(rowOf("无损的歌"));
    await waitFor(() => expect(played.at(-1)).toMatch(/file%3Aape\/stream$/));
    const progress = screen.getByRole("slider", { name: "播放进度" });
    expect(progress.getAttribute("aria-valuemax")).toBe("300");
    fireEvent.keyDown(progress, { key: "PageUp" });
    await waitFor(() => expect(played.at(-1)).toMatch(/file%3Aape\/stream\?start=20\.000$/));
    expect(progress.getAttribute("aria-valuenow")).toBe("20");
  });

  it("keeps favorites and shows them under 我的收藏", async () => {
    const fetchMock = serve();
    render(<MusicPanel userId="user:u1" />);
    await screen.findByText("晴天");
    fireEvent.click(within(rowOf("晴天")).getByRole("button", { name: "收藏" }));
    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        "/api/v1/music/favorites/file%3Aa1",
        expect.objectContaining({ method: "PUT" }),
      ),
    );
    fireEvent.click(screen.getByRole("button", { name: /我的收藏/ }));
    expect(await screen.findByRole("heading", { name: "我的收藏" })).toBeTruthy();
    expect(rowOf("晴天")).toBeTruthy();
    expect(screen.queryByText("以父之名")).toBeNull();
  });

  it("creates a playlist from a song's context menu", async () => {
    const user = userEvent.setup();
    const fetchMock = serve();
    render(<MusicPanel userId="user:u1" />);
    await screen.findByText("Lantern Light");
    fireEvent.contextMenu(rowOf("Lantern Light"));
    await user.click(screen.getByRole("menuitem", { name: "添加到歌单" }));
    // Moving from the menu to its submenu must not close the submenu.
    await user.click(screen.getByRole("menuitem", { name: "新建歌单…" }));
    await user.type(screen.getByRole("textbox", { name: "歌单名称" }), "夜跑");
    await user.click(screen.getByRole("button", { name: "创建" }));
    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        "/api/v1/music/playlists",
        expect.objectContaining({ method: "POST", body: JSON.stringify({ name: "夜跑", trackIds: ["file:b1"] }) }),
      ),
    );
    expect(await screen.findByRole("button", { name: /夜跑/ })).toBeTruthy();
  });

  it("explains how to add music when the library is empty", async () => {
    serve([]);
    const openFiles = vi.fn();
    render(<MusicPanel userId="user:u1" onOpenFiles={openFiles} />);
    expect(await screen.findByRole("heading", { name: "还没有音乐" })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "打开文件管理" }));
    expect(openFiles).toHaveBeenCalled();
  });
});
