import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setSession } from "../api";
import { PhotosPanel } from "./PhotosPanel";
import type { PhotoAlbum, PhotoAsset, PhotoLibrary } from "./photosApi";

const privateLibrary: PhotoLibrary = { id: "library:mine", kind: "private", ownerUserId: "user:alice", createdAt: "2026-10-08T00:00:00Z" };
const sharedLibrary: PhotoLibrary = { id: "library:shared", kind: "shared", createdAt: "2026-10-08T00:00:00Z" };
const trip: PhotoAlbum = { id: "album:trip", libraryId: privateLibrary.id, name: "旅行", createdBy: "user:alice", createdAt: "2026-10-09T00:00:00Z", photos: 1, coverId: "photo:a" };

function asset(id: string, overrides: Partial<PhotoAsset> = {}): PhotoAsset {
  return {
    id, libraryId: privateLibrary.id, name: `${id}.jpg`, mediaType: "image/jpeg", sizeBytes: 2_400_000, uploadedBy: "user:alice",
    importedAt: "2026-10-08T09:00:00Z", takenAt: "2026-05-01T08:00:00Z", width: 4000, height: 3000, thumbnail: "ready", ...overrides,
  };
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

type Route = (url: string, init?: RequestInit) => Response | Promise<Response> | undefined;

// timeline serves one library's photos the way the photo service does: by
// month of their capture (or import) time.
function timeline(libraryId: string, photos: () => PhotoAsset[]): Route {
  const path = `/api/v1/photos/libraries/${encodeURIComponent(libraryId)}/timeline`;
  const month = (photo: PhotoAsset) => (photo.takenAt ?? photo.importedAt).slice(0, 7);
  return (url) => {
    if (url === `${path}/months`) {
      const counts = new Map<string, number>();
      for (const photo of photos()) counts.set(month(photo), (counts.get(month(photo)) ?? 0) + 1);
      return json({ items: [...counts].sort((a, b) => b[0].localeCompare(a[0])).map(([key, value]) => ({ month: key, photos: value })) });
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
    if (url.startsWith("/api/v1/photos/labels")) return json({ items: [], ready: false });
    if (url.endsWith("/timeline/months")) return json({ items: [] });
    if (/\/assets\/[^/]+$/.test(url) && !init?.method) return json(asset(decodeURIComponent(url.split("/").at(-1) ?? "")));
    return json({ items: [] });
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

// FakeUpload stands in for XMLHttpRequest, which uploads use for progress.
class FakeUpload {
  static sent: FakeUpload[] = [];
  static respond: (upload: FakeUpload) => { status: number; body: unknown } = () => ({ status: 201, body: asset("photo:new") });
  upload: { onprogress: ((event: ProgressEvent) => void) | null } = { onprogress: null };
  status = 0;
  responseText = "";
  url = "";
  headers: Record<string, string> = {};
  body?: FormData;
  onload: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onabort: (() => void) | null = null;
  open(_: string, url: string) { this.url = url; }
  setRequestHeader(name: string, value: string) { this.headers[name] = value; }
  abort() { /* uploads are not cancelled in these tests */ }
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

const alice = { csrfToken: "csrf-photo", expiresAt: "", user: { id: "user:alice", username: "alice", role: "member" as const, status: "active" as const, createdAt: "" } };
const sizes = Object.getOwnPropertyDescriptors(HTMLElement.prototype);

beforeEach(() => {
  // jsdom lays nothing out; the photo scroller gets a window-sized view.
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", { configurable: true, get() { return this.classList?.contains("ph-scroll") ? 800 : 0; } });
  Object.defineProperty(HTMLElement.prototype, "offsetWidth", { configurable: true, get() { return this.classList?.contains("ph-scroll") ? 1000 : 0; } });
  vi.stubGlobal("ResizeObserver", class { observe() { /* no layout in jsdom */ } unobserve() { /* none */ } disconnect() { /* none */ } });
  setSession(alice);
});

afterEach(() => {
  vi.unstubAllGlobals();
  for (const name of ["offsetHeight", "offsetWidth"] as const) Object.defineProperty(HTMLElement.prototype, name, sizes[name]);
  window.localStorage.clear();
  FakeUpload.sent = [];
  setSession();
});

const openPhoto = async (user: ReturnType<typeof userEvent.setup>, name: string) => user.click(await screen.findByRole("button", { name: `查看 ${name}` }));

describe("PhotosPanel timeline", () => {
  it("opens the caller's library by month and day, with thumbnails or originals", async () => {
    serve(timeline(privateLibrary.id, () => [
      asset("photo:a"), asset("photo:b", { thumbnail: "pending", duplicate: "duplicate" }), asset("photo:c", { takenAt: undefined }),
    ]));
    render(<PhotosPanel userId="user:alice" isAdmin={false} />);

    expect(await screen.findByRole("heading", { name: "2026年10月" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "2026年5月" })).toBeTruthy();
    expect(screen.getByRole("tab", { name: "我的", selected: true })).toBeTruthy();
    expect(await screen.findByText("3 张照片")).toBeTruthy();
    const a = await screen.findByRole("img", { name: "photo:a.jpg" });
    expect(a.getAttribute("src")).toBe("/api/v1/photos/assets/photo%3Aa/thumbnail");
    expect(screen.getByRole("img", { name: "photo:b.jpg" }).getAttribute("src")).toBe("/api/v1/photos/assets/photo%3Ab/original");
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
        (url) => url === "/api/v1/photos/assets/photo%3Ap" ? json(asset("photo:p", { thumbnail: ready ? "ready" : "pending" })) : undefined,
        timeline(privateLibrary.id, () => [asset("photo:p", { thumbnail: "pending" })]),
      );
      render(<PhotosPanel userId="user:alice" isAdmin={false} />);
      expect((await screen.findByRole("img", { name: "photo:p.jpg" })).getAttribute("src")).toBe("/api/v1/photos/assets/photo%3Ap/original");
      ready = true;
      await vi.advanceTimersByTimeAsync(3000);
      await vi.waitFor(() => expect(screen.getByRole("img", { name: "photo:p.jpg" }).getAttribute("src")).toBe("/api/v1/photos/assets/photo%3Ap/thumbnail"));
    } finally {
      vi.useRealTimers();
    }
  });

  it("uploads dropped photos, shows them at once and reloads the timeline", async () => {
    vi.stubGlobal("XMLHttpRequest", FakeUpload);
    vi.stubGlobal("URL", Object.assign(URL, { createObjectURL: () => "blob:preview", revokeObjectURL: () => undefined }));
    let uploaded = false;
    FakeUpload.respond = (upload) => {
      const file = upload.body?.get("file") as File;
      uploaded = true;
      return { status: 201, body: asset("photo:new", { name: file.name }) };
    };
    serve(timeline(privateLibrary.id, () => uploaded ? [asset("photo:new", { name: "cat.jpg" })] : []));
    render(<PhotosPanel userId="user:alice" isAdmin={false} />);
    expect(await screen.findByText("把照片拖到这里")).toBeTruthy();

    const app = document.querySelector(".ph-app") as HTMLElement;
    const files = [new File(["jpeg"], "cat.jpg", { type: "image/jpeg" }), new File(["text"], "notes.txt", { type: "text/plain" })];
    fireEvent.dragEnter(app, { dataTransfer: { types: ["Files"], files } });
    expect(screen.getByText("照片会保存到我的图库")).toBeTruthy();
    fireEvent.drop(app, { dataTransfer: { types: ["Files"], files } });

    expect(await screen.findByRole("button", { name: "查看 cat.jpg" })).toBeTruthy();
    expect(await screen.findByText("照片已保存到我的图库")).toBeTruthy();
    expect(FakeUpload.sent).toHaveLength(1);
    expect(FakeUpload.sent[0].url).toBe("/api/v1/photos/libraries/library%3Amine/uploads");
    expect(FakeUpload.sent[0].headers["X-CSRF-Token"]).toBe("csrf-photo");
    // The text file never left the browser; the queue says why.
    expect(screen.getByText("1 张未能上传")).toBeTruthy();
    // It never appears in the grid either.
    expect(screen.queryByText("只支持 JPEG 和 PNG 照片")).toBeNull();
    await userEvent.setup().click(screen.getByRole("button", { name: "展开上传队列" }));
    expect(screen.getByText("只支持 JPEG 和 PNG 照片")).toBeTruthy();
  });
});

describe("PhotosPanel viewer", () => {
  it("shows details, steps with the arrow keys and closes with Escape", async () => {
    serve(
      (url) => url === "/api/v1/photos/assets/photo%3Aa" ? json(asset("photo:a", { alsoKeptBy: ["bob", "carol"] })) : undefined,
      timeline(privateLibrary.id, () => [asset("photo:a", { alsoKeptBy: ["bob", "carol"] }), asset("photo:b", { takenAt: "2026-05-01T07:00:00Z" })]),
    );
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" isAdmin={false} />);
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

  it("moves a photo to the trash and undoes it", async () => {
    let trashed = false;
    const fetchMock = serve(
      (url, init) => {
        if (url === "/api/v1/photos/assets/photo%3Aa" && init?.method === "DELETE") { trashed = true; return json(asset("photo:a")); }
        if (url === "/api/v1/photos/assets/photo%3Aa/restore") { trashed = false; return json(asset("photo:a")); }
        return undefined;
      },
      timeline(privateLibrary.id, () => trashed ? [] : [asset("photo:a")]),
    );
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" isAdmin={false} />);
    await openPhoto(user, "photo:a.jpg");
    await user.click(screen.getByRole("button", { name: "移到回收站" }));

    expect(await screen.findByText("已移到回收站：1 张")).toBeTruthy();
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.queryByRole("button", { name: "查看 photo:a.jpg" })).toBeNull();
    await user.click(screen.getByRole("button", { name: "撤销" }));
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/photos/assets/photo%3Aa/restore", expect.objectContaining({ method: "POST" }));
    expect(await screen.findByRole("button", { name: "查看 photo:a.jpg" })).toBeTruthy();
  });

  it("offers changes to a shared photo only to its uploader or an administrator, and copies for anyone", async () => {
    const shared = asset("photo:s", { libraryId: sharedLibrary.id, uploadedBy: "user:bob" });
    const fetchMock = serve(
      (url, init) => url.endsWith("/copies") && init?.method === "POST" ? json(asset("photo:mine"), 201) : undefined,
      timeline(sharedLibrary.id, () => [shared]),
    );
    const user = userEvent.setup();
    const { unmount } = render(<PhotosPanel userId="user:alice" isAdmin={false} />);
    await user.click(await screen.findByRole("tab", { name: "共享" }));
    await openPhoto(user, "photo:s.jpg");
    expect(screen.queryByRole("button", { name: "移到回收站" })).toBeNull();
    expect(screen.queryByRole("button", { name: "重命名照片" })).toBeNull();
    await user.click(await screen.findByRole("button", { name: "复制到我的图库" }));
    expect(await screen.findByText("已复制 1 张到我的图库")).toBeTruthy();
    const copy = fetchMock.mock.calls.find(([url]) => String(url).endsWith("/copies"));
    expect(JSON.parse(String(copy?.[1]?.body))).toEqual({ libraryId: privateLibrary.id });
    unmount();

    render(<PhotosPanel userId="user:admin" isAdmin />);
    await user.click(await screen.findByRole("tab", { name: "共享" }));
    await openPhoto(user, "photo:s.jpg");
    expect(screen.getByRole("button", { name: "移到回收站" })).toBeTruthy();
  });

  it("tags a photo, hides a wrong AI label and opens the photos with a label", async () => {
    let details = asset("photo:cat", { aiLabels: [{ id: "cat", name: "猫", score: 0.76 }, { id: "dog", name: "狗", score: 0.7 }] });
    const fetchMock = serve(
      (url, init) => {
        if (url === "/api/v1/photos/assets/photo%3Acat" && !init?.method) return json(details);
        if (url === "/api/v1/photos/assets/photo%3Acat/tags" && init?.method === "POST") { details = { ...details, tags: ["小橘"] }; return json(details); }
        if (url === "/api/v1/photos/assets/photo%3Acat/ai-labels/dog" && init?.method === "DELETE") { details = { ...details, aiLabels: details.aiLabels?.slice(0, 1) }; return json(details); }
        if (url.startsWith("/api/v1/photos/search?q=%E7%8C%AB")) return json({ items: [asset("photo:cat"), asset("photo:kitten")], semantic: true, match: "labels", labels: [{ id: "cat", name: "猫" }] });
        return undefined;
      },
      timeline(privateLibrary.id, () => [asset("photo:cat")]),
    );
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" isAdmin={false} />);
    await openPhoto(user, "photo:cat.jpg");

    await user.type(await screen.findByRole("textbox", { name: "添加标签" }), "小橘{Enter}");
    expect(await within(screen.getByLabelText("标签")).findByRole("button", { name: "小橘" })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "隐藏 AI 标签 狗" }));
    await waitFor(() => expect(screen.queryByRole("button", { name: "狗" })).toBeNull());

    await user.click(within(screen.getByLabelText("AI 标签")).getByRole("button", { name: "猫" }));
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/photos/search?q=%E7%8C%AB&limit=120", expect.anything());
    expect(await screen.findByRole("button", { name: "查看 photo:kitten.jpg" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "AI 搜图" })).toBeTruthy();
    expect(screen.getByText("只显示本地 AI 识别为“猫”的照片，识别可能有误")).toBeTruthy();
    expect(screen.getByText("找到 2 张")).toBeTruthy();
  });
});

describe("PhotosPanel selection", () => {
  const photos = () => [asset("photo:1", { takenAt: "2026-05-03T08:00:00Z" }), asset("photo:2", { takenAt: "2026-05-02T08:00:00Z" }), asset("photo:3"), asset("photo:4", { takenAt: "2026-04-01T08:00:00Z" })];

  it("selects with the check, ranges with Shift, all with Ctrl+A and clears with Escape", async () => {
    serve(timeline(privateLibrary.id, photos));
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" isAdmin={false} />);
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
        if (match && init?.method === "DELETE") { trashed.add(decodeURIComponent(match[1])); return json(asset(decodeURIComponent(match[1]))); }
        return undefined;
      },
      timeline(privateLibrary.id, () => photos().filter((photo) => !trashed.has(photo.id))),
    );
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" isAdmin={false} />);
    await user.click(await screen.findByRole("checkbox", { name: /选择5月1日.*的 1 张照片/ }));
    await user.click(screen.getByRole("checkbox", { name: "选择 photo:2.jpg" }));
    await user.click(screen.getByRole("button", { name: "删除" }));

    expect(await screen.findByText("已移到回收站：2 张")).toBeTruthy();
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === "DELETE").map(([url]) => url).sort()).toEqual(["/api/v1/photos/assets/photo%3A2", "/api/v1/photos/assets/photo%3A3"]);
    expect(screen.queryByRole("button", { name: "查看 photo:3.jpg" })).toBeNull();
    expect(screen.getByRole("button", { name: "查看 photo:1.jpg" })).toBeTruthy();
  });

  it("adds the selection to an album, creating one on the way", async () => {
    const fetchMock = serve(
      (url, init) => {
        if (url === "/api/v1/photos/libraries/library%3Amine/albums" && init?.method === "POST") return json({ ...trip, id: "album:new", name: JSON.parse(String(init.body)).name, photos: 0 }, 201);
        if (url === "/api/v1/photos/libraries/library%3Amine/albums") return json({ items: [trip] });
        if (url.startsWith("/api/v1/photos/albums/") && init?.method === "POST") return json(asset("photo:1"));
        return undefined;
      },
      timeline(privateLibrary.id, photos),
    );
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" isAdmin={false} />);
    await user.click(await screen.findByRole("checkbox", { name: "选择 photo:1.jpg" }));
    await user.click(screen.getByRole("checkbox", { name: "选择 photo:2.jpg" }));
    await user.click(screen.getByRole("button", { name: "加入相册" }));
    const picker = await screen.findByRole("dialog", { name: "加入相册" });
    expect(within(picker).getByText("共享图库")).toBeTruthy();
    expect(within(picker).getByText("照片会先复制到共享图库")).toBeTruthy();
    await user.click(within(picker).getByRole("button", { name: /旅行/ }));

    expect(await screen.findByText("已加入相册“旅行”")).toBeTruthy();
    expect(fetchMock.mock.calls.filter(([url, init]) => url === "/api/v1/photos/albums/album%3Atrip/assets" && init?.method === "POST").map(([, init]) => JSON.parse(String(init?.body)).assetId).sort()).toEqual(["photo:1", "photo:2"]);

    await user.click(screen.getByRole("checkbox", { name: "选择 photo:4.jpg" }));
    await user.click(screen.getByRole("button", { name: "加入相册" }));
    await user.click(within(await screen.findByRole("dialog", { name: "加入相册" })).getByRole("button", { name: "新建相册" }));
    await user.type(screen.getByRole("textbox", { name: "新相册名称" }), "春天{Enter}");
    expect(await screen.findByText("已加入相册“春天”")).toBeTruthy();
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/photos/albums/album%3Anew/assets", expect.objectContaining({ method: "POST" }));
  });
});

describe("PhotosPanel albums, trash and labels", () => {
  it("lists albums, opens one, renames it and takes a photo out of it", async () => {
    let removed = false;
    const fetchMock = serve((url, init) => {
      if (url === "/api/v1/photos/libraries/library%3Amine/albums") return json({ items: [trip] });
      if (url.startsWith("/api/v1/photos/albums/album%3Atrip/assets?")) return json({ items: removed ? [] : [asset("photo:a")] });
      if (url === "/api/v1/photos/albums/album%3Atrip" && init?.method === "PATCH") return json({ ...trip, name: JSON.parse(String(init.body)).name });
      if (url === "/api/v1/photos/albums/album%3Atrip/assets/photo%3Aa" && init?.method === "DELETE") { removed = true; return new Response(null, { status: 204 }); }
      return undefined;
    });
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" isAdmin={false} />);
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
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/photos/albums/album%3Atrip/assets/photo%3Aa", expect.objectContaining({ method: "DELETE" }));
    expect(await screen.findByText(/照片仍在图库中/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "查看 photo:a.jpg" })).toBeNull();
  });

  it("restores from the trash and asks in the window before deleting for good", async () => {
    const purgeAfter = new Date(Date.now() + 3 * 86_400_000).toISOString();
    let items = [asset("photo:t1", { trash: { trashedAt: "2026-10-08T09:00:00Z", trashedBy: "user:alice", purgeAfter } }), asset("photo:t2", { trash: { trashedAt: "2026-10-08T09:00:00Z", trashedBy: "user:alice", purgeAfter } })];
    const fetchMock = serve((url, init) => {
      if (url.endsWith("/restore")) { items = items.filter((item) => !url.includes("photo%3At1")); return json(asset("photo:t1")); }
      if (url.startsWith("/api/v1/photos/trash/")) { items = []; return new Response(null, { status: 204 }); }
      if (url.endsWith("/trash") && !init?.method) return json({ items });
      return undefined;
    });
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" isAdmin={false} />);
    await user.click(await screen.findByRole("button", { name: "回收站" }));
    expect((await screen.findAllByText("3 天后删除")).length).toBe(2);

    // In the trash a click selects.
    await user.click(screen.getByRole("button", { name: "查看 photo:t1.jpg" }));
    await user.click(screen.getByRole("button", { name: "恢复" }));
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/photos/assets/photo%3At1/restore", expect.objectContaining({ method: "POST" }));
    expect(await screen.findByText("已恢复 1 张照片")).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "查看 photo:t2.jpg" }));
    await user.click(screen.getByRole("button", { name: "永久删除" }));
    const dialog = screen.getByRole("dialog", { name: "永久删除 1 张照片？" });
    await user.click(within(dialog).getByRole("button", { name: "取消" }));
    expect(fetchMock.mock.calls.some(([url]) => String(url).startsWith("/api/v1/photos/trash/"))).toBe(false);
    await user.click(screen.getByRole("button", { name: "永久删除" }));
    await user.click(within(screen.getByRole("dialog", { name: "永久删除 1 张照片？" })).getByRole("button", { name: "永久删除" }));
    expect(await screen.findByText("回收站是空的")).toBeTruthy();
  });

  it("starts AI search with recent searches and the things local AI recognises, and explains when it is off", async () => {
    let ready = true;
    const fetchMock = serve((url) => {
      if (url === "/api/v1/photos/labels") return json(ready
        ? { items: [{ id: "cat", name: "猫", category: "animal", photos: 8, coverId: "photo:cat" }, { id: "pizza", name: "披萨", category: "food", photos: 3, coverId: "photo:pizza" }], ready: true }
        : { items: [], ready: false });
      if (url === "/api/v1/photos/ai") return json(ready ? { state: "working", ready: 3, pending: 2, failed: 1 } : { state: "unavailable", ready: 0, pending: 5, failed: 0 });
      if (url.startsWith("/api/v1/photos/search?q=")) return json({ items: [asset("photo:cat")], semantic: true, match: "labels", labels: [{ id: "cat", name: "猫" }] });
      return undefined;
    });
    window.localStorage.setItem("a-nas.photos.recentSearches", JSON.stringify(["海边的日落"]));
    const user = userEvent.setup();
    const { unmount } = render(<PhotosPanel userId="user:alice" isAdmin={false} />);
    expect(await screen.findByText("正在整理 3 / 6")).toBeTruthy();
    expect(screen.getByText(/1 张无法识别/)).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "AI 搜图" }));
    expect(await screen.findByRole("heading", { name: "用一句话找照片" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: /^动物/ })).toBeTruthy();
    expect(within(await screen.findByRole("button", { name: "搜索 猫" })).getByText("8 张")).toBeTruthy();
    expect(within(screen.getByLabelText("最近搜索")).getByRole("button", { name: "海边的日落" })).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "搜索 猫" }));
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/photos/search?q=%E7%8C%AB&limit=120", expect.anything());
    expect(await screen.findByRole("button", { name: "查看 photo:cat.jpg" })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "返回" }));
    expect(within(await screen.findByLabelText("最近搜索")).getByRole("button", { name: "猫" })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "清除最近搜索" }));
    expect(screen.queryByLabelText("最近搜索")).toBeNull();
    unmount();

    ready = false;
    render(<PhotosPanel userId="user:alice" isAdmin={false} />);
    expect(await screen.findByText("本地 AI 未启用")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "AI 搜图" }));
    expect(await screen.findByText(/此设备还没有启用本地 AI/)).toBeTruthy();
  });
});

describe("PhotosPanel search and viewing", () => {
  it("searches from any page into AI search and says what the results are", async () => {
    const results: Record<string, object> = {
      "%E6%B5%B7%E8%BE%B9%E7%9A%84%E7%8C%AB": { items: [asset("photo:sea", { libraryId: sharedLibrary.id, uploadedBy: "user:bob" })], semantic: true, match: "labels", labels: [{ id: "cat", name: "猫" }] },
      "%E7%94%B5%E5%8A%A8%E8%BD%A6": { items: [asset("photo:bike")], semantic: true, match: "closest" },
      "%E9%BB%84%E6%98%8F": { items: [], semantic: false, match: "names" },
    };
    const fetchMock = serve(
      (url) => {
        const query = url.match(/^\/api\/v1\/photos\/search\?q=([^&]+)/)?.[1];
        return query ? json(results[query]) : undefined;
      },
      timeline(privateLibrary.id, () => [asset("photo:home")]),
    );
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" isAdmin={false} />);
    await screen.findByRole("button", { name: "查看 photo:home.jpg" });
    await user.type(screen.getByRole("searchbox", { name: "搜索照片" }), "海边的猫{Enter}");

    expect(fetchMock).toHaveBeenCalledWith("/api/v1/photos/search?q=%E6%B5%B7%E8%BE%B9%E7%9A%84%E7%8C%AB&limit=120", expect.anything());
    expect(await screen.findByRole("button", { name: "查看 photo:sea.jpg" })).toBeTruthy();
    expect(screen.getByText("只显示本地 AI 识别为“猫”的照片，识别可能有误")).toBeTruthy();
    await openPhoto(user, "photo:sea.jpg");
    expect(screen.queryByRole("button", { name: "移到回收站" })).toBeNull();
    await user.keyboard("{Escape}");

    const box = screen.getByRole("searchbox", { name: "搜索照片" });
    await user.clear(box);
    await user.type(box, "电动车{Enter}");
    expect(await screen.findByText("没有能确定的结果，以下是最接近的照片")).toBeTruthy();
    await user.clear(screen.getByRole("searchbox", { name: "搜索照片" }));
    await user.type(screen.getByRole("searchbox", { name: "搜索照片" }), "黄昏{Enter}");
    expect(await screen.findByText("本地 AI 暂不可用，只按名称和标签匹配")).toBeTruthy();
    expect(screen.getByText("没有找到相关照片")).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "返回" }));
    expect(await screen.findByRole("heading", { name: "用一句话找照片" })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: /^照片/ }));
    expect(await screen.findByRole("button", { name: "查看 photo:home.jpg" })).toBeTruthy();
    expect((screen.getByRole("searchbox", { name: "搜索照片" }) as HTMLInputElement).value).toBe("");
  });

  it("browses a viewed member library read-only, searches it only from there and ends the viewing", async () => {
    const viewed: PhotoLibrary = { id: "library:alice", kind: "private", ownerUserId: "user:alice", ownerName: "alice", createdAt: "2026-10-08T00:00:00Z", viewing: { grantId: "viewing:7", expiresAt: "2026-10-09T09:00:00Z" } };
    const own: PhotoLibrary = { ...privateLibrary, id: "library:admin", ownerUserId: "user:admin", ownerName: "admin" };
    let ended = false;
    const fetchMock = serve(
      (url, init) => {
        if (url === "/api/v1/photos/libraries") return json({ items: ended ? [own, sharedLibrary] : [own, sharedLibrary, viewed] });
        if (url === "/api/v1/viewing/viewing%3A7" && init?.method === "DELETE") { ended = true; return new Response(null, { status: 204 }); }
        if (url.startsWith("/api/v1/photos/search")) return json({ items: [], semantic: true, match: "closest" });
        return undefined;
      },
      timeline(viewed.id, () => [asset("photo:w", { libraryId: viewed.id })]),
    );
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:admin" isAdmin />);
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
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/photos/search?q=%E7%8C%AB&limit=120&viewing=library%3Aalice", expect.anything());

    await user.click(screen.getByRole("button", { name: "结束查看" }));
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/viewing/viewing%3A7", expect.objectContaining({ method: "DELETE" }));
    await waitFor(() => expect(screen.queryByRole("tab", { name: "alice" })).toBeNull());
  });

  it("reports an unavailable photo service and recovers on retry", async () => {
    let available = false;
    serve((url) => url === "/api/v1/photos/libraries" && !available
      ? json({ error: { code: "photos_unavailable", message: "the photo library is not available on this device" } }, 503)
      : undefined);
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" isAdmin={false} />);
    expect(await screen.findByText(/相册暂时不可用：数据卷未就绪/)).toBeTruthy();
    available = true;
    await act(async () => { await user.click(screen.getByRole("button", { name: "重试" })); });
    expect(await screen.findByRole("tab", { name: "我的" })).toBeTruthy();
  });
});
