import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";

import { http, server } from "../test/server";
import type { MontageJob, MontageProjectDetail } from "./api";
import { MontagePanel } from "./MontagePanel";

const steps = ["analysis", "catalog", "song", "pick", "shots", "fill", "render", "save"];

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
  seen: { generated: number; dropped?: string[] },
) {
  server.use(
    http.get("/api/v1/montage/projects", ({ response }) =>
      response(200).json({
        items: [{ id: "project:1", name: detail().name, assetCount: 2, createdAt: "2026-11-18T09:00:00Z" }],
      }),
    ),
    http.get("/api/v1/montage/projects/{projectId}", ({ response }) => response(200).json(detail())),
    http.get("/api/v1/montage/projects/{projectId}/song", ({ response }) =>
      response(404).json({ error: { code: "no_song", message: "the project has no song yet" } }),
    ),
    http.patch("/api/v1/montage/projects/{projectId}", async ({ request, response }) =>
      response(200).json({ ...detail(), ...((await request.json()) as object) }),
    ),
    http.post("/api/v1/montage/projects/{projectId}/generate", ({ response }) => {
      seen.generated++;
      return response(202).json(job("queued"));
    }),
    http.get("/api/v1/montage/jobs/{jobId}", ({ response }) => response(200).json(jobs())),
    http.post("/api/v1/montage/jobs/{jobId}/drop-assets", async ({ request, response }) => {
      seen.dropped = ((await request.json()) as { assetIds: string[] }).assetIds;
      return response(200).json(job("queued"));
    }),
  );
}

async function openProject() {
  render(<MontagePanel userId="user:alice" />);
  await userEvent.click(await screen.findByRole("button", { name: /2026-08-02 至 08-04/ }));
  return screen.findByRole("region", { name: "2026-08-02 至 08-04" });
}

afterEach(() => server.resetHandlers());

describe("剪辑中心", () => {
  it("starts a film, follows its job and shows the version it saved", async () => {
    const seen = { generated: 0 };
    let state: MontageJob["state"] = "queued";
    let versions: MontageProjectDetail["versions"] = [];
    serve(
      () => project({ versions }),
      () =>
        job(
          state,
          state === "done"
            ? { version: { number: 1, filmAssetId: "photo:film", createdAt: "2026-11-18T10:05:00Z" } }
            : {},
        ),
      seen,
    );
    const page = await openProject();
    expect(within(page).getByText("2 段视频，共 1.0 分钟")).toBeInTheDocument();
    expect(await within(page).findByText("还没有选歌：从音乐库里选一首。")).toBeInTheDocument();
    await userEvent.click(within(page).getByRole("button", { name: "开始" }));
    expect(seen.generated).toBe(1);
    expect(await within(page).findByText("排队中")).toBeInTheDocument();
    // The job goes on and saves a film: the page asks again and shows it.
    state = "done";
    versions = [{ number: 1, filmAssetId: "photo:film", createdAt: "2026-11-18T10:05:00Z" }];
    expect(await within(page).findByText("成片已存进相册", {}, { timeout: 4000 })).toBeInTheDocument();
    expect(await within(page).findByText(/第 1 版/)).toBeInTheDocument();
    expect(within(page).getByRole("link", { name: "下载" })).toHaveAttribute(
      "href",
      "/api/v1/photos/assets/photo%3Afilm/original?download=1",
    );
    expect(within(page).getByRole("button", { name: /再生成一版/ })).toBeInTheDocument();
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
    await userEvent.type(within(page).getByRole("textbox", { name: "一句话要求" }), "剪成竖屏，加字幕");
    expect(within(page).getByRole("note")).toHaveTextContent("首版固定横屏 1920×1080；首版不加字幕");
    expect(within(page).getByRole("button", { name: "开始" })).toBeEnabled();
  });
});
