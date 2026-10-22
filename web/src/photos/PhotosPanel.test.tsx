import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setSession } from "../api";
import { PhotosPanel } from "./PhotosPanel";
import type { PhotoAlbum, PhotoAsset, PhotoLibrary } from "./photosApi";

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

const privateLibrary: PhotoLibrary = {
  id: "library:mine",
  kind: "private",
  ownerUserId: "user:alice",
  createdAt: "2026-10-08T00:00:00Z",
};
const sharedLibrary: PhotoLibrary = { id: "library:shared", kind: "shared", createdAt: "2026-10-08T00:00:00Z" };
const trip: PhotoAlbum = {
  id: "album:trip",
  libraryId: privateLibrary.id,
  name: "旅行",
  createdBy: "user:alice",
  createdAt: "2026-10-09T00:00:00Z",
  photos: 1,
  coverId: "photo:a",
};

function asset(id: string, overrides: Partial<PhotoAsset> = {}): PhotoAsset {
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

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

type Route = (url: string, init?: RequestInit) => Response | Promise<Response | undefined> | undefined;

// timeline serves one library's photos the way the photo service does: by
// month of their capture (or import) time.
function timeline(libraryId: string, photos: () => PhotoAsset[]): Route {
  const path = `/api/v1/photos/libraries/${encodeURIComponent(libraryId)}/timeline`;
  const month = (photo: PhotoAsset) => (photo.takenAt ?? photo.importedAt).slice(0, 7);
  return (url) => {
    if (url === `${path}/months`) {
      const counts = new Map<string, number>();
      for (const photo of photos()) counts.set(month(photo), (counts.get(month(photo)) ?? 0) + 1);
      return json({
        items: [...counts]
          .sort((a, b) => b[0].localeCompare(a[0]))
          .map(([key, value]) => ({ month: key, photos: value })),
      });
    }
    const wanted = url.startsWith(`${path}?`) ? new URL(url, "http://nas").searchParams.get("month") : null;
    if (wanted) return json({ items: photos().filter((photo) => month(photo) === wanted) });
    if (url.startsWith(`${path}?`)) return json({ items: photos() });
    return undefined;
  };
}

function serve(...routes: Route[]) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    for (const route of routes) {
      const response = await route(url, init);
      if (response) return response;
    }
    if (url === "/api/v1/photos/libraries") return json({ items: [sharedLibrary, privateLibrary] });
    if (url === "/api/v1/photos/ai") return json({ state: "idle", ready: 3, pending: 0, failed: 0 });
    if (url.startsWith("/api/v1/photos/labels?") || url === "/api/v1/photos/labels")
      return json({ items: [], ready: false });
    if (url.endsWith("/timeline/months")) return json({ items: [] });
    if (/\/assets\/[^/]+$/.test(url) && !init?.method)
      return json(asset(decodeURIComponent(url.split("/").at(-1) ?? "")));
    return json({ items: [] });
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

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

const alice = {
  csrfToken: "csrf-photo",
  expiresAt: "",
  user: { id: "user:alice", username: "alice", role: "member" as const, status: "active" as const, createdAt: "" },
};
const sizes = Object.getOwnPropertyDescriptors(HTMLElement.prototype);

beforeEach(() => {
  // jsdom lays nothing out; the photo scroller gets a window-sized view.
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", {
    configurable: true,
    get() {
      return this.classList?.contains("ph-scroll") ? 800 : 0;
    },
  });
  Object.defineProperty(HTMLElement.prototype, "offsetWidth", {
    configurable: true,
    get() {
      return this.classList?.contains("ph-scroll") ? 1000 : 0;
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
  window.localStorage.clear();
  FakeUpload.sent = [];
  setSession();
});

const openPhoto = async (user: ReturnType<typeof userEvent.setup>, name: string) =>
  user.click(await screen.findByRole("button", { name: `查看 ${name}` }));

describe("PhotosPanel timeline", () => {
  it("opens the caller's library by month and day, with thumbnails or originals", async () => {
    serve(
      timeline(privateLibrary.id, () => [
        asset("photo:a"),
        asset("photo:b", { thumbnail: "pending", duplicate: "duplicate" }),
        asset("photo:c", { takenAt: undefined }),
      ]),
    );
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
        (url) =>
          url === "/api/v1/photos/assets/photo%3Ap"
            ? json(asset("photo:p", { thumbnail: ready ? "ready" : "pending" }))
            : undefined,
        timeline(privateLibrary.id, () => [asset("photo:p", { thumbnail: "pending" })]),
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
    serve(timeline(privateLibrary.id, () => (uploaded ? [asset("photo:new", { name: "cat.jpg" })] : [])));
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    expect(await screen.findByText("把照片拖到这里")).toBeTruthy();

    const app = document.querySelector(".ph-app") as HTMLElement;
    const files = [
      new File(["jpeg"], "cat.jpg", { type: "image/jpeg" }),
      new File(["text"], "notes.txt", { type: "text/plain" }),
    ];
    fireEvent.dragEnter(app, { dataTransfer: { types: ["Files"], files } });
    expect(screen.getByText("照片和视频会保存到我的图库")).toBeTruthy();
    fireEvent.drop(app, { dataTransfer: { types: ["Files"], files } });

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
    serve(
      (url) => (url === "/api/v1/photos/assets/photo%3Alive" ? json(stored[0]) : undefined),
      timeline(privateLibrary.id, () => stored),
    );
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    expect(await screen.findByText("把照片拖到这里")).toBeTruthy();
    const app = document.querySelector(".ph-app") as HTMLElement;
    const drop = (file: File) => {
      fireEvent.dragEnter(app, { dataTransfer: { types: ["Files"], files: [file] } });
      fireEvent.drop(app, { dataTransfer: { types: ["Files"], files: [file] } });
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
    expect(await screen.findByText("动态照片", { selector: ".ph-motion-badge" })).toBeTruthy();
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
    serve(
      (url) => (url === "/api/v1/photos/assets/photo%3Alive" ? json(still()) : undefined),
      timeline(privateLibrary.id, () => [still()]),
    );
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
    expect(screen.queryByText("实况照片只上传了静态画面")).toBeNull();
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
    const fetchMock = serve(
      (url) =>
        url.startsWith("/api/v1/photos/assets/photo%3Abeach/video/playback?")
          ? json({ mode: "direct", duration: 83.4 })
          : undefined,
      (url) => (url === "/api/v1/photos/assets/photo%3Abeach" ? json(stored[0]) : undefined),
      timeline(privateLibrary.id, () => stored),
    );
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    expect(await screen.findByText("把照片拖到这里")).toBeTruthy();
    const app = document.querySelector(".ph-app") as HTMLElement;
    const files = [new File(["mp4"], "beach.mp4", { type: "video/mp4" })];
    fireEvent.dragEnter(app, { dataTransfer: { types: ["Files"], files } });
    fireEvent.drop(app, { dataTransfer: { types: ["Files"], files } });

    expect(await screen.findByText("视频已保存到我的图库")).toBeTruthy();
    expect(FakeUpload.sent[0].url).toBe("/api/v1/photos/libraries/library%3Amine/uploads");
    expect(await screen.findByText("1:23", { selector: ".ph-tile-video" })).toBeTruthy();

    await openPhoto(user, "photo:beach.mp4");
    const viewer = await screen.findByRole("dialog", { name: "查看 photo:beach.mp4" });
    const controls = await within(viewer).findByRole("toolbar", { name: "视频控制" });
    const media = viewer.querySelector("video") as HTMLVideoElement;
    await waitFor(() => expect(media.getAttribute("src")).toBe("/api/v1/photos/assets/photo%3Abeach/video/original"));
    expect(
      fetchMock.mock.calls.some(([url]) =>
        String(url).startsWith("/api/v1/photos/assets/photo%3Abeach/video/playback?caps="),
      ),
    ).toBe(true);
    // A video has nothing alike to find and no zoom.
    expect(within(viewer).queryByRole("button", { name: "找相似" })).toBeNull();
    expect(controls.querySelector(".ph-video-time")?.textContent).toBe("0:00 / 1:23");
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
      (url) =>
        url.startsWith("/api/v1/photos/assets/photo%3Aold/video/playback?")
          ? json({ mode: "direct", duration: 20 })
          : undefined,
      timeline(privateLibrary.id, () => [clip("photo:old", { mediaType: "video/quicktime", video: { duration: 20 } })]),
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
    serve(timeline(privateLibrary.id, () => [asset("photo:a"), asset("photo:b", { takenAt: "2026-05-01T07:00:00Z" })]));
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
    serve(
      (url) =>
        url === "/api/v1/photos/assets/photo%3Aa"
          ? json(asset("photo:a", { alsoKeptBy: ["bob", "carol"] }))
          : undefined,
      timeline(privateLibrary.id, () => [
        asset("photo:a", { alsoKeptBy: ["bob", "carol"] }),
        asset("photo:b", { takenAt: "2026-05-01T07:00:00Z" }),
      ]),
    );
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
      (url) =>
        url === "/api/v1/photos/assets/photo%3Az"
          ? json(asset("photo:z", { takenAt: "2025-08-02T09:00:00Z" }))
          : undefined,
      timeline(privateLibrary.id, () => [asset("photo:a")]),
    );
    const handled = vi.fn();
    const user = userEvent.setup();
    render(
      <PhotosPanel
        userId="user:alice"
        userName="alice"
        isAdmin={false}
        focus={{ assetId: "photo:z", at: 1 }}
        onFocusHandled={handled}
      />,
    );
    expect(await screen.findByRole("dialog", { name: "查看 photo:z.jpg" })).toBeTruthy();
    expect(handled).toHaveBeenCalled();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("moves a photo to the trash and undoes it", async () => {
    let trashed = false;
    const fetchMock = serve(
      (url, init) => {
        if (url === "/api/v1/photos/assets/photo%3Aa" && init?.method === "DELETE") {
          trashed = true;
          return json(asset("photo:a"));
        }
        if (url === "/api/v1/photos/assets/photo%3Aa/restore") {
          trashed = false;
          return json(asset("photo:a"));
        }
        return undefined;
      },
      timeline(privateLibrary.id, () => (trashed ? [] : [asset("photo:a")])),
    );
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    await openPhoto(user, "photo:a.jpg");
    await user.click(screen.getByRole("button", { name: "移到回收站" }));

    expect(await screen.findByText("已移到回收站：1 张")).toBeTruthy();
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.queryByRole("button", { name: "查看 photo:a.jpg" })).toBeNull();
    await user.click(screen.getByRole("button", { name: "撤销" }));
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/photos/assets/photo%3Aa/restore",
      expect.objectContaining({ method: "POST" }),
    );
    expect(await screen.findByRole("button", { name: "查看 photo:a.jpg" })).toBeTruthy();
  });

  it("offers changes to a shared photo only to its uploader or an administrator, and copies for anyone", async () => {
    const shared = asset("photo:s", { libraryId: sharedLibrary.id, uploadedBy: "user:bob" });
    const fetchMock = serve(
      (url, init) => (url.endsWith("/copies") && init?.method === "POST" ? json(asset("photo:mine"), 201) : undefined),
      timeline(sharedLibrary.id, () => [shared]),
    );
    const user = userEvent.setup();
    const { unmount } = render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    await user.click(await screen.findByRole("tab", { name: "共享" }));
    await openPhoto(user, "photo:s.jpg");
    expect(screen.queryByRole("button", { name: "移到回收站" })).toBeNull();
    expect(screen.queryByRole("button", { name: "重命名照片" })).toBeNull();
    await user.click(await screen.findByRole("button", { name: "复制到我的图库" }));
    expect(await screen.findByText("已复制 1 张到我的图库")).toBeTruthy();
    const copy = fetchMock.mock.calls.find(([url]) => String(url).endsWith("/copies"));
    expect(JSON.parse(String(copy?.[1]?.body))).toEqual({ libraryId: privateLibrary.id });
    unmount();

    render(<PhotosPanel userId="user:admin" userName="alice" isAdmin />);
    await user.click(await screen.findByRole("tab", { name: "共享" }));
    await openPhoto(user, "photo:s.jpg");
    expect(screen.getByRole("button", { name: "移到回收站" })).toBeTruthy();
  });

  it("tags a photo and opens the group of its tag", async () => {
    let details = asset("photo:cat");
    const fetchMock = serve(
      (url, init) => {
        if (url === "/api/v1/photos/assets/photo%3Acat" && !init?.method) return json(details);
        if (url === "/api/v1/photos/assets/photo%3Acat/tags" && init?.method === "POST") {
          details = { ...details, tags: ["小橘"] };
          return json(details);
        }
        if (url.startsWith("/api/v1/photos/tags/%E5%B0%8F%E6%A9%98/assets"))
          return json({ items: [asset("photo:cat"), asset("photo:kitten")] });
        return undefined;
      },
      timeline(privateLibrary.id, () => [asset("photo:cat")]),
    );
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    await openPhoto(user, "photo:cat.jpg");

    await user.type(await screen.findByRole("textbox", { name: "添加标签" }), "小橘{Enter}");
    expect(await within(screen.getByLabelText("标签")).findByRole("button", { name: "小橘" })).toBeTruthy();
    // Photo details never list AI labels, only the user's tags.
    expect(screen.queryByLabelText("AI 标签")).toBeNull();

    await user.click(within(screen.getByLabelText("标签")).getByRole("button", { name: "小橘" }));
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/photos/tags/%E5%B0%8F%E6%A9%98/assets?limit=120",
      expect.anything(),
    );
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
    serve(timeline(privateLibrary.id, photos));
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
    const fetchMock = serve(
      (url, init) => {
        const match = url.match(/^\/api\/v1\/photos\/assets\/([^/]+)$/);
        if (match && init?.method === "DELETE") {
          trashed.add(decodeURIComponent(match[1]));
          return json(asset(decodeURIComponent(match[1])));
        }
        return undefined;
      },
      timeline(privateLibrary.id, () => photos().filter((photo) => !trashed.has(photo.id))),
    );
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    await user.click(await screen.findByRole("checkbox", { name: /选择5月1日.*的 1 张照片/ }));
    await user.click(screen.getByRole("checkbox", { name: "选择 photo:2.jpg" }));
    await user.click(screen.getByRole("button", { name: "删除" }));

    expect(await screen.findByText("已移到回收站：2 张")).toBeTruthy();
    expect(
      fetchMock.mock.calls
        .filter(([, init]) => init?.method === "DELETE")
        .map(([url]) => url)
        .sort(),
    ).toEqual(["/api/v1/photos/assets/photo%3A2", "/api/v1/photos/assets/photo%3A3"]);
    expect(screen.queryByRole("button", { name: "查看 photo:3.jpg" })).toBeNull();
    expect(screen.getByRole("button", { name: "查看 photo:1.jpg" })).toBeTruthy();
  });

  it("adds the selection to an album, creating one on the way", async () => {
    const fetchMock = serve(
      (url, init) => {
        if (url === "/api/v1/photos/libraries/library%3Amine/albums" && init?.method === "POST")
          return json({ ...trip, id: "album:new", name: JSON.parse(String(init.body)).name, photos: 0 }, 201);
        if (url === "/api/v1/photos/libraries/library%3Amine/albums") return json({ items: [trip] });
        if (url.startsWith("/api/v1/photos/albums/") && init?.method === "POST") return json(asset("photo:1"));
        return undefined;
      },
      timeline(privateLibrary.id, photos),
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
      fetchMock.mock.calls
        .filter(([url, init]) => url === "/api/v1/photos/albums/album%3Atrip/assets" && init?.method === "POST")
        .map(([, init]) => JSON.parse(String(init?.body)).assetId)
        .sort(),
    ).toEqual(["photo:1", "photo:2"]);

    await user.click(screen.getByRole("checkbox", { name: "选择 photo:4.jpg" }));
    await user.click(screen.getByRole("button", { name: "加入相册" }));
    await user.click(
      within(await screen.findByRole("dialog", { name: "加入相册" })).getByRole("button", { name: "新建相册" }),
    );
    await user.type(screen.getByRole("textbox", { name: "新相册名称" }), "春天{Enter}");
    expect(await screen.findByText("已加入相册“春天”")).toBeTruthy();
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/photos/albums/album%3Anew/assets",
      expect.objectContaining({ method: "POST" }),
    );
  });
});

describe("PhotosPanel albums, trash and AI 聚合", () => {
  it("lists albums, opens one, renames it and takes a photo out of it", async () => {
    let removed = false;
    const fetchMock = serve((url, init) => {
      if (url === "/api/v1/photos/libraries/library%3Amine/albums") return json({ items: [trip] });
      if (url.startsWith("/api/v1/photos/albums/album%3Atrip/assets?"))
        return json({ items: removed ? [] : [asset("photo:a")] });
      if (url === "/api/v1/photos/albums/album%3Atrip" && init?.method === "PATCH")
        return json({ ...trip, name: JSON.parse(String(init.body)).name });
      if (url === "/api/v1/photos/albums/album%3Atrip/assets/photo%3Aa" && init?.method === "DELETE") {
        removed = true;
        return new Response(null, { status: 204 });
      }
      return undefined;
    });
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
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/photos/albums/album%3Atrip/assets/photo%3Aa",
      expect.objectContaining({ method: "DELETE" }),
    );
    expect(await screen.findByText(/照片仍在图库中/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "查看 photo:a.jpg" })).toBeNull();
  });

  it("restores from the trash and asks in the window before deleting for good", async () => {
    const purgeAfter = new Date(Date.now() + 3 * 86_400_000).toISOString();
    let items = [
      asset("photo:t1", { trash: { trashedAt: "2026-10-08T09:00:00Z", trashedBy: "user:alice", purgeAfter } }),
      asset("photo:t2", { trash: { trashedAt: "2026-10-08T09:00:00Z", trashedBy: "user:alice", purgeAfter } }),
    ];
    const fetchMock = serve((url, init) => {
      if (url.endsWith("/restore")) {
        items = items.filter((item) => !url.includes("photo%3At1"));
        return json(asset("photo:t1"));
      }
      if (url.startsWith("/api/v1/photos/trash/")) {
        items = [];
        return new Response(null, { status: 204 });
      }
      if (url.endsWith("/trash") && !init?.method) return json({ items });
      return undefined;
    });
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    await user.click(await screen.findByRole("button", { name: "回收站" }));
    expect((await screen.findAllByText("3 天后删除")).length).toBe(2);

    // In the trash a click selects.
    await user.click(screen.getByRole("button", { name: "查看 photo:t1.jpg" }));
    await user.click(screen.getByRole("button", { name: "恢复" }));
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/photos/assets/photo%3At1/restore",
      expect.objectContaining({ method: "POST" }),
    );
    expect(await screen.findByText("已恢复 1 张照片")).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "查看 photo:t2.jpg" }));
    await user.click(screen.getByRole("button", { name: "永久删除" }));
    const dialog = screen.getByRole("dialog", { name: "永久删除 1 张照片？" });
    await user.click(within(dialog).getByRole("button", { name: "取消" }));
    expect(fetchMock.mock.calls.some(([url]) => String(url).startsWith("/api/v1/photos/trash/"))).toBe(false);
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
    const fetchMock = serve((url, init) => {
      if (url === "/api/v1/photos/ai") return json({ state: "working", ready: 3, pending: 2, failed: 1 });
      if (url === "/api/v1/photos/labels")
        return json({
          items: [cat, { id: "sea", name: "海", category: "nature", photos: 1, coverId: "photo:sea" }],
          ready: true,
        });
      if (url === "/api/v1/photos/tags") return json({ items: [{ name: "小橘", photos: 2, coverId: "photo:orange" }] });
      if (url.startsWith("/api/v1/photos/tags/%E5%B0%8F%E6%A9%98/assets"))
        return json({ items: untagged ? [asset("photo:orange")] : [asset("photo:orange"), asset("photo:ginger")] });
      if (url === "/api/v1/photos/assets/photo%3Aginger/tags/%E5%B0%8F%E6%A9%98" && init?.method === "DELETE") {
        untagged = true;
        return json(asset("photo:ginger"));
      }
      if (url.startsWith("/api/v1/photos/labels/cat/assets"))
        return json({ items: hidden ? [asset("photo:cat")] : [asset("photo:cat"), asset("photo:fox")] });
      if (url === "/api/v1/photos/assets/photo%3Afox/ai-labels/cat" && init?.method === "DELETE") {
        hidden = true;
        return json(asset("photo:fox"));
      }
      if (url.startsWith("/api/v1/photos/search?q="))
        return json({ items: [asset("photo:sea")], semantic: true, closest: 1 });
      return undefined;
    });
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
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/photos/search?q=%E7%8C%AB&limit=120", expect.anything());
    expect(fetchMock.mock.calls.some(([url]) => String(url).startsWith("/api/v1/photos/labels/"))).toBe(false);
    await user.click(await screen.findByRole("button", { name: "返回" }));

    // A cluster opens on its own; a wrong photo can be taken out of it.
    await user.click(await screen.findByRole("button", { name: "打开 猫" }));
    expect(await screen.findByRole("heading", { name: "猫" })).toBeTruthy();
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/photos/labels/cat/assets?limit=120", expect.anything());
    await user.click(await screen.findByRole("checkbox", { name: "选择 photo:fox.jpg" }));
    await user.click(screen.getByRole("button", { name: "不是猫" }));
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/photos/assets/photo%3Afox/ai-labels/cat",
      expect.objectContaining({ method: "DELETE" }),
    );
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
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/photos/assets/photo%3Aginger/tags/%E5%B0%8F%E6%A9%98",
      expect.objectContaining({ method: "DELETE" }),
    );
    await waitFor(() => expect(screen.queryByRole("button", { name: "查看 photo:ginger.jpg" })).toBeNull());
    expect(await screen.findByText("已从 1 张照片上去掉“小橘”")).toBeTruthy();
  });

  it("shows clusters being prepared and lists them once local AI has settled", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      // The label texts first, then the photos, then nothing waits.
      let phase = 0;
      const fetchMock = serve((url) => {
        if (url === "/api/v1/photos/ai")
          return json({
            state: phase === 2 ? "idle" : "working",
            ready: 3,
            pending: phase === 2 ? 0 : 2,
            failed: 0,
            clusters: phase ? "ready" : "preparing",
          });
        if (url.startsWith("/api/v1/photos/labels"))
          return json(
            phase === 2
              ? { items: [{ id: "cat", name: "猫", category: "animal", photos: 2, coverId: "photo:cat" }], ready: true }
              : { items: [], ready: phase === 1 },
          );
        return undefined;
      });
      const listed = () =>
        fetchMock.mock.calls.filter(([url]) => String(url).startsWith("/api/v1/photos/labels")).length;
      const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
      render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
      await user.click(await screen.findByRole("button", { name: "AI 聚合" }));
      const browse = await screen.findByRole("region", { name: "按内容聚合" });
      // Placeholder cards under the bare heading say the groups are coming.
      const coming = () => screen.getByRole("region", { name: "按内容聚合" }).getAttribute("aria-busy") === "true";
      expect(browse.querySelectorAll(".ph-ghost").length).toBeGreaterThan(0);
      expect(within(browse).getByRole("banner").textContent).toBe("按内容聚合");
      expect(screen.queryByRole("region", { name: "可以这样搜" })).toBeNull();

      // Waiting polls only the AI status: any other request would hold the
      // AI work back.
      await vi.advanceTimersByTimeAsync(15_000);
      expect(listed()).toBe(1);
      // Groups ready, photos still waiting: still coming, and not asked for
      // yet, which would hold the photos back.
      phase = 1;
      await vi.advanceTimersByTimeAsync(15_000);
      expect(listed()).toBe(1);
      expect(coming()).toBe(true);
      phase = 2;
      await vi.advanceTimersByTimeAsync(15_000);
      expect(await screen.findByRole("button", { name: "打开 猫" })).toBeTruthy();
      await vi.advanceTimersByTimeAsync(15_000);
      expect(listed()).toBe(2);
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
    const fetchMock = serve((url) => {
      if (url === "/api/v1/photos/ai")
        return json({ state: "idle", ready: 8, pending: 0, failed: 0, clusters: "ready" });
      if (url === "/api/v1/photos/labels") return json({ items: animals, ready: true });
      if (url === "/api/v1/photos/tags") return json({ items: [{ name: "小橘", photos: 2, coverId: "photo:orange" }] });
      if (url.startsWith("/api/v1/photos/labels/animal-7/assets")) return json({ items: [asset("photo:zebra")] });
      return undefined;
    });
    const user = userEvent.setup();
    const { container } = render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    await user.click(await screen.findByRole("button", { name: "AI 聚合" }));
    const clusters = await screen.findByRole("region", { name: "按内容聚合" });
    const tags = await screen.findByRole("region", { name: "你的标签" });
    expect(clusters.compareDocumentPosition(tags) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    // The headings stand alone.
    expect(within(clusters).getByRole("banner").textContent).toBe("按内容聚合");
    expect(within(tags).getByRole("banner").textContent).toBe("你的标签");

    await user.click(screen.getByRole("button", { name: "展开全部 8 类" }));
    const scroller = container.querySelector<HTMLElement>(".ph-discover")!;
    scroller.scrollTop = 420;
    await user.click(screen.getByRole("button", { name: "打开 斑马" }));
    expect(await screen.findByRole("button", { name: "查看 photo:zebra.jpg" })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "返回" }));
    expect(await screen.findByRole("button", { name: "收起" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "打开 斑马" })).toBeTruthy();
    expect(container.querySelector<HTMLElement>(".ph-discover")!.scrollTop).toBe(420);
    expect(document.activeElement).not.toBe(screen.getByRole("searchbox", { name: "用一句话搜索照片" }));

    // Opened from the sidebar, AI 聚合 starts from the top again.
    await user.click(screen.getByRole("button", { name: "图库" }));
    await user.click(screen.getByRole("button", { name: "AI 聚合" }));
    expect(await screen.findByRole("button", { name: "展开全部 8 类" })).toBeTruthy();
    expect(container.querySelector<HTMLElement>(".ph-discover")!.scrollTop).toBe(0);
    expect(fetchMock).toHaveBeenCalled();
  });

  it("leaves 按内容聚合 out when local AI is sure of no photos", async () => {
    const fetchMock = serve((url) => {
      if (url === "/api/v1/photos/ai")
        return json({ state: "idle", ready: 3, pending: 0, failed: 0, clusters: "ready" });
      if (url.startsWith("/api/v1/photos/labels")) return json({ items: [], ready: true });
      if (url === "/api/v1/photos/tags") return json({ items: [{ name: "小橘", photos: 2, coverId: "photo:orange" }] });
      return undefined;
    });
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    await user.click(await screen.findByRole("button", { name: "AI 聚合" }));
    expect(await screen.findByRole("region", { name: "你的标签" })).toBeTruthy();
    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(expect.stringMatching(/^\/api\/v1\/photos\/labels/), expect.anything()),
    );
    expect(screen.queryByRole("region", { name: "按内容聚合" })).toBeNull();
  });

  it("leaves the clusters out while local AI is away, and never says AI is off", async () => {
    const fetchMock = serve((url) => {
      if (url === "/api/v1/photos/ai")
        return json({ state: "unavailable", ready: 0, pending: 5, failed: 0, clusters: "preparing" });
      return undefined;
    });
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    await user.click(await screen.findByRole("button", { name: "AI 聚合" }));
    expect(await screen.findByRole("heading", { name: "alice，您可以用一句话来找照片" })).toBeTruthy();
    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(expect.stringMatching(/^\/api\/v1\/photos\/labels/), expect.anything()),
    );
    expect(screen.queryByRole("region", { name: "按内容聚合" })).toBeNull();
    expect(screen.queryByRole("region", { name: "可以这样搜" })).toBeNull();
    expect(screen.queryByRole("status", { name: "本地 AI 状态" })).toBeNull();
    expect(screen.queryByText(/未启用|不可用/)).toBeNull();
  });
});

describe("PhotosPanel people and pets", () => {
  const ming = {
    id: "person:ming",
    libraryId: "library:mine",
    kind: "person",
    name: "小明",
    hidden: false,
    photos: 3,
    coverId: "face:ming",
    canChange: true,
  };
  const nameless = {
    id: "person:new",
    libraryId: "library:mine",
    kind: "person",
    name: "",
    hidden: false,
    photos: 2,
    coverId: "face:new",
    canChange: true,
  };
  const dog = {
    id: "person:dog",
    libraryId: "library:shared",
    kind: "dog",
    name: "",
    hidden: true,
    photos: 4,
    coverId: "face:dog",
    canChange: true,
  };
  const cat = {
    id: "person:cat",
    libraryId: "library:mine",
    kind: "cat",
    name: "咪咪",
    hidden: false,
    photos: 5,
    coverId: "face:cat",
    canChange: true,
  };

  it("lists people and pets on AI 聚合, names a person and takes a wrong photo out", async () => {
    let named = "";
    let removed = false;
    const fetchMock = serve((url, init) => {
      if (url === "/api/v1/photos/people?hidden=1")
        return json({ items: [ming, cat, { ...nameless, name: named }, dog] });
      if (url === "/api/v1/photos/people/person%3Anew" && init?.method === "PATCH") {
        named = JSON.parse(String(init.body)).name;
        return json({ ...nameless, name: named });
      }
      if (url.startsWith("/api/v1/photos/people/person%3Anew/assets?"))
        return json({ items: removed ? [asset("photo:grandma")] : [asset("photo:grandma"), asset("photo:aunt")] });
      if (url === "/api/v1/photos/people/person%3Anew/assets/photo%3Aaunt" && init?.method === "DELETE") {
        removed = true;
        return new Response(null, { status: 204 });
      }
      return undefined;
    });
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
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/photos/people/person%3Anew/assets?limit=120", expect.anything());
    await user.click(screen.getByRole("button", { name: "添加名字" }));
    await user.type(await screen.findByRole("textbox", { name: "名字" }), "奶奶");
    await user.click(screen.getByRole("button", { name: "保存" }));
    expect(await screen.findByRole("heading", { name: "奶奶" })).toBeTruthy();
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/photos/people/person%3Anew",
      expect.objectContaining({ method: "PATCH", body: JSON.stringify({ name: "奶奶" }) }),
    );
    expect(await screen.findByText("搜索“奶奶”就能找到 TA 的照片")).toBeTruthy();

    await user.click(await screen.findByRole("checkbox", { name: "选择 photo:aunt.jpg" }));
    await user.click(screen.getByRole("button", { name: "不是奶奶" }));
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/photos/people/person%3Anew/assets/photo%3Aaunt",
      expect.objectContaining({ method: "DELETE" }),
    );
    await waitFor(() => expect(screen.queryByRole("button", { name: "查看 photo:aunt.jpg" })).toBeNull());
    expect(await screen.findByText("已从“奶奶”移出 1 张，照片仍在图库中")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "返回" }));
    expect(await screen.findByLabelText("人物与宠物")).toBeTruthy();
  });

  it("names a face in a photo's details by choosing someone already named", async () => {
    let details: PhotoAsset = { ...asset("photo:party"), people: [{ occurrenceId: "face:1", kind: "face" }] };
    const fetchMock = serve(
      (url, init) => {
        if (url === "/api/v1/photos/assets/photo%3Aparty" && !init?.method) return json(details);
        if (url === "/api/v1/photos/people?hidden=1") return json({ items: [ming, nameless] });
        if (url === "/api/v1/photos/assets/photo%3Aparty/people/face%3A1" && init?.method === "PUT") {
          details = { ...details, people: [{ occurrenceId: "face:1", kind: "face", personId: ming.id, name: "小明" }] };
          return json(details);
        }
        if (url.startsWith("/api/v1/photos/people/person%3Aming/assets?"))
          return json({ items: [asset("photo:party")] });
        return undefined;
      },
      timeline(privateLibrary.id, () => [asset("photo:party")]),
    );
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    await openPhoto(user, "photo:party.jpg");
    const faces = await screen.findByLabelText("照片中的人物");
    await user.click(within(faces).getByRole("button", { name: "这是谁？" }));
    await user.type(within(faces).getByRole("textbox", { name: "名字" }), "小");
    await user.click(await within(faces).findByRole("option", { name: /小明/ }));
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/photos/assets/photo%3Aparty/people/face%3A1",
      expect.objectContaining({ method: "PUT", body: JSON.stringify({ personId: "person:ming" }) }),
    );
    await user.click(await within(faces).findByRole("button", { name: "小明" }));
    expect(await screen.findByRole("heading", { name: "小明" })).toBeTruthy();
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/photos/people/person%3Aming/assets?limit=120", expect.anything());
  });

  it("turns people and pets off for the caller's own library after asking, and leaves the shared one to administrators", async () => {
    let mine = { ...privateLibrary, people: true, canSetPeople: true };
    const fetchMock = serve((url, init) => {
      if (url === "/api/v1/photos/libraries" && !init?.method)
        return json({ items: [{ ...sharedLibrary, people: true, canSetPeople: false }, mine] });
      if (url === "/api/v1/photos/libraries/library%3Amine" && init?.method === "PATCH") {
        mine = { ...mine, people: JSON.parse(String(init.body)).people };
        return json(mine);
      }
      if (url === "/api/v1/photos/people?hidden=1") return json({ items: mine.people ? [ming, nameless] : [] });
      return undefined;
    });
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    await user.click(await screen.findByRole("button", { name: "AI 聚合" }));
    expect(await screen.findByLabelText("人物与宠物")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "人物与宠物设置" }));
    const settings = await screen.findByRole("dialog", { name: "人物与宠物设置" });
    const own = within(settings).getByRole("switch", { name: "我的图库的人物与宠物" });
    expect(own.getAttribute("aria-checked")).toBe("true");
    expect((within(settings).getByRole("switch", { name: "共享图库的人物与宠物" }) as HTMLButtonElement).disabled).toBe(
      true,
    );
    expect(within(settings).getByText("已开启 · 由管理员设置")).toBeTruthy();

    // Off clears what was recognised, so it asks first.
    await user.click(own);
    const ask = await screen.findByRole("dialog", { name: "关闭我的图库的人物与宠物？" });
    await user.click(within(ask).getByRole("button", { name: "关闭并删除" }));
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/photos/libraries/library%3Amine",
      expect.objectContaining({ method: "PATCH", body: JSON.stringify({ people: false }) }),
    );
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
  it("searches from any page into AI search and divides the closest results from the less related", async () => {
    const results: Record<string, object> = {
      "%E6%B5%B7%E8%BE%B9%E7%9A%84%E7%8C%AB": {
        items: [asset("photo:sea", { libraryId: sharedLibrary.id, uploadedBy: "user:bob" })],
        semantic: true,
        closest: 1,
      },
      "%E7%94%B5%E5%8A%A8%E8%BD%A6": {
        items: [asset("photo:bike"), asset("photo:scooter"), asset("photo:garden")],
        semantic: true,
        closest: 2,
      },
      "%E9%BB%84%E6%98%8F": { items: [], semantic: false, closest: 0 },
    };
    const fetchMock = serve(
      (url) => {
        const query = url.match(/^\/api\/v1\/photos\/search\?q=([^&]+)/)?.[1];
        return query ? json(results[query]) : undefined;
      },
      timeline(privateLibrary.id, () => [asset("photo:home")]),
    );
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    await screen.findByRole("button", { name: "查看 photo:home.jpg" });
    await user.type(screen.getByRole("searchbox", { name: "搜索照片" }), "海边的猫{Enter}");

    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/photos/search?q=%E6%B5%B7%E8%BE%B9%E7%9A%84%E7%8C%AB&limit=120",
      expect.anything(),
    );
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
    const order = [...document.querySelectorAll(".ph-entry")].flatMap((entry) =>
      entry.querySelector("[role=separator]")
        ? ["divider"]
        : [...entry.querySelectorAll("button[aria-label^='查看 ']")].map((button) => button.getAttribute("aria-label")),
    );
    expect(order).toEqual(["查看 photo:bike.jpg", "查看 photo:scooter.jpg", "divider", "查看 photo:garden.jpg"]);
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
    const results: Record<string, object> = {
      [encodeURIComponent("手机")]: {
        items: [asset("photo:phone"), asset("photo:desk-phone"), asset("photo:desk")],
        semantic: true,
        closest: 2,
        objects: { words: ["手机"], detected: 2, undetected: 5 },
      },
      [encodeURIComponent("长颈鹿")]: {
        items: [asset("photo:zoo"), asset("photo:park")],
        semantic: true,
        closest: 1,
        objects: { words: ["长颈鹿"], detected: 0, undetected: 0 },
      },
      [encodeURIComponent("猫和狗")]: {
        items: [],
        semantic: true,
        closest: 0,
        objects: { words: ["猫", "狗"], detected: 0, undetected: 3 },
      },
    };
    serve(
      (url) => {
        const query = url.match(/^\/api\/v1\/photos\/search\?q=([^&]+)/)?.[1];
        return query ? json(results[query]) : undefined;
      },
      timeline(privateLibrary.id, () => [asset("photo:home")]),
    );
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    await screen.findByRole("button", { name: "查看 photo:home.jpg" });
    const search = async (text: string) => {
      const box = screen.getByRole("searchbox", { name: "搜索照片" });
      await user.clear(box);
      await user.type(box, `${text}{Enter}`);
    };
    const chip = () => document.querySelector(".ph-found-chip")?.textContent;

    await search("手机");
    expect(await screen.findByRole("separator", { name: "没有检测到手机的照片" })).toBeTruthy();
    // Photos still waiting for the detector may show one too.
    expect(screen.getByText("按与搜索内容的相似程度排列；其中有些照片还没识别，也可能有手机")).toBeTruthy();
    expect(chip()).toBe("检测到手机的照片 2 张");
    expect(screen.getByText("还有 5 张尚未识别完")).toBeTruthy();
    expect(screen.getByText("共 3 张 · 我的图库和共享图库")).toBeTruthy();
    const order = [...document.querySelectorAll(".ph-entry")].flatMap((entry) =>
      entry.querySelector("[role=separator]")
        ? ["divider"]
        : [...entry.querySelectorAll("button[aria-label^='查看 ']")].map((button) => button.getAttribute("aria-label")),
    );
    expect(order).toEqual(["查看 photo:phone.jpg", "查看 photo:desk-phone.jpg", "divider", "查看 photo:desk.jpg"]);

    // Nothing found: the closest photos follow, as for any search.
    await search("长颈鹿");
    expect(await screen.findByRole("separator", { name: "相关度较低" })).toBeTruthy();
    expect(chip()).toBe("没有检测到长颈鹿");
    expect(screen.getByText("下面是画面最接近的照片")).toBeTruthy();
    expect(screen.getByText("最接近 1 张 · 共 2 张 · 我的图库和共享图库")).toBeTruthy();
    expect(screen.queryByText(/尚未识别完/)).toBeNull();

    // While photos wait for the detector, "none" says so.
    await search("猫和狗");
    expect(await screen.findByText("还有 3 张照片尚未识别完，稍后再搜一次试试。")).toBeTruthy();
    expect(chip()).toBe("没有检测到猫和狗");
    expect(screen.queryByText("下面是画面最接近的照片")).toBeNull();
  });

  it("finds photos similar to the one shown and goes back to where they were asked for", async () => {
    const fetchMock = serve(
      (url) => {
        if (url === "/api/v1/photos/assets/photo%3Aa/similar?limit=120")
          return json({ items: [asset("photo:c"), asset("photo:d")], ready: true });
        if (url === "/api/v1/photos/assets/photo%3Asea/similar?limit=120") return json({ items: [], ready: false });
        if (url.startsWith("/api/v1/photos/search?q="))
          return json({ items: [asset("photo:sea")], semantic: true, closest: 1 });
        return undefined;
      },
      timeline(privateLibrary.id, () => [asset("photo:a"), asset("photo:b", { takenAt: "2026-05-01T07:00:00Z" })]),
    );
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    await openPhoto(user, "photo:a.jpg");
    await user.click(screen.getByRole("button", { name: "找相似" }));

    expect(screen.queryByRole("dialog")).toBeNull();
    expect(await screen.findByRole("button", { name: "查看 photo:c.jpg" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "相似的照片" })).toBeTruthy();
    expect(screen.getByText("2 张")).toBeTruthy();
    expect(document.querySelector(".ph-similar-source strong")?.textContent).toBe("photo:a.jpg");
    await user.click(screen.getByRole("button", { name: "返回" }));
    expect(await screen.findByRole("button", { name: "查看 photo:b.jpg" })).toBeTruthy();

    // From a search, S asks from the viewer and going back returns to the search.
    await user.type(screen.getByRole("searchbox", { name: "搜索照片" }), "海边{Enter}");
    await openPhoto(user, "photo:sea.jpg");
    await user.keyboard("s");
    expect(await screen.findByText("这张照片还在整理中")).toBeTruthy();
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/photos/assets/photo%3Asea/similar?limit=120", expect.anything());
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
    const fetchMock = serve(
      async (url, init) => {
        if (url === "/api/v1/photos/image-searches" && init?.method === "POST") {
          if (!available)
            return json(
              { error: { code: "ai_unavailable", message: "local AI cannot read the image right now" } },
              503,
            );
          const image = (init.body as FormData).get("file") as File;
          return json({ id: `${await image.text()}-${++posted}` }, 201);
        }
        const id = url.match(/^\/api\/v1\/photos\/image-searches\/([^/]+)\/assets\?limit=120$/)?.[1];
        if (!id) return undefined;
        if (expired.has(id))
          return json({ error: { code: "search_expired", message: "the image search is no longer kept" } }, 404);
        return json({
          items: id.startsWith("cat") ? [asset("photo:c"), asset("photo:d", { libraryId: sharedLibrary.id })] : [],
        });
      },
      timeline(privateLibrary.id, () => [asset("photo:a")]),
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

    // Chosen with the camera at the end of the search box.
    chosen.push(new File(["cat"], "cat.jpg", { type: "image/jpeg" }));
    await user.click(screen.getByRole("button", { name: "以图搜图" }));
    expect(await screen.findByRole("button", { name: "查看 photo:c.jpg" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "以图搜图" })).toBeTruthy();
    expect(screen.getByText("2 张")).toBeTruthy();
    expect(document.querySelector(".ph-image-source strong")?.textContent).toBe("cat.jpg");
    expect(screen.getByText("按画面和画中物品的相似程度排列，最像的在前")).toBeTruthy();
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/photos/image-searches",
      expect.objectContaining({ method: "POST" }),
    );
    const post = fetchMock.mock.calls.find(([url]) => url === "/api/v1/photos/image-searches")!;
    expect(new Headers(post[1]?.headers).get("X-CSRF-Token")).toBe("csrf-photo");
    // Results mix libraries, so the shared photo says where it is from.
    expect(
      screen.getByRole("button", { name: "查看 photo:d.jpg" }).closest(".ph-tile")?.querySelector(".ph-tile-source")
        ?.textContent,
    ).toBe("共享");

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
    const app = document.querySelector(".ph-app")!;
    const box = screen.getByRole("search");
    const dog = { types: ["Files"], files: [new File(["dog"], "dog.png", { type: "image/png" })] };
    fireEvent.dragEnter(app, { dataTransfer: dog });
    fireEvent.dragOver(box, { dataTransfer: dog });
    expect(screen.getByText("松开以图搜图")).toBeTruthy();
    fireEvent.drop(box, { dataTransfer: dog });
    expect(await screen.findByText("没有找到相似的照片")).toBeTruthy();
    expect(document.querySelector(".ph-image-source strong")?.textContent).toBe("dog.png");
    expect(FakeUpload.sent).toHaveLength(0);
    expect(screen.queryByText("松开以图搜图")).toBeNull();

    // When local AI cannot read it, the image can be tried again.
    available = false;
    await user.click(screen.getByRole("button", { name: "AI 聚合" }));
    const hero = await screen.findByRole("searchbox", { name: "用一句话搜索照片" });
    fireEvent.paste(hero, { clipboardData: { files: [new File(["cat"], "image.png", { type: "image/png" })] } });
    expect(await screen.findByText("没能识别这张图片", { selector: "strong" })).toBeTruthy();
    expect(screen.getByText("暂时无法识别图片，请稍后再试")).toBeTruthy();
    expect(document.querySelector(".ph-image-source strong")?.textContent).toBe("粘贴的图片");
    available = true;
    await user.click(screen.getByRole("button", { name: "重试" }));
    expect(await screen.findByRole("button", { name: "查看 photo:c.jpg" })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "返回" }));
    expect(await screen.findByRole("heading", { name: "alice，您可以用一句话来找照片" })).toBeTruthy();
  });

  it("names where results come from and narrows a search to one library", async () => {
    const sea = asset("photo:sea", { libraryId: sharedLibrary.id, uploadedBy: "user:bob" });
    const beach = asset("photo:beach");
    const fetchMock = serve(
      (url) => {
        if (!url.startsWith("/api/v1/photos/search?")) return undefined;
        const library = new URL(url, "http://nas").searchParams.get("library");
        const items = [sea, beach].filter((photo) => !library || photo.libraryId === library);
        return json({ items, semantic: true, closest: items.length });
      },
      timeline(privateLibrary.id, () => [asset("photo:home")]),
    );
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    await screen.findByRole("button", { name: "查看 photo:home.jpg" });
    await user.type(screen.getByRole("searchbox", { name: "搜索照片" }), "海边{Enter}");

    expect(await screen.findByText("找到 2 张 · 我的图库和共享图库")).toBeTruthy();
    const scope = screen.getByRole("radiogroup", { name: "搜索范围" });
    expect(within(scope).getByRole("radio", { name: "全部", checked: true })).toBeTruthy();
    // Results mixing libraries mark the shared photo, not the caller's own.
    const tile = (name: string) => screen.getByRole("button", { name: `查看 ${name}` }).closest(".ph-tile")!;
    expect(tile("photo:sea.jpg").querySelector(".ph-tile-source")?.textContent).toBe("共享");
    expect(tile("photo:beach.jpg").querySelector(".ph-tile-source")).toBeNull();

    await user.click(within(scope).getByRole("radio", { name: "共享" }));
    expect(await screen.findByText("找到 1 张 · 共享图库")).toBeTruthy();
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/photos/search?q=%E6%B5%B7%E8%BE%B9&limit=120&library=library%3Ashared",
      expect.anything(),
    );
    expect(screen.queryByRole("button", { name: "查看 photo:beach.jpg" })).toBeNull();
    expect(tile("photo:sea.jpg").querySelector(".ph-tile-source")).toBeNull();

    // The choice holds for the next search.
    const box = screen.getByRole("searchbox", { name: "搜索照片" });
    await user.clear(box);
    await user.type(box, "沙滩{Enter}");
    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        "/api/v1/photos/search?q=%E6%B2%99%E6%BB%A9&limit=120&library=library%3Ashared",
        expect.anything(),
      ),
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
    const fetchMock = serve(
      (url, init) => {
        if (url === "/api/v1/photos/libraries")
          return json({ items: ended ? [own, sharedLibrary] : [own, sharedLibrary, viewed] });
        if (url === "/api/v1/viewing/viewing%3A7" && init?.method === "DELETE") {
          ended = true;
          return new Response(null, { status: 204 });
        }
        if (url.startsWith("/api/v1/photos/search")) return json({ items: [], semantic: true, closest: 0 });
        return undefined;
      },
      timeline(viewed.id, () => [asset("photo:w", { libraryId: viewed.id })]),
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
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/photos/search?q=%E7%8C%AB&limit=120&viewing=library%3Aalice",
      expect.anything(),
    );

    await user.click(screen.getByRole("button", { name: "结束查看" }));
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/viewing/viewing%3A7",
      expect.objectContaining({ method: "DELETE" }),
    );
    await waitFor(() => expect(screen.queryByRole("tab", { name: "alice" })).toBeNull());
  });

  it("reports an unavailable photo service and recovers on retry", async () => {
    let available = false;
    serve((url) =>
      url === "/api/v1/photos/libraries" && !available
        ? json(
            { error: { code: "photos_unavailable", message: "the photo library is not available on this device" } },
            503,
          )
        : undefined,
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
    serve(
      timeline(privateLibrary.id, () =>
        uploaded ? [asset("photo:live", { name: "live.heic", motion: { kind: "live-photo", duration: 2 } })] : [],
      ),
    );
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    await screen.findByText("把照片拖到这里");
    fireEvent.drop(document.querySelector(".ph-app")!, {
      dataTransfer: { types: ["Files"], files: [new File(["mov"], "live.MOV"), new File(["heic"], "live.heic")] },
    });
    await screen.findByRole("button", { name: "查看 live.heic" });
    expect(FakeUpload.sent).toHaveLength(1);
    expect(FakeUpload.sent[0].url).toContain("/live-photos");
    expect((FakeUpload.sent[0].body?.get("motion") as File).name).toBe("live.MOV");
  });
  it("loads video only on request and removes it on navigation or failure", async () => {
    const first = asset("photo:live", { mediaType: "image/heic", motion: { kind: "live-photo", duration: 2 } });
    serve(timeline(privateLibrary.id, () => [first, asset("photo:next")]));
    render(<PhotosPanel userId="user:alice" userName="alice" isAdmin={false} />);
    const user = userEvent.setup();
    await openPhoto(user, first.name);
    expect(document.querySelector("video")).toBeNull();
    expect(document.querySelector<HTMLImageElement>(".ph-viewer-full")?.getAttribute("src")).toContain("/preview");
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
