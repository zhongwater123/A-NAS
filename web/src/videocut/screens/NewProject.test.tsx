import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { UploadItem } from "../../photos/uploads";
import { asset, photosHandlers, privateLibrary, sharedLibrary } from "../../test/handlers/photos";
import { sizedScrollers } from "../../test/layout";
import { apiError, http, server } from "../../test/server";
import { VideoCutPanel } from "../VideoCutPanel";

// The photo library's upload queue, as each test leaves it: what it holds
// and what was handed to it.
const queue = vi.hoisted(() => ({ items: [] as unknown[], added: [] as string[][] }));
vi.mock("../../photos/uploads", () => ({
  useUploads: () => ({
    items: queue.items,
    add: (files: File[]) => queue.added.push(files.map((file) => file.name)),
    retry: () => undefined,
    prune: () => undefined,
    clear: () => undefined,
  }),
}));

function upload(name: string, state: UploadItem["state"], extra: Partial<UploadItem> = {}) {
  return { key: name, name, size: 1, state, progress: state === "done" ? 1 : 0, video: true, ...extra };
}

interface Seen {
  created?: string[];
  added?: string[];
}

function serve(seen: Seen, options: Parameters<typeof photosHandlers>[0] = {}) {
  server.use(
    ...photosHandlers(options),
    // The device keeps no film of its own for the home page.
    http.get("/api/v1/montage/hero", ({ response }) => response(404).json(apiError("not_found", "resource not found"))),
    http.get("/api/v1/montage/projects", ({ response }) => response(200).json({ items: [] })),
    http.post("/api/v1/montage/projects", async ({ request, response }) => {
      seen.created = ((await request.json()) as { assetIds: string[] }).assetIds;
      return response(201).json({
        id: "project:new",
        name: "2026-08-02",
        assetCount: seen.created.length,
        createdAt: "2026-11-18T09:00:00Z",
        ask: "",
        chronological: true,
        assets: [],
        versions: [],
      });
    }),
  );
}

async function openNew() {
  render(<VideoCutPanel userId="user:alice" />);
  await userEvent.click(await screen.findByRole("button", { name: "新建项目" }));
  return screen.findByRole("region", { name: "新建剪辑项目" });
}

// The picker's grid lays out only in a scroller of some size.
sizedScrollers();

afterEach(() => {
  server.resetHandlers();
  queue.items = [];
  queue.added = [];
});

describe("剪辑中心：新建项目", () => {
  it("makes the project of what uploaded when a later batch failed altogether", async () => {
    const seen: Seen = {};
    serve(seen);
    // Three videos uploaded; then one more was added and failed, and a file
    // too large was refused at once. Nothing is uploading any more.
    queue.items = [
      upload("a.mov", "done", { assetId: "photo:a" }),
      upload("b.mov", "done", { assetId: "photo:b" }),
      upload("c.mp4", "done", { assetId: "photo:c" }),
      upload("d.mp4", "failed", { error: "网络连接中断，请检查网络后重试" }),
      upload("e.mkv", "failed", { error: "文件超过导入大小上限", rejected: true }),
    ];
    await openNew();
    await userEvent.click(await screen.findByRole("button", { name: "只用上传成功的 3 段新建项目" }));
    await waitFor(() => expect(seen.created).toEqual(["photo:a", "photo:b", "photo:c"]));
  });

  it("waits for the uploads still on their way, and leaves out a MOV that completed a Live Photo", async () => {
    serve({});
    queue.items = [upload("a.mov", "done", { assetId: "photo:a" }), upload("b.mov", "uploading", { progress: 0.4 })];
    const { rerender } = render(<VideoCutPanel userId="user:alice" />);
    await userEvent.click(await screen.findByRole("button", { name: "新建项目" }));
    await screen.findByRole("region", { name: "新建剪辑项目" });
    expect(screen.queryByRole("button", { name: /新建项目（/ })).not.toBeInTheDocument();
    // The second became no video of its own: it completed a Live Photo.
    queue.items = [upload("a.mov", "done", { assetId: "photo:a" }), upload("b.mov", "done")];
    rerender(<VideoCutPanel userId="user:alice" />);
    expect(await screen.findByRole("button", { name: "新建项目（1 段）" })).toBeInTheDocument();
    expect(screen.getByText("1 个 MOV 补进了图库里的实况照片，没有加入项目")).toBeInTheDocument();
  });

  it("hands the queue every video file the photo library takes, by type or by name", async () => {
    serve({});
    const region = await openNew();
    const files = [
      new File(["x"], "trip.mkv", { type: "" }),
      new File(["x"], "clip.webm", { type: "video/webm" }),
      new File(["x"], "phone.3gp", { type: "" }),
      new File(["x"], "notes.txt", { type: "text/plain" }),
    ];
    // The input's accept only guides the file dialog; what is not a video is left out here.
    await userEvent.upload(await within(region).findByLabelText("选择视频"), files, { applyAccept: false });
    expect(queue.added).toEqual([["trip.mkv", "clip.webm", "phone.3gp"]]);
  });

  it("picks videos, and videos only, from the member's own library or the shared one", async () => {
    const seen: Seen = {};
    const video = (id: string, libraryId: string) =>
      asset(id, { libraryId, name: `${id}.mp4`, mediaType: "video/mp4", video: { duration: 12 } });
    serve(seen, {
      // Another member's library an administrator is viewing is not a source.
      libraries: () => [
        sharedLibrary,
        privateLibrary,
        {
          id: "library:bob",
          kind: "private",
          ownerUserId: "user:bob",
          ownerName: "bob",
          createdAt: "2026-10-08T00:00:00Z",
          viewing: { grantId: "grant:1", expiresAt: "2026-11-19T00:00:00Z" },
        },
      ],
      timelines: {
        [sharedLibrary.id]: () => [
          asset("photo:still", { libraryId: sharedLibrary.id }),
          video("photo:clip", sharedLibrary.id),
        ],
      },
    });
    const region = await openNew();
    expect(await within(region).findByRole("button", { name: /从我的图库选/ })).toBeInTheDocument();
    expect(within(region).queryByRole("button", { name: /bob/ })).not.toBeInTheDocument();
    await userEvent.click(within(region).getByRole("button", { name: /从共享图库选/ }));
    const picker = await screen.findByRole("dialog", { name: "从共享图库选视频" });
    await userEvent.click(await within(picker).findByRole("button", { name: "查看 photo:clip.mp4" }));
    expect(within(picker).queryByRole("button", { name: "查看 photo:still.jpg" })).not.toBeInTheDocument();
    await userEvent.click(within(picker).getByRole("button", { name: "添加 1 个视频" }));
    await waitFor(() => expect(seen.created).toEqual(["photo:clip"]));
  });
});
