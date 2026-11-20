import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { WINDOW_ID } from "../desktop/windowSnapshot";
import { asset } from "../test/handlers/photos";
import { apiError, http, server } from "../test/server";
import type {
  MontageDraft,
  MontageDraftShot,
  MontageJob,
  MontageProjectDetail,
  MontageSong,
  ProjectChange,
} from "./api";
import { MontagePanel } from "./MontagePanel";
import { excerptAudio } from "./SongPage";

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
    kind: "generate",
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
      { assetId: "photo:b", status: "available", duration: 29.6, addedAt: "2026-11-18T09:00:00Z" },
    ],
    versions: [],
    ...extra,
  };
}

const symphony: MontageSong = {
  id: "song:1",
  trackId: "file:symphony",
  title: "Symphony",
  duration: 212,
  transcode: false,
  state: "ready",
  tempo: 120,
  selection: { tempo: 120, bar: 2, excerpt: [60, 90], seconds: 30, parts: [], chorus: true },
};

function track(id: string, title: string, artist: string, durationMs: number) {
  return {
    id,
    spaceId: "space:alice",
    folder: "",
    name: `${id}.mp3`,
    sizeBytes: 1,
    format: "mp3" as const,
    title,
    artist,
    albumId: "album:unknown",
    durationMs,
    addedAt: "2026-11-18T09:00:00Z",
  };
}

// A version's draft: two parts of the music, three shots, the second 竖.
function draftOf(versionId: string, extra: Partial<MontageDraft> = {}): MontageDraft {
  const shot = (
    number: number,
    part: number,
    start: number,
    end: number,
    beat: string,
    more: Partial<MontageDraftShot> = {},
  ) => ({
    number,
    beat,
    assetId: number === 2 ? "photo:b" : "photo:a",
    part,
    picture: 1,
    slots: [number],
    start,
    end,
    from: start + 1,
    to: end + 1,
    query: number === 3 ? "山路" : "海边散步",
    shotSize: "全景",
    what: `第 ${number} 镜的画面`,
    locked: false,
    changed: false,
    extended: false,
    portrait: number === 2,
    ...more,
  });
  return {
    id: `draft:${versionId}`,
    versionId,
    projectId: "project:1",
    versionNumber: Number(versionId.replace(/\D/g, "")) || 1,
    revision: 0,
    canUndo: false,
    songStart: 60,
    songTranscode: false,
    seconds: 12,
    plan: { design: "从海边走到山里", opening: "海", ending: "山" },
    parts: [
      { number: 1, name: "海边", summary: "走", level: "中等", label: "verse", start: 0, end: 8 },
      { number: 2, name: "山里", summary: "爬", level: "高潮", label: "chorus", start: 8, end: 12 },
    ],
    shots: [shot(1, 1, 0, 4, "c1/1"), shot(2, 1, 4, 8, "c2/1"), shot(3, 2, 8, 12, "c3/2")],
    counts: { shots: 3, videos: 2, portrait: 1 },
    updatedAt: "2026-11-20T09:00:00Z",
    ...extra,
  };
}

const candidates = (shot: number) => ({
  shot,
  query: "海边散步",
  sameCard: [
    {
      beat: "c1/2",
      assetId: "photo:a",
      from: 6,
      to: 10,
      shotSize: "近景",
      what: "浪花",
      scene: "海边",
      portrait: false,
      fits: true,
    },
  ],
  planned: [],
  results: [
    {
      beat: "c3/1",
      assetId: "photo:a",
      from: 20,
      to: 24,
      shotSize: "远景",
      what: "山路",
      scene: "山路",
      portrait: false,
      fits: true,
      usedBy: 3,
    },
  ],
  scenes: [{ scene: "海边", cards: 2 }],
});

interface Seen {
  generated: number;
  // How often the job was asked about.
  polled: number;
  dropped?: string[];
  chosen?: string;
  changes: ProjectChange[];
  did: string[];
  // The steps of the edit, with the revision each was made on.
  ops: { baseRevision: number; op: { type: string; shot?: number; beat?: string } }[];
  // The answer to the next step: a code to refuse it with.
  refuse?: { status: 409 | 422; code: string; message: string };
  drafts: number;
  // What the draft has that the film has not: an edit not saved yet.
  edited?: Partial<MontageDraft>;
}
const fresh = (): Seen => ({ generated: 0, polled: 0, changes: [], did: [], ops: [], drafts: 0 });

function serve(
  detail: () => MontageProjectDetail,
  jobs: () => MontageJob,
  seen: Seen,
  song: MontageSong | null = symphony,
) {
  server.use(
    // The device keeps no film of its own for the home page.
    http.get("/api/v1/montage/hero", ({ response }) => response(404).json(apiError("not_found", "resource not found"))),
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
      song ? response(200).json(song) : response(404).json(apiError("no_song", "the project has no song yet")),
    ),
    http.patch("/api/v1/montage/projects/{projectId}", async ({ request, response }) => {
      const change = (await request.json()) as ProjectChange;
      seen.changes.push(change);
      return response(200).json({ ...detail(), ...change });
    }),
    http.post("/api/v1/montage/projects/{projectId}/generate", ({ response }) => {
      seen.generated++;
      return response(202).json(job("queued"));
    }),
    http.get("/api/v1/photos/assets/{assetId}", ({ response }) =>
      response(404).json(apiError("not_found", "the film is not served in this test")),
    ),
    http.get("/api/v1/montage/jobs/{jobId}", ({ response }) => {
      seen.polled++;
      return response(200).json(jobs());
    }),
    http.get("/api/v1/music/tracks", ({ response }) =>
      response(200).json({
        version: 1,
        items: [
          track("file:symphony", "Symphony", "Forester", 212_000),
          // Another artist's song of the same title.
          track("file:symphony-live", "Symphony", "Clean Bandit", 200_000),
          track("file:forever", "Forever", "Keshi", 180_000),
        ],
      }),
    ),
    http.put("/api/v1/montage/projects/{projectId}/song", async ({ request, response }) => {
      seen.chosen = ((await request.json()) as { trackId: string }).trackId;
      return response(200).json({
        id: "song:2",
        trackId: seen.chosen,
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
    http.post("/api/v1/montage/jobs/{jobId}/retry", ({ response }) => {
      seen.did.push("retry");
      return response(200).json(job("queued"));
    }),
    http.post("/api/v1/montage/jobs/{jobId}/replan", ({ response }) => {
      seen.did.push("replan");
      return response(200).json(job("queued"));
    }),
    http.get("/api/v1/montage/versions/{versionId}/draft", ({ params, response }) => {
      seen.drafts++;
      return response(200).json(draftOf(params.versionId, seen.edited));
    }),
    // The film as it is, whatever its draft has become.
    http.get("/api/v1/montage/versions/{versionId}/outline", ({ params, response }) => {
      const { versionId, projectId, versionNumber, seconds, plan, parts, shots, counts } = draftOf(params.versionId);
      // The first version's plan found no 直升机 among the videos.
      const unmet = params.versionId === "version:1" ? ["直升机"] : [];
      return response(200).json({ versionId, projectId, versionNumber, seconds, plan, parts, shots, counts, unmet });
    }),
    http.post("/api/v1/montage/versions/{versionId}/confirm", ({ params, response }) => {
      seen.did.push(`confirm ${params.versionId}`);
      return response(200).json({ ...detail(), chosenVersionId: params.versionId });
    }),
    http.post("/api/v1/montage/versions/{versionId}/recut", async ({ request, response }) => {
      const asked = (await request.json()) as { change: string };
      seen.did.push(`recut ${asked.change}`);
      if (seen.refuse) return response(409).json(apiError(seen.refuse.code, seen.refuse.message));
      return response(202).json(job("queued", { kind: "recut", steps: [] }));
    }),
    http.post("/api/v1/montage/drafts/{draftId}/ops", async ({ params, request, response }) => {
      const step = (await request.json()) as Seen["ops"][number];
      seen.ops.push(step);
      const refuse = seen.refuse;
      if (refuse) {
        seen.refuse = undefined;
        return response(refuse.status).json(apiError(refuse.code, refuse.message));
      }
      const message =
        step.op.type === "undo" ? "已撤销" : step.op.type === "replace" ? `第 ${step.op.shot} 镜已替换` : "";
      return response(200).json(
        draftOf(params.draftId.replace("draft:", ""), {
          revision: step.baseRevision + 1,
          canUndo: step.op.type !== "undo",
          message,
        }),
      );
    }),
    http.get("/api/v1/montage/drafts/{draftId}/candidates", ({ request, response }) =>
      response(200).json(candidates(Number(new URL(request.url).searchParams.get("shot")))),
    ),
    // The rough preview plays the videos' originals.
    http.get("/api/v1/photos/assets/{assetId}/video/playback", ({ response }) =>
      response(200).json({ mode: "direct", duration: 30 }),
    ),
    http.post("/api/v1/montage/drafts/{draftId}/save", ({ response }) => {
      seen.did.push("save");
      return response(202).json(job("queued", { kind: "edit", steps: [{ name: "source", state: "pending" }] }));
    }),
  );
}

async function openProject() {
  render(<MontagePanel userId="user:alice" />);
  // The window opens on the member's projects; the arrow opens one.
  await userEvent.click(await screen.findByRole("button", { name: "打开 2026-08-02 至 08-04" }));
  return screen.findByRole("main", { name: "2026-08-02 至 08-04" });
}

afterEach(() => {
  server.resetHandlers();
  vi.unstubAllGlobals();
});

// A job's two 方案, as the job and the project list them.
const saved: MontageProjectDetail["versions"] = [
  {
    id: "version:1",
    number: 1,
    jobId: "job:1",
    plan: 1,
    filmAssetId: "photo:film-1",
    createdAt: "2026-11-18T10:05:00Z",
    editable: true,
    kind: "plan",
  },
  {
    id: "version:2",
    number: 2,
    jobId: "job:1",
    plan: 2,
    filmAssetId: "photo:film-2",
    createdAt: "2026-11-18T10:08:00Z",
    editable: true,
    kind: "plan",
  },
];

describe("剪辑中心", () => {
  it("starts a film, follows its job and shows the two 方案 it saved side by side", async () => {
    const seen = fresh();
    let state: MontageJob["state"] = "queued";
    let versions: MontageProjectDetail["versions"] = [];
    serve(
      () => project({ versions }),
      () => job(state, state === "done" ? { version: saved[0], versions: saved } : {}),
      seen,
    );
    const page = await openProject();
    // The length is rounded once: 59.6 seconds are not "60 秒" past a minute.
    expect(within(page).getByText("2 段视频 · 59 秒 · 还没有成片")).toBeInTheDocument();
    expect(await within(page).findByRole("button", { name: /配乐 Symphony/ })).toBeInTheDocument();
    await userEvent.click(within(page).getByRole("button", { name: "开始剪辑" }));
    expect(seen.generated).toBe(1);
    expect(await within(page).findByText("排队中")).toBeInTheDocument();
    // The job goes on and saves its films: the page asks again and shows them.
    state = "done";
    versions = [...saved].reverse();
    expect(await within(page).findByText("2 个方案，都已存进你的个人图库", {}, { timeout: 4000 })).toBeInTheDocument();
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

  it("shows the first 方案 as soon as it is saved, while the second is still planned", async () => {
    let versions: MontageProjectDetail["versions"] = [];
    const going = () => job("waiting", { waitReason: "vnas", step: "pick-2", versions: versions.slice(0, 1) });
    serve(() => project({ versions, job: going() }), going, fresh());
    const page = await openProject();
    expect(await within(page).findByText("等待 VNAS 规划")).toBeInTheDocument();
    expect(within(page).queryByRole("article", { name: "方案一" })).not.toBeInTheDocument();
    // The first film is saved; the job is still in progress.
    versions = [saved[0]];
    expect(await within(page).findByRole("article", { name: "方案一" }, { timeout: 4000 })).toBeInTheDocument();
    expect(within(page).queryByRole("article", { name: "方案二" })).not.toBeInTheDocument();
    expect(within(page).getByRole("status", { name: "生成进度" })).toBeInTheDocument();
  });

  it("keeps the job it started in view after a visit to the song's page", async () => {
    const seen = fresh();
    // The project as the server gave it before the job started: asked for
    // again within seconds, the cached one is what the page reads.
    serve(
      () => project(),
      () => job("running", { step: "analysis" }),
      seen,
    );
    const page = await openProject();
    await userEvent.click(await within(page).findByRole("button", { name: "开始剪辑" }));
    expect(await within(page).findByRole("status", { name: "生成进度" })).toBeInTheDocument();
    await userEvent.click(within(page).getByRole("button", { name: /配乐 Symphony/ }));
    await userEvent.click(await screen.findByRole("button", { name: "完成" }));
    const back = await screen.findByRole("main", { name: "2026-08-02 至 08-04" });
    expect(await within(back).findByRole("status", { name: "生成进度" })).toBeInTheDocument();
    expect(within(back).getByRole("button", { name: "开始剪辑" })).toBeDisabled();
    expect(seen.generated).toBe(1);
  });

  it("picks up a job that stopped for the member, and goes on without the missing videos", async () => {
    const seen = fresh();
    const stopped = job("needs_input", { waitReason: "asset_missing", missingAssetIds: ["photo:b"], step: "render" });
    serve(
      () => project({ job: stopped }),
      () => stopped,
      seen,
    );
    const page = await openProject();
    expect(await within(page).findByText("1 个视频已经不在相册里")).toBeInTheDocument();
    await userEvent.click(within(page).getByRole("button", { name: "去掉这些继续" }));
    await waitFor(() => expect(seen.dropped).toEqual(["photo:b"]));
    expect(within(page).getByRole("button", { name: "取消" })).toBeInTheDocument();
  });

  it("names every video too long to cut from, and only offers to take them out", async () => {
    const seen = fresh();
    const stopped = job("needs_input", {
      waitReason: "video_too_long",
      missingAssetIds: ["photo:a", "photo:b"],
      step: "analysis",
    });
    serve(
      () => project({ job: stopped }),
      () => stopped,
      seen,
    );
    const page = await openProject();
    const progress = await within(page).findByRole("status", { name: "生成进度" });
    expect(within(progress).getByText("2 个视频超过 6 小时，不能用于剪辑")).toBeInTheDocument();
    expect(within(within(progress).getByRole("list", { name: "涉及的视频" })).getAllByRole("listitem")).toHaveLength(2);
    expect(within(progress).queryByRole("button", { name: "重试分析" })).not.toBeInTheDocument();
    await userEvent.click(within(progress).getByRole("button", { name: "去掉这些继续" }));
    await waitFor(() => expect(seen.dropped).toEqual(["photo:a", "photo:b"]));
  });

  it("asks to retry once the device has the model, naming no video", async () => {
    const seen = fresh();
    const stopped = job("needs_input", { waitReason: "model_unavailable", step: "analysis" });
    serve(
      () => project({ job: stopped }),
      () => stopped,
      seen,
    );
    const page = await openProject();
    const progress = await within(page).findByRole("status", { name: "生成进度" });
    expect(within(progress).getByText("这台设备还没有安装描述画面用的模型，装好后点重试")).toBeInTheDocument();
    expect(within(progress).queryByRole("list", { name: "涉及的视频" })).not.toBeInTheDocument();
    expect(within(progress).queryByRole("button", { name: "去掉这些继续" })).not.toBeInTheDocument();
    expect(within(progress).queryByRole("button", { name: "重试分析" })).not.toBeInTheDocument();
    await userEvent.click(within(progress).getByRole("button", { name: "重试" }));
    await waitFor(() => expect(seen.did).toEqual(["retry"]));
  });

  it("offers to analyse again or to drop the videos whose analysis failed", async () => {
    const stopped = job("needs_input", {
      waitReason: "analysis_failed",
      missingAssetIds: ["photo:a"],
      step: "analysis",
    });
    serve(
      () => project({ job: stopped }),
      () => stopped,
      fresh(),
    );
    const progress = within(await openProject()).findByRole("status", { name: "生成进度" });
    expect(within(await progress).getByText("1 个视频分析失败")).toBeInTheDocument();
    expect(within(await progress).getByRole("button", { name: "重试分析" })).toBeInTheDocument();
    expect(within(await progress).getByRole("button", { name: "去掉这些继续" })).toBeInTheDocument();
  });

  it("asks before another round when VNAS handed back the first 方案 only", async () => {
    const seen = fresh();
    const stopped = job("needs_input", { waitReason: "vnas_unknown", step: "pick-2", versions: [saved[0]] });
    serve(
      () => project({ job: stopped, versions: [saved[0]] }),
      () => stopped,
      seen,
    );
    const page = await openProject();
    expect(await within(page).findByText("VNAS 没有交回方案二的规划，重新规划？")).toBeInTheDocument();
    expect(within(page).getByRole("article", { name: "方案一" })).toBeInTheDocument();
    await userEvent.click(within(page).getByRole("button", { name: "重新规划" }));
    await waitFor(() => expect(seen.did).toEqual(["replan"]));
  });

  it("says what failed when the planning failed twice, and plans again", async () => {
    const seen = fresh();
    const stopped = job("needs_input", {
      waitReason: "plan_error",
      step: "pick",
      error: "session.create: unknown method",
    });
    serve(
      () => project({ job: stopped }),
      () => stopped,
      seen,
    );
    const page = await openProject();
    expect(await within(page).findByText("规划出错：session.create: unknown method")).toBeInTheDocument();
    await userEvent.click(within(page).getByRole("button", { name: "重新规划" }));
    await waitFor(() => expect(seen.did).toEqual(["replan"]));
  });

  it("says why a job waits: for VNAS with the reason, and for the photo library", async () => {
    let waiting = job("waiting", { waitReason: "vnas", step: "pick", error: "VNAS 没有开启" });
    serve(
      () => project({ job: waiting }),
      () => waiting,
      fresh(),
    );
    const page = await openProject();
    expect(await within(page).findByText("等待 VNAS 规划：VNAS 没有开启")).toBeInTheDocument();
    // A waiting job that says nothing more needs nothing of the member.
    expect(within(page).queryByRole("button", { name: "重新规划" })).not.toBeInTheDocument();
    waiting = job("waiting", { waitReason: "photos_unavailable", step: "save" });
    expect(await within(page).findByText("相册暂时不可用，会自动重试", {}, { timeout: 4000 })).toBeInTheDocument();
  });

  it("says of a failed job what was written for the member, and keeps diagnostics out of the page", async () => {
    let failed = job("failed", { step: "fill", error: "the fill asked again for v1-0/2" });
    serve(
      () => project({ job: failed }),
      () => failed,
      fresh(),
    );
    const first = render(<MontagePanel userId="user:alice" />);
    await userEvent.click(await screen.findByRole("button", { name: "打开 2026-08-02 至 08-04" }));
    const progress = await screen.findByRole("status", { name: "生成进度" });
    expect(within(progress).getByText("没有剪成")).toBeInTheDocument();
    expect(within(progress).queryByText(/the fill asked/)).not.toBeInTheDocument();
    // A failed job is over: nothing to cancel, and another film may start.
    expect(within(progress).queryByRole("button", { name: "取消" })).not.toBeInTheDocument();
    expect(await screen.findByRole("button", { name: "开始剪辑" })).toBeEnabled();
    first.unmount();

    failed = job("failed", { step: "save", error: "相册授权已撤销，无法确认上次存回的结果，请在相册中核对成片" });
    await openProject();
    expect(await screen.findByText("相册授权已撤销，无法确认上次存回的结果，请在相册中核对成片")).toBeInTheDocument();
  });

  it("shows where the job is when it can no longer be cancelled", async () => {
    let current = job("running", { step: "render" });
    serve(
      () => project({ job: current }),
      () => current,
      fresh(),
    );
    server.use(
      http.post("/api/v1/montage/jobs/{jobId}/cancel", ({ response }) => {
        // The film went to the photo library meanwhile.
        current = job("running", { step: "save" });
        return response(409).json(apiError("job_state", "the job cannot do this now"));
      }),
    );
    const page = await openProject();
    expect(await within(page).findByText("正在渲染方案一")).toBeInTheDocument();
    await userEvent.click(within(page).getByRole("button", { name: "取消" }));
    expect(await within(page).findByText("任务的状态已经变化，页面已刷新")).toBeInTheDocument();
    expect(await within(page).findByText("正在保存方案一")).toBeInTheDocument();
    // It goes on asking, and shows the film once the photo library answered.
    current = job("done", { version: saved[0], versions: saved });
    await waitFor(() => expect(within(page).queryByRole("status", { name: "生成进度" })).not.toBeInTheDocument(), {
      timeout: 4000,
    });
  });

  it("asks nothing about the job while its window is minimized", async () => {
    const seen = fresh();
    const running = job("running", { step: "render" });
    serve(
      () => project({ job: running }),
      () => running,
      seen,
    );
    // As the desktop frames an app's window (AppWindow).
    const frame = document.createElement("section");
    frame.setAttribute(WINDOW_ID, "montage");
    document.body.append(frame);
    try {
      render(<MontagePanel userId="user:alice" />, { container: frame });
      await userEvent.click(await screen.findByRole("button", { name: "打开 2026-08-02 至 08-04" }));
      await waitFor(() => expect(seen.polled).toBeGreaterThan(0), { timeout: 4000 });
      frame.hidden = true;
      // A request already on its way may still arrive.
      await new Promise((resolve) => setTimeout(resolve, 300));
      const asked = seen.polled;
      await new Promise((resolve) => setTimeout(resolve, 2600));
      expect(seen.polled).toBe(asked);
      frame.hidden = false;
      await waitFor(() => expect(seen.polled).toBeGreaterThan(asked), { timeout: 4000 });
    } finally {
      frame.remove();
    }
  });

  it("leaves the two films still until the member plays one", async () => {
    const play = vi.spyOn(HTMLMediaElement.prototype, "play").mockImplementation(function (this: HTMLMediaElement) {
      this.dispatchEvent(new Event("play"));
      return Promise.resolve();
    });
    try {
      serve(
        () => project({ versions: [...saved].reverse() }),
        () => job("done", { versions: saved }),
        fresh(),
      );
      server.use(
        http.get("/api/v1/photos/assets/{assetId}", ({ params, response }) =>
          response(200).json(
            asset(params.assetId, { name: "film.mp4", mediaType: "video/mp4", video: { duration: 92 } }),
          ),
        ),
        http.get("/api/v1/photos/assets/{assetId}/video/playback", ({ response }) =>
          response(200).json({ mode: "direct", duration: 92 }),
        ),
      );
      const page = await openProject();
      const first = await within(page).findByRole("article", { name: "方案一" });
      const second = within(page).getByRole("article", { name: "方案二" });
      const films = await waitFor(() => {
        const found = [first, second].map((card) => card.querySelector("video"));
        expect(found.every((video) => video?.getAttribute("src"))).toBe(true);
        return found as HTMLVideoElement[];
      });
      for (const film of films) fireEvent(film, new Event("loadedmetadata"));
      // Two films side by side do not both start, sound and all, when the page opens.
      expect(play).not.toHaveBeenCalled();
      await userEvent.click(await within(first).findByRole("button", { name: "播放" }));
      expect(play).toHaveBeenCalledTimes(1);
    } finally {
      play.mockRestore();
    }
  });

  it("plays and downloads the films of earlier jobs", async () => {
    const earlier = {
      id: "version:0",
      number: 1,
      jobId: "job:0",
      plan: 1,
      filmAssetId: "photo:film-0",
      createdAt: "2026-11-17T10:00:00Z",
      editable: true,
      kind: "plan" as const,
    };
    const gone = { ...earlier, id: "version:00", number: 2, plan: 2, filmAssetId: "photo:film-00", gone: true };
    const later = saved.map((version) => ({ ...version, id: `${version.id}+2`, number: version.number + 2 }));
    serve(
      () => project({ versions: [...later].reverse().concat(gone, earlier) }),
      () => job("done"),
      fresh(),
    );
    const page = await openProject();
    const list = await within(page).findByRole("list", { name: "更早的版本" });
    expect(within(list).getByText("成片已不在相册")).toBeInTheDocument();
    expect(within(list).getByRole("link", { name: "下载第 1 版" })).toHaveAttribute(
      "href",
      "/api/v1/photos/assets/photo%3Afilm-0/original?download=1",
    );
    expect(within(list).queryByRole("button", { name: "播放第 2 版" })).not.toBeInTheDocument();
    await userEvent.click(within(list).getByRole("button", { name: "播放第 1 版" }));
    expect(await within(page).findByRole("group", { name: "第 1 版" })).toBeInTheDocument();
  });

  it("tells what the first version cannot do, without stopping the member", async () => {
    const seen = fresh();
    serve(
      () => project(),
      () => job("queued"),
      seen,
    );
    const page = await openProject();
    await userEvent.click(within(page).getByRole("button", { name: /剪辑要求 按拍摄时间/ }));
    await userEvent.type(await screen.findByRole("textbox", { name: "一句话要求" }), "剪成竖屏，加字幕");
    expect(screen.getByRole("note")).toHaveTextContent("首版固定横屏 1920×1080；首版不加字幕");
    // Leaving right after the box lost the focus saves the sentence once.
    await userEvent.click(screen.getByRole("button", { name: "完成" }));
    const back = await screen.findByRole("main", { name: "2026-08-02 至 08-04" });
    expect(await within(back).findByRole("button", { name: "开始剪辑" })).toBeEnabled();
    expect(seen.changes).toEqual([{ ask: "剪成竖屏，加字幕" }]);
  });

  it("saves an idea when it is clicked, also when the member leaves by 返回", async () => {
    const seen = fresh();
    serve(
      () => project(),
      () => job("queued"),
      seen,
    );
    const page = await openProject();
    await userEvent.click(within(page).getByRole("button", { name: /剪辑要求 按拍摄时间/ }));
    await userEvent.click(await screen.findByRole("button", { name: "慢下来的旅行" }));
    expect(screen.getByRole("textbox", { name: "一句话要求" })).toHaveValue("慢下来的旅行");
    await userEvent.click(screen.getByRole("button", { name: "返回" }));
    const back = await screen.findByRole("main", { name: "2026-08-02 至 08-04" });
    expect(seen.changes).toEqual([{ ask: "慢下来的旅行" }]);
    expect(await within(back).findByRole("button", { name: /剪辑要求 慢下来的旅行/ })).toBeInTheDocument();
  });

  it("asks for a song before a film can start", async () => {
    serve(
      () => project(),
      () => job("queued"),
      fresh(),
      null,
    );
    const page = await openProject();
    expect(await within(page).findByRole("button", { name: /配乐 还没选歌/ })).toBeInTheDocument();
    expect(within(page).getByRole("button", { name: "开始剪辑" })).toBeDisabled();
    expect(within(page).getByText("先选一首歌")).toBeInTheDocument();
  });

  it("sends the member to the song when the montage center says there is none", async () => {
    const seen = fresh();
    serve(
      () => project(),
      () => job("queued"),
      seen,
    );
    // The song went between the page's reading it and the click.
    server.use(
      http.post("/api/v1/montage/projects/{projectId}/generate", ({ response }) =>
        response(404).json(apiError("no_song", "the project has no song yet")),
      ),
    );
    const page = await openProject();
    await userEvent.click(await within(page).findByRole("button", { name: "开始剪辑" }));
    expect(await within(page).findByText("先选一首歌")).toBeInTheDocument();
    expect(within(page).getByRole("button", { name: /配乐/ })).toBeInTheDocument();
    // The planning grant could not be registered: nothing was started, and the page says so.
    server.use(
      http.post("/api/v1/montage/projects/{projectId}/generate", ({ response }) =>
        response(503).json(apiError("assistant_unavailable", "the planning grant could not be registered")),
      ),
    );
    await userEvent.click(within(page).getByRole("button", { name: "开始剪辑" }));
    expect(await within(page).findByText("没能登记 VNAS 的规划授权，请稍后重试")).toBeInTheDocument();
    expect(within(page).queryByRole("status", { name: "生成进度" })).not.toBeInTheDocument();
  });

  it("plays a song the NAS decodes from the excerpt's start, for as long as the excerpt lasts", () => {
    // That stream starts where it is asked to and cannot seek: the fragment counts from there.
    expect(excerptAudio("project:1", { ...symphony, transcode: true }, [60, 90])).toBe(
      "/api/v1/montage/projects/project%3A1/song/audio?start=60.00#t=0,30.00",
    );
  });

  it("shows the song's excerpt and chooses another from the music library", async () => {
    const seen = fresh();
    serve(
      () => project(),
      () => job("queued"),
      seen,
    );
    const page = await openProject();
    await userEvent.click(await within(page).findByRole("button", { name: /配乐 Symphony/ }));
    const songs = await screen.findByRole("region", { name: "配乐" });
    expect(await within(songs).findByText("选段 1:00–1:30")).toBeInTheDocument();
    // The track the song was copied from is marked, not its namesake.
    expect(await within(songs).findByRole("button", { name: "用《Symphony》（Forester）配乐" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(within(songs).getByRole("button", { name: "用《Symphony》（Clean Bandit）配乐" })).toHaveAttribute(
      "aria-pressed",
      "false",
    );
    // What plays is the excerpt, from its start to its end, not the song from its beginning.
    expect(within(songs).getByLabelText("试听选段")).toHaveAttribute(
      "src",
      "/api/v1/montage/projects/project%3A1/song/audio#t=60.00,90.00",
    );
    await userEvent.click(await within(songs).findByRole("button", { name: "用《Forever》（Keshi）配乐" }));
    await waitFor(() => expect(seen.chosen).toBe("file:forever"));
    expect(await within(songs).findByText("正在分析节拍与段落")).toBeInTheDocument();
  });

  it("moves the excerpt when the slider lets go, wherever the pointer was released", async () => {
    const seen = fresh();
    serve(
      () => project(),
      () => job("queued"),
      seen,
    );
    const page = await openProject();
    await userEvent.click(await within(page).findByRole("button", { name: /配乐 Symphony/ }));
    const slider = await screen.findByRole("slider", { name: "选段起点" });
    // Dragging only shows where the excerpt would be.
    fireEvent.input(slider, { target: { value: "72" } });
    expect(await screen.findByText("选段 1:12–1:42")).toBeInTheDocument();
    expect(seen.changes).toEqual([]);
    // The browser's change event ends the drag; no pointer event on the slider is needed.
    slider.dispatchEvent(new Event("change", { bubbles: true }));
    await waitFor(() => expect(seen.changes).toEqual([{ songStart: 72 }]));
  });

  it("tells why a project could not load, and loads it again on 重试", async () => {
    let down = true;
    serve(
      () => project(),
      () => job("queued"),
      fresh(),
    );
    server.use(
      http.get("/api/v1/montage/projects/{projectId}", ({ response }) =>
        down
          ? response(503).json(apiError("montage_unavailable", "the montage service is not running"))
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

  it("plays the device's own film behind the hero when it keeps one, and never the member's videos", async () => {
    serve(
      () => project(),
      () => job("queued"),
      fresh(),
    );
    const listed = http.get("/api/v1/montage/projects", ({ response }) =>
      response(200).json({
        items: [
          {
            id: "project:1",
            name: "2026-08-02 至 08-04",
            assetCount: 2,
            createdAt: "2026-11-18T09:00:00Z",
            previewAssetIds: ["photo:a", "photo:b"],
          },
        ],
      }),
    );
    // Playing the member's videos kept the photo library in use for as long as
    // the window was open, which held back the analysis the job waits for.
    let played = 0;
    server.use(
      listed,
      http.get("/api/v1/photos/assets/{assetId}/video/playback", ({ response }) => {
        played++;
        return response(200).json({ mode: "direct", duration: 30 });
      }),
    );
    const first = render(<MontagePanel userId="user:alice" />);
    await screen.findByRole("button", { name: "选中 2026-08-02 至 08-04" });
    // No film of the device's: no player at all.
    await waitFor(() => expect(first.container.querySelector("video")).toBeNull());
    expect(played).toBe(0);
    first.unmount();

    let asked = "";
    server.use(
      listed,
      http.get("/api/v1/montage/hero", ({ request }) => {
        asked = request.headers.get("Range") ?? "";
        return new Response("f", {
          status: 206,
          headers: { "Content-Type": "video/mp4", "Content-Range": "bytes 0-0/9" },
        });
      }),
    );
    const second = render(<MontagePanel userId="user:alice" />);
    await waitFor(() => expect(second.container.querySelector('video[src^="/api/v1/montage/hero"]')).not.toBeNull());
    expect(asked).toBe("bytes=0-0");
    expect(second.container.querySelectorAll("video")).toHaveLength(1);
    expect(played).toBe(0);
  });

  it("starts a film from the home page, for the project selected there", async () => {
    const seen = fresh();
    serve(
      () => project(),
      () => job("queued"),
      seen,
    );
    // Plain LAN HTTP has no crypto.randomUUID: the request key does not need it.
    vi.stubGlobal("crypto", { getRandomValues: crypto.getRandomValues.bind(crypto) });
    render(<MontagePanel userId="user:alice" />);
    // The newest project is selected; a click unselects it, another selects it again.
    const row = await screen.findByRole("button", { name: "选中 2026-08-02 至 08-04" });
    expect(row).toHaveAttribute("aria-pressed", "true");
    await userEvent.click(row);
    expect(row).toHaveAttribute("aria-pressed", "false");
    expect(screen.getByText("先选一个项目")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "开始剪辑" })).toBeDisabled();
    await userEvent.click(row);
    expect(row).toHaveAttribute("aria-pressed", "true");
    expect(await screen.findByRole("button", { name: /配乐 Symphony/ })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "开始剪辑" }));
    await waitFor(() => expect(seen.generated).toBe(1));
    expect(await screen.findByText("排队中")).toBeInTheDocument();
  });
  it("confirms a version, and recuts one from a sentence", async () => {
    const seen = fresh();
    serve(
      () => project({ versions: [...saved].reverse(), job: job("done") }),
      () => job("done"),
      seen,
    );
    const page = await openProject();
    const first = await within(page).findByRole("article", { name: "方案一" });
    // What the version is: its plan, its parts in a row and its numbers, 竖 marked.
    expect(await within(first).findByText("从海边走到山里")).toBeInTheDocument();
    expect(within(first).getByText("海边")).toBeInTheDocument();
    expect(within(first).getByText("个竖拍").parentElement).toHaveAttribute("data-warn", "true");
    // What the plan did not find among the videos is listed as it was written.
    expect(within(first).getByText("素材里没找到：直升机")).toBeInTheDocument();
    await userEvent.click(within(first).getByRole("button", { name: "看完整规划" }));
    expect(within(first).getByText("爬")).toBeInTheDocument();
    await userEvent.click(within(first).getByRole("button", { name: "确认这一版" }));
    expect(await within(first).findByRole("button", { name: "已确认" })).toBeDisabled();
    expect(seen.did).toContain("confirm version:1");
    // A sentence recuts the version; what the first version cannot do is said under it.
    const say = within(first).getByRole("textbox", { name: "对第 1 版的修改要求" });
    await userEvent.type(say, "加字幕");
    expect(within(first).getByText("首版不加字幕")).toBeInTheDocument();
    // A recut starts from the plan: the edits of 修改镜头 do not go with it.
    expect(within(first).getByText(/“修改镜头”里的改动不会带进去/)).toBeInTheDocument();
    await userEvent.clear(say);
    await userEvent.type(say, "结尾留长一点");
    await userEvent.click(within(first).getByRole("button", { name: "重剪" }));
    await waitFor(() => expect(seen.did).toContain("recut 结尾留长一点"));
    // Another job in progress: the page says to wait for it.
    seen.refuse = { status: 409, code: "job_in_progress", message: "the project already has a job in progress" };
    await userEvent.type(say, "开头短一点");
    await userEvent.click(within(first).getByRole("button", { name: "重剪" }));
    expect(await within(first).findByText(/等这个任务做完/)).toBeInTheDocument();
  });

  it("shows a version as its film is, whatever its draft has become", async () => {
    const seen = fresh();
    // The draft has one shot fewer, none of them 竖: an edit not saved yet.
    seen.edited = { counts: { shots: 2, videos: 1, portrait: 0 } };
    serve(
      () =>
        project({
          versions: [{ ...saved[1], editable: false }, saved[0]],
          job: job("done"),
        }),
      () => job("done"),
      seen,
    );
    const page = await openProject();
    const first = await within(page).findByRole("article", { name: "方案一" });
    expect((await within(first).findByText("个镜头")).parentElement).toHaveTextContent("3");
    expect(within(first).getByText("个竖拍").parentElement).toHaveAttribute("data-warn", "true");
    // Looking at the versions makes no draft.
    expect(seen.drafts).toBe(0);
    // A version whose shots are not known is neither edited nor recut.
    const second = within(page).getByRole("article", { name: "方案二" });
    expect(within(second).getByRole("button", { name: "修改镜头" })).toBeDisabled();
    expect(within(second).getByRole("textbox", { name: "对第 2 版的修改要求" })).toBeDisabled();
    expect(within(second).getByText(/只能播放，不能修改或重剪/)).toBeInTheDocument();
    expect(within(second).queryByText(/素材里没找到/)).not.toBeInTheDocument();
  });

  it("says a recut of an edited version leaves its shots changed by hand behind", async () => {
    const seen = fresh();
    const edited = { ...saved[1], kind: "edit" as const, sourceVersionId: "version:1", sourceNumber: 1 };
    serve(
      () => project({ versions: [edited, saved[0]], job: job("done") }),
      () => job("done"),
      seen,
    );
    const page = await openProject();
    const card = await within(page).findByRole("article", { name: "改自第 1 版" });
    expect(within(card).getByText("重剪从这一版所来自的规划出发，手动换过的镜头不会带进去")).toBeInTheDocument();
  });

  it("marks the shots of a save that stopped for a missing video", async () => {
    const seen = fresh();
    const stopped = job("needs_input", {
      kind: "edit",
      waitReason: "asset_missing",
      missingAssetIds: ["photo:b"],
      step: "render",
      steps: [{ name: "source", state: "done" }],
    });
    serve(
      () => project({ versions: [...saved].reverse(), job: stopped }),
      () => stopped,
      seen,
    );
    const page = await openProject();
    // An edit takes no video out: only cancelling is offered, and where to go is said.
    expect(await within(page).findByText(/取消后在“修改镜头”里换掉用到它们的镜头/)).toBeInTheDocument();
    expect(within(page).queryByRole("button", { name: "去掉这些继续" })).not.toBeInTheDocument();
    const first = await within(page).findByRole("article", { name: "方案一" });
    await userEvent.click(within(first).getByRole("button", { name: "修改镜头" }));
    const edit = await screen.findByRole("main", { name: "第 1 版 · 精修镜头" });
    expect(await within(edit).findByText(/第 2 镜用到的视频\s*已经不在相册里/)).toBeInTheDocument();
    const sea = within(edit).getByRole("region", { name: "段落 1" });
    expect(within(sea).getAllByText("素材不在了")).toHaveLength(1);
  });

  it("asks for a song that cannot seek from where the rough preview begins", async () => {
    const seen = fresh();
    seen.edited = { songTranscode: true };
    serve(
      () => project({ versions: [...saved].reverse(), job: job("done") }),
      () => job("done"),
      seen,
    );
    const page = await openProject();
    const first = await within(page).findByRole("article", { name: "方案一" });
    await userEvent.click(within(first).getByRole("button", { name: "修改镜头" }));
    const edit = await screen.findByRole("main", { name: "第 1 版 · 精修镜头" });
    // From the second shot, four seconds into an excerpt that starts at a minute.
    await userEvent.click(within(edit).getByRole("button", { name: /^第 2 镜，/ }));
    await userEvent.click(await within(edit).findByRole("button", { name: "从这里预览" }));
    const rough = await screen.findByRole("dialog", { name: "粗预览" });
    expect(rough.querySelector("audio")).toHaveAttribute(
      "src",
      "/api/v1/montage/drafts/draft%3Aversion%3A1/song?start=64.00",
    );
  });

  it("edits a version shot by shot on the server's draft", async () => {
    const seen = fresh();
    serve(
      () => project({ versions: [...saved].reverse(), job: job("done") }),
      () => job("done"),
      seen,
    );
    const page = await openProject();
    const first = await within(page).findByRole("article", { name: "方案一" });
    await userEvent.click(within(first).getByRole("button", { name: "修改镜头" }));
    const edit = await screen.findByRole("main", { name: "第 1 版 · 精修镜头" });
    // The music's parts coloured by strength, the shots grouped by part.
    expect(within(edit).getByText("海边", { selector: "[data-strength] *, [data-strength]" })).toHaveAttribute(
      "data-strength",
      "mid",
    );
    const sea = within(edit).getByRole("region", { name: "段落 1" });
    expect(within(sea).getByText("竖")).toBeInTheDocument();
    expect(within(sea).getByText("↔ 拖动换顺序")).toBeInTheDocument();
    // A shot opens under its part, with the beats it could have; a double click puts one in.
    await userEvent.click(within(sea).getByRole("button", { name: /^第 1 镜，/ }));
    const drawer = await within(sea).findByRole("group", { name: "第 1 镜" });
    expect(within(drawer).getByText("规划要：海边散步")).toBeInTheDocument();
    await userEvent.dblClick(await within(drawer).findByRole("button", { name: "海边 · 浪花" }));
    await waitFor(() =>
      expect(seen.ops).toEqual([{ baseRevision: 0, op: { type: "replace", shot: 1, beat: "c1/2" } }]),
    );
    // Each step says what it did, with 撤销, which is a step on the new revision.
    await userEvent.click(await screen.findByRole("button", { name: "撤销" }));
    await waitFor(() => expect(seen.ops[1]).toEqual({ baseRevision: 1, op: { type: "undo" } }));
    // A step refused says why; one on a revision another window moved reads the draft again.
    seen.refuse = { status: 422, code: "edit_refused", message: "前后两镜都不够长，换一个镜头代替" };
    await userEvent.click(within(drawer).getByRole("button", { name: "删除这一镜" }));
    expect(await screen.findByText("前后两镜都不够长，换一个镜头代替")).toBeInTheDocument();
    const before = seen.drafts;
    seen.refuse = { status: 409, code: "draft_conflict", message: "the draft changed since; read it again" };
    await userEvent.click(within(sea).getByRole("button", { name: "锁定第 2 镜" }));
    expect(await screen.findByText("修改稿在别的窗口改过，已重新读取")).toBeInTheDocument();
    await waitFor(() => expect(seen.drafts).toBeGreaterThan(before));
    // Nothing locked: a shuffle asks first.
    await userEvent.click(within(edit).getByRole("button", { name: "重洗没锁的" }));
    expect(await screen.findByText("还没有锁定镜头，重洗会换掉全部 3 个镜头。")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "先去锁定" }));
    expect(seen.ops.some((step) => step.op.type === "shuffle")).toBe(false);
    await userEvent.click(within(edit).getByRole("button", { name: "重洗没锁的" }));
    await userEvent.click(await screen.findByRole("button", { name: "全部重洗" }));
    await waitFor(() => expect(seen.ops.at(-1)?.op).toEqual({ type: "shuffle" }));
    expect(within(edit).getByRole("link", { name: "导出镜头清单" })).toHaveAttribute(
      "href",
      "/api/v1/montage/drafts/draft%3Aversion%3A1/shots",
    );
    // The rough preview plays the draft over its song.
    await userEvent.click(within(edit).getByRole("button", { name: "粗预览" }));
    const rough = await screen.findByRole("dialog", { name: "粗预览" });
    expect(within(rough).getByText("粗预览，不含放慢、竖拍垫底与淡出")).toBeInTheDocument();
    expect(rough.querySelector("audio")).toHaveAttribute("src", "/api/v1/montage/drafts/draft%3Aversion%3A1/song");
    await userEvent.click(within(rough).getByRole("button", { name: "关闭粗预览" }));
    // Saved as a new version: back on the project, the job shows as generating's does.
    await userEvent.click(within(edit).getByRole("button", { name: "存为新版本" }));
    const back = await screen.findByRole("main", { name: "2026-08-02 至 08-04" });
    expect(seen.did).toContain("save");
    expect(await within(back).findByRole("list", { name: "准备" })).toHaveTextContent("读取修改稿");
  });
});
