import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setSession } from "../api/client";
import { json, stubAPI } from "../test/api";
import { transferStore } from "../transfers/store";
import type { Episode, Home, MediaLibrary, ShowDetail, Title, VideoDetail } from "./api";
import { MediaCenter } from "./MediaCenter";
import { Player } from "./Player";

const art = { poster: true, backdrop: true, thumb: true, version: "1" };
const film: Title = {
  id: "video:heat",
  type: "movie",
  libraryId: "library:films",
  title: "盗火线",
  year: 1995,
  addedAt: "2026-10-09T00:00:00Z",
  duration: 10200,
  resolution: "1080p",
  artwork: art,
  favorite: false,
};
const show: Title = {
  id: "show:blossoms",
  type: "show",
  libraryId: "library:films",
  title: "繁花",
  year: 2023,
  addedAt: "2026-10-08T00:00:00Z",
  artwork: art,
  favorite: false,
  seasons: 1,
  episodes: 30,
  watchedEpisodes: 3,
};
const resuming: Title = {
  ...film,
  id: "video:dune",
  title: "沙丘",
  progress: { position: 600, duration: 9300, watched: false, playedAt: "2026-10-10T08:00:00Z" },
};
const library: MediaLibrary = {
  id: "library:films",
  name: "家庭影院",
  kind: "mixed",
  spaceId: "space:shared",
  spaceKind: "shared",
  createdBy: "user:admin",
  createdAt: "2026-10-01T00:00:00Z",
  folders: [{ entryId: "file:films", name: "影视", path: "影视" }],
  counts: { movies: 2, shows: 1, episodes: 30, others: 0, sizeBytes: 9e10 },
  scan: { state: "idle", pending: 0, scannedAt: "2026-10-10T07:00:00Z" },
  canManage: true,
  covers: [film.id],
};
const home: Home = {
  featured: [resuming, film],
  continue: [resuming],
  recent: [film, show],
  movies: [film, resuming],
  shows: [show],
  others: [],
  favorites: [],
  libraries: 1,
  scanning: false,
};

function detail(title: Title, overrides: Partial<VideoDetail> = {}): VideoDetail {
  return {
    ...title,
    plot: "洛杉矶的警探与大盗。",
    file: { name: "Heat (1995).mkv", folder: "影视/电影", sizeBytes: 8e9, modifiedAt: "2026-10-01T00:00:00Z" },
    media: { container: "MKV", videoCodec: "H.264", audioCodec: "AC3", width: 1920, height: 1080 },
    probeState: "ready",
    audio: [{ index: 0, label: "英语 · AC3 · 5.1", codec: "AC3", default: true }],
    subtitles: [],
    collections: [],
    ...overrides,
  };
}

type Route = (url: string, init?: RequestInit) => Response | undefined;

function serve(libraries: MediaLibrary[], ...routes: Route[]) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    for (const route of routes) {
      const response = route(url, init);
      if (response) return response;
    }
    if (url === "/api/v1/media/libraries" && (init?.method ?? "GET") === "GET")
      return json({ items: libraries, processing: true });
    if (url === "/api/v1/media/home")
      return json(
        libraries.length
          ? home
          : { ...home, featured: [], continue: [], recent: [], movies: [], shows: [], libraries: 0 },
      );
    if (url.startsWith("/api/v1/media/titles"))
      return json({ items: [film, resuming], total: 2, genres: ["剧情", "犯罪"] });
    if (init?.method && init.method !== "GET") return json(undefined, 204);
    return json({ items: [] });
  });
  stubAPI(fetchMock);
  return fetchMock;
}

// Uploads go through the transfer center's tus client; tests answer it.
const tus = vi.hoisted(() => {
  class FakeUpload {
    url: string | null = null;
    constructor(
      readonly file: File,
      readonly options: Record<string, any>,
    ) {
      uploads.push(this);
    }
    start() {}
    abort() {
      return Promise.resolve();
    }
    get metadata() {
      return this.options.metadata as Record<string, string>;
    }
    fail(status: number, code: string) {
      this.options.onError(
        Object.assign(new Error(code), {
          originalRequest: { getMethod: () => "POST" },
          originalResponse: {
            getStatus: () => status,
            getBody: () => JSON.stringify({ error: { code, message: code } }),
          },
        }),
      );
    }
  }
  const uploads: FakeUpload[] = [];
  return { FakeUpload, uploads };
});
vi.mock("tus-js-client", () => ({ Upload: tus.FakeUpload }));

function withPath(file: File, path: string): File {
  Object.defineProperty(file, "webkitRelativePath", { value: path });
  return file;
}

const calls = (mock: ReturnType<typeof serve>, method: string, prefix: string) =>
  mock.mock.calls.filter(([url, init]) => String(url).startsWith(prefix) && (init?.method ?? "GET") === method);

beforeEach(() => {
  setSession({
    csrfToken: "csrf",
    expiresAt: "2099-01-01T00:00:00Z",
    user: {
      id: "user:admin",
      username: "owner",
      role: "admin",
      status: "active",
      createdAt: "",
      mustChangePassword: false,
    } as never,
  });
  vi.spyOn(HTMLMediaElement.prototype, "play").mockImplementation(() => Promise.resolve());
  vi.spyOn(HTMLMediaElement.prototype, "pause").mockImplementation(() => undefined);
  window.localStorage.clear();
  tus.uploads.length = 0;
  transferStore.attach("user:admin");
});

afterEach(() => {
  transferStore.reset();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("MediaCenter", () => {
  it("guides a new household to create a library from folders of a space", async () => {
    const created = {
      ...library,
      id: "library:new",
      name: "电影",
      kind: "movies" as const,
      scan: { state: "scanning" as const, pending: 0 },
    };
    const fetchMock = serve(
      [],
      (url) =>
        url === "/api/v1/spaces"
          ? json({
              items: [
                { id: "space:mine", kind: "private", name: "owner", ownerUserId: "user:admin", createdAt: "" },
                { id: "space:shared", kind: "shared", name: "Shared", createdAt: "" },
              ],
            })
          : undefined,
      (url) =>
        url.startsWith("/api/v1/spaces/space%3Ashared/entries?parentId=&") ||
        url === "/api/v1/spaces/space%3Ashared/entries?parentId="
          ? json({
              items: [
                {
                  id: "file:movies",
                  spaceId: "space:shared",
                  name: "电影",
                  kind: "directory",
                  sizeBytes: 0,
                  modifiedAt: "",
                },
                {
                  id: "file:notes",
                  spaceId: "space:shared",
                  name: "notes.txt",
                  kind: "file",
                  sizeBytes: 3,
                  modifiedAt: "",
                },
              ],
            })
          : undefined,
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
    expect(JSON.parse(String(init?.body))).toEqual({
      name: "电影",
      kind: "movies",
      folderIds: ["file:movies"],
      spaceId: "space:shared",
    });
    expect((init?.headers as Headers | undefined)?.get("X-CSRF-Token")).toBe("csrf");
    expect(await screen.findByText("已创建“电影”，正在扫描")).toBeTruthy();
  });

  it("names an unnamed library after its folder and picks folders by pressing them", async () => {
    const fetchMock = serve(
      [],
      (url) =>
        url === "/api/v1/spaces"
          ? json({ items: [{ id: "space:shared", kind: "shared", name: "Shared", createdAt: "" }] })
          : undefined,
      (url) =>
        url === "/api/v1/spaces/space%3Ashared/entries?parentId="
          ? json({
              items: [
                {
                  id: "file:media",
                  spaceId: "space:shared",
                  name: "影视",
                  kind: "directory",
                  sizeBytes: 0,
                  modifiedAt: "",
                },
              ],
            })
          : undefined,
      (url) =>
        url === "/api/v1/spaces/space%3Ashared/entries?parentId=file%3Amedia"
          ? json({
              items: [
                {
                  id: "file:movies",
                  spaceId: "space:shared",
                  name: "电影",
                  kind: "directory",
                  sizeBytes: 0,
                  modifiedAt: "",
                },
                {
                  id: "file:shows",
                  spaceId: "space:shared",
                  name: "剧集",
                  kind: "directory",
                  sizeBytes: 0,
                  modifiedAt: "",
                },
              ],
            })
          : undefined,
      (url, init) =>
        url === "/api/v1/media/libraries" && init?.method === "POST"
          ? json({ ...library, id: "library:new", name: "电影" }, 201)
          : undefined,
    );
    const user = userEvent.setup();
    render(<MediaCenter userId="user:admin" isAdmin />);
    await user.click(await screen.findByRole("button", { name: "新建媒体库" }));
    const dialog = await screen.findByRole("dialog", { name: "新建媒体库" });
    // A name is not needed to go on.
    await user.click(within(dialog).getByRole("button", { name: "下一步" }));

    // Pressing a folder picks it; its arrow goes inside, where the folder itself can be picked too.
    await user.click(await within(dialog).findByRole("button", { name: "选择影视" }));
    expect(within(dialog).getByLabelText("已选择的文件夹").textContent).toBe("影视");
    await user.click(within(dialog).getByRole("button", { name: "打开影视" }));
    expect(await within(dialog).findByRole("button", { name: "选择电影" })).toBeTruthy();
    await user.click(within(dialog).getByRole("button", { name: "取消选择影视" }));
    await user.click(within(dialog).getByRole("button", { name: "选择电影" }));
    expect(within(dialog).getByRole("button", { name: "取消选择电影" }).getAttribute("aria-pressed")).toBe("true");
    await user.click(within(dialog).getByRole("button", { name: "创建并扫描" }));

    await waitFor(() => expect(calls(fetchMock, "POST", "/api/v1/media/libraries")).toHaveLength(1));
    const [, init] = calls(fetchMock, "POST", "/api/v1/media/libraries")[0];
    expect(JSON.parse(String(init?.body))).toEqual({
      name: "电影",
      kind: "movies",
      folderIds: ["file:movies"],
      spaceId: "space:shared",
    });
  });

  it("takes files dropped on the home page into an upload instead of letting the browser open them", async () => {
    serve([library]);
    render(<MediaCenter userId="user:admin" isAdmin />);
    await screen.findByRole("region", { name: "精选" });
    const dataTransfer = { types: ["Files"], items: [], files: [new File(["clip"], "Up (2009).mp4")] };
    const main = screen.getByRole("main");
    expect(fireEvent.dragOver(main, { dataTransfer })).toBe(false);
    fireEvent.dragEnter(main, { dataTransfer });
    const overlay = await screen.findByText("松开即可上传到媒体库");
    expect(fireEvent.drop(overlay, { dataTransfer })).toBe(false);
    const dialog = await screen.findByRole("dialog", { name: "上传视频" });
    expect(await within(dialog).findByText("Up (2009).mp4")).toBeTruthy();
    // The dialog has the keyboard, so Escape closes it.
    expect(document.activeElement).toBe(dialog);
    fireEvent.keyDown(dialog, { key: "Escape" });
    expect(screen.queryByRole("dialog", { name: "上传视频" })).toBeNull();
  });

  it("shows the home rows and navigates the sidebar", async () => {
    const fetchMock = serve([library], (url) =>
      url === "/api/v1/media/videos/video%3Aheat" ? json(detail(film)) : undefined,
    );
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
    await waitFor(() =>
      expect(fetchMock.mock.calls.some(([url]) => String(url).includes("/api/v1/media/titles?category=movie"))).toBe(
        true,
      ),
    );
    await user.click(screen.getByRole("button", { name: "犯罪" }));
    await waitFor(() =>
      expect(fetchMock.mock.calls.some(([url]) => String(url).includes("genre=%E7%8A%AF%E7%BD%AA"))).toBe(true),
    );

    await user.type(screen.getByRole("textbox", { name: "搜索影视" }), "沙丘");
    expect(await screen.findByRole("heading", { name: /沙丘”的搜索结果/ })).toBeTruthy();
    await waitFor(() =>
      expect(fetchMock.mock.calls.some(([url]) => String(url).includes("q=%E6%B2%99%E4%B8%98"))).toBe(true),
    );
  });

  it("lists history by day and removes entries", async () => {
    const episode: Title = {
      ...film,
      id: "video:blossoms-3",
      type: "episode",
      title: "第 3 集",
      showId: show.id,
      showTitle: "繁花",
      season: 1,
      episode: 3,
      progress: { position: 0, duration: 2700, watched: true, playedAt: "2026-10-10T07:00:00Z" },
    };
    const fetchMock = serve([library], (url, init) =>
      url.startsWith("/api/v1/media/history?") && (init?.method ?? "GET") === "GET"
        ? json({ items: [resuming, episode], total: 2 })
        : undefined,
    );
    const user = userEvent.setup();
    render(<MediaCenter userId="user:admin" isAdmin />);
    const nav = await screen.findByRole("navigation", { name: "影视中心导航" });
    await user.click(within(nav).getByRole("button", { name: "最近播放" }));
    const card = await screen.findByRole("article", { name: "沙丘" });
    expect(card.textContent).toMatch(/剩余/);
    // An episode's card shows the show, so its caption says which episode.
    expect(screen.getByRole("article", { name: "繁花" }).textContent).toContain("S1 · E3 · 已看完");
    await user.click(within(card).getByRole("button", { name: "沙丘的更多操作" }));
    await user.click(screen.getByRole("menuitem", { name: "从记录中移除" }));
    await waitFor(() => expect(calls(fetchMock, "DELETE", "/api/v1/media/history/video%3Adune")).toHaveLength(1));
    await user.click(screen.getByRole("button", { name: "清空记录" }));
    await user.click(
      within(await screen.findByRole("dialog", { name: "清空播放记录？" })).getByRole("button", { name: "清空" }),
    );
    await waitFor(() => expect(calls(fetchMock, "DELETE", "/api/v1/media/history")).toHaveLength(2));
  });

  it("resumes a converted stream, restarts it to seek and saves progress", async () => {
    const stream = "/api/v1/media/videos/video%3Adune/stream?mode=remux&audio=0&quality=original";
    const fetchMock = serve(
      [library],
      (url) =>
        url === "/api/v1/media/videos/video%3Adune"
          ? json(
              detail(resuming, { subtitles: [{ id: "x0", label: "简体中文", language: "zh-Hans", external: true }] }),
            )
          : undefined,
      (url) =>
        url.startsWith("/api/v1/media/videos/video%3Adune/playback?")
          ? json({
              mode: "remux",
              url: stream,
              duration: 9300,
              reason: "浏览器无法播放 AC3 音频，正在转换音频",
              audio: 0,
              quality: "original",
              qualities: ["original", "720p", "480p"],
            })
          : undefined,
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
    await act(async () => {
      fireEvent.keyDown(player, { key: "Escape" });
    });
    await act(async () => {
      fireEvent.keyDown(player, { key: "Escape" });
    });
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "播放 沙丘" })).toBeNull());
  });

  it("rescans a library and shows that it is scanning", async () => {
    const scanning = { ...library, scan: { state: "scanning" as const, pending: 0 } };
    let scanned = false;
    const fetchMock = serve(
      [library],
      (url, init) => {
        if (url !== "/api/v1/media/libraries/library%3Afilms/scan" || init?.method !== "POST") return undefined;
        scanned = true;
        return json(scanning, 202);
      },
      // The server keeps reporting the scan while it runs.
      (url, init) =>
        scanned && url === "/api/v1/media/libraries" && (init?.method ?? "GET") === "GET"
          ? json({ items: [scanning], processing: true })
          : undefined,
    );
    const user = userEvent.setup();
    render(<MediaCenter userId="user:admin" isAdmin />);
    const nav = await screen.findByRole("navigation", { name: "影视中心导航" });
    await user.click(within(nav).getByRole("button", { name: /媒体库/ }));
    await user.click(await screen.findByRole("button", { name: "家庭影院的操作" }));
    await user.click(screen.getByRole("menuitem", { name: "重新扫描" }));
    expect(await screen.findByText("正在重新扫描“家庭影院”")).toBeTruthy();
    expect(screen.getByText("正在扫描文件夹…")).toBeTruthy();
    expect(screen.queryByText("请求失败，请稍后再试")).toBeNull();
    expect(calls(fetchMock, "POST", "/api/v1/media/libraries/library%3Afilms/scan")).toHaveLength(1);
  });

  it("creates a collection and adds a title to it from a card", async () => {
    const fetchMock = serve([library], (url, init) =>
      url === "/api/v1/media/collections" && (init?.method ?? "GET") === "GET"
        ? json({
            items: [
              { id: "collection:weekend", name: "周末片单", automatic: false, count: 0, updatedAt: "", covers: [] },
            ],
          })
        : undefined,
    );
    const user = userEvent.setup();
    render(<MediaCenter userId="user:admin" isAdmin />);
    const recent = await screen.findByRole("region", { name: "最近添加" });
    await user.click(within(recent).getByRole("button", { name: "盗火线的更多操作" }));
    await user.click(screen.getByRole("menuitem", { name: "加入合集…" }));
    const picker = await screen.findByRole("dialog", { name: "加入合集" });
    await user.click(await within(picker).findByRole("button", { name: /周末片单/ }));
    await waitFor(() =>
      expect(calls(fetchMock, "POST", "/api/v1/media/collections/collection%3Aweekend/items")).toHaveLength(1),
    );
    const [, init] = calls(fetchMock, "POST", "/api/v1/media/collections/collection%3Aweekend/items")[0];
    expect(JSON.parse(String(init?.body))).toEqual({ items: ["video:heat"] });
  });

  it("uploads films from the movies page into a library that holds them", async () => {
    const movies: MediaLibrary = {
      ...library,
      id: "library:movies",
      name: "电影收藏",
      kind: "movies",
      folders: [{ entryId: "file:movies", name: "电影", path: "电影" }],
    };
    const shows: MediaLibrary = {
      ...library,
      id: "library:shows",
      name: "追剧",
      kind: "shows",
      folders: [{ entryId: "file:shows", name: "剧集", path: "剧集" }],
    };
    serve([library, movies, shows]);
    const user = userEvent.setup();
    render(<MediaCenter userId="user:admin" isAdmin />);
    const nav = await screen.findByRole("navigation", { name: "影视中心导航" });
    await user.click(within(nav).getByRole("button", { name: /电影/ }));
    await user.click(await screen.findByRole("button", { name: "上传" }));
    const dialog = await screen.findByRole("dialog", { name: "上传视频" });
    // The movies page offers the libraries whose films it lists.
    expect(
      within(dialog)
        .getAllByRole("radio")
        .map((choice) => choice.textContent),
    ).toEqual([expect.stringContaining("家庭影院"), expect.stringContaining("电影收藏")]);
    await user.click(within(dialog).getByRole("radio", { name: /电影收藏/ }));
    fireEvent.change(within(dialog).getByLabelText("选择要上传的文件"), {
      target: {
        files: [
          new File(["video"], "Heat (1995).mkv"),
          new File(["subtitle"], "Heat (1995).zh.srt"),
          new File(["x"], "readme.txt"),
        ],
      },
    });
    expect(within(dialog).getByText("不是视频、字幕、NFO 或海报，已跳过")).toBeTruthy();
    await user.click(within(dialog).getByRole("button", { name: "上传 2 个文件" }));
    // The whole batch is queued at once in the transfer center.
    expect(transferStore.getSnapshot().uploads.map((upload) => [upload.name, upload.target.label])).toEqual([
      ["Heat (1995).mkv", "影视中心 · 电影收藏"],
      ["Heat (1995).zh.srt", "影视中心 · 电影收藏"],
    ]);
    expect(
      tus.uploads.map((upload) => [
        upload.options.endpoint,
        upload.metadata.spaceId,
        upload.metadata.parentId,
        upload.metadata.filename,
      ]),
    ).toEqual([
      ["/api/v1/uploads", "space:shared", "file:movies", "Heat (1995).mkv"],
      ["/api/v1/uploads", "space:shared", "file:movies", "Heat (1995).zh.srt"],
    ]);
    const headers: Record<string, string> = {};
    await tus.uploads[0].options.onBeforeRequest({
      getMethod: () => "POST",
      getURL: () => "/api/v1/uploads",
      setHeader: (name: string, value: string) => {
        headers[name] = value;
      },
    });
    expect(headers["X-CSRF-Token"]).toBe("csrf");
    act(() => {
      for (const upload of tus.uploads) upload.options.onSuccess();
    });
    await waitFor(() => expect(within(dialog).getAllByText("已上传")).toHaveLength(2));
    await user.click(within(dialog).getByRole("button", { name: "完成" }));
    expect(screen.queryByRole("dialog", { name: "上传视频" })).toBeNull();
  });

  it("uploads a show folder whose folders the NAS creates and keeps files that already exist", async () => {
    const fetchMock = serve([library]);
    const user = userEvent.setup();
    render(<MediaCenter userId="user:admin" isAdmin />);
    const nav = await screen.findByRole("navigation", { name: "影视中心导航" });
    await user.click(within(nav).getByRole("button", { name: /电视剧/ }));
    await user.click(await screen.findByRole("button", { name: "上传" }));
    const dialog = await screen.findByRole("dialog", { name: "上传视频" });
    expect(within(dialog).getByText(/才会识别为剧集/)).toBeTruthy();
    fireEvent.change(within(dialog).getByLabelText("选择要上传的文件夹"), {
      target: {
        files: [
          withPath(new File(["e1"], "三体 S01E01.mp4"), "三体/Season 1/三体 S01E01.mp4"),
          withPath(new File(["p"], "poster.jpg"), "三体/poster.jpg"),
          withPath(new File(["junk"], ".DS_Store"), "三体/.DS_Store"),
        ],
      },
    });
    expect(within(dialog).getByText("三体/Season 1 · 2 B")).toBeTruthy();
    await user.click(within(dialog).getByRole("button", { name: "上传 2 个文件" }));
    // The NAS creates or reuses each folder of the upload as the signed-in user.
    expect(
      tus.uploads.map((upload) => [upload.metadata.parentId, upload.metadata.relativePath, upload.metadata.filename]),
    ).toEqual([
      ["file:films", "三体/Season 1", "三体 S01E01.mp4"],
      ["file:films", "三体", "poster.jpg"],
    ]);
    expect(calls(fetchMock, "POST", "/api/v1/spaces/space%3Ashared/directories")).toHaveLength(0);
    // A file already there is left alone, without asking.
    act(() => {
      tus.uploads[1].fail(409, "name_conflict");
      tus.uploads[0].options.onSuccess();
    });
    expect(await within(dialog).findByText("同名文件已存在，未覆盖")).toBeTruthy();
    expect(within(dialog).getByText("已上传 1 个，1 个已存在")).toBeTruthy();
  });

  it("keeps uploading to a library chosen from its card after the dialog closes", async () => {
    serve([library]);
    const user = userEvent.setup();
    render(<MediaCenter userId="user:admin" isAdmin />);
    const nav = await screen.findByRole("navigation", { name: "影视中心导航" });
    await user.click(within(nav).getByRole("button", { name: /媒体库/ }));
    await user.click(await screen.findByRole("button", { name: "家庭影院的操作" }));
    await user.click(screen.getByRole("menuitem", { name: "上传到此媒体库" }));
    const dialog = await screen.findByRole("dialog", { name: "上传视频" });
    fireEvent.change(within(dialog).getByLabelText("选择要上传的文件"), {
      target: { files: [new File(["clip"], "Up (2009).mp4")] },
    });
    await user.click(within(dialog).getByRole("button", { name: "上传 1 个文件" }));
    act(() => {
      tus.uploads[0].options.onProgress(1, 4);
    });
    expect(await within(dialog).findByText("25%")).toBeTruthy();
    await user.click(within(dialog).getByRole("button", { name: "在后台上传" }));
    expect(screen.queryByRole("dialog", { name: "上传视频" })).toBeNull();
    expect(screen.getByRole("button", { name: /正在上传，已完成 25%/ }).textContent).toContain("上传中 1/1");
    await act(async () => {
      tus.uploads[0].options.onSuccess();
    });
    expect(await screen.findByText("已上传 1 个文件，新视频稍后出现在影视中心")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /正在上传/ })).toBeNull();
    expect(tus.uploads[0].metadata.parentId).toBe("file:films");
  });

  // Uploads that finish together refresh the libraries while a load is
  // still running. A second poll left behind outlived the panel and fired
  // after the tests had ended (CI, 2026-10-14).
  it("stops polling libraries when it closes, even after overlapping refreshes", async () => {
    const polls = new Set<number>();
    const setTimer = window.setTimeout.bind(window);
    const clearTimer = window.clearTimeout.bind(window);
    vi.spyOn(window, "setTimeout").mockImplementation(((handler: TimerHandler, delay?: number, ...rest: unknown[]) => {
      const id = setTimer(handler, delay, ...rest);
      // The library poll waits 3 or 20 seconds; no other media timer does.
      if (delay === 3000 || delay === 20_000) polls.add(id);
      return id;
    }) as typeof window.setTimeout);
    vi.spyOn(window, "clearTimeout").mockImplementation(((id?: number) => {
      polls.delete(id as number);
      clearTimer(id);
    }) as typeof window.clearTimeout);
    serve([library]);
    const user = userEvent.setup();
    const { unmount } = render(<MediaCenter userId="user:admin" isAdmin />);
    const nav = await screen.findByRole("navigation", { name: "影视中心导航" });
    await user.click(within(nav).getByRole("button", { name: /媒体库/ }));
    await user.click(await screen.findByRole("button", { name: "家庭影院的操作" }));
    await user.click(screen.getByRole("menuitem", { name: "上传到此媒体库" }));
    const dialog = await screen.findByRole("dialog", { name: "上传视频" });
    fireEvent.change(within(dialog).getByLabelText("选择要上传的文件"), {
      target: { files: [new File(["a"], "Up (2009).mp4"), new File(["b"], "Coco (2017).mp4")] },
    });
    await user.click(within(dialog).getByRole("button", { name: "上传 2 个文件" }));
    await act(async () => {
      for (const upload of tus.uploads) upload.options.onSuccess();
    });
    await waitFor(() => expect(within(dialog).getAllByText("已上传")).toHaveLength(2));

    unmount();
    expect(polls.size).toBe(0);
  });

  it("renames a film's file from its card, keeping the extension", async () => {
    let attempts = 0;
    const fetchMock = serve([library], (url, init) => {
      if (url !== "/api/v1/media/videos/video%3Aheat") return undefined;
      if (init?.method !== "PATCH") return json(detail(film));
      attempts++;
      if (attempts === 1)
        return json({ error: { code: "name_conflict", message: "a file with this name already exists" } }, 409);
      return json(
        detail(film, {
          file: { name: "盗火线 (1995).mkv", folder: "影视/电影", sizeBytes: 8e9, modifiedAt: "2026-10-01T00:00:00Z" },
        }),
      );
    });
    const user = userEvent.setup();
    render(<MediaCenter userId="user:admin" isAdmin />);
    const recent = await screen.findByRole("region", { name: "最近添加" });
    await user.click(within(recent).getByRole("button", { name: "盗火线的更多操作" }));
    await user.click(screen.getByRole("menuitem", { name: "重命名片源…" }));
    const dialog = await screen.findByRole("dialog", { name: "重命名片源" });
    const input = await within(dialog).findByRole("textbox", { name: "新文件名" });
    expect((input as HTMLInputElement).value).toBe("Heat (1995)");
    expect(within(dialog).getByText(".mkv")).toBeTruthy();
    await user.clear(input);
    await user.type(input, "盗火线 (1995)");
    await user.click(within(dialog).getByRole("button", { name: "重命名" }));
    expect(await within(dialog).findByText("这个文件夹里已经有同名的文件。")).toBeTruthy();
    await user.click(within(dialog).getByRole("button", { name: "重命名" }));
    expect(await screen.findByText("已重命名为“盗火线 (1995).mkv”，正在重新识别片名")).toBeTruthy();
    expect(screen.queryByRole("dialog", { name: "重命名片源" })).toBeNull();
    const renames = calls(fetchMock, "PATCH", "/api/v1/media/videos/video%3Aheat");
    expect(renames.map(([, init]) => JSON.parse(String(init?.body)))).toEqual([
      { fileName: "盗火线 (1995).mkv" },
      { fileName: "盗火线 (1995).mkv" },
    ]);
  });

  it("renames a film's file with a double-click on its title", async () => {
    const fetchMock = serve([library], (url, init) => {
      if (url !== "/api/v1/media/videos/video%3Aheat") return undefined;
      if (init?.method !== "PATCH") return json(detail(film));
      return json(
        detail(film, {
          file: { name: "盗火线 (1995).mkv", folder: "影视/电影", sizeBytes: 8e9, modifiedAt: "2026-10-01T00:00:00Z" },
        }),
      );
    });
    const user = userEvent.setup();
    render(<MediaCenter userId="user:admin" isAdmin />);
    await user.click(
      within(await screen.findByRole("region", { name: "最近添加" })).getByRole("button", { name: "打开盗火线" }),
    );
    const heading = await screen.findByRole("heading", { level: 1, name: /盗火线/ });

    // The title turns into the file name, without its extension.
    await user.dblClick(heading);
    let input = within(heading).getByRole("textbox", { name: "重命名片源" }) as HTMLInputElement;
    expect(input.value).toBe("Heat (1995)");
    expect(within(heading).getByText(".mkv")).toBeTruthy();
    await user.type(input, "{Escape}");
    expect(within(heading).queryByRole("textbox")).toBeNull();
    expect(calls(fetchMock, "PATCH", "/api/v1/media/videos/video%3Aheat")).toHaveLength(0);

    await user.dblClick(heading);
    input = within(heading).getByRole("textbox", { name: "重命名片源" }) as HTMLInputElement;
    await user.clear(input);
    await user.type(input, "盗火线 (1995){Enter}");
    expect(await screen.findByText("已重命名为“盗火线 (1995).mkv”，正在重新识别片名")).toBeTruthy();
    const renames = calls(fetchMock, "PATCH", "/api/v1/media/videos/video%3Aheat");
    expect(renames.map(([, init]) => JSON.parse(String(init?.body)))).toEqual([{ fileName: "盗火线 (1995).mkv" }]);
    expect(within(heading).queryByRole("textbox")).toBeNull();
  });

  it("renames a whole show and an episode's file in place", async () => {
    const episode = (number: number): Episode => ({
      id: `video:blossoms-${number}`,
      season: 1,
      episode: number,
      title: `第 ${number} 集`,
      addedAt: "2026-10-08T00:00:00Z",
      artwork: art,
      duration: 2700,
      file: `繁花.S01E0${number}.mkv`,
    });
    const blossoms: ShowDetail = {
      ...show,
      collections: [],
      folder: "繁花 (2023)",
      next: episode(1),
      seasonList: [{ number: 1, episodes: [episode(1), episode(2)] }],
    };
    const fetchMock = serve(
      [library],
      (url, init) =>
        url === "/api/v1/media/shows/show%3Ablossoms"
          ? json(init?.method === "PATCH" ? { ...blossoms, folder: "Blossoms Shanghai (2023)" } : blossoms)
          : undefined,
      (url, init) =>
        url === "/api/v1/media/videos/video%3Ablossoms-1" && init?.method === "PATCH"
          ? json(
              detail(
                { ...film, id: "video:blossoms-1" },
                { file: { name: "繁花 第一集.mkv", folder: "影视/繁花 (2023)", sizeBytes: 1e9, modifiedAt: "" } },
              ),
            )
          : undefined,
    );
    const user = userEvent.setup();
    render(<MediaCenter userId="user:admin" isAdmin />);
    await user.click(
      within(await screen.findByRole("region", { name: "最近添加" })).getByRole("button", { name: "打开繁花" }),
    );
    const heading = await screen.findByRole("heading", { level: 1, name: /繁花/ });

    // The show's name is its folder's, year and all.
    await user.dblClick(heading);
    const input = within(heading).getByRole("textbox", { name: "重命名剧集" }) as HTMLInputElement;
    expect(input.value).toBe("繁花 (2023)");
    expect(within(heading).getByText("剧集文件夹和带剧名的各集文件一起改名")).toBeTruthy();
    await user.clear(input);
    await user.type(input, "Blossoms Shanghai (2023){Enter}");
    expect(await screen.findByText("已重命名为“Blossoms Shanghai (2023)”，正在重新整理剧集")).toBeTruthy();
    expect(
      calls(fetchMock, "PATCH", "/api/v1/media/shows/show%3Ablossoms").map(([, init]) =>
        JSON.parse(String(init?.body)),
      ),
    ).toEqual([{ name: "Blossoms Shanghai (2023)" }]);

    // An episode's title renames its file.
    const episodes = screen.getByRole("list");
    await user.dblClick(within(episodes).getByText("第 1 集"));
    const file = within(episodes).getByRole("textbox", { name: "重命名片源" }) as HTMLInputElement;
    expect(file.value).toBe("繁花.S01E01");
    await user.clear(file);
    await user.type(file, "繁花 第一集");
    fireEvent.blur(file);
    expect(await screen.findByText("已重命名为“繁花 第一集.mkv”，正在重新识别片名")).toBeTruthy();
    expect(
      calls(fetchMock, "PATCH", "/api/v1/media/videos/video%3Ablossoms-1").map(([, init]) =>
        JSON.parse(String(init?.body)),
      ),
    ).toEqual([{ fileName: "繁花 第一集.mkv" }]);
  });

  it("picks a library and an order from the page's dropdowns", async () => {
    const kids: MediaLibrary = { ...library, id: "library:kids", name: "少儿", covers: [] };
    const fetchMock = serve([library, kids]);
    const titleCalls = () =>
      fetchMock.mock.calls
        .map(([url]) => String(url))
        .filter((url) => url.startsWith("/api/v1/media/titles?category=movie"));
    const user = userEvent.setup();
    render(<MediaCenter userId="user:admin" isAdmin />);
    const nav = await screen.findByRole("navigation", { name: "影视中心导航" });
    await user.click(within(nav).getByRole("button", { name: /电影/ }));

    await user.click(await screen.findByRole("button", { name: "媒体库：全部媒体库" }));
    const libraries = screen.getByRole("listbox", { name: "媒体库" });
    expect(
      within(libraries)
        .getAllByRole("option")
        .map((option) => option.textContent),
    ).toEqual(["全部媒体库", "家庭影院", "少儿"]);
    await user.click(within(libraries).getByRole("option", { name: "少儿" }));
    expect(screen.queryByRole("listbox")).toBeNull();
    await waitFor(() => expect(titleCalls().some((url) => url.includes("library=library%3Akids"))).toBe(true));

    // The keyboard opens the list at the current choice and moves through it.
    screen.getByRole("button", { name: "排序：最近添加" }).focus();
    await user.keyboard("{ArrowDown}");
    expect(document.activeElement?.textContent).toBe("最近添加");
    await user.keyboard("{ArrowDown}{Enter}");
    expect(await screen.findByRole("button", { name: "排序：名称" })).toBe(document.activeElement);
    await waitFor(() =>
      expect(titleCalls().some((url) => url.includes("sort=name") && url.includes("library=library%3Akids"))).toBe(
        true,
      ),
    );
  });

  it("picks another episode or another film without leaving the player", async () => {
    const episode = (number: number): Episode => ({
      id: `video:blossoms-${number}`,
      season: 1,
      episode: number,
      title: `第 ${number} 集`,
      addedAt: "2026-10-08T00:00:00Z",
      artwork: art,
      duration: 2700,
    });
    const blossoms: ShowDetail = {
      ...show,
      collections: [],
      next: episode(2),
      seasonList: [{ number: 1, episodes: [episode(1), episode(2), episode(3)] }],
    };
    const playing: VideoDetail = detail({
      ...film,
      id: "video:blossoms-2",
      type: "episode",
      title: "第 2 集",
      showId: show.id,
      showTitle: "繁花",
      season: 1,
      episode: 2,
    });
    const fetchMock = serve(
      [library],
      (url) => (url === "/api/v1/media/videos/video%3Ablossoms-2" ? json(playing) : undefined),
      (url) =>
        url.startsWith("/api/v1/media/videos/video%3Ablossoms-2/playback?")
          ? json({
              mode: "direct",
              url: "/api/v1/media/videos/video%3Ablossoms-2/file",
              audio: 0,
              quality: "original",
              qualities: ["original"],
            })
          : undefined,
      (url) => (url === "/api/v1/media/shows/show%3Ablossoms" ? json(blossoms) : undefined),
    );
    const navigate = vi.fn();
    const user = userEvent.setup();
    render(
      <Player
        request={{ videoId: "video:blossoms-2" }}
        onClose={() => undefined}
        onNavigate={navigate}
        onSaved={() => undefined}
      />,
    );
    const player = await screen.findByRole("dialog", { name: "播放 繁花" });

    await user.click(within(player).getByRole("button", { name: "选集" }));
    const episodes = await within(player).findByRole("list", { name: "剧集" });
    const rows = within(episodes).getAllByRole("button");
    expect(rows.map((row) => row.textContent)).toEqual([
      expect.stringContaining("S1 · E1"),
      expect.stringContaining("正在播放"),
      expect.stringContaining("S1 · E3"),
    ]);
    await user.click(rows[2]);
    expect(navigate).toHaveBeenLastCalledWith("video:blossoms-3");

    // The library tab lists the library's titles; a film plays at once.
    await user.click(within(player).getByRole("button", { name: "选集" }));
    await user.click(await within(player).findByRole("tab", { name: "片库" }));
    await user.click(within(player).getByRole("button", { name: "电影" }));
    const films = await within(player).findByRole("list", { name: "影片" });
    await waitFor(() =>
      expect(
        fetchMock.mock.calls.some(
          ([url]) => String(url).includes("category=movie") && String(url).includes("library=library%3Afilms"),
        ),
      ).toBe(true),
    );
    await user.click(within(films).getByRole("button", { name: /盗火线/ }));
    expect(navigate).toHaveBeenLastCalledWith("video:heat");
    expect(within(player).queryByRole("complementary")).toBeNull();
  });

  it("plays an episode pressed anywhere on its row, its title after a moment", async () => {
    const episode = (number: number): Episode => ({
      id: `video:blossoms-${number}`,
      season: 1,
      episode: number,
      title: `第 ${number} 集`,
      addedAt: "2026-10-08T00:00:00Z",
      artwork: art,
      duration: 2700,
      file: `繁花.S01E0${number}.mkv`,
    });
    const blossoms: ShowDetail = {
      ...show,
      collections: [],
      folder: "繁花 (2023)",
      next: episode(1),
      seasonList: [{ number: 1, episodes: [episode(1), episode(2)] }],
    };
    const fetchMock = serve(
      [library],
      (url) => (url === "/api/v1/media/shows/show%3Ablossoms" ? json(blossoms) : undefined),
      (url) =>
        /^\/api\/v1\/media\/videos\/video%3Ablossoms-\d$/.test(url)
          ? json(
              detail({
                ...film,
                id: "video:blossoms-2",
                type: "episode",
                title: "第 2 集",
                showId: show.id,
                showTitle: "繁花",
                season: 1,
                episode: 2,
              }),
            )
          : undefined,
      (url) =>
        /\/videos\/video%3Ablossoms-\d\/playback/.test(url)
          ? json({
              mode: "direct",
              url: "/api/v1/media/videos/x/file",
              audio: 0,
              quality: "original",
              qualities: ["original"],
            })
          : undefined,
    );
    const user = userEvent.setup();
    render(<MediaCenter userId="user:admin" isAdmin />);
    await user.click(
      within(await screen.findByRole("region", { name: "最近添加" })).getByRole("button", { name: "打开繁花" }),
    );
    const episodes = await screen.findByRole("list");
    const opened = () =>
      fetchMock.mock.calls
        .map(([url]) => String(url))
        .filter((url) => /\/videos\/video%3Ablossoms-\d\/playback/.test(url));

    await user.click(within(episodes).getAllByText("45 分钟")[1]);
    await waitFor(() => expect(opened()).toEqual([expect.stringContaining("video%3Ablossoms-2")]));
    await act(async () => {
      fireEvent.keyDown(await screen.findByRole("dialog", { name: /播放/ }), { key: "Escape" });
    });

    // A double press on a title renames it and plays nothing.
    await user.dblClick(within(episodes).getByText("第 1 集"));
    expect(within(episodes).getByRole("textbox", { name: "重命名片源" })).toBeTruthy();
    await new Promise((resolve) => setTimeout(resolve, 350));
    expect(opened()).toHaveLength(1);
  });

  it("keeps the keyboard on the player after the next episode loads", async () => {
    const episode = (number: number): VideoDetail =>
      detail(
        {
          ...film,
          id: `video:blossoms-${number}`,
          type: "episode",
          title: `第 ${number} 集`,
          showId: show.id,
          showTitle: "繁花",
          season: 1,
          episode: number,
        },
        { nextId: `video:blossoms-${number + 1}` },
      );
    serve(
      [library],
      (url) => {
        const match = /^\/api\/v1\/media\/videos\/video%3Ablossoms-(\d)$/.exec(url);
        return match ? json(episode(Number(match[1]))) : undefined;
      },
      (url) =>
        /\/videos\/video%3Ablossoms-\d\/playback/.test(url)
          ? json({
              mode: "direct",
              url: "/api/v1/media/videos/x/file",
              audio: 0,
              quality: "original",
              qualities: ["original"],
            })
          : undefined,
    );
    const play = vi.mocked(HTMLMediaElement.prototype.play);
    const { rerender } = render(
      <Player
        request={{ videoId: "video:blossoms-1" }}
        onClose={() => undefined}
        onNavigate={() => undefined}
        onSaved={() => undefined}
      />,
    );
    const player = await screen.findByRole("dialog", { name: "播放 繁花" });
    // The viewer last pressed the episode button, which goes away while the next episode loads.
    within(player).getByRole("button", { name: "选集" }).focus();
    rerender(
      <Player
        request={{ videoId: "video:blossoms-2" }}
        onClose={() => undefined}
        onNavigate={() => undefined}
        onSaved={() => undefined}
      />,
    );
    await waitFor(() => expect(within(player).getByText("S1 · E2")).toBeTruthy());
    expect(document.activeElement).toBe(player);
    const before = play.mock.calls.length;
    fireEvent.keyDown(document.activeElement!, { key: " " });
    await waitFor(() => expect(play.mock.calls.length).toBe(before + 1));
    // A key leaves a notice of what it did.
    fireEvent.keyDown(document.activeElement!, { key: "ArrowDown" });
    expect(within(player).getByText("音量 90%")).toBeTruthy();
  });

  it("switches the player's library list between libraries", async () => {
    const kids: MediaLibrary = { ...library, id: "library:kids", name: "少儿", covers: [] };
    const playing: VideoDetail = detail(film);
    const fetchMock = serve(
      [library, kids],
      (url) => (url === "/api/v1/media/videos/video%3Aheat" ? json(playing) : undefined),
      (url) =>
        url.startsWith("/api/v1/media/videos/video%3Aheat/playback?")
          ? json({
              mode: "direct",
              url: "/api/v1/media/videos/video%3Aheat/file",
              audio: 0,
              quality: "original",
              qualities: ["original"],
            })
          : undefined,
    );
    const lastList = () =>
      fetchMock.mock.calls
        .map(([url]) => String(url))
        .filter((url) => url.startsWith("/api/v1/media/titles?"))
        .at(-1) ?? "";
    const user = userEvent.setup();
    render(
      <Player
        request={{ videoId: "video:heat" }}
        onClose={() => undefined}
        onNavigate={() => undefined}
        onSaved={() => undefined}
      />,
    );
    const player = await screen.findByRole("dialog", { name: "播放 盗火线" });
    await user.click(within(player).getByRole("button", { name: "片库" }));
    await waitFor(() => expect(lastList()).toContain("library=library%3Afilms"));

    // Pressing the library moves on to the next one.
    await user.click(await within(player).findByRole("button", { name: "媒体库：家庭影院，切换到少儿" }));
    await waitFor(() => expect(lastList()).toContain("library=library%3Akids"));
    expect(within(player).getByRole("button", { name: "媒体库：少儿，切换到全部媒体库" })).toBeTruthy();

    // Its arrows open every library to pick from.
    await user.click(within(player).getByRole("button", { name: "选择媒体库" }));
    const choices = within(player).getByRole("listbox", { name: "媒体库" });
    expect(within(choices).getByRole("option", { name: "少儿" }).getAttribute("aria-selected")).toBe("true");
    await user.click(within(choices).getByRole("option", { name: "全部媒体库" }));
    await waitFor(() => expect(lastList()).not.toContain("library="));
    expect(within(player).getByRole("button", { name: "媒体库：全部媒体库，切换到家庭影院" })).toBeTruthy();
  });

  it("plays what a converted stream already holds and restarts it once after quick skips", async () => {
    const stream = "/api/v1/media/videos/video%3Adune/stream?mode=remux&audio=0&quality=original";
    serve(
      [library],
      (url) => (url === "/api/v1/media/videos/video%3Adune" ? json(detail(resuming)) : undefined),
      (url) =>
        url.startsWith("/api/v1/media/videos/video%3Adune/playback?")
          ? json({ mode: "remux", url: stream, duration: 9300, audio: 0, quality: "original", qualities: ["original"] })
          : undefined,
    );
    const user = userEvent.setup();
    render(<MediaCenter userId="user:admin" isAdmin />);
    await user.click(
      within(await screen.findByRole("region", { name: "精选" })).getByRole("button", { name: /继续播放/ }),
    );
    const player = await screen.findByRole("dialog", { name: "播放 沙丘" });
    const video = player.querySelector("video")!;
    await waitFor(() => expect(video.getAttribute("src")).toBe(`${stream}&start=600.000`));
    // The stream, started at 10:00, has sent its first 30 seconds.
    let at = 0;
    Object.defineProperty(video, "currentTime", {
      configurable: true,
      get: () => at,
      set: (value: number) => {
        at = value;
      },
    });
    Object.defineProperty(video, "buffered", {
      configurable: true,
      get: () => ({ length: 1, start: () => 0, end: () => 30 }),
    });
    const sources: string[] = [];
    new MutationObserver(() => sources.push(video.getAttribute("src") ?? "")).observe(video, {
      attributes: true,
      attributeFilter: ["src"],
    });
    fireEvent.progress(video);
    expect(player.querySelectorAll(".mc-seek-buffer")).toHaveLength(1);

    // Within what arrived, a skip plays from there at once.
    fireEvent.keyDown(player, { key: "ArrowRight" });
    expect(at).toBe(10);
    expect(within(player).queryByText(/跳转到/)).toBeNull();

    // Past it, three quick skips restart the stream once, where they end.
    fireEvent.keyDown(player, { key: "ArrowRight" });
    fireEvent.keyDown(player, { key: "ArrowRight" });
    fireEvent.keyDown(player, { key: "ArrowRight" });
    expect(at).toBe(20);
    expect(within(player).getByText("跳转到 10:40")).toBeTruthy();
    await waitFor(() => expect(video.getAttribute("src")).toBe(`${stream}&start=640.000`));
    expect(sources).toEqual([`${stream}&start=640.000`]);
  });
});
