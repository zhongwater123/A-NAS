import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { setSession } from "./api";
import { PhotosPanel } from "./PhotosPanel";
import type { PhotoAsset, PhotoLibrary } from "./photosApi";

const privateLibrary: PhotoLibrary = { id: "library:mine", kind: "private", ownerUserId: "user:alice", createdAt: "2026-10-08T00:00:00Z" };
const sharedLibrary: PhotoLibrary = { id: "library:shared", kind: "shared", createdAt: "2026-10-08T00:00:00Z" };

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

function serve(...routes: Route[]) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    for (const route of routes) {
      const response = await route(url, init);
      if (response) return response;
    }
    if (url === "/api/v1/photos/libraries") return json({ items: [sharedLibrary, privateLibrary] });
    if (url.includes("/trash")) return json({ items: [] });
    return json({ items: [] });
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

afterEach(() => { vi.unstubAllGlobals(); setSession(); });

describe("PhotosPanel", () => {
  it("opens the caller's own library and shows thumbnails or originals by day", async () => {
    serve((url) => url.startsWith("/api/v1/photos/libraries/library%3Amine/timeline") ? json({
      items: [asset("photo:a"), asset("photo:b", { thumbnail: "pending", duplicate: "duplicate" }), asset("photo:c", { takenAt: undefined })],
    }) : undefined);
    render(<PhotosPanel userId="user:alice" isAdmin={false} />);

    const may = await screen.findByRole("region", { name: "2026年5月1日" });
    expect((screen.getByRole("combobox", { name: "图库" }) as HTMLSelectElement).value).toBe("library:mine");
    const images = within(may).getAllByRole("img");
    expect(images[0].getAttribute("src")).toBe("/api/v1/photos/assets/photo%3Aa/thumbnail");
    expect(images[1].getAttribute("src")).toBe("/api/v1/photos/assets/photo%3Ab/original");
    expect(within(may).getByText("重复")).toBeTruthy();
    // Without a capture time the import day is used.
    expect(screen.getByRole("region", { name: "2026年10月8日" })).toBeTruthy();

    // A ready thumbnail that cannot be fetched falls back to the original.
    fireEvent.error(images[0]);
    expect(images[0].getAttribute("src")).toBe("/api/v1/photos/assets/photo%3Aa/original");
  });

  it("uploads several photos, reports the ones that failed and reloads", async () => {
    setSession({ csrfToken: "csrf-photo", expiresAt: "", user: { id: "user:alice", username: "alice", role: "member", status: "active", createdAt: "" } });
    let timelineCalls = 0;
    const fetchMock = serve((url, init) => {
      if (url.endsWith("/uploads")) {
        const file = (init?.body as FormData).get("file") as File;
        return file.name === "notes.txt"
          ? json({ error: { code: "unsupported_media_type", message: "only JPEG and PNG photos are supported" } }, 415)
          : json(asset("photo:new"), 201);
      }
      if (url.includes("/timeline")) { timelineCalls++; return json({ items: timelineCalls > 1 ? [asset("photo:new")] : [] }); }
      return undefined;
    });
    const user = userEvent.setup({ applyAccept: false });
    render(<PhotosPanel userId="user:alice" isAdmin={false} />);
    expect(await screen.findByText(/还没有照片/)).toBeTruthy();

    await user.upload(screen.getByLabelText("上传照片"), [
      new File(["jpeg"], "cat.jpg", { type: "image/jpeg" }),
      new File(["text"], "notes.txt", { type: "text/plain" }),
    ]);

    expect((await screen.findByRole("alert")).textContent).toContain("1 张未能导入。notes.txt：只支持 JPEG 和 PNG 照片");
    expect(await screen.findByRole("button", { name: "查看 photo:new.jpg" })).toBeTruthy();
    const uploads = fetchMock.mock.calls.filter(([url]) => String(url).endsWith("/uploads"));
    expect(uploads).toHaveLength(2);
    expect(new Headers(uploads[0][1]?.headers).get("X-CSRF-Token")).toBe("csrf-photo");
  });

  it("shows details in the viewer and moves a photo to the trash", async () => {
    let trashed = false;
    const fetchMock = serve((url, init) => {
      if (url === "/api/v1/photos/assets/photo%3Aa/copies" && init?.method === "POST") return json(asset("photo:copy", { libraryId: sharedLibrary.id }), 201);
      if (url === "/api/v1/photos/assets/photo%3Aa" && init?.method === "DELETE") { trashed = true; return json(asset("photo:a")); }
      if (url.includes("/timeline")) return json({ items: trashed ? [] : [asset("photo:a")] });
      return undefined;
    });
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" isAdmin={false} />);
    await user.click(await screen.findByRole("button", { name: "查看 photo:a.jpg" }));

    const viewer = screen.getByRole("dialog", { name: "查看 photo:a.jpg" });
    expect(within(viewer).getByText("4000 × 3000")).toBeTruthy();
    expect(within(viewer).getByRole("link", { name: "下载原图" }).getAttribute("href")).toBe("/api/v1/photos/assets/photo%3Aa/original?download=1");
    await user.click(within(viewer).getByRole("button", { name: "复制到共享图库" }));
    expect((await within(viewer).findByRole("status")).textContent).toBe("已复制到共享图库");
    expect(within(viewer).queryByRole("button", { name: "复制到我的图库" })).toBeNull();
    await user.click(within(viewer).getByRole("button", { name: "移到回收站" }));

    expect(await screen.findByText(/还没有照片/)).toBeTruthy();
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/photos/assets/photo%3Aa", expect.objectContaining({ method: "DELETE" }));
  });

  it("closes the viewer with Escape and steps through photos with the arrow keys", async () => {
    serve((url) => url.includes("/timeline") ? json({ items: [asset("photo:a"), asset("photo:b")] }) : undefined);
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" isAdmin={false} />);
    await user.click(await screen.findByRole("button", { name: "查看 photo:a.jpg" }));
    await user.keyboard("{ArrowRight}");
    expect(screen.getByRole("dialog", { name: "查看 photo:b.jpg" })).toBeTruthy();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("offers changes to a shared photo only to its uploader or an administrator", async () => {
    const shared = asset("photo:s", { libraryId: sharedLibrary.id, uploadedBy: "user:bob" });
    serve((url) => url.includes("library%3Ashared/timeline") ? json({ items: [shared] }) : url.includes("/timeline") ? json({ items: [] }) : undefined);
    const user = userEvent.setup();
    const { unmount } = render(<PhotosPanel userId="user:alice" isAdmin={false} />);
    await user.selectOptions(await screen.findByRole("combobox", { name: "图库" }), "library:shared");
    await user.click(await screen.findByRole("button", { name: "查看 photo:s.jpg" }));
    expect(screen.queryByRole("button", { name: "移到回收站" })).toBeNull();
    expect(screen.queryByRole("button", { name: "重命名" })).toBeNull();
    unmount();

    render(<PhotosPanel userId="user:admin" isAdmin />);
    await user.selectOptions(await screen.findByRole("combobox", { name: "图库" }), "library:shared");
    await user.click(await screen.findByRole("button", { name: "查看 photo:s.jpg" }));
    expect(screen.getByRole("button", { name: "移到回收站" })).toBeTruthy();
  });

  it("lets any member copy a shared photo into their own library", async () => {
    const shared = asset("photo:s", { libraryId: sharedLibrary.id, uploadedBy: "user:bob" });
    const fetchMock = serve((url, init) => {
      if (url.endsWith("/copies") && init?.method === "POST") return json(asset("photo:mine"), 201);
      return url.includes("library%3Ashared/timeline") ? json({ items: [shared] }) : url.includes("/timeline") ? json({ items: [] }) : undefined;
    });
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" isAdmin={false} />);
    await user.selectOptions(await screen.findByRole("combobox", { name: "图库" }), "library:shared");
    await user.click(await screen.findByRole("button", { name: "查看 photo:s.jpg" }));
    expect(screen.queryByRole("button", { name: "复制到共享图库" })).toBeNull();
    await user.click(screen.getByRole("button", { name: "复制到我的图库" }));

    expect((await screen.findByRole("status")).textContent).toBe("已复制到我的图库");
    const copy = fetchMock.mock.calls.find(([url]) => String(url).endsWith("/copies"));
    expect(JSON.parse(String(copy?.[1]?.body))).toEqual({ libraryId: privateLibrary.id });
  });

  it("asks before permanently deleting a single photo", async () => {
    const trashed = asset("photo:t", { trash: { trashedAt: "2026-10-08T09:00:00Z", trashedBy: "user:alice", purgeAfter: "2026-10-23T09:00:00Z" } });
    const fetchMock = serve((url) => url.endsWith("/trash") ? json({ items: [trashed] }) : undefined);
    const confirm = vi.fn(() => false);
    vi.stubGlobal("confirm", confirm);
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" isAdmin={false} />);
    await user.click(await screen.findByRole("tab", { name: "回收站" }));
    await user.click(await screen.findByRole("button", { name: "永久删除" }));

    expect(confirm).toHaveBeenCalledWith("永久删除“photo:t.jpg”？此操作无法撤销。");
    expect(fetchMock.mock.calls.some(([url]) => String(url).startsWith("/api/v1/photos/trash/"))).toBe(false);
  });

  it("switches a tile to its thumbnail once the thumbnail is ready", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      let ready = false;
      serve((url) => {
        if (url === "/api/v1/photos/assets/photo%3Ap") return json(asset("photo:p", { thumbnail: ready ? "ready" : "pending" }));
        return url.includes("/timeline") ? json({ items: [asset("photo:p", { thumbnail: "pending" })] }) : undefined;
      });
      render(<PhotosPanel userId="user:alice" isAdmin={false} />);
      expect((await screen.findByRole("img", { name: "photo:p.jpg" })).getAttribute("src")).toBe("/api/v1/photos/assets/photo%3Ap/original");

      ready = true;
      await vi.advanceTimersByTimeAsync(3000);
      await vi.waitFor(() => expect(screen.getByRole("img", { name: "photo:p.jpg" }).getAttribute("src")).toBe("/api/v1/photos/assets/photo%3Ap/thumbnail"));
    } finally {
      vi.useRealTimers();
    }
  });

  it("restores, purges and empties the trash", async () => {
    let items = [asset("photo:t1", { trash: { trashedAt: "2026-10-08T09:00:00Z", trashedBy: "user:alice", purgeAfter: "2026-10-23T09:00:00Z" } }), asset("photo:t2")];
    const fetchMock = serve((url, init) => {
      if (url.endsWith("/restore")) { items = items.filter((item) => item.id !== "photo:t1"); return json(asset("photo:t1")); }
      if (url.startsWith("/api/v1/photos/trash/")) { items = []; return new Response(null, { status: 204 }); }
      if (url.endsWith("/trash") && init?.method === "DELETE") { items = []; return json({ purged: 1 }); }
      if (url.endsWith("/trash")) return json({ items });
      return undefined;
    });
    vi.stubGlobal("confirm", () => true);
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" isAdmin={false} />);
    await user.click(await screen.findByRole("tab", { name: "回收站" }));

    const first = (await screen.findByText("photo:t1.jpg")).closest(".data-row") as HTMLElement;
    await user.click(within(first).getByRole("button", { name: "恢复" }));
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/photos/assets/photo%3At1/restore", expect.objectContaining({ method: "POST" }));
    const second = (await screen.findByText("photo:t2.jpg")).closest(".data-row") as HTMLElement;
    await user.click(within(second).getByRole("button", { name: "永久删除" }));
    expect(await screen.findByText("回收站为空")).toBeTruthy();
  });

  it("loads the next timeline page on request", async () => {
    serve((url) => {
      if (!url.includes("/timeline")) return undefined;
      return url.includes("cursor=") ? json({ items: [asset("photo:older", { takenAt: "2025-01-01T00:00:00Z" })] }) : json({ items: [asset("photo:newer")], next: "page-2" });
    });
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" isAdmin={false} />);
    await user.click(await screen.findByRole("button", { name: "加载更多" }));
    expect(await screen.findByRole("button", { name: "查看 photo:older.jpg" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "查看 photo:newer.jpg" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "加载更多" })).toBeNull();
  });
});

describe("PhotosPanel availability", () => {
  it("reports an unavailable photo service and recovers on refresh", async () => {
    let available = false;
    serve((url) => url === "/api/v1/photos/libraries" && !available
      ? json({ error: { code: "photos_unavailable", message: "the photo library is not available on this device" } }, 503)
      : undefined);
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" isAdmin={false} />);
    expect((await screen.findByRole("alert")).textContent).toContain("相册暂时不可用");
    expect(screen.queryByText(/还没有照片/)).toBeNull();

    available = true;
    await user.click(screen.getByRole("button", { name: "刷新相册" }));
    expect(await screen.findByRole("option", { name: "我的图库" })).toBeTruthy();
    await vi.waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
  });
});

describe("PhotosPanel viewing and hints", () => {
  it("browses a viewed member library read-only and can end the viewing", async () => {
    const viewed: PhotoLibrary = { id: "library:alice", kind: "private", ownerUserId: "user:alice", ownerName: "alice", createdAt: "2026-10-08T00:00:00Z", viewing: { grantId: "viewing:7", expiresAt: "2026-10-09T09:00:00Z" } };
    const own: PhotoLibrary = { ...privateLibrary, id: "library:admin", ownerUserId: "user:admin", ownerName: "admin" };
    let ended = false;
    const fetchMock = serve((url, init) => {
      if (url === "/api/v1/photos/libraries") return json({ items: ended ? [own, sharedLibrary] : [own, sharedLibrary, viewed] });
      if (url === "/api/v1/viewing/viewing%3A7" && init?.method === "DELETE") { ended = true; return new Response(null, { status: 204 }); }
      if (url.includes("library%3Aalice/timeline")) return json({ items: [asset("photo:w", { libraryId: viewed.id, uploadedBy: "user:alice" })] });
      if (url.includes("/timeline")) return json({ items: [] });
      return undefined;
    });
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:admin" isAdmin />);
    const select = await screen.findByRole("combobox", { name: "图库" });
    expect(screen.getByRole("option", { name: "只读查看 · alice" })).toBeTruthy();
    await user.selectOptions(select, "library:alice");
    expect((await screen.findByRole("status")).textContent).toContain("只读查看 alice 的私有图库");
    expect(screen.queryByLabelText("上传照片")).toBeNull();
    expect(screen.queryByRole("tab", { name: "回收站" })).toBeNull();
    await user.click(await screen.findByRole("button", { name: "查看 photo:w.jpg" }));
    expect(screen.queryByRole("button", { name: "移到回收站" })).toBeNull();
    expect(screen.queryByRole("button", { name: "重命名" })).toBeNull();
    expect(screen.queryByRole("button", { name: "复制到共享图库" })).toBeNull();
    expect(screen.getByRole("link", { name: "下载原图" })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "关闭查看" }));
    await user.click(screen.getByRole("button", { name: "结束查看" }));
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/viewing/viewing%3A7", expect.objectContaining({ method: "DELETE" }));
    expect(await screen.findByRole("option", { name: "我的图库" })).toBeTruthy();
    expect(screen.queryByRole("option", { name: "只读查看 · alice" })).toBeNull();
  });

  it("names other members who kept the same photo", async () => {
    serve((url) => url.includes("/timeline") ? json({ items: [asset("photo:a", { alsoKeptBy: ["bob", "carol"] })] }) : undefined);
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" isAdmin={false} />);
    await user.click(await screen.findByRole("button", { name: "查看 photo:a.jpg" }));
    expect(screen.getByText("bob、carol 也保存了相同的照片。")).toBeTruthy();
  });
});

describe("PhotosPanel search", () => {
  it("searches the caller's and the shared library, says when only names matched and returns to the timeline", async () => {
    const fetchMock = serve((url) => {
      if (url.startsWith("/api/v1/photos/search")) return json({ items: [asset("photo:sea", { libraryId: sharedLibrary.id, uploadedBy: "user:bob" })], semantic: false });
      if (url.includes("/timeline")) return json({ items: [asset("photo:home")] });
      return undefined;
    });
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:alice" isAdmin={false} />);
    await screen.findByRole("button", { name: "查看 photo:home.jpg" });

    await user.type(screen.getByRole("searchbox", { name: "搜索照片" }), "海边的猫{Enter}");
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/photos/search?q=%E6%B5%B7%E8%BE%B9%E7%9A%84%E7%8C%AB&limit=120", expect.anything());
    await user.click(await screen.findByRole("button", { name: "查看 photo:sea.jpg" }));
    expect(screen.getByText("智能搜索暂不可用，只按照片名称匹配。")).toBeTruthy();
    // A shared photo someone else uploaded: Alice may copy it but not change it.
    expect(screen.getByRole("button", { name: "复制到我的图库" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "移到回收站" })).toBeNull();
    await user.click(screen.getByRole("button", { name: "关闭查看" }));

    await user.click(screen.getByRole("button", { name: "清除搜索" }));
    expect(await screen.findByRole("button", { name: "查看 photo:home.jpg" })).toBeTruthy();
    expect((screen.getByRole("searchbox", { name: "搜索照片" }) as HTMLInputElement).value).toBe("");
  });

  it("adds the viewed member library only while viewing it", async () => {
    const viewed: PhotoLibrary = { id: "library:alice", kind: "private", ownerUserId: "user:alice", ownerName: "alice", createdAt: "2026-10-08T00:00:00Z", viewing: { grantId: "viewing:7", expiresAt: "2026-10-09T09:00:00Z" } };
    const own: PhotoLibrary = { ...privateLibrary, id: "library:admin", ownerUserId: "user:admin" };
    const fetchMock = serve((url) => {
      if (url === "/api/v1/photos/libraries") return json({ items: [own, sharedLibrary, viewed] });
      if (url.startsWith("/api/v1/photos/search")) return json({ items: [], semantic: true });
      return undefined;
    });
    const user = userEvent.setup();
    render(<PhotosPanel userId="user:admin" isAdmin />);
    const box = await screen.findByRole("searchbox", { name: "搜索照片" });
    await user.type(box, "猫{Enter}");
    expect(await screen.findByText("没有找到相关照片")).toBeTruthy();
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/photos/search?q=%E7%8C%AB&limit=120", expect.anything());

    await user.selectOptions(screen.getByRole("combobox", { name: "图库" }), "library:alice");
    await user.type(box, "猫{Enter}");
    expect(await screen.findByText(/alice 的私有图库中搜索/)).toBeTruthy();
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/photos/search?q=%E7%8C%AB&limit=120&viewing=library%3Aalice", expect.anything());
  });
});
