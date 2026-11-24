import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HttpResponse } from "msw";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setSession } from "../api/client";
import type { MontageSongRules, MusicSources, MusicStatus, MusicTrack } from "../api.generated";
import { session } from "../test/fixtures";
import { filesHandlers, sharedSpace, space } from "../test/handlers/files";
import { songRules, songRulesHandler } from "../test/handlers/montage";
import { musicStatus } from "../test/handlers/music";
import { transfer, transfersHandlers } from "../test/handlers/transfers";
import { apiError, http, server } from "../test/server";
import * as tus from "../test/tus";
import { transferStore } from "../transfers/store";
import type { MontageSong } from "./api";
import { SongPage } from "./SongPage";

// Uploads go through the transfer center's tus client; tests answer it, as
// the media center's tests do (test/tus.ts).
vi.mock("tus-js-client", async () => ({ Upload: (await import("../test/tus")).FakeUpload }));

// finish lands the upload on the NAS as the transfer id.
function finish(upload: tus.FakeUpload, id: string) {
  upload.created(id);
  upload.options.onSuccess();
}

const symphony: MontageSong = {
  id: "song:1",
  trackId: "file:symphony",
  title: "Symphony",
  duration: 212,
  transcode: false,
  state: "ready",
};

function track(id: string, title: string): MusicTrack {
  return {
    id,
    spaceId: "space:alice",
    folder: "音乐",
    name: `${title}.mp3`,
    sizeBytes: 1,
    format: "mp3",
    title,
    albumId: "album:unknown",
    durationMs: 200_000,
    addedAt: "2026-11-19T09:00:00Z",
  };
}

interface Library {
  // Whether the uploaded file is a track once the library scanned.
  indexes?: boolean;
  sources?: MusicSources;
  scanError?: MusicStatus["scan"]["error"];
  // How the montage center answers the song chosen.
  refuse?: { status: 413 | 422; code: string };
  // The device's rules of a song; null when the page cannot read them.
  rules?: MontageSongRules | null;
}

// serve answers the song page: the rules of a song, the project's song, the
// member's library, whose scan runs until it is asked about once and then has
// the upload in it, and the transfer the upload became, saved as file:new.
function serve(library: Library = {}) {
  const seen = { scans: 0, chosen: [] as string[] };
  let scanned = false;
  const tracks = () => [
    track("file:symphony", "Symphony"),
    ...(scanned && library.indexes !== false ? [track("file:new", "New Song")] : []),
  ];
  server.use(
    http.get("/api/v1/music/sources", ({ response }) =>
      response(200).json(library.sources ?? { custom: false, items: [] }),
    ),
    http.get("/api/v1/music/tracks", ({ response }) => response(200).json({ version: 1, items: tracks() })),
    http.post("/api/v1/music/library/scan", ({ response }) => {
      seen.scans++;
      return response(202).json(musicStatus(1, { scan: { state: "scanning", done: 0, total: 0 } }));
    }),
    http.get("/api/v1/music/library", ({ response }) => {
      scanned = true;
      return response(200).json(
        musicStatus(2, { version: 2, scan: { state: "idle", done: 2, total: 2, error: library.scanError } }),
      );
    }),
    ...filesHandlers({
      spaces: [space("space:alice", { kind: "private", name: "alice", ownerUserId: "user:alice" }), sharedSpace],
    }),
    ...transfersHandlers(() => [
      transfer({
        id: "upload-1",
        direction: "upload",
        name: "New Song.mp3",
        location: { spaceId: "space:alice", spaceKind: "private", folders: [] },
        entryId: "file:new",
      }),
    ]).handlers,
    library.rules === null
      ? http.get("/api/v1/montage/song-rules", () => HttpResponse.error())
      : songRulesHandler(library.rules),
    http.get("/api/v1/montage/projects/{projectId}/song", ({ response }) => response(200).json(symphony)),
    http.put("/api/v1/montage/projects/{projectId}/song", async ({ request, response }) => {
      const { trackId } = (await request.json()) as { trackId: string };
      seen.chosen.push(trackId);
      if (library.refuse)
        return response(library.refuse.status).json(apiError(library.refuse.code, "the song cannot be used"));
      return response(200).json({ ...symphony, id: "song:2", trackId, title: "New Song", state: "analyzing" });
    }),
  );
  return seen;
}

// open shows the page once it has read the rules it was served: the picker
// then offers their extensions. Without rules it offers every file.
async function open(rules: MontageSongRules | null = songRules) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <SongPage id="project:1" userId="user:alice" onBack={() => undefined} />
    </QueryClientProvider>,
  );
  const page = await screen.findByRole("region", { name: "配乐" });
  if (rules)
    await waitFor(() =>
      expect(within(page).getByLabelText("选择歌曲")).toHaveAttribute("accept", rules.extensions.join(",")),
    );
  return page;
}

// song is an audio file of size bytes that the browser reads as seconds
// long, or cannot read without them.
function song(name: string, size: number, seconds?: number): File {
  const file = new File(["x"], name, { type: "audio/mpeg" });
  Object.defineProperty(file, "size", { value: size });
  vi.spyOn(HTMLMediaElement.prototype, "duration", "get").mockReturnValue(seconds ?? Number.NaN);
  vi.spyOn(HTMLMediaElement.prototype, "src", "set").mockImplementation(function (this: HTMLMediaElement) {
    setTimeout(() => this.dispatchEvent(new Event(seconds === undefined ? "error" : "loadedmetadata")));
  });
  return file;
}

// upload picks the file in the browser's dialog; shown every file there,
// the member may pick one the page did not offer.
async function upload(page: HTMLElement, file: File, shownEveryFile = false) {
  await userEvent.upload(within(page).getByLabelText("选择歌曲"), file, { applyAccept: !shownEveryFile });
}

beforeEach(() => {
  setSession(session({ id: "user:alice" }, { csrfToken: "csrf", expiresAt: "2099-01-01T00:00:00Z" }));
  vi.spyOn(HTMLMediaElement.prototype, "pause").mockImplementation(() => undefined);
  URL.createObjectURL = () => "blob:song";
  URL.revokeObjectURL = () => undefined;
  tus.uploads.length = 0;
  server.use(...transfersHandlers().handlers);
  transferStore.attach("user:alice");
});

afterEach(() => {
  transferStore.reset();
  vi.restoreAllMocks();
});

describe("配乐页上传配乐", () => {
  it("refuses a song over 100 MiB before sending it", async () => {
    serve();
    const page = await open();
    await upload(page, song("Long Mix.mp3", 101 << 20, 200));
    expect(await within(page).findByText("歌曲超过 100 MiB")).toBeInTheDocument();
    expect(tus.uploads).toHaveLength(0);
  });

  it("refuses a song shorter than 30 seconds before sending it", async () => {
    serve();
    const page = await open();
    await upload(page, song("Jingle.mp3", 1024, 12));
    expect(await within(page).findByText("歌曲时长要在 30 秒到 8 分钟之间")).toBeInTheDocument();
    expect(tus.uploads).toHaveLength(0);
  });

  it("checks a song by the rules the NAS gives, not by its own", async () => {
    // A device whose music library does not index MP3, and takes 1 KiB at most.
    const rules = { ...songRules, extensions: [".flac"], maxBytes: 1024 };
    serve({ rules });
    const page = await open(rules);
    await upload(page, song("New Song.mp3", 512, 200), true);
    expect(await within(page).findByText("这个文件不是音频")).toBeInTheDocument();
    await upload(page, song("New Song.flac", 2048, 200));
    expect(await within(page).findByText("歌曲超过 100 MiB")).toBeInTheDocument();
    expect(tus.uploads).toHaveLength(0);
  });

  it("marks a song of the library longer than the rules allow", async () => {
    // Symphony plays 3:20; a device that takes 2 minutes at most.
    const rules = { ...songRules, maxSeconds: 120 };
    serve({ rules });
    const page = await open(rules);
    const library = within(page).getByRole("list", { name: "音乐库" });
    expect(await within(library).findByText("超过 2 分钟")).toBeInTheDocument();
  });

  it("sends the song unchecked when the rules cannot be read, and tells what the montage center says", async () => {
    const seen = serve({ rules: null, refuse: { status: 413, code: "song_too_large" } });
    const page = await open(null);
    expect(within(page).getByLabelText("选择歌曲")).not.toHaveAttribute("accept");
    await upload(page, song("Long Mix.mp3", 101 << 20, 200));
    await waitFor(() => expect(tus.uploads).toHaveLength(1));
    act(() => finish(tus.uploads[0], "upload-1"));
    expect(await within(page).findByText("歌曲超过 100 MiB", {}, { timeout: 4000 })).toBeInTheDocument();
    expect(seen.chosen).toEqual(["file:new"]);
  });

  it("uploads into the 音乐 folder, scans the library and chooses the song", async () => {
    const seen = serve();
    const page = await open();
    await upload(page, song("New Song.mp3", 1024, 200));
    await waitFor(() => expect(tus.uploads).toHaveLength(1));
    expect(tus.uploads[0].metadata).toMatchObject({
      spaceId: "space:alice",
      parentId: "",
      relativePath: "音乐",
      filename: "New Song.mp3",
    });
    expect(transferStore.getSnapshot().uploads[0].target.label).toBe("剪辑中心 · 音乐");
    expect(within(page).getByRole("button", { name: /上传配乐/ })).toBeDisabled();
    act(() => finish(tus.uploads[0], "upload-1"));
    await waitFor(() => expect(seen.chosen).toEqual(["file:new"]), { timeout: 4000 });
    expect(seen.scans).toBe(1);
    expect(await within(page).findByText("正在分析节拍与段落")).toBeInTheDocument();
    expect(within(page).getByRole("button", { name: "用《New Song》配乐" })).toHaveAttribute("aria-pressed", "true");
    expect(within(page).queryByRole("list", { name: "上传配乐" })).toBeNull();
  });

  it("tells why the montage center refused the uploaded song, read by the NAS only", async () => {
    serve({ refuse: { status: 422, code: "song_length" } });
    const page = await open();
    // The browser cannot read this file's length: the montage center decides.
    await upload(page, song("Live.ape", 1024));
    await waitFor(() => expect(tus.uploads).toHaveLength(1));
    act(() => finish(tus.uploads[0], "upload-1"));
    expect(await within(page).findByText("歌曲时长要在 30 秒到 8 分钟之间", {}, { timeout: 4000 })).toBeInTheDocument();
  });

  it("says the library could not read the song when it is not there after the scan", async () => {
    const seen = serve({ indexes: false });
    const page = await open();
    await upload(page, song("New Song.mp3", 1024, 200));
    await waitFor(() => expect(tus.uploads).toHaveLength(1));
    act(() => finish(tus.uploads[0], "upload-1"));
    expect(await within(page).findByText(/音乐库读不出它的格式/, {}, { timeout: 4000 })).toBeInTheDocument();
    expect(seen.chosen).toEqual([]);
  });

  it("says the 音乐 folder is not among the library's own folders when the member chose them", async () => {
    serve({
      indexes: false,
      sources: { custom: true, items: [{ spaceId: "space:shared", spaceKind: "shared", path: "/唱片" }] },
    });
    const page = await open();
    await upload(page, song("New Song.mp3", 1024, 200));
    await waitFor(() => expect(tus.uploads).toHaveLength(1));
    act(() => finish(tus.uploads[0], "upload-1"));
    expect(await within(page).findByText(/不在音乐库的来源里/, {}, { timeout: 4000 })).toBeInTheDocument();
  });

  it("says the scan failed when the library could not be scanned", async () => {
    const seen = serve({ scanError: "failed" });
    const page = await open();
    await upload(page, song("New Song.mp3", 1024, 200));
    await waitFor(() => expect(tus.uploads).toHaveLength(1));
    act(() => finish(tus.uploads[0], "upload-1"));
    expect(await within(page).findByText(/音乐库扫描失败/, {}, { timeout: 4000 })).toBeInTheDocument();
    expect(seen.chosen).toEqual([]);
  });

  it("leaves the song to be chosen by hand when the page is left before the scan ends", async () => {
    const seen = serve();
    const page = await open();
    await upload(page, song("New Song.mp3", 1024, 200));
    await waitFor(() => expect(tus.uploads).toHaveLength(1));
    act(() => finish(tus.uploads[0], "upload-1"));
    await waitFor(() => expect(seen.scans).toBe(1));
    cleanup();
    // Longer than the scan is asked about again.
    await new Promise((resolve) => setTimeout(resolve, 2000));
    expect(seen.chosen).toEqual([]);
  });

  it("says why an upload failed and sends it again on 重试", async () => {
    serve();
    const page = await open();
    await upload(page, song("New Song.mp3", 1024, 200));
    await waitFor(() => expect(tus.uploads).toHaveLength(1));
    act(() => tus.uploads[0].fail("POST", 403, "forbidden"));
    // The transfer center's own words for it, as its window shows them.
    expect(await within(page).findByText("没有写入这个位置的权限")).toBeInTheDocument();
    await userEvent.click(within(page).getByRole("button", { name: "重试" }));
    await waitFor(() => expect(tus.uploads).toHaveLength(2));
  });
});
