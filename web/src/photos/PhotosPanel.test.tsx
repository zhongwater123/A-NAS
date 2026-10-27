import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { HttpHandler } from "msw";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setSession } from "../api/client";
import type { PhotoAsset, PhotoLibrary, PhotoSearchResults } from "../api.generated";
import { session } from "../test/fixtures";
import {
  album,
  asset,
  type PhotosOptions,
  person,
  photoDetails,
  photosHandlers,
  privateLibrary,
  sharedLibrary,
} from "../test/handlers/photos";
import { apiError, formOf, http, pathOf, server } from "../test/server";
import { PhotosPanel } from "./PhotosPanel";

// jsdom decodes no images: the image searched by is the file as it came.
vi.mock("./imageSearch", async (original) => ({
  ...(await original<typeof import("./imageSearch")>()),
  prepareImageQuery: async (file: Blob, name: string) => ({
    key: `query-${name}-${Math.random()}`,
    name,
    blob: file,
    url: "blob:query",
    width: 4,
    height: 3,
  }),
}));

const trip = album("album:trip", "旅行", { coverId: "photo:a" });

// serve answers the photos window from what options hold, with the test's
// own handlers ahead of that.
function serve(options: PhotosOptions, ...handlers: HttpHandler[]) {
  server.use(...photosHandlers(options));
  server.use(...handlers);
}
// mine holds photos in alice's own library.
const mine = (photos: () => PhotoAsset[]) => ({ [privateLibrary.id]: photos });

// FakeUpload stands in for XMLHttpRequest, which uploads use for progress.
class FakeUpload {
  static sent: FakeUpload[] = [];
  static respond: (upload: FakeUpload) => { status: number; body: unknown } = () => ({
    status: 201,
    body: asset("photo:new"),
  });
  upload: { onprogress: ((event: ProgressEvent) => void) | null } = { onprogress: null };
  status = 0;
  responseText = "";
  url = "";
  headers: Record<string, string> = {};
  body?: FormData;
  onload: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onabort: (() => void) | null = null;
  open(_: string, url: string) {
    this.url = url;
  }
  setRequestHeader(name: string, value: string) {
    this.headers[name] = value;
  }
  abort() {
    /* uploads are not cancelled in these tests */
  }
  send(body: FormData) {
    this.body = body;
    FakeUpload.sent.push(this);
    queueMicrotask(() => {
      const { status, body: response } = FakeUpload.respond(this);
      this.status = status;
      this.responseText = JSON.stringify(response);
      this.onload?.();
    });
  }
}

const alice = session({ id: "user:alice", username: "alice", role: "member" }, { csrfToken: "csrf-photo" });
const sizes = Object.getOwnPropertyDescriptors(HTMLElement.prototype);
// jsdom lays nothing out; what scrolls, as the photo scroller does, gets a
// window-sized view. virtual-core measures it by offset* before 3.19 and by
// client* since.
const scrollerSize = { offsetHeight: 800, offsetWidth: 1000, clientHeight: 800, clientWidth: 1000 };
const scrolls = (element: Element) => element.isConnected && getComputedStyle(element).overflowY === "auto";
// scrollerOf is the element that scrolls element into view.
function scrollerOf(element: Element): HTMLElement {
  let parent = element.parentElement;
  while (parent && !scrolls(parent)) parent = parent.parentElement;
  if (!parent) throw new Error("nothing scrolls the element");
  return parent;
}

beforeEach(() => {
  for (const [name, size] of Object.entries(scrollerSize))
    Object.defineProperty(HTMLElement.prototype, name, {
      configurable: true,
      get() {
        return this instanceof HTMLElement && scrolls(this) ? size : 0;
      },
    });
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {
        /* no layout in jsdom */
      }
      unobserve() {
        /* none */
      }
      disconnect() {
        /* none */
      }
    },
  );
  setSession(alice);
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  for (const name of ["offsetHeight", "offsetWidth"] as const)
    Object.defineProperty(HTMLElement.prototype, name, sizes[name]);
  // client* are Element.prototype's; without HTMLElement's stubs they show again.
  for (const name of ["clientHeight", "clientWidth"] as const) Reflect.deleteProperty(HTMLElement.prototype, name);
  window.localStorage.clear();
  FakeUpload.sent = [];
  setSession();
});

const openPhoto = async (user: ReturnType<typeof userEvent.setup>, name: string) =>
  user.click(await screen.findByRole("button", { name: `查看 ${name}` }));
// Files are dropped on the window's main area, from where the drop reaches the window.
const dropArea = () => screen.getByRole("main");
// tileOf is a photo's tile in the grid: its button, its check and its badges.
const tileOf = (name: string) =>
  screen.getByRole("button", { name: `查看 ${name}` }).closest<HTMLElement>("[data-asset-id]")!;
// sourceOf is the badge that names the library a photo of mixed results is from.
const sourceOf = (name: string) => within(tileOf(name)).queryByTitle(/^来自/);
// fullText matches an element by all of its text, which its children may split.
const fullText = (text: string) => (_: string, element: Element | null) => element?.textContent === text;
// resultOrder lists the grid's photos in order, with "divider" where the less
// related ones start.
const resultOrder = () =>
  [...screen.getByRole("main").querySelectorAll("[role=separator], button[aria-label^='查看 ']")].map((element) =>
    element.getAttribute("role") === "separator" ? "divider" : element.getAttribute("aria-label"),
  );

describe("PhotosPanel timeline", () => {
  it("opens the caller's library by month and day, with thumbnails or originals", async () => {
    serve({
      timelines: mine(() => [
        asset("photo:a"),
        asset("photo:b", { thumbnail: "pending", duplicate: "duplicate" }),
        asset("photo:c", { takenAt: undefined }),
      ]),
    });
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);

    expect(await screen.findByRole("heading", { name: "2026年10月" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "2026年5月" })).toBeTruthy();
    expect(screen.getByRole("tab", { name: "我的", selected: true })).toBeTruthy();
    expect(await screen.findByText("3 张照片")).toBeTruthy();
    const a = await screen.findByRole("img", { name: "photo:a.jpg" });
    expect(a.getAttribute("src")).toBe("/api/v1/photos/assets/photo%3Aa/thumbnail");
    expect(screen.getByRole("img", { name: "photo:b.jpg" }).getAttribute("src")).toBe(
      "/api/v1/photos/assets/photo%3Ab/original",
    );
    expect(screen.getByText("重复")).toBeTruthy();
    expect(screen.getByRole("checkbox", { name: /选择.*5月1日.*2 张照片/ })).toBeTruthy();

    // A ready thumbnail that cannot be fetched falls back to the original.
    fireEvent.error(a);
    expect(a.getAttribute("src")).toBe("/api/v1/photos/assets/photo%3Aa/original");
  });

  it("switches a tile to its thumbnail once the thumbnail is ready", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      let ready = false;
      serve(
        { timelines: mine(() => [asset("photo:p", { thumbnail: "pending" })]) },
        photoDetails(() => [asset("photo:p", { thumbnail: ready ? "ready" : "pending" })]),
      );
      render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
      expect((await screen.findByRole("img", { name: "photo:p.jpg" })).getAttribute("src")).toBe(
        "/api/v1/photos/assets/photo%3Ap/original",
      );
      ready = true;
      await vi.advanceTimersByTimeAsync(3000);
      await vi.waitFor(() =>
        expect(screen.getByRole("img", { name: "photo:p.jpg" }).getAttribute("src")).toBe(
          "/api/v1/photos/assets/photo%3Ap/thumbnail",
        ),
      );
    } finally {
      vi.useRealTimers();
    }
  });

  it("uploads dropped photos, shows them at once and reloads the timeline", async () => {
    vi.stubGlobal("XMLHttpRequest", FakeUpload);
    vi.stubGlobal(
      "URL",
      Object.assign(URL, { createObjectURL: () => "blob:preview", revokeObjectURL: () => undefined }),
    );
    let uploaded = false;
    FakeUpload.respond = (upload) => {
      const file = upload.body?.get("file") as File;
      uploaded = true;
      return { status: 201, body: asset("photo:new", { name: file.name }) };
    };
    serve({ timelines: mine(() => (uploaded ? [asset("photo:new", { name: "cat.jpg" })] : [])) });
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    expect(await screen.findByText("把照片拖到这里")).toBeTruthy();

    const files = [
      new File(["jpeg"], "cat.jpg", { type: "image/jpeg" }),
      new File(["text"], "notes.txt", { type: "text/plain" }),
    ];
    fireEvent.dragEnter(dropArea(), { dataTransfer: { types: ["Files"], files } });
    expect(screen.getByText("照片和视频会保存到我的图库")).toBeTruthy();
    fireEvent.drop(dropArea(), { dataTransfer: { types: ["Files"], files } });

    expect(await screen.findByRole("button", { name: "查看 cat.jpg" })).toBeTruthy();
    expect(await screen.findByText("照片已保存到我的图库")).toBeTruthy();
    expect(FakeUpload.sent).toHaveLength(1);
    expect(FakeUpload.sent[0].url).toBe("/api/v1/photos/libraries/library%3Amine/uploads");
    expect(FakeUpload.sent[0].headers["X-CSRF-Token"]).toBe("csrf-photo");
    // The text file never left the browser; the queue says why.
    expect(screen.getByText("1 张未能上传")).toBeTruthy();
    // It never appears in the grid either.
    expect(screen.queryByText("支持 JPEG、PNG、HEIC 照片与 MP4、MOV 等常见视频")).toBeNull();
    await userEvent.setup().click(screen.getByRole("button", { name: "展开上传队列" }));
    expect(screen.getByText("支持 JPEG、PNG、HEIC 照片与 MP4、MOV 等常见视频")).toBeTruthy();
  });
});

describe("PhotosPanel Live Photos", () => {
  it("says when a Live Photo arrived without its video and completes it with the MOV later", async () => {
    vi.stubGlobal("XMLHttpRequest", FakeUpload);
    vi.stubGlobal(
      "URL",
      Object.assign(URL, { createObjectURL: () => "blob:preview", revokeObjectURL: () => undefined }),
    );
    let stored: PhotoAsset[] = [];
    FakeUpload.respond = (upload) => {
      if ((upload.body?.get("file") as File | null)?.name.endsWith(".MOV")) {
        stored = stored.map((photo) => ({
          ...photo,
          liveVideoMissing: undefined,
          motion: { kind: "live-photo", duration: 2.5 },
        }));
        return { status: 200, body: { completed: stored } };
      }
      const still = asset("photo:live", { name: "IMG_1.HEIC", mediaType: "image/heic", liveVideoMissing: true });
      stored = [still];
      return { status: 201, body: still };
    };
    serve({ timelines: mine(() => stored) });
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    expect(await screen.findByText("把照片拖到这里")).toBeTruthy();
    const drop = (file: File) => {
      fireEvent.dragEnter(dropArea(), { dataTransfer: { types: ["Files"], files: [file] } });
      fireEvent.drop(dropArea(), { dataTransfer: { types: ["Files"], files: [file] } });
    };

    // The still alone: it is kept, and the toast says what is missing.
    drop(new File(["heic"], "IMG_1.HEIC", { type: "image/heic" }));
    expect(await screen.findByText("照片已保存到我的图库；其中 1 张实况照片只收到了静态画面")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "怎么补上" }));
    expect(screen.getByText(/导出未修改的原件/)).toBeTruthy();
    await openPhoto(user, "IMG_1.HEIC");
    expect(await screen.findByText("实况照片只上传了静态画面")).toBeTruthy();
    expect(screen.getByText(/HEIC/, { selector: "dd" })).toBeTruthy();
    await user.keyboard("{Escape}");

    // The MOV on its own is uploaded like any file and completes it.
    drop(new File(["mov"], "IMG_1.MOV", { type: "video/quicktime" }));
    expect(await screen.findByText("已为 1 张照片补上实况视频")).toBeTruthy();
    expect(FakeUpload.sent.at(-1)?.url).toBe("/api/v1/photos/libraries/library%3Amine/uploads");
    expect(tileOf("IMG_1.HEIC").contains(await screen.findByText("动态照片"))).toBe(true);
  });

  it("uploads the missing video from the photo's details", async () => {
    vi.stubGlobal("XMLHttpRequest", FakeUpload);
    let complete = false;
    FakeUpload.respond = () => {
      complete = true;
      return { status: 200, body: { items: [asset("photo:live", { motion: { kind: "live-photo", duration: 2 } })] } };
    };
    const still = () =>
      asset("photo:live", complete ? { motion: { kind: "live-photo", duration: 2 } } : { liveVideoMissing: true });
    serve({ timelines: mine(() => [still()]) });
    vi.spyOn(HTMLInputElement.prototype, "click").mockImplementation(function (this: HTMLInputElement) {
      if (this.type !== "file") return;
      Object.defineProperty(this, "files", { value: [new File(["mov"], "IMG_9.MOV", { type: "video/quicktime" })] });
      this.dispatchEvent(new Event("change"));
    });
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    await openPhoto(user, "photo:live.jpg");
    await user.click(await screen.findByRole("button", { name: "上传视频" }));
    expect(await screen.findByText("已补上实况视频")).toBeTruthy();
    expect(FakeUpload.sent[0].url).toBe("/api/v1/photos/libraries/library%3Amine/live-videos");
    expect(await screen.findByRole("button", { name: "播放动态照片" })).toBeTruthy();
    // The details, read again for the completed photo, no longer say so.
    await waitFor(() => expect(screen.queryByText("实况照片只上传了静态画面")).toBeNull());
  });
});

describe("PhotosPanel videos", () => {
  const clip = (id: string, overrides: Partial<PhotoAsset> = {}) =>
    asset(id, {
      name: `${id}.mp4`,
      mediaType: "video/mp4",
      video: { duration: 83.4 },
      width: 1080,
      height: 1920,
      ...overrides,
    });

  it("uploads a video, shows it in the grid with its length and plays it in the viewer", async () => {
    vi.stubGlobal("XMLHttpRequest", FakeUpload);
    vi.stubGlobal(
      "URL",
      Object.assign(URL, { createObjectURL: () => "blob:preview", revokeObjectURL: () => undefined }),
    );
    const play = vi.spyOn(HTMLMediaElement.prototype, "play").mockImplementation(function (this: HTMLMediaElement) {
      this.dispatchEvent(new Event("play"));
      return Promise.resolve();
    });
    vi.spyOn(HTMLMediaElement.prototype, "pause").mockImplementation(function (this: HTMLMediaElement) {
      this.dispatchEvent(new Event("pause"));
    });
    let stored: PhotoAsset[] = [];
    FakeUpload.respond = () => {
      stored = [clip("photo:beach")];
      return { status: 201, body: stored[0] };
    };
    const playback: string[] = [];
    serve(
      { timelines: mine(() => stored) },
      http.get("/api/v1/photos/assets/{assetId}/video/playback", ({ request, response }) => {
        playback.push(pathOf(request));
        return response(200).json({ mode: "direct", duration: 83.4 });
      }),
    );
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    expect(await screen.findByText("把照片拖到这里")).toBeTruthy();
    const files = [new File(["mp4"], "beach.mp4", { type: "video/mp4" })];
    fireEvent.dragEnter(dropArea(), { dataTransfer: { types: ["Files"], files } });
    fireEvent.drop(dropArea(), { dataTransfer: { types: ["Files"], files } });

    expect(await screen.findByText("视频已保存到我的图库")).toBeTruthy();
    expect(FakeUpload.sent[0].url).toBe("/api/v1/photos/libraries/library%3Amine/uploads");
    expect((await screen.findByTitle("视频 · 1:23")).textContent).toBe("1:23");

    await openPhoto(user, "photo:beach.mp4");
    const viewer = await screen.findByRole("dialog", { name: "查看 photo:beach.mp4" });
    const controls = await within(viewer).findByRole("toolbar", { name: "视频控制" });
    const media = viewer.querySelector("video") as HTMLVideoElement;
    await waitFor(() => expect(media.getAttribute("src")).toBe("/api/v1/photos/assets/photo%3Abeach/video/original"));
    expect(playback.some((path) => path.startsWith("/api/v1/photos/assets/photo%3Abeach/video/playback?caps="))).toBe(
      true,
    );
    // A video has nothing alike to find and no zoom.
    expect(within(viewer).queryByRole("button", { name: "找相似" })).toBeNull();
    expect(within(controls).getByText(fullText("0:00 / 1:23"), { selector: "span" })).toBeTruthy();
    fireEvent(media, new Event("loadedmetadata"));
    await waitFor(() => expect(play).toHaveBeenCalled());
    expect(await within(controls).findByRole("button", { name: "暂停" })).toBeTruthy();
    // The space bar pauses the video instead of starting a slideshow.
    await user.keyboard(" ");
    expect(await within(controls).findByRole("button", { name: "播放" })).toBeTruthy();
    expect(screen.queryByRole("dialog", { name: "幻灯片" })).toBeNull();
    const showInfo = screen.queryByRole("button", { name: "显示信息" });
    if (showInfo) await user.click(showInfo);
    expect(await screen.findByText("1 分 23 秒")).toBeTruthy();
    expect(screen.getByText("1080 × 1920 · 1080p")).toBeTruthy();
  });

  it("converts what the browser cannot play and falls back when the original fails", async () => {
    vi.spyOn(HTMLMediaElement.prototype, "play").mockImplementation(() => Promise.resolve());
    serve(
      { timelines: mine(() => [clip("photo:old", { mediaType: "video/quicktime", video: { duration: 20 } })]) },
      http.get("/api/v1/photos/assets/{assetId}/video/playback", ({ response }) =>
        response(200).json({ mode: "direct", duration: 20 }),
      ),
    );
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    await openPhoto(user, "photo:old.mp4");
    const viewer = await screen.findByRole("dialog", { name: "查看 photo:old.mp4" });
    const media = viewer.querySelector("video") as HTMLVideoElement;
    await waitFor(() => expect(media.getAttribute("src")).toBe("/api/v1/photos/assets/photo%3Aold/video/original"));
    // jsdom has no Media Source Extensions: the stream plays as it comes.
    fireEvent(media, new Event("error"));
    await waitFor(() =>
      expect(media.getAttribute("src")).toMatch(
        /^\/api\/v1\/photos\/assets\/photo%3Aold\/video\/stream\?start=0\.000&caps=.*&transcode=1$/,
      ),
    );
    expect(await within(viewer).findByText("转码播放")).toBeTruthy();
    // A second failure is reported, with the original still to download.
    fireEvent(media, new Event("error"));
    expect(await within(viewer).findByText("视频暂时无法播放")).toBeTruthy();
    expect(within(viewer).getByRole("button", { name: "下载原件" })).toBeTruthy();
  });
});

describe("PhotosPanel slideshow", () => {
  it("plays full screen from the viewer and comes back to the photo it ended on", async () => {
    Object.defineProperty(HTMLImageElement.prototype, "decode", { configurable: true, value: () => Promise.resolve() });
    serve({ timelines: mine(() => [asset("photo:a"), asset("photo:b", { takenAt: "2026-05-01T07:00:00Z" })]) });
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    await openPhoto(user, "photo:a.jpg");
    await user.keyboard(" ");
    const show = await screen.findByRole("dialog", { name: "幻灯片" });
    // It covers the whole page, outside the photos window.
    expect(show.parentElement).toBe(document.body);
    expect(screen.getByText(/1 \/ 2/)).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "下一张" }));
    expect(await screen.findByText(/2 \/ 2/)).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "退出幻灯片" }));
    expect(screen.queryByRole("dialog", { name: "幻灯片" })).toBeNull();
    expect(screen.getByRole("dialog", { name: "查看 photo:b.jpg" })).toBeTruthy();
    delete (HTMLImageElement.prototype as { decode?: unknown }).decode;
  });
});

describe("PhotosPanel viewer", () => {
  it("shows details, steps with the arrow keys and closes with Escape", async () => {
    serve({
      timelines: mine(() => [
        asset("photo:a", { alsoKeptBy: ["bob", "carol"] }),
        asset("photo:b", { takenAt: "2026-05-01T07:00:00Z" }),
      ]),
    });
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    await openPhoto(user, "photo:a.jpg");

    const viewer = screen.getByRole("dialog", { name: "查看 photo:a.jpg" });
    expect(await within(viewer).findByText("4000 × 3000 · 12.0 MP")).toBeTruthy();
    expect(within(viewer).getByText("bob、carol 也保存了这张照片。")).toBeTruthy();
    await user.keyboard("{ArrowRight}");
    expect(screen.getByRole("dialog", { name: "查看 photo:b.jpg" })).toBeTruthy();
    await user.keyboard("i");
    expect(screen.queryByText("4000 × 3000 · 12.0 MP")).toBeNull();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("shows a photo another window asks for, such as one the assistant named", async () => {
    serve(
      { timelines: mine(() => [asset("photo:a")]) },
      photoDetails(() => [asset("photo:z", { takenAt: "2025-08-02T09:00:00Z" })]),
    );
    const handled = vi.fn();
    const user = userEvent.setup();
    render(
      <PhotosPanel
        userId="user:alice"
        userName="alice"
        isAdmin={false}
        intent={{ assetId: "photo:z" }}
        onIntentHandled={handled}
      />,
    );
    expect(await screen.findByRole("dialog", { name: "查看 photo:z.jpg" })).toBeTruthy();
    expect(handled).toHaveBeenCalled();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("says so when a photo another window asks for cannot be read", async () => {
    serve(
      { timelines: mine(() => [asset("photo:a")]) },
      http.get("/api/v1/photos/assets/{assetId}", ({ params, response }) =>
        params.assetId === "photo:gone" ? response(404).json(apiError("not_found", "asset not found")) : undefined,
      ),
    );
    const handled = vi.fn();
    render(
      <PhotosPanel
        userId="user:alice"
        userName="alice"
        isAdmin={false}
        intent={{ assetId: "photo:gone" }}
        onIntentHandled={handled}
      />,
    );
    expect(await screen.findByText("照片或相册已不存在")).toBeTruthy();
    expect(handled).toHaveBeenCalled();
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(await screen.findByRole("button", { name: "查看 photo:a.jpg" })).toBeTruthy();
  });

  it("moves a photo to the trash and undoes it", async () => {
    let trashed = false;
    const restored: string[] = [];
    serve(
      { timelines: mine(() => (trashed ? [] : [asset("photo:a")])) },
      http.delete("/api/v1/photos/assets/{assetId}", ({ params, response }) => {
        trashed = true;
        return response(200).json(asset(params.assetId));
      }),
      http.post("/api/v1/photos/assets/{assetId}/restore", ({ params, response }) => {
        trashed = false;
        restored.push(params.assetId);
        return response(200).json(asset(params.assetId));
      }),
    );
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    await openPhoto(user, "photo:a.jpg");
    await user.click(screen.getByRole("button", { name: "移到回收站" }));

    expect(await screen.findByText("已移到回收站：1 张")).toBeTruthy();
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.queryByRole("button", { name: "查看 photo:a.jpg" })).toBeNull();
    await user.click(screen.getByRole("button", { name: "撤销" }));
    expect(restored).toEqual(["photo:a"]);
    expect(await screen.findByRole("button", { name: "查看 photo:a.jpg" })).toBeTruthy();
  });

  it("offers changes to a shared photo only to its uploader or an administrator, and copies for anyone", async () => {
    const shared = asset("photo:s", { libraryId: sharedLibrary.id, uploadedBy: "user:bob" });
    const copies: unknown[] = [];
    serve(
      { timelines: { [sharedLibrary.id]: () => [shared] } },
      http.post("/api/v1/photos/assets/{assetId}/copies", async ({ request, response }) => {
        copies.push(await request.json());
        return response(201).json(asset("photo:mine"));
      }),
    );
    const user = userEvent.setup();
    const { unmount } = render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    await user.click(await screen.findByRole("tab", { name: "共享" }));
    await openPhoto(user, "photo:s.jpg");
    expect(screen.queryByRole("button", { name: "移到回收站" })).toBeNull();
    expect(screen.queryByRole("button", { name: "重命名照片" })).toBeNull();
    await user.click(await screen.findByRole("button", { name: "复制到我的图库" }));
    expect(await screen.findByText("已复制 1 张到我的图库")).toBeTruthy();
    expect(copies).toEqual([{ libraryId: privateLibrary.id }]);
    unmount();

    render(<PhotosPanel userId="user:admin" userName="alice" isAdmin />);
    await user.click(await screen.findByRole("tab", { name: "共享" }));
    await openPhoto(user, "photo:s.jpg");
    expect(screen.getByRole("button", { name: "移到回收站" })).toBeTruthy();
  });

  it("tags a photo and opens the group of its tag", async () => {
    let details = asset("photo:cat");
    const groups: string[] = [];
    serve(
      { timelines: mine(() => [asset("photo:cat")]) },
      photoDetails(() => [details]),
      http.post("/api/v1/photos/assets/{assetId}/tags", async ({ request, response }) => {
        details = { ...details, tags: [(await request.json()).name] };
        return response(200).json(details);
      }),
      http.get("/api/v1/photos/tags/{name}/assets", ({ request, response }) => {
        groups.push(pathOf(request));
        return response(200).json({ items: [asset("photo:cat"), asset("photo:kitten")] });
      }),
    );
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    await openPhoto(user, "photo:cat.jpg");

    await user.type(await screen.findByRole("textbox", { name: "添加标签" }), "小橘{Enter}");
    expect(await within(screen.getByLabelText("标签")).findByRole("button", { name: "小橘" })).toBeTruthy();
    // Photo details never list AI labels, only the user's tags.
    expect(screen.queryByLabelText("AI 标签")).toBeNull();

    await user.click(within(screen.getByLabelText("标签")).getByRole("button", { name: "小橘" }));
    expect(groups).toContain("/api/v1/photos/tags/%E5%B0%8F%E6%A9%98/assets?limit=120");
    expect(await screen.findByRole("button", { name: "查看 photo:kitten.jpg" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "小橘" })).toBeTruthy();
    expect(screen.getByText("2 张 · 你的标签")).toBeTruthy();
  });
});

describe("PhotosPanel selection", () => {
  const photos = () => [
    asset("photo:1", { takenAt: "2026-05-03T08:00:00Z" }),
    asset("photo:2", { takenAt: "2026-05-02T08:00:00Z" }),
    asset("photo:3"),
    asset("photo:4", { takenAt: "2026-04-01T08:00:00Z" }),
  ];

  it("selects with the check, ranges with Shift, all with Ctrl+A and clears with Escape", async () => {
    serve({ timelines: mine(photos) });
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    await user.click(await screen.findByRole("checkbox", { name: "选择 photo:1.jpg" }));
    expect(screen.getByRole("toolbar", { name: "所选照片的操作" }).textContent).toContain("已选 1 张");
    // While selecting, a click on a photo selects it instead of opening it.
    await user.keyboard("{Shift>}");
    await user.click(screen.getByRole("button", { name: "查看 photo:3.jpg" }));
    await user.keyboard("{/Shift}");
    expect(screen.getByText("已选 3 张")).toBeTruthy();
    expect(screen.queryByRole("dialog")).toBeNull();
    await user.click(await screen.findByRole("button", { name: "查看 photo:4.jpg" }));
    expect(screen.getByText("已选 4 张")).toBeTruthy();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("toolbar", { name: "所选照片的操作" })).toBeNull();
    screen.getByRole("button", { name: "查看 photo:1.jpg" }).focus();
    await user.keyboard("{Control>}a{/Control}");
    expect(screen.getByText("已选 4 张")).toBeTruthy();
  });

  it("selects a whole day from its header and moves it to the trash", async () => {
    const trashed = new Set<string>();
    const deleted: string[] = [];
    serve(
      { timelines: mine(() => photos().filter((photo) => !trashed.has(photo.id))) },
      http.delete("/api/v1/photos/assets/{assetId}", ({ params, request, response }) => {
        trashed.add(params.assetId);
        deleted.push(pathOf(request));
        return response(200).json(asset(params.assetId));
      }),
    );
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    await user.click(await screen.findByRole("checkbox", { name: /选择5月1日.*的 1 张照片/ }));
    await user.click(screen.getByRole("checkbox", { name: "选择 photo:2.jpg" }));
    await user.click(screen.getByRole("button", { name: "删除" }));

    expect(await screen.findByText("已移到回收站：2 张")).toBeTruthy();
    expect(deleted.sort()).toEqual(["/api/v1/photos/assets/photo%3A2", "/api/v1/photos/assets/photo%3A3"]);
    expect(screen.queryByRole("button", { name: "查看 photo:3.jpg" })).toBeNull();
    expect(screen.getByRole("button", { name: "查看 photo:1.jpg" })).toBeTruthy();
  });

  it("adds the selection to an album, creating one on the way", async () => {
    const added: Array<{ albumId: string; assetId: string }> = [];
    serve(
      { timelines: mine(photos), albums: { [privateLibrary.id]: () => [trip] } },
      http.post("/api/v1/photos/libraries/{libraryId}/albums", async ({ request, response }) =>
        response(201).json({ ...trip, id: "album:new", name: (await request.json()).name, photos: 0 }),
      ),
      http.post("/api/v1/photos/albums/{albumId}/assets", async ({ params, request, response }) => {
        added.push({ albumId: params.albumId, assetId: (await request.json()).assetId });
        return response(200).json(asset("photo:1"));
      }),
    );
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    await user.click(await screen.findByRole("checkbox", { name: "选择 photo:1.jpg" }));
    await user.click(screen.getByRole("checkbox", { name: "选择 photo:2.jpg" }));
    await user.click(screen.getByRole("button", { name: "加入相册" }));
    const picker = await screen.findByRole("dialog", { name: "加入相册" });
    expect(within(picker).getByText("共享图库")).toBeTruthy();
    expect(within(picker).getByText("照片会先复制到共享图库")).toBeTruthy();
    await user.click(within(picker).getByRole("button", { name: /旅行/ }));

    expect(await screen.findByText("已加入相册“旅行”")).toBeTruthy();
    expect(
      added
        .filter(({ albumId }) => albumId === trip.id)
        .map(({ assetId }) => assetId)
        .sort(),
    ).toEqual(["photo:1", "photo:2"]);

    await user.click(screen.getByRole("checkbox", { name: "选择 photo:4.jpg" }));
    await user.click(screen.getByRole("button", { name: "加入相册" }));
    await user.click(
      within(await screen.findByRole("dialog", { name: "加入相册" })).getByRole("button", { name: "新建相册" }),
    );
    await user.type(screen.getByRole("textbox", { name: "新相册名称" }), "春天{Enter}");
    expect(await screen.findByText("已加入相册“春天”")).toBeTruthy();
    expect(added.map(({ albumId }) => albumId)).toContain("album:new");
  });
});

describe("PhotosPanel sidebar albums", () => {
  it("keeps the sidebar's albums on screen while it lists them again", async () => {
    let albums = [trip];
    // While held, listing the albums waits until the test releases it.
    let held: Promise<void> | undefined;
    let release = () => undefined as void;
    serve(
      { timelines: mine(() => [asset("photo:a")]) },
      http.get("/api/v1/photos/libraries/{libraryId}/albums", async ({ params, response }) => {
        if (params.libraryId !== privateLibrary.id) return response(200).json({ items: [] });
        await held;
        return response(200).json({ items: albums });
      }),
      http.post("/api/v1/photos/albums/{albumId}/assets", ({ response }) => response(200).json(asset("photo:a"))),
    );
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    const nav = await screen.findByRole("navigation", { name: "相册导航" });
    expect(await within(nav).findByRole("button", { name: /旅行/ })).toBeTruthy();
    await user.click(await screen.findByRole("checkbox", { name: "选择 photo:a.jpg" }));
    await user.click(screen.getByRole("button", { name: "加入相册" }));
    const picker = await screen.findByRole("dialog", { name: "加入相册" });
    // Adding to the album lists the albums again; that list does not answer yet.
    held = new Promise((resolve) => {
      release = resolve;
    });
    await user.click(within(picker).getByRole("button", { name: /旅行/ }));
    expect(await screen.findByText("已加入相册“旅行”")).toBeTruthy();
    expect(within(nav).getByRole("button", { name: /旅行/ })).toBeTruthy();
    albums = [{ ...trip, name: "旅行（更新）" }];
    release();
    expect(await within(nav).findByRole("button", { name: /旅行（更新）/ })).toBeTruthy();
  });
});

describe("PhotosPanel albums, trash and AI 聚合", () => {
  it("lists albums, opens one, renames it and takes a photo out of it", async () => {
    let removed = false;
    const removals: string[] = [];
    serve(
      { albums: { [privateLibrary.id]: () => [trip] } },
      http.get("/api/v1/photos/albums/{albumId}/assets", ({ response }) =>
        response(200).json({ items: removed ? [] : [asset("photo:a")] }),
      ),
      http.patch("/api/v1/photos/albums/{albumId}", async ({ request, response }) =>
        response(200).json({ ...trip, name: (await request.json()).name }),
      ),
      http.delete("/api/v1/photos/albums/{albumId}/assets/{assetId}", ({ request, response }) => {
        removed = true;
        removals.push(pathOf(request));
        return response(204).empty();
      }),
    );
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    await user.click(await screen.findByRole("button", { name: /^相册/ }));
    await user.click(await screen.findByRole("button", { name: "打开相册 旅行" }));
    expect(await screen.findByRole("heading", { name: "旅行" })).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "重命名相册" }));
    const dialog = screen.getByRole("dialog", { name: "重命名相册" });
    const name = within(dialog).getByRole("textbox");
    await user.clear(name);
    await user.type(name, "海边{Enter}");
    expect(await screen.findByRole("heading", { name: "海边" })).toBeTruthy();

    await user.click(await screen.findByRole("checkbox", { name: "选择 photo:a.jpg" }));
    await user.click(screen.getByRole("button", { name: "移出相册" }));
    expect(removals).toEqual(["/api/v1/photos/albums/album%3Atrip/assets/photo%3Aa"]);
    expect(await screen.findByText(/照片仍在图库中/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "查看 photo:a.jpg" })).toBeNull();
  });

  it("restores from the trash and asks in the window before deleting for good", async () => {
    const purgeAfter = new Date(Date.now() + 3 * 86_400_000).toISOString();
    let items = [
      asset("photo:t1", { trash: { trashedAt: "2026-10-08T09:00:00Z", trashedBy: "user:alice", purgeAfter } }),
      asset("photo:t2", { trash: { trashedAt: "2026-10-08T09:00:00Z", trashedBy: "user:alice", purgeAfter } }),
    ];
    const restored: string[] = [];
    const purged: string[] = [];
    serve(
      { trash: { [privateLibrary.id]: () => items } },
      http.post("/api/v1/photos/assets/{assetId}/restore", ({ params, response }) => {
        items = items.filter((photo) => photo.id !== params.assetId);
        restored.push(params.assetId);
        return response(200).json(asset(params.assetId));
      }),
      http.delete("/api/v1/photos/trash/{assetId}", ({ params, response }) => {
        items = items.filter((photo) => photo.id !== params.assetId);
        purged.push(params.assetId);
        return response(204).empty();
      }),
    );
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    await user.click(await screen.findByRole("button", { name: "回收站" }));
    expect((await screen.findAllByText("3 天后删除")).length).toBe(2);

    // In the trash a click selects.
    await user.click(screen.getByRole("button", { name: "查看 photo:t1.jpg" }));
    await user.click(screen.getByRole("button", { name: "恢复" }));
    expect(restored).toEqual(["photo:t1"]);
    expect(await screen.findByText("已恢复 1 张照片")).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "查看 photo:t2.jpg" }));
    await user.click(screen.getByRole("button", { name: "永久删除" }));
    const dialog = screen.getByRole("dialog", { name: "永久删除 1 张照片？" });
    await user.click(within(dialog).getByRole("button", { name: "取消" }));
    expect(purged).toEqual([]);
    await user.click(screen.getByRole("button", { name: "永久删除" }));
    await user.click(
      within(screen.getByRole("dialog", { name: "永久删除 1 张照片？" })).getByRole("button", { name: "永久删除" }),
    );
    expect(await screen.findByText("回收站是空的")).toBeTruthy();
  });

  it("opens AI 聚合 with searches and clusters, and takes a wrong photo out of a cluster", async () => {
    const cat = { id: "cat", name: "猫", category: "animal", photos: 2, coverId: "photo:cat" };
    let hidden = false;
    let untagged = false;
    const searches: string[] = [];
    const clusters: string[] = [];
    const removed: string[] = [];
    serve(
      {
        ai: () => ({ state: "working", ready: 3, pending: 2, failed: 1 }),
        labels: () => ({
          items: [cat, { id: "sea", name: "海", category: "nature", photos: 1, coverId: "photo:sea" }],
          ready: true,
        }),
        tags: () => [{ name: "小橘", photos: 2, coverId: "photo:orange" }],
      },
      http.get("/api/v1/photos/tags/{name}/assets", ({ response }) =>
        response(200).json({
          items: untagged ? [asset("photo:orange")] : [asset("photo:orange"), asset("photo:ginger")],
        }),
      ),
      http.delete("/api/v1/photos/assets/{assetId}/tags/{name}", ({ params, request, response }) => {
        untagged = true;
        removed.push(pathOf(request));
        return response(200).json(asset(params.assetId));
      }),
      http.get("/api/v1/photos/labels/{labelId}/assets", ({ request, response }) => {
        clusters.push(pathOf(request));
        return response(200).json({ items: hidden ? [asset("photo:cat")] : [asset("photo:cat"), asset("photo:fox")] });
      }),
      http.delete("/api/v1/photos/assets/{assetId}/ai-labels/{labelId}", ({ params, request, response }) => {
        hidden = true;
        removed.push(pathOf(request));
        return response(200).json(asset(params.assetId));
      }),
      http.get("/api/v1/photos/search", ({ request, response }) => {
        searches.push(pathOf(request));
        return response(200).json({ items: [asset("photo:sea")], semantic: true, closest: 1 });
      }),
    );
    window.localStorage.setItem("a-nas.photos.recentSearches", JSON.stringify(["海边的日落"]));
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    expect(await screen.findByText("正在整理 3 / 6")).toBeTruthy();
    expect(screen.getByText(/1 张无法识别/)).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "AI 聚合" }));
    expect(await screen.findByRole("heading", { name: "alice，您可以用一句话来找照片" })).toBeTruthy();
    expect(within(screen.getByLabelText("最近搜索")).getByRole("button", { name: "海边的日落" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: /^动物/ })).toBeTruthy();
    expect(within(await screen.findByRole("button", { name: "打开 猫" })).getByText("2 张")).toBeTruthy();
    expect(screen.getByRole("button", { name: "打开 海" })).toBeTruthy();

    // A search stays a search: it never goes through the clusters.
    await user.type(screen.getByRole("searchbox", { name: "用一句话搜索照片" }), "猫{Enter}");
    expect(searches).toContain("/api/v1/photos/search?q=%E7%8C%AB&limit=120");
    expect(clusters).toEqual([]);
    await user.click(await screen.findByRole("button", { name: "返回" }));

    // A cluster opens on its own; a wrong photo can be taken out of it.
    await user.click(await screen.findByRole("button", { name: "打开 猫" }));
    expect(await screen.findByRole("heading", { name: "猫" })).toBeTruthy();
    expect(clusters).toContain("/api/v1/photos/labels/cat/assets?limit=120");
    await user.click(await screen.findByRole("checkbox", { name: "选择 photo:fox.jpg" }));
    await user.click(screen.getByRole("button", { name: "不是猫" }));
    expect(removed).toEqual(["/api/v1/photos/assets/photo%3Afox/ai-labels/cat"]);
    await waitFor(() => expect(screen.queryByRole("button", { name: "查看 photo:fox.jpg" })).toBeNull());
    expect(await screen.findByText("已从“猫”移出 1 张，照片仍在图库中")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "返回" }));
    expect(await screen.findByRole("heading", { name: "alice，您可以用一句话来找照片" })).toBeTruthy();

    // The users' own tags are groups too; a tag can be taken off from there.
    expect(within(screen.getByLabelText("你的标签")).getByText("2 张")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "打开标签 小橘" }));
    expect(await screen.findByRole("heading", { name: "小橘" })).toBeTruthy();
    await user.click(await screen.findByRole("checkbox", { name: "选择 photo:ginger.jpg" }));
    await user.click(screen.getByRole("button", { name: "去掉标签" }));
    expect(removed.at(-1)).toBe("/api/v1/photos/assets/photo%3Aginger/tags/%E5%B0%8F%E6%A9%98");
    await waitFor(() => expect(screen.queryByRole("button", { name: "查看 photo:ginger.jpg" })).toBeNull());
    expect(await screen.findByText("已从 1 张照片上去掉“小橘”")).toBeTruthy();
  });

  it("shows clusters being prepared and lists them once local AI has settled", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      // The label texts first, then the photos, then nothing waits.
      let phase = 0;
      const labels = vi.fn(() =>
        phase === 2
          ? { items: [{ id: "cat", name: "猫", category: "animal", photos: 2, coverId: "photo:cat" }], ready: true }
          : { items: [], ready: phase === 1 },
      );
      serve({
        ai: () => ({
          state: phase === 2 ? "idle" : "working",
          ready: 3,
          pending: phase === 2 ? 0 : 2,
          failed: 0,
          clusters: phase ? "ready" : "preparing",
        }),
        labels,
      });
      const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
      render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
      await user.click(await screen.findByRole("button", { name: "AI 聚合" }));
      const browse = await screen.findByRole("region", { name: "按内容聚合" });
      // Placeholder cards under the bare heading say the groups are coming;
      // they are only drawn, hidden from assistive technology.
      const coming = () => screen.getByRole("region", { name: "按内容聚合" }).getAttribute("aria-busy") === "true";
      expect(browse.querySelectorAll("[aria-hidden='true'] > span").length).toBeGreaterThan(0);
      expect(within(browse).getByRole("banner").textContent).toBe("按内容聚合");
      expect(screen.queryByRole("region", { name: "可以这样搜" })).toBeNull();

      // Waiting polls only the AI status: any other request would hold the
      // AI work back.
      await vi.advanceTimersByTimeAsync(15_000);
      expect(labels).toHaveBeenCalledTimes(1);
      // Groups ready, photos still waiting: still coming, and not asked for
      // yet, which would hold the photos back.
      phase = 1;
      await vi.advanceTimersByTimeAsync(15_000);
      expect(labels).toHaveBeenCalledTimes(1);
      expect(coming()).toBe(true);
      phase = 2;
      await vi.advanceTimersByTimeAsync(15_000);
      expect(await screen.findByRole("button", { name: "打开 猫" })).toBeTruthy();
      await vi.advanceTimersByTimeAsync(15_000);
      expect(labels).toHaveBeenCalledTimes(2);
    } finally {
      vi.useRealTimers();
    }
  });

  it("lists the clusters above the user's tags and comes back from a group to where it was", async () => {
    const animals = ["猫", "狗", "鸟", "马", "羊", "牛", "熊", "斑马"].map((name, index) => ({
      id: `animal-${index}`,
      name,
      category: "animal",
      photos: 8 - index,
      coverId: `photo:a${index}`,
    }));
    const opened: string[] = [];
    serve(
      {
        ai: () => ({ state: "idle", ready: 8, pending: 0, failed: 0, clusters: "ready" }),
        labels: () => ({ items: animals, ready: true }),
        tags: () => [{ name: "小橘", photos: 2, coverId: "photo:orange" }],
      },
      http.get("/api/v1/photos/labels/{labelId}/assets", ({ params, response }) => {
        opened.push(params.labelId);
        return response(200).json({ items: params.labelId === "animal-7" ? [asset("photo:zebra")] : [] });
      }),
    );
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    await user.click(await screen.findByRole("button", { name: "AI 聚合" }));
    const clusters = await screen.findByRole("region", { name: "按内容聚合" });
    const tags = await screen.findByRole("region", { name: "你的标签" });
    expect(clusters.compareDocumentPosition(tags) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    // The headings stand alone.
    expect(within(clusters).getByRole("banner").textContent).toBe("按内容聚合");
    expect(within(tags).getByRole("banner").textContent).toBe("你的标签");

    await user.click(screen.getByRole("button", { name: "展开全部 8 类" }));
    // The page of AI 聚合 scrolls as a whole.
    const page = () => scrollerOf(screen.getByRole("region", { name: "按内容聚合" }));
    page().scrollTop = 420;
    await user.click(screen.getByRole("button", { name: "打开 斑马" }));
    expect(await screen.findByRole("button", { name: "查看 photo:zebra.jpg" })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "返回" }));
    expect(await screen.findByRole("button", { name: "收起" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "打开 斑马" })).toBeTruthy();
    expect(page().scrollTop).toBe(420);
    expect(document.activeElement).not.toBe(screen.getByRole("searchbox", { name: "用一句话搜索照片" }));

    // Opened from the sidebar, AI 聚合 starts from the top again.
    await user.click(screen.getByRole("button", { name: "图库" }));
    await user.click(screen.getByRole("button", { name: "AI 聚合" }));
    expect(await screen.findByRole("button", { name: "展开全部 8 类" })).toBeTruthy();
    expect(page().scrollTop).toBe(0);
    expect(opened).toEqual(["animal-7"]);
  });

  it("leaves 按内容聚合 out when local AI is sure of no photos", async () => {
    const labels = vi.fn(() => ({ items: [], ready: true }));
    serve({
      ai: () => ({ state: "idle", ready: 3, pending: 0, failed: 0, clusters: "ready" }),
      labels,
      tags: () => [{ name: "小橘", photos: 2, coverId: "photo:orange" }],
    });
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    await user.click(await screen.findByRole("button", { name: "AI 聚合" }));
    expect(await screen.findByRole("region", { name: "你的标签" })).toBeTruthy();
    await waitFor(() => expect(labels).toHaveBeenCalled());
    expect(screen.queryByRole("region", { name: "按内容聚合" })).toBeNull();
  });

  it("leaves the clusters out while local AI is away, and never says AI is off", async () => {
    const labels = vi.fn(() => ({ items: [], ready: false }));
    serve({ ai: () => ({ state: "unavailable", ready: 0, pending: 5, failed: 0, clusters: "preparing" }), labels });
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    await user.click(await screen.findByRole("button", { name: "AI 聚合" }));
    expect(await screen.findByRole("heading", { name: "alice，您可以用一句话来找照片" })).toBeTruthy();
    await waitFor(() => expect(labels).toHaveBeenCalled());
    expect(screen.queryByRole("region", { name: "按内容聚合" })).toBeNull();
    expect(screen.queryByRole("region", { name: "可以这样搜" })).toBeNull();
    expect(screen.queryByRole("status", { name: "本地 AI 状态" })).toBeNull();
    expect(screen.queryByText(/未启用|不可用/)).toBeNull();
  });
});

describe("PhotosPanel people and pets", () => {
  const ming = person("person:ming", { name: "小明", photos: 3, coverId: "face:ming" });
  const nameless = person("person:new", { photos: 2, coverId: "face:new" });
  const dog = person("person:dog", {
    libraryId: sharedLibrary.id,
    kind: "dog",
    hidden: true,
    photos: 4,
    coverId: "face:dog",
  });
  const cat = person("person:cat", { kind: "cat", name: "咪咪", photos: 5, coverId: "face:cat" });

  it("lists people and pets on AI 聚合, names a person and takes a wrong photo out", async () => {
    let named = "";
    let removed = false;
    const changes: unknown[] = [];
    const opened: string[] = [];
    const removals: string[] = [];
    serve(
      { people: () => [ming, cat, { ...nameless, name: named }, dog] },
      http.patch("/api/v1/photos/people/{personId}", async ({ request, response }) => {
        const change = await request.json();
        changes.push(change);
        named = change.name ?? named;
        return response(200).json({ ...nameless, name: named });
      }),
      http.get("/api/v1/photos/people/{personId}/assets", ({ request, response }) => {
        opened.push(pathOf(request));
        return response(200).json({
          items: removed ? [asset("photo:grandma")] : [asset("photo:grandma"), asset("photo:aunt")],
        });
      }),
      http.delete("/api/v1/photos/people/{personId}/assets/{assetId}", ({ request, response }) => {
        removed = true;
        removals.push(pathOf(request));
        return response(204).empty();
      }),
    );
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    await user.click(await screen.findByRole("button", { name: "AI 聚合" }));
    const row = await screen.findByLabelText("人物与宠物");
    // Named people and pets first, cats as one by one as dogs; a nameless one
    // invites a name; hidden ones wait.
    const cards = within(row).getAllByRole("button", { name: /^打开 / });
    expect(cards.map((card) => card.getAttribute("aria-label"))).toEqual(["打开 小明", "打开 咪咪", "打开 未命名"]);
    expect(within(row).getByText("添加名字")).toBeTruthy();
    expect(screen.getByText(/照片和人脸都在这台 NAS 上由本地 AI 识别/)).toBeTruthy();
    await user.click(within(row).getByRole("button", { name: "已隐藏 1 位" }));
    expect(within(row).getByRole("button", { name: "打开 未命名的狗" })).toBeTruthy();

    await user.click(within(row).getByRole("button", { name: "打开 未命名" }));
    expect(await screen.findByRole("heading", { name: "未命名" })).toBeTruthy();
    expect(opened).toContain("/api/v1/photos/people/person%3Anew/assets?limit=120");
    await user.click(screen.getByRole("button", { name: "添加名字" }));
    await user.type(await screen.findByRole("textbox", { name: "名字" }), "奶奶");
    await user.click(screen.getByRole("button", { name: "保存" }));
    expect(await screen.findByRole("heading", { name: "奶奶" })).toBeTruthy();
    expect(changes).toEqual([{ name: "奶奶" }]);
    expect(await screen.findByText("搜索“奶奶”就能找到 TA 的照片")).toBeTruthy();

    await user.click(await screen.findByRole("checkbox", { name: "选择 photo:aunt.jpg" }));
    await user.click(screen.getByRole("button", { name: "不是奶奶" }));
    expect(removals).toEqual(["/api/v1/photos/people/person%3Anew/assets/photo%3Aaunt"]);
    await waitFor(() => expect(screen.queryByRole("button", { name: "查看 photo:aunt.jpg" })).toBeNull());
    expect(await screen.findByText("已从“奶奶”移出 1 张，照片仍在图库中")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "返回" }));
    expect(await screen.findByLabelText("人物与宠物")).toBeTruthy();
  });

  it("names a face in a photo's details by choosing someone already named", async () => {
    let details: PhotoAsset = { ...asset("photo:party"), people: [{ occurrenceId: "face:1", kind: "face" }] };
    const assigned: Array<{ path: string; body: unknown }> = [];
    const opened: string[] = [];
    serve(
      { timelines: mine(() => [asset("photo:party")]), people: () => [ming, nameless] },
      photoDetails(() => [details]),
      http.put("/api/v1/photos/assets/{assetId}/people/{occurrenceId}", async ({ request, response }) => {
        assigned.push({ path: pathOf(request), body: await request.json() });
        details = { ...details, people: [{ occurrenceId: "face:1", kind: "face", personId: ming.id, name: "小明" }] };
        return response(200).json(details);
      }),
      http.get("/api/v1/photos/people/{personId}/assets", ({ request, response }) => {
        opened.push(pathOf(request));
        return response(200).json({ items: [asset("photo:party")] });
      }),
    );
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    await openPhoto(user, "photo:party.jpg");
    const faces = await screen.findByLabelText("照片中的人物");
    await user.click(within(faces).getByRole("button", { name: "这是谁？" }));
    await user.type(within(faces).getByRole("textbox", { name: "名字" }), "小");
    await user.click(await within(faces).findByRole("option", { name: /小明/ }));
    expect(assigned).toEqual([
      { path: "/api/v1/photos/assets/photo%3Aparty/people/face%3A1", body: { personId: "person:ming" } },
    ]);
    await user.click(await within(faces).findByRole("button", { name: "小明" }));
    expect(await screen.findByRole("heading", { name: "小明" })).toBeTruthy();
    expect(opened).toContain("/api/v1/photos/people/person%3Aming/assets?limit=120");
  });

  it("turns people and pets off for the caller's own library after asking, and leaves the shared one to administrators", async () => {
    let own = { ...privateLibrary, people: true, canSetPeople: true };
    const changes: Array<{ path: string; body: unknown }> = [];
    serve(
      {
        libraries: () => [{ ...sharedLibrary, people: true, canSetPeople: false }, own],
        people: () => (own.people ? [ming, nameless] : []),
      },
      http.patch("/api/v1/photos/libraries/{libraryId}", async ({ request, response }) => {
        const change = await request.json();
        changes.push({ path: pathOf(request), body: change });
        own = { ...own, people: change.people ?? own.people };
        return response(200).json(own);
      }),
    );
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    await user.click(await screen.findByRole("button", { name: "AI 聚合" }));
    expect(await screen.findByLabelText("人物与宠物")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "人物与宠物设置" }));
    const settings = await screen.findByRole("dialog", { name: "人物与宠物设置" });
    const switchOwn = within(settings).getByRole("switch", { name: "我的图库的人物与宠物" });
    expect(switchOwn.getAttribute("aria-checked")).toBe("true");
    expect((within(settings).getByRole("switch", { name: "共享图库的人物与宠物" }) as HTMLButtonElement).disabled).toBe(
      true,
    );
    expect(within(settings).getByText("已开启 · 由管理员设置")).toBeTruthy();

    // Off clears what was recognised, so it asks first.
    await user.click(switchOwn);
    const ask = await screen.findByRole("dialog", { name: "关闭我的图库的人物与宠物？" });
    await user.click(within(ask).getByRole("button", { name: "关闭并删除" }));
    expect(changes).toEqual([{ path: "/api/v1/photos/libraries/library%3Amine", body: { people: false } }]);
    expect(await screen.findByText("已关闭我的图库的人物与宠物")).toBeTruthy();
    await waitFor(() =>
      expect(screen.getByRole("switch", { name: "我的图库的人物与宠物" }).getAttribute("aria-checked")).toBe("false"),
    );
    await user.click(
      within(screen.getByRole("dialog", { name: "人物与宠物设置" })).getByRole("button", { name: "关闭" }),
    );
    await waitFor(() => expect(screen.queryByLabelText("人物与宠物")).toBeNull());
  });
});

describe("PhotosPanel search and viewing", () => {
  it("reports a search it cannot even send instead of failing the window", async () => {
    serve({ timelines: mine(() => [asset("photo:home")]) });
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    await screen.findByRole("button", { name: "查看 photo:home.jpg" });
    // A lone surrogate cannot be put in a URL, so building the request throws.
    const box = screen.getByRole("searchbox", { name: "搜索照片" });
    fireEvent.change(box, { target: { value: "猫\uD800" } });
    fireEvent.submit(box.closest("form")!);
    expect(await screen.findByText("请求失败，请稍后重试")).toBeTruthy();
    expect(screen.queryByText("URI malformed")).toBeNull();
    expect(screen.getByRole("searchbox", { name: "搜索照片" })).toBeTruthy();
  });

  it("searches from any page into AI search and divides the closest results from the less related", async () => {
    const sea = asset("photo:sea", { libraryId: sharedLibrary.id, uploadedBy: "user:bob" });
    const results: Record<string, PhotoSearchResults> = {
      海边的猫: { items: [sea], semantic: true, closest: 1 },
      电动车: {
        items: [asset("photo:bike"), asset("photo:scooter"), asset("photo:garden")],
        semantic: true,
        closest: 2,
      },
      黄昏: { items: [], semantic: false, closest: 0 },
    };
    const searches: string[] = [];
    serve(
      { timelines: { ...mine(() => [asset("photo:home")]), [sharedLibrary.id]: () => [sea] } },
      http.get("/api/v1/photos/search", ({ query, request, response }) => {
        searches.push(pathOf(request));
        return response(200).json(results[query.get("q") ?? ""]);
      }),
    );
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    await screen.findByRole("button", { name: "查看 photo:home.jpg" });
    await user.type(screen.getByRole("searchbox", { name: "搜索照片" }), "海边的猫{Enter}");

    expect(searches).toContain("/api/v1/photos/search?q=%E6%B5%B7%E8%BE%B9%E7%9A%84%E7%8C%AB&limit=120");
    expect(await screen.findByRole("button", { name: "查看 photo:sea.jpg" })).toBeTruthy();
    expect(screen.queryByRole("separator", { name: "相关度较低" })).toBeNull();
    await openPhoto(user, "photo:sea.jpg");
    expect(screen.queryByRole("button", { name: "移到回收站" })).toBeNull();
    await user.keyboard("{Escape}");

    const box = screen.getByRole("searchbox", { name: "搜索照片" });
    await user.clear(box);
    await user.type(box, "电动车{Enter}");
    expect(await screen.findByRole("separator", { name: "相关度较低" })).toBeTruthy();
    expect(screen.getByText("以下照片与搜索的内容差距较大，可能并不相关")).toBeTruthy();
    expect(screen.getByText("最接近 2 张 · 共 3 张 · 我的图库和共享图库")).toBeTruthy();
    // The less related photos come after the divider, in the order given.
    expect(resultOrder()).toEqual([
      "查看 photo:bike.jpg",
      "查看 photo:scooter.jpg",
      "divider",
      "查看 photo:garden.jpg",
    ]);
    await user.clear(screen.getByRole("searchbox", { name: "搜索照片" }));
    await user.type(screen.getByRole("searchbox", { name: "搜索照片" }), "黄昏{Enter}");
    expect(await screen.findByText("没有找到相关照片")).toBeTruthy();
    expect(screen.queryByText(/不可用/)).toBeNull();

    await user.click(screen.getByRole("button", { name: "返回" }));
    expect(await screen.findByRole("heading", { name: "alice，您可以用一句话来找照片" })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: /^图库/ }));
    expect(await screen.findByRole("button", { name: "查看 photo:home.jpg" })).toBeTruthy();
    expect((screen.getByRole("searchbox", { name: "搜索照片" }) as HTMLInputElement).value).toBe("");
  });

  it("says which photos the object detector found the query's things in, and when it found none", async () => {
    const results: Record<string, PhotoSearchResults> = {
      手机: {
        items: [asset("photo:phone"), asset("photo:desk-phone"), asset("photo:desk")],
        semantic: true,
        closest: 2,
        objects: { words: ["手机"], detected: 2, undetected: 5 },
      },
      长颈鹿: {
        items: [asset("photo:zoo"), asset("photo:park")],
        semantic: true,
        closest: 1,
        objects: { words: ["长颈鹿"], detected: 0, undetected: 0 },
      },
      猫和狗: {
        items: [],
        semantic: true,
        closest: 0,
        objects: { words: ["猫", "狗"], detected: 0, undetected: 3 },
      },
    };
    serve(
      { timelines: mine(() => [asset("photo:home")]) },
      http.get("/api/v1/photos/search", ({ query, response }) => response(200).json(results[query.get("q") ?? ""])),
    );
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    await screen.findByRole("button", { name: "查看 photo:home.jpg" });
    const search = async (text: string) => {
      const box = screen.getByRole("searchbox", { name: "搜索照片" });
      await user.clear(box);
      await user.type(box, `${text}{Enter}`);
    };
    // The status under the search box names what the detector found.
    const chip = (text: string) => screen.queryByText(fullText(text), { selector: "[role=status] > span" });

    await search("手机");
    expect(await screen.findByRole("separator", { name: "没有检测到手机的照片" })).toBeTruthy();
    // Photos still waiting for the detector may show one too.
    expect(screen.getByText("按与搜索内容的相似程度排列；其中有些照片还没识别，也可能有手机")).toBeTruthy();
    expect(chip("检测到手机的照片 2 张")).toBeTruthy();
    expect(screen.getByText("还有 5 张尚未识别完")).toBeTruthy();
    expect(screen.getByText("共 3 张 · 我的图库和共享图库")).toBeTruthy();
    expect(resultOrder()).toEqual([
      "查看 photo:phone.jpg",
      "查看 photo:desk-phone.jpg",
      "divider",
      "查看 photo:desk.jpg",
    ]);

    // Nothing found: the closest photos follow, as for any search.
    await search("长颈鹿");
    expect(await screen.findByRole("separator", { name: "相关度较低" })).toBeTruthy();
    expect(chip("没有检测到长颈鹿")).toBeTruthy();
    expect(screen.getByText("下面是画面最接近的照片")).toBeTruthy();
    expect(screen.getByText("最接近 1 张 · 共 2 张 · 我的图库和共享图库")).toBeTruthy();
    expect(screen.queryByText(/尚未识别完/)).toBeNull();

    // While photos wait for the detector, "none" says so.
    await search("猫和狗");
    expect(await screen.findByText("还有 3 张照片尚未识别完，稍后再搜一次试试。")).toBeTruthy();
    expect(chip("没有检测到猫和狗")).toBeTruthy();
    expect(screen.queryByText("下面是画面最接近的照片")).toBeNull();
  });

  it("finds photos similar to the one shown and goes back to where they were asked for", async () => {
    const similar: string[] = [];
    serve(
      { timelines: mine(() => [asset("photo:a"), asset("photo:b", { takenAt: "2026-05-01T07:00:00Z" })]) },
      photoDetails(() => [asset("photo:sea")]),
      http.get("/api/v1/photos/assets/{assetId}/similar", ({ params, request, response }) => {
        similar.push(pathOf(request));
        return params.assetId === "photo:a"
          ? response(200).json({ items: [asset("photo:c"), asset("photo:d")], ready: true })
          : response(200).json({ items: [], ready: false });
      }),
      http.get("/api/v1/photos/search", ({ response }) =>
        response(200).json({ items: [asset("photo:sea")], semantic: true, closest: 1 }),
      ),
    );
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    await openPhoto(user, "photo:a.jpg");
    await user.click(screen.getByRole("button", { name: "找相似" }));

    expect(screen.queryByRole("dialog")).toBeNull();
    expect(await screen.findByRole("button", { name: "查看 photo:c.jpg" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "相似的照片" })).toBeTruthy();
    expect(screen.getByText("2 张")).toBeTruthy();
    // The photo they were asked for is named above them.
    expect(
      within(screen.getByRole("main", { name: "相似的照片" })).getByText("photo:a.jpg", { selector: "strong" }),
    ).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "返回" }));
    expect(await screen.findByRole("button", { name: "查看 photo:b.jpg" })).toBeTruthy();

    // From a search, S asks from the viewer and going back returns to the search.
    await user.type(screen.getByRole("searchbox", { name: "搜索照片" }), "海边{Enter}");
    await openPhoto(user, "photo:sea.jpg");
    await user.keyboard("s");
    expect(await screen.findByText("这张照片还在整理中")).toBeTruthy();
    expect(similar).toContain("/api/v1/photos/assets/photo%3Asea/similar?limit=120");
    await user.click(screen.getByRole("button", { name: "返回" }));
    expect(await screen.findByRole("button", { name: "查看 photo:sea.jpg" })).toBeTruthy();
    expect((screen.getByRole("searchbox", { name: "搜索照片" }) as HTMLInputElement).value).toBe("海边");
  });

  it("searches by an image chosen, dropped or pasted, and sends it again once the photo service has let it go", async () => {
    vi.stubGlobal("URL", Object.assign(URL, { createObjectURL: () => "blob:query", revokeObjectURL: () => undefined }));
    vi.stubGlobal("XMLHttpRequest", FakeUpload);
    let posted = 0;
    let available = true;
    const expired = new Set<string>();
    const tokens: Array<string | null> = [];
    serve(
      { timelines: mine(() => [asset("photo:a")]) },
      photoDetails(() => [asset("photo:c")]),
      http.post("/api/v1/photos/image-searches", async ({ request, response }) => {
        tokens.push(request.headers.get("X-CSRF-Token"));
        if (!available)
          return response(503).json(apiError("ai_unavailable", "local AI cannot read the image right now"));
        const image = formOf(request).get("file") as File;
        return response(201).json({ id: `${await image.text()}-${++posted}` });
      }),
      http.get("/api/v1/photos/image-searches/{searchId}/assets", ({ params, response }) => {
        if (expired.has(params.searchId))
          return response(404).json(apiError("search_expired", "the image search is no longer kept"));
        return response(200).json({
          items: params.searchId.startsWith("cat")
            ? [asset("photo:c"), asset("photo:d", { libraryId: sharedLibrary.id })]
            : [],
        });
      }),
      // Nothing is like the photo found: finding similar ones is only a way there and back.
      http.get("/api/v1/photos/assets/{assetId}/similar", ({ response }) =>
        response(200).json({ items: [], ready: false }),
      ),
    );
    const chosen: File[] = [];
    vi.spyOn(HTMLInputElement.prototype, "click").mockImplementation(function (this: HTMLInputElement) {
      if (this.type !== "file") return;
      Object.defineProperty(this, "files", { value: chosen.splice(0) });
      this.dispatchEvent(new Event("change"));
    });
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    await screen.findByRole("button", { name: "查看 photo:a.jpg" });
    // The image searched by is named above the results.
    const imageName = (name: string) =>
      within(screen.getByRole("main", { name: "以图搜图" })).getByText(name, { selector: "strong" });

    // Chosen with the camera at the end of the search box.
    chosen.push(new File(["cat"], "cat.jpg", { type: "image/jpeg" }));
    await user.click(screen.getByRole("button", { name: "以图搜图" }));
    expect(await screen.findByRole("button", { name: "查看 photo:c.jpg" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "以图搜图" })).toBeTruthy();
    expect(screen.getByText("2 张")).toBeTruthy();
    expect(imageName("cat.jpg")).toBeTruthy();
    expect(screen.getByText("按画面和画中物品的相似程度排列，最像的在前")).toBeTruthy();
    expect(posted).toBe(1);
    expect(tokens[0]).toBe("csrf-photo");
    // Results mix libraries, so the shared photo says where it is from.
    expect(sourceOf("photo:d.jpg")?.textContent).toBe("共享");

    // The photo service let the search go: coming back sends the image again.
    expired.add("cat-1");
    await openPhoto(user, "photo:c.jpg");
    await user.click(screen.getByRole("button", { name: "找相似" }));
    expect(await screen.findByRole("heading", { name: "相似的照片" })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "返回" }));
    expect(await screen.findByRole("button", { name: "查看 photo:d.jpg" })).toBeTruthy();
    expect(posted).toBe(2);
    await user.click(screen.getByRole("button", { name: "返回" }));
    expect(await screen.findByRole("button", { name: "查看 photo:a.jpg" })).toBeTruthy();

    // Dropped on the search box it is searched by, not uploaded.
    const box = screen.getByRole("search");
    const dog = { types: ["Files"], files: [new File(["dog"], "dog.png", { type: "image/png" })] };
    fireEvent.dragEnter(dropArea(), { dataTransfer: dog });
    fireEvent.dragOver(box, { dataTransfer: dog });
    expect(screen.getByText("松开以图搜图")).toBeTruthy();
    fireEvent.drop(box, { dataTransfer: dog });
    expect(await screen.findByText("没有找到相似的照片")).toBeTruthy();
    expect(imageName("dog.png")).toBeTruthy();
    expect(FakeUpload.sent).toHaveLength(0);
    expect(screen.queryByText("松开以图搜图")).toBeNull();

    // When local AI cannot read it, the image can be tried again.
    available = false;
    await user.click(screen.getByRole("button", { name: "AI 聚合" }));
    const hero = await screen.findByRole("searchbox", { name: "用一句话搜索照片" });
    fireEvent.paste(hero, { clipboardData: { files: [new File(["cat"], "image.png", { type: "image/png" })] } });
    expect(await screen.findByText("没能识别这张图片", { selector: "strong" })).toBeTruthy();
    expect(screen.getByText("暂时无法识别图片，请稍后再试")).toBeTruthy();
    expect(imageName("粘贴的图片")).toBeTruthy();
    available = true;
    await user.click(screen.getByRole("button", { name: "重试" }));
    expect(await screen.findByRole("button", { name: "查看 photo:c.jpg" })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "返回" }));
    expect(await screen.findByRole("heading", { name: "alice，您可以用一句话来找照片" })).toBeTruthy();
  });

  it("names where results come from and narrows a search to one library", async () => {
    const sea = asset("photo:sea", { libraryId: sharedLibrary.id, uploadedBy: "user:bob" });
    const beach = asset("photo:beach");
    const searches: string[] = [];
    serve(
      { timelines: mine(() => [asset("photo:home")]) },
      http.get("/api/v1/photos/search", ({ query, request, response }) => {
        searches.push(pathOf(request));
        const library = query.get("library");
        const items = [sea, beach].filter((photo) => !library || photo.libraryId === library);
        return response(200).json({ items, semantic: true, closest: items.length });
      }),
    );
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    await screen.findByRole("button", { name: "查看 photo:home.jpg" });
    await user.type(screen.getByRole("searchbox", { name: "搜索照片" }), "海边{Enter}");

    expect(await screen.findByText("找到 2 张 · 我的图库和共享图库")).toBeTruthy();
    const scope = screen.getByRole("radiogroup", { name: "搜索范围" });
    expect(within(scope).getByRole("radio", { name: "全部", checked: true })).toBeTruthy();
    // Results mixing libraries mark the shared photo, not the caller's own.
    expect(sourceOf("photo:sea.jpg")?.textContent).toBe("共享");
    expect(sourceOf("photo:beach.jpg")).toBeNull();

    await user.click(within(scope).getByRole("radio", { name: "共享" }));
    expect(await screen.findByText("找到 1 张 · 共享图库")).toBeTruthy();
    expect(searches).toContain("/api/v1/photos/search?q=%E6%B5%B7%E8%BE%B9&limit=120&library=library%3Ashared");
    expect(screen.queryByRole("button", { name: "查看 photo:beach.jpg" })).toBeNull();
    expect(sourceOf("photo:sea.jpg")).toBeNull();

    // The choice holds for the next search.
    const box = screen.getByRole("searchbox", { name: "搜索照片" });
    await user.clear(box);
    await user.type(box, "沙滩{Enter}");
    await waitFor(() =>
      expect(searches).toContain("/api/v1/photos/search?q=%E6%B2%99%E6%BB%A9&limit=120&library=library%3Ashared"),
    );
    await user.click(within(screen.getByRole("radiogroup", { name: "搜索范围" })).getByRole("radio", { name: "我的" }));
    expect(await screen.findByText("找到 1 张 · 我的图库")).toBeTruthy();
  });

  it("browses a viewed member library read-only, searches it only from there and ends the viewing", async () => {
    const viewed: PhotoLibrary = {
      id: "library:alice",
      kind: "private",
      ownerUserId: "user:alice",
      ownerName: "alice",
      createdAt: "2026-10-08T00:00:00Z",
      viewing: { grantId: "viewing:7", expiresAt: "2026-10-09T09:00:00Z" },
    };
    const own: PhotoLibrary = { ...privateLibrary, id: "library:admin", ownerUserId: "user:admin", ownerName: "admin" };
    let ended = false;
    const searches: string[] = [];
    const endings: string[] = [];
    serve(
      {
        libraries: () => (ended ? [own, sharedLibrary] : [own, sharedLibrary, viewed]),
        timelines: { [viewed.id]: () => [asset("photo:w", { libraryId: viewed.id })] },
      },
      http.delete("/api/v1/viewing/{grantId}", ({ params, response }) => {
        ended = true;
        endings.push(params.grantId);
        return response(204).empty();
      }),
      http.get("/api/v1/photos/search", ({ request, response }) => {
        searches.push(pathOf(request));
        return response(200).json({ items: [], semantic: true, closest: 0 });
      }),
    );
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:admin" userName="alice" isAdmin />);
    await user.click(await screen.findByRole("tab", { name: "alice" }));
    expect((await screen.findByText(/只读查看 alice的图库/)).textContent).toContain("本次访问已写入审计");
    expect(screen.queryByRole("button", { name: /上传/ })).toBeNull();
    expect(screen.queryByRole("button", { name: "回收站" })).toBeNull();
    await openPhoto(user, "photo:w.jpg");
    expect(screen.queryByRole("button", { name: "移到回收站" })).toBeNull();
    expect(screen.queryByRole("button", { name: "加入相册" })).toBeNull();
    expect(screen.getByRole("button", { name: "下载原图" })).toBeTruthy();
    await user.keyboard("{Escape}");

    await user.type(screen.getByRole("searchbox", { name: "搜索照片" }), "猫{Enter}");
    expect(await screen.findByText("没有找到相关照片")).toBeTruthy();
    expect(searches).toContain("/api/v1/photos/search?q=%E7%8C%AB&limit=120&viewing=library%3Aalice");

    await user.click(screen.getByRole("button", { name: "结束查看" }));
    expect(endings).toEqual(["viewing:7"]);
    await waitFor(() => expect(screen.queryByRole("tab", { name: "alice" })).toBeNull());
  });

  it("reports an unavailable photo service and recovers on retry", async () => {
    let available = false;
    serve(
      {},
      http.get("/api/v1/photos/libraries", ({ response }) =>
        available
          ? response(200).json({ items: [sharedLibrary, privateLibrary] })
          : response(503).json(apiError("photos_unavailable", "the photo library is not available on this device")),
      ),
    );
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    expect(await screen.findByText(/相册暂时不可用：数据卷未就绪/)).toBeTruthy();
    available = true;
    await act(async () => {
      await user.click(screen.getByRole("button", { name: "重试" }));
    });
    expect(await screen.findByRole("tab", { name: "我的" })).toBeTruthy();
  });
});

describe("PhotosPanel motion photos", () => {
  it("uploads a Live Photo pair once and preserves both form parts", async () => {
    vi.stubGlobal("XMLHttpRequest", FakeUpload);
    let uploaded = false;
    FakeUpload.respond = () => {
      uploaded = true;
      return {
        status: 201,
        body: asset("photo:live", { name: "live.heic", motion: { kind: "live-photo", duration: 2 } }),
      };
    };
    serve({
      timelines: mine(() =>
        uploaded ? [asset("photo:live", { name: "live.heic", motion: { kind: "live-photo", duration: 2 } })] : [],
      ),
    });
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    await screen.findByText("把照片拖到这里");
    fireEvent.drop(dropArea(), {
      dataTransfer: { types: ["Files"], files: [new File(["mov"], "live.MOV"), new File(["heic"], "live.heic")] },
    });
    await screen.findByRole("button", { name: "查看 live.heic" });
    expect(FakeUpload.sent).toHaveLength(1);
    expect(FakeUpload.sent[0].url).toContain("/live-photos");
    expect((FakeUpload.sent[0].body?.get("motion") as File | undefined)?.name).toBe("live.MOV");
  });
  it("loads video only on request and removes it on navigation or failure", async () => {
    const first = asset("photo:live", { mediaType: "image/heic", motion: { kind: "live-photo", duration: 2 } });
    serve({ timelines: mine(() => [first, asset("photo:next")]) });
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    const user = userEvent.setup();
    await openPhoto(user, first.name);
    expect(document.querySelector("video")).toBeNull();
    const viewer = screen.getByRole("dialog", { name: `查看 ${first.name}` });
    expect(within(viewer).getByRole("img", { name: first.name }).getAttribute("src")).toContain("/preview");
    await user.click(screen.getByRole("button", { name: "播放动态照片" }));
    expect(document.querySelector("video")?.getAttribute("src")).toContain("/motion/play");
    fireEvent.error(document.querySelector("video")!);
    expect(document.querySelector("video")).toBeNull();
    expect(screen.getByText("动态片段暂时无法播放，仍可查看照片或下载原件。")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "播放动态照片" }));
    fireEvent.keyDown(screen.getByRole("dialog"), { key: "ArrowRight" });
    expect(document.querySelector("video")).toBeNull();
  });
});
