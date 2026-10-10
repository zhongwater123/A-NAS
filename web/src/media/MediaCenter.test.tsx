import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setSession } from "../api";
import { MediaCenter } from "./MediaCenter";
import type { Home, MediaLibrary, Title, VideoDetail } from "./mediaApi";

const art = { poster: true, backdrop: true, thumb: true, version: "1" };
const film: Title = { id: "video:heat", type: "movie", libraryId: "library:films", title: "盗火线", year: 1995, addedAt: "2026-10-09T00:00:00Z", duration: 10200, resolution: "1080p", artwork: art, favorite: false };
const show: Title = { id: "show:blossoms", type: "show", libraryId: "library:films", title: "繁花", year: 2023, addedAt: "2026-10-08T00:00:00Z", artwork: art, favorite: false, seasons: 1, episodes: 30, watchedEpisodes: 3 };
const resuming: Title = { ...film, id: "video:dune", title: "沙丘", progress: { position: 600, duration: 9300, watched: false, playedAt: "2026-10-10T08:00:00Z" } };
const library: MediaLibrary = {
  id: "library:films", name: "家庭影院", kind: "mixed", spaceId: "space:shared", spaceKind: "shared", createdBy: "user:admin", createdAt: "2026-10-01T00:00:00Z",
  folders: [{ entryId: "file:films", name: "影视", path: "影视" }], counts: { movies: 2, shows: 1, episodes: 30, others: 0, sizeBytes: 9e10 },
  scan: { state: "idle", pending: 0, scannedAt: "2026-10-10T07:00:00Z" }, canManage: true, covers: [film.id],
};
const home: Home = { featured: [resuming, film], continue: [resuming], recent: [film, show], movies: [film, resuming], shows: [show], others: [], favorites: [], libraries: 1, scanning: false };

function detail(title: Title, overrides: Partial<VideoDetail> = {}): VideoDetail {
  return {
    ...title, plot: "洛杉矶的警探与大盗。", file: { name: "Heat (1995).mkv", folder: "影视/电影", sizeBytes: 8e9, modifiedAt: "2026-10-01T00:00:00Z" },
    media: { container: "MKV", videoCodec: "H.264", audioCodec: "AC3", width: 1920, height: 1080 }, probeState: "ready",
    audio: [{ index: 0, label: "英语 · AC3 · 5.1", codec: "AC3", default: true }], subtitles: [], collections: [], ...overrides,
  };
}

function json(body: unknown, status = 200) {
  return new Response(status === 204 ? null : JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

type Route = (url: string, init?: RequestInit) => Response | undefined;

function serve(libraries: MediaLibrary[], ...routes: Route[]) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    for (const route of routes) {
      const response = route(url, init);
      if (response) return response;
    }
    if (url === "/api/v1/media/libraries" && !init?.method) return json({ items: libraries, processing: true });
    if (url === "/api/v1/media/home") return json(libraries.length ? home : { ...home, featured: [], continue: [], recent: [], movies: [], shows: [], libraries: 0 });
    if (url.startsWith("/api/v1/media/titles")) return json({ items: [film, resuming], total: 2, genres: ["剧情", "犯罪"] });
    if (init?.method && init.method !== "GET") return json(undefined, 204);
    return json({ items: [] });
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

const calls = (mock: ReturnType<typeof serve>, method: string, prefix: string) =>
  mock.mock.calls.filter(([url, init]) => String(url).startsWith(prefix) && (init?.method ?? "GET") === method);

beforeEach(() => {
  setSession({ csrfToken: "csrf", expiresAt: "2099-01-01T00:00:00Z", user: { id: "user:admin", username: "owner", role: "admin", status: "active", createdAt: "", mustChangePassword: false } as never });
  vi.spyOn(HTMLMediaElement.prototype, "play").mockImplementation(() => Promise.resolve());
  vi.spyOn(HTMLMediaElement.prototype, "pause").mockImplementation(() => undefined);
  window.localStorage.clear();
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("MediaCenter", () => {
  it("guides a new household to create a library from folders of a space", async () => {
    const created = { ...library, id: "library:new", name: "电影", kind: "movies" as const, scan: { state: "scanning" as const, pending: 0 } };
    const fetchMock = serve([],
      (url) => (url === "/api/v1/spaces" ? json({ items: [
        { id: "space:mine", kind: "private", name: "owner", ownerUserId: "user:admin", createdAt: "" },
        { id: "space:shared", kind: "shared", name: "Shared", createdAt: "" },
      ] }) : undefined),
      (url) => (url.startsWith("/api/v1/spaces/space%3Ashared/entries?parentId=&") || url === "/api/v1/spaces/space%3Ashared/entries?parentId=" ? json({ items: [
        { id: "file:movies", spaceId: "space:shared", name: "电影", kind: "directory", sizeBytes: 0, modifiedAt: "" },
        { id: "file:notes", spaceId: "space:shared", name: "notes.txt", kind: "file", sizeBytes: 3, modifiedAt: "" },
      ] }) : undefined),
      (url, init) => (url === "/api/v1/media/libraries" && init?.method === "POST" ? json(created, 201) : undefined),
    );
    const user = userEvent.setup();
    render(<MediaCenter userId="user:admin" isAdmin />);
    expect(await screen.findByRole("heading", { name: "把你的视频变成私人影院" })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "新建媒体库" }));

    const dialog = await screen.findByRole("dialog", { name: "新建媒体库" });
    await user.type(within(dialog).getByRole("textbox"), "电影");
    await user.click(within(dialog).getByRole("radio", { name: /^电影/ }));
    await user.click(within(dialog).getByRole("button", { name: "下一步" }));
    expect(within(dialog).getByRole("tab", { name: "共享空间" }).getAttribute("aria-selected")).toBe("true");
    expect(await within(dialog).findByRole("button", { name: "选择电影" })).toBeTruthy();
    expect(within(dialog).queryByText("notes.txt")).toBeNull();
    await user.click(within(dialog).getByRole("button", { name: "选择电影" }));
    expect(within(dialog).getByLabelText("已选择的文件夹").textContent).toContain("电影");
    await user.click(within(dialog).getByRole("button", { name: "创建并扫描" }));

    await waitFor(() => expect(calls(fetchMock, "POST", "/api/v1/media/libraries")).toHaveLength(1));
    const [, init] = calls(fetchMock, "POST", "/api/v1/media/libraries")[0];
    expect(JSON.parse(String(init?.body))).toEqual({ name: "电影", kind: "movies", folderIds: ["file:movies"], spaceId: "space:shared" });
    expect((init?.headers as Headers).get("X-CSRF-Token")).toBe("csrf");
    expect(await screen.findByText("已创建“电影”，正在扫描")).toBeTruthy();
  });

  it("shows the home rows and navigates the sidebar", async () => {
    const fetchMock = serve([library], (url) => (url === "/api/v1/media/videos/video%3Aheat" ? json(detail(film)) : undefined));
    const user = userEvent.setup();
    render(<MediaCenter userId="user:admin" isAdmin />);
    const hero = await screen.findByRole("region", { name: "精选" });
    expect(within(hero).getByRole("heading", { name: "沙丘" })).toBeTruthy();
    expect(within(hero).getByRole("button", { name: /继续播放/ })).toBeTruthy();
    const continueRow = screen.getByRole("region", { name: "继续观看" });
    expect(within(continueRow).getByText("沙丘")).toBeTruthy();
    const nav = screen.getByRole("navigation", { name: "影视中心导航" });
    expect(within(nav).getByRole("button", { name: /电视剧/ }).textContent).toContain("1");

    // A card opens its detail page; the heart saves a favorite.
    const recent = screen.getByRole("region", { name: "最近添加" });
    await user.click(within(recent).getByRole("button", { name: "打开盗火线" }));
    expect(await screen.findByRole("heading", { name: "盗火线" })).toBeTruthy();
    expect(screen.getByText("洛杉矶的警探与大盗。")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "收藏" }));
    await waitFor(() => expect(calls(fetchMock, "PUT", "/api/v1/media/favorites/video%3Aheat")).toHaveLength(1));

    await user.click(within(nav).getByRole("button", { name: /电影/ }));
    expect(await screen.findByRole("heading", { name: /^电影/ })).toBeTruthy();
    await waitFor(() => expect(fetchMock.mock.calls.some(([url]) => String(url).includes("/api/v1/media/titles?category=movie"))).toBe(true));
    await user.click(screen.getByRole("button", { name: "犯罪" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([url]) => String(url).includes("genre=%E7%8A%AF%E7%BD%AA"))).toBe(true));

    await user.type(screen.getByRole("textbox", { name: "搜索影视" }), "沙丘");
    expect(await screen.findByRole("heading", { name: /沙丘”的搜索结果/ })).toBeTruthy();
    await waitFor(() => expect(fetchMock.mock.calls.some(([url]) => String(url).includes("q=%E6%B2%99%E4%B8%98"))).toBe(true));
  });

  it("lists history by day and removes entries", async () => {
    const fetchMock = serve([library], (url, init) => (url.startsWith("/api/v1/media/history?") && !init?.method ? json({ items: [resuming], total: 1 }) : undefined));
    const user = userEvent.setup();
    render(<MediaCenter userId="user:admin" isAdmin />);
    const nav = await screen.findByRole("navigation", { name: "影视中心导航" });
    await user.click(within(nav).getByRole("button", { name: "最近播放" }));
    const card = await screen.findByRole("article", { name: "沙丘" });
    expect(card.textContent).toMatch(/剩余/);
    await user.click(within(card).getByRole("button", { name: "沙丘的更多操作" }));
    await user.click(screen.getByRole("menuitem", { name: "从记录中移除" }));
    await waitFor(() => expect(calls(fetchMock, "DELETE", "/api/v1/media/history/video%3Adune")).toHaveLength(1));
    await user.click(screen.getByRole("button", { name: "清空记录" }));
    await user.click(within(await screen.findByRole("dialog", { name: "清空播放记录？" })).getByRole("button", { name: "清空" }));
    await waitFor(() => expect(calls(fetchMock, "DELETE", "/api/v1/media/history")).toHaveLength(2));
  });

  it("resumes a converted stream, restarts it to seek and saves progress", async () => {
    const stream = "/api/v1/media/videos/video%3Adune/stream?mode=remux&audio=0&quality=original";
    const fetchMock = serve([library],
      (url) => (url === "/api/v1/media/videos/video%3Adune" ? json(detail(resuming, { subtitles: [{ id: "x0", label: "简体中文", language: "zh-Hans", external: true }] })) : undefined),
      (url) => (url.startsWith("/api/v1/media/videos/video%3Adune/playback?") ? json({ mode: "remux", url: stream, duration: 9300, reason: "浏览器无法播放 AC3 音频，正在转换音频", audio: 0, quality: "original", qualities: ["original", "720p", "480p"] }) : undefined),
    );
    const user = userEvent.setup();
    render(<MediaCenter userId="user:admin" isAdmin />);
    const hero = await screen.findByRole("region", { name: "精选" });
    await user.click(within(hero).getByRole("button", { name: /继续播放/ }));
    const player = await screen.findByRole("dialog", { name: "播放 沙丘" });
    const video = player.querySelector("video")!;
    await waitFor(() => expect(video.getAttribute("src")).toBe(`${stream}&start=600.000`));
    expect(within(player).getByText("转换封装")).toBeTruthy();
    expect(within(player).getByRole("status").textContent).toContain("已从 10:00 继续播放");
    expect(player.querySelector("track")?.getAttribute("src")).toBe("/api/v1/media/videos/video%3Adune/subtitles/x0");

    fireEvent.playing(video);
    fireEvent.keyDown(player, { key: "ArrowRight" });
    await waitFor(() => expect(video.getAttribute("src")).toBe(`${stream}&start=610.000`));
    fireEvent.pause(video);
    await waitFor(() => expect(calls(fetchMock, "PUT", "/api/v1/media/videos/video%3Adune/progress")).toHaveLength(1));
    const [, init] = calls(fetchMock, "PUT", "/api/v1/media/videos/video%3Adune/progress")[0];
    expect(JSON.parse(String(init?.body))).toEqual({ position: 610, duration: 9300 });

    await user.click(within(player).getByRole("button", { name: "播放设置" }));
    expect(within(player).getByText("浏览器无法播放 AC3 音频，正在转换音频")).toBeTruthy();
    await act(async () => { fireEvent.keyDown(player, { key: "Escape" }); });
    await act(async () => { fireEvent.keyDown(player, { key: "Escape" }); });
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "播放 沙丘" })).toBeNull());
  });

  it("creates a collection and adds a title to it from a card", async () => {
    const fetchMock = serve([library],
      (url, init) => (url === "/api/v1/media/collections" && !init?.method ? json({ items: [{ id: "collection:weekend", name: "周末片单", automatic: false, count: 0, updatedAt: "", covers: [] }] }) : undefined),
    );
    const user = userEvent.setup();
    render(<MediaCenter userId="user:admin" isAdmin />);
    const recent = await screen.findByRole("region", { name: "最近添加" });
    await user.click(within(recent).getByRole("button", { name: "盗火线的更多操作" }));
    await user.click(screen.getByRole("menuitem", { name: "加入合集…" }));
    const picker = await screen.findByRole("dialog", { name: "加入合集" });
    await user.click(await within(picker).findByRole("button", { name: /周末片单/ }));
    await waitFor(() => expect(calls(fetchMock, "POST", "/api/v1/media/collections/collection%3Aweekend/items")).toHaveLength(1));
    const [, init] = calls(fetchMock, "POST", "/api/v1/media/collections/collection%3Aweekend/items")[0];
    expect(JSON.parse(String(init?.body))).toEqual({ items: ["video:heat"] });
  });
});
