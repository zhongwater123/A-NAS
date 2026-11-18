import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";

import { http, server } from "../test/server";
import type { MontageJob, MontageProjectDetail } from "./api";
import { MontagePanel } from "./MontagePanel";

const steps = [
  "analysis",
  "catalog",
  "song",
  "pick",
  "shots",
  "fill",
  "render",
  "save",
  "pick-2",
  "shots-2",
  "fill-2",
  "render-2",
  "save-2",
];

function job(state: MontageJob["state"], extra: Partial<MontageJob> = {}): MontageJob {
  return {
    id: "job:1",
    projectId: "project:1",
    state,
    createdAt: "2026-11-18T10:00:00Z",
    steps: steps.map((name, i) => ({
      name: name as MontageJob["steps"][number]["name"],
      state: state === "done" || i < 2 ? "done" : "pending",
    })),
    ...extra,
  };
}

function project(extra: Partial<MontageProjectDetail> = {}): MontageProjectDetail {
  return {
    id: "project:1",
    name: "2026-08-02 至 08-04",
    assetCount: 2,
    createdAt: "2026-11-18T09:00:00Z",
    ask: "",
    chronological: true,
    assets: [
      { assetId: "photo:a", status: "available", duration: 30, addedAt: "2026-11-18T09:00:00Z" },
      { assetId: "photo:b", status: "available", duration: 30, addedAt: "2026-11-18T09:00:00Z" },
    ],
    versions: [],
    ...extra,
  };
}

function serve(
  detail: () => MontageProjectDetail,
  jobs: () => MontageJob,
  seen: { generated: number; dropped?: string[]; chosen?: string },
  song = true,
) {
  server.use(
    http.get("/api/v1/montage/projects", ({ response }) =>
      response(200).json({
        items: [
          {
            id: "project:1",
            name: detail().name,
            assetCount: 2,
            createdAt: "2026-11-18T09:00:00Z",
            previewAssetIds: [],
          },
        ],
      }),
    ),
    http.get("/api/v1/montage/projects/{projectId}", ({ response }) => response(200).json(detail())),
    http.get("/api/v1/montage/projects/{projectId}/song", ({ response }) =>
      song
        ? response(200).json({
            id: "song:1",
            title: "Symphony",
            duration: 212,
            transcode: false,
            state: "ready",
            tempo: 120,
            selection: { tempo: 120, bar: 2, excerpt: [60, 90], seconds: 30, parts: [], chorus: true },
          })
        : response(404).json({ error: { code: "no_song", message: "the project has no song yet" } }),
    ),
    http.patch("/api/v1/montage/projects/{projectId}", async ({ request, response }) =>
      response(200).json({ ...detail(), ...((await request.json()) as object) }),
    ),
    http.post("/api/v1/montage/projects/{projectId}/generate", ({ response }) => {
      seen.generated++;
      return response(202).json(job("queued"));
    }),
    http.get("/api/v1/photos/assets/{assetId}", ({ response }) =>
      response(404).json({ error: { code: "not_found", message: "the film is not served in this test" } }),
    ),
    http.get("/api/v1/montage/jobs/{jobId}", ({ response }) => response(200).json(jobs())),
    http.get("/api/v1/music/tracks", ({ response }) =>
      response(200).json({
        version: 1,
        items: [
          {
            id: "file:symphony",
            spaceId: "space:alice",
            folder: "",
            name: "symphony.mp3",
            sizeBytes: 1,
            format: "mp3",
            title: "Symphony",
            artist: "Forester",
            albumId: "album:unknown",
            durationMs: 212_000,
            addedAt: "2026-11-18T09:00:00Z",
          },
          {
            id: "file:forever",
            spaceId: "space:alice",
            folder: "",
            name: "forever.mp3",
            sizeBytes: 1,
            format: "mp3",
            title: "Forever",
            artist: "Keshi",
            albumId: "album:unknown",
            durationMs: 180_000,
            addedAt: "2026-11-18T09:00:00Z",
          },
        ],
      }),
    ),
    http.put("/api/v1/montage/projects/{projectId}/song", async ({ request, response }) => {
      seen.chosen = ((await request.json()) as { trackId: string }).trackId;
      return response(200).json({
        id: "song:2",
        title: "Forever",
        duration: 180,
        transcode: false,
        state: "analyzing",
      });
    }),
    http.post("/api/v1/montage/jobs/{jobId}/drop-assets", async ({ request, response }) => {
      seen.dropped = ((await request.json()) as { assetIds: string[] }).assetIds;
      return response(200).json(job("queued"));
    }),
  );
}

async function openProject() {
  render(<MontagePanel userId="user:alice" />);
  // The window opens on the member's projects; the arrow opens one.
  await userEvent.click(await screen.findByRole("button", { name: "打开 2026-08-02 至 08-04" }));
  return screen.findByRole("main", { name: "2026-08-02 至 08-04" });
}

afterEach(() => server.resetHandlers());

// A job's two 方案, as the job and the project list them.
const saved: MontageProjectDetail["versions"] = [
  { number: 1, jobId: "job:1", plan: 1, filmAssetId: "photo:film-1", createdAt: "2026-11-18T10:05:00Z" },
  { number: 2, jobId: "job:1", plan: 2, filmAssetId: "photo:film-2", createdAt: "2026-11-18T10:08:00Z" },
];

describe("剪辑中心", () => {
  it("starts a film, follows its job and shows the two 方案 it saved side by side", async () => {
    const seen = { generated: 0 };
    let state: MontageJob["state"] = "queued";
    let versions: MontageProjectDetail["versions"] = [];
    serve(
      () => project({ versions }),
      () =>
        job(
          state,
          state === "done"
            ? {
                version: saved[0],
                versions: saved,
              }
            : {},
        ),
      seen,
    );
    const page = await openProject();
    expect(within(page).getByText("2 段视频 · 约 1 分 0 秒 · 还没有成片")).toBeInTheDocument();
    expect(await within(page).findByRole("button", { name: /配乐 Symphony/ })).toBeInTheDocument();
    await userEvent.click(within(page).getByRole("button", { name: "开始剪辑" }));
    expect(seen.generated).toBe(1);
    expect(await within(page).findByText("排队中")).toBeInTheDocument();
    // The job goes on and saves a film: the page asks again and shows it.
    state = "done";
    versions = [...saved].reverse();
    expect(await within(page).findByText("2 个方案，挑一个", {}, { timeout: 4000 })).toBeInTheDocument();
    const first = within(page).getByRole("article", { name: "方案一" });
    expect(within(first).getByText("第 1 版")).toBeInTheDocument();
    expect(within(first).getByRole("link", { name: "下载方案一" })).toHaveAttribute(
      "href",
      "/api/v1/photos/assets/photo%3Afilm-1/original?download=1",
    );
    const second = within(page).getByRole("article", { name: "方案二" });
    expect(within(second).getByRole("link", { name: "下载方案二" })).toHaveAttribute(
      "href",
      "/api/v1/photos/assets/photo%3Afilm-2/original?download=1",
    );
    expect(within(page).getByRole("button", { name: "再剪一版" })).toBeEnabled();
  });

  it("picks up a job that stopped for the member, and goes on without the missing videos", async () => {
    const seen: { generated: number; dropped?: string[] } = { generated: 0 };
    const stopped = job("needs_input", { waitReason: "asset_missing", missingAssetIds: ["photo:b"], step: "render" });
    serve(
      () => project({ job: stopped }),
      () => stopped,
      seen,
    );
    const page = await openProject();
    expect(await within(page).findByText("有视频已经不在相册里了")).toBeInTheDocument();
    await userEvent.click(within(page).getByRole("button", { name: "去掉这些继续" }));
    await waitFor(() => expect(seen.dropped).toEqual(["photo:b"]));
    expect(within(page).getByRole("button", { name: "取消" })).toBeInTheDocument();
  });

  it("tells what the first version cannot do, without stopping the member", async () => {
    serve(
      () => project(),
      () => job("queued"),
      { generated: 0 },
    );
    const page = await openProject();
    await userEvent.click(within(page).getByRole("button", { name: /剪辑要求 按拍摄时间/ }));
    await userEvent.type(await screen.findByRole("textbox", { name: "一句话要求" }), "剪成竖屏，加字幕");
    expect(screen.getByRole("note")).toHaveTextContent("首版固定横屏 1920×1080；首版不加字幕");
    await userEvent.click(screen.getByRole("button", { name: "好了，回去开始剪辑" }));
    const back = await screen.findByRole("main", { name: "2026-08-02 至 08-04" });
    expect(await within(back).findByRole("button", { name: "开始剪辑" })).toBeEnabled();
  });

  it("asks for a song before a film can start", async () => {
    serve(
      () => project(),
      () => job("queued"),
      { generated: 0 },
      false,
    );
    const page = await openProject();
    expect(await within(page).findByRole("button", { name: /配乐 还没选歌/ })).toBeInTheDocument();
    expect(within(page).getByRole("button", { name: "开始剪辑" })).toBeDisabled();
  });

  it("shows the song's excerpt and chooses another from the music library", async () => {
    const seen: { generated: number; chosen?: string } = { generated: 0 };
    serve(
      () => project(),
      () => job("queued"),
      seen,
    );
    const page = await openProject();
    await userEvent.click(await within(page).findByRole("button", { name: /配乐 Symphony/ }));
    const songs = await screen.findByRole("region", { name: "配乐" });
    expect(await within(songs).findByText("选段 1:00–1:30")).toBeInTheDocument();
    expect(within(songs).getByRole("button", { name: "用《Symphony》配乐" })).toHaveAttribute("aria-pressed", "true");
    await userEvent.click(await within(songs).findByRole("button", { name: "用《Forever》配乐" }));
    await waitFor(() => expect(seen.chosen).toBe("file:forever"));
    expect(await within(songs).findByText("正在分析节拍与段落…")).toBeInTheDocument();
  });

  it("tells why a project could not load, and loads it again on 重试", async () => {
    let down = true;
    serve(
      () => project(),
      () => job("queued"),
      { generated: 0 },
    );
    server.use(
      http.get("/api/v1/montage/projects/{projectId}", ({ response }) =>
        down
          ? response(503).json({
              error: { code: "montage_unavailable", message: "the montage service is not running" },
            })
          : response(200).json(project()),
      ),
    );
    render(<MontagePanel userId="user:alice" />);
    await userEvent.click(await screen.findByRole("button", { name: "打开 2026-08-02 至 08-04" }));
    const failed = await screen.findByRole("alert", {}, { timeout: 4000 });
    down = false;
    await userEvent.click(within(failed).getByRole("button", { name: "重试" }));
    expect(await screen.findByRole("main", { name: "2026-08-02 至 08-04" })).toBeInTheDocument();
  });

  it("starts a film from the home page, for the project selected there", async () => {
    const seen = { generated: 0 };
    serve(
      () => project(),
      () => job("queued"),
      seen,
    );
    render(<MontagePanel userId="user:alice" />);
    // The newest project is selected; a click on it keeps it selected.
    const row = await screen.findByRole("button", { name: "选中 2026-08-02 至 08-04" });
    expect(row).toHaveAttribute("aria-pressed", "true");
    await userEvent.click(row);
    expect(await screen.findByRole("button", { name: /配乐 Symphony/ })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "开始剪辑" }));
    await waitFor(() => expect(seen.generated).toBe(1));
    expect(await screen.findByText("排队中")).toBeInTheDocument();
  });
});
