import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { FileEntry, Space } from "../api";
import { BackupCenter } from "./BackupCenter";
import type { RemovedEntry, Snapshot, SnapshotEntry, SnapshotMatch, SnapshotSchedule } from "./backupApi";

const owner = "user:owner";
const personal: Space = { id: "space:personal", kind: "private", name: "owner", ownerUserId: owner, createdAt: "2026-10-01T00:00:00Z" };
const shared: Space = { id: "space:shared", kind: "shared", name: "Shared", createdAt: "2026-10-01T00:00:00Z" };
const viewed: Space = { id: "space:alice", kind: "private", name: "alice", ownerUserId: "user:alice", createdAt: "2026-10-01T00:00:00Z", viewing: { grantId: "grant:1", expiresAt: "2026-10-12T12:00:00Z" } };

const hoursAgo = (hours: number) => new Date(Date.now() - hours * 3_600_000).toISOString();
const nightly: Snapshot = { id: "snapshot:nightly", spaceId: personal.id, name: "定时快照 2026-10-12 03:00", origin: "scheduled", createdBy: "system", createdAt: hoursAgo(3), fileCount: 3, sizeBytes: 3_145_728 };
const manual: Snapshot = { id: "snapshot:manual", spaceId: personal.id, name: "交付前", origin: "manual", createdBy: owner, createdAt: hoursAgo(30), fileCount: 2, sizeBytes: 2048 };
const familyShot: Snapshot = { id: "snapshot:family", spaceId: shared.id, name: "整理家庭文件", origin: "manual", createdBy: owner, createdAt: hoursAgo(50), fileCount: 1, sizeBytes: 100 };

function schedule(space: Space, overrides: Partial<SnapshotSchedule> = {}): SnapshotSchedule {
  return {
    spaceId: space.id, enabled: false, frequency: "daily", time: "03:00", weekday: 0, keep: 14, retention: "count", tiers: { hourly: 0, daily: 0, weekly: 0, monthly: 0 },
    canManage: true, ...overrides,
  };
}

const folder: SnapshotEntry = { id: "entry:docs", snapshotId: nightly.id, name: "文档", kind: "directory", sizeBytes: 0, modifiedAt: hoursAgo(40) };
const plan: SnapshotEntry = { id: "entry:plan", snapshotId: nightly.id, parentId: folder.id, name: "计划.docx", kind: "file", sizeBytes: 2048, modifiedAt: hoursAgo(40) };
const liveDocs: FileEntry = { id: "file:docs", spaceId: personal.id, name: "文档", kind: "directory", sizeBytes: 0, modifiedAt: hoursAgo(1) };

function json(body: unknown, status = 200) {
  return new Response(status === 204 ? null : JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

interface Call { url: string; method: string; body?: unknown }
type Route = (url: string, init?: RequestInit) => Response | undefined;

function serve(options: { schedules?: Record<string, SnapshotSchedule>; routes?: Route[] } = {}) {
  const calls: Call[] = [];
  const schedules = options.schedules ?? { [personal.id]: schedule(personal, { enabled: true, nextRunAt: new Date(Date.now() + 3_600_000).toISOString() }), [shared.id]: schedule(shared) };
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    calls.push({ url, method, body: init?.body ? JSON.parse(String(init.body)) : undefined });
    for (const route of options.routes ?? []) {
      const response = route(url, init);
      if (response) return response;
    }
    if (url === "/api/v1/spaces") return json({ items: [personal, shared, viewed] });
    if (url === `/api/v1/spaces/${encodeURIComponent(personal.id)}/snapshots`) return json({ items: [nightly, manual] });
    if (url === `/api/v1/spaces/${encodeURIComponent(shared.id)}/snapshots`) return json({ items: [familyShot] });
    const scheduled = Object.values(schedules).find((item) => url === `/api/v1/spaces/${encodeURIComponent(item.spaceId)}/snapshot-schedule`);
    if (scheduled && method === "GET") return json(scheduled);
    if (scheduled && method === "PUT") return json({ ...scheduled, ...JSON.parse(String(init?.body)) });
    if (url === `/api/v1/snapshots/${encodeURIComponent(nightly.id)}/entries?parentId=`) return json({ items: [folder] });
    if (url === `/api/v1/snapshots/${encodeURIComponent(nightly.id)}/entries?parentId=${encodeURIComponent(folder.id)}`) return json({ items: [plan] });
    if (url.startsWith(`/api/v1/snapshots/${encodeURIComponent(manual.id)}/entries`)) return json({ items: [] });
    if (url === `/api/v1/spaces/${encodeURIComponent(personal.id)}/entries?parentId=`) return json({ items: [liveDocs] });
    if (url.includes("/entries?parentId=")) return json({ items: [] });
    return json({ error: { code: "not_found", message: "not found" } }, 404);
  }));
  return calls;
}

afterEach(() => vi.unstubAllGlobals());

describe("BackupCenter", () => {
  it("sums up how each of the user's own spaces is protected", async () => {
    serve();
    render(<BackupCenter userId={owner} isAdmin />);
    expect(await screen.findByRole("heading", { name: "1 个空间尚未开启定时快照" })).toBeTruthy();
    const personalCard = await screen.findByRole("region", { name: "个人空间" });
    await waitFor(() => expect(within(personalCard).getByText("定时 · 每天 03:00")).toBeTruthy());
    expect(within(personalCard).getByText("2")).toBeTruthy();
    expect(within(screen.getByRole("region", { name: "共享空间" })).getByText("未开启定时快照")).toBeTruthy();
    // A space viewed through Administrative Viewing Mode is not managed here.
    expect(screen.queryByText(/alice/)).toBeNull();
    expect(screen.getByRole("region", { name: "快照不是备份" })).toBeTruthy();
  });

  it("restores a file to its original folder and offers another name when it is taken", async () => {
    let attempts = 0;
    const calls = serve({
      routes: [(url, init) => {
        if (!url.endsWith(`/entries/${encodeURIComponent(plan.id)}/restore`)) return undefined;
        attempts += 1;
        const body = JSON.parse(String(init?.body));
        return attempts === 1
          ? json({ error: { code: "name_conflict", message: "a file with this name already exists" } }, 409)
          : json({ id: "file:restored", spaceId: personal.id, parentId: liveDocs.id, name: body.name, kind: "file", sizeBytes: 2048, modifiedAt: hoursAgo(0) }, 201);
      }],
    });
    const user = userEvent.setup();
    render(<BackupCenter userId={owner} isAdmin />);
    await user.click(within(await screen.findByRole("navigation", { name: "备份中心导航" })).getByRole("button", { name: "快照" }));
    const browser = await screen.findByRole("region", { name: `快照 ${nightly.name}` });
    await user.click(await within(browser).findByRole("button", { name: "文档" }));
    await user.click(await within(browser).findByRole("button", { name: "恢复 计划.docx" }));

    const dialog = await screen.findByRole("dialog", { name: "恢复文件" });
    await waitFor(() => expect(within(dialog).getByText("个人空间 / 文档")).toBeTruthy());
    expect(within(dialog).getByText("原位置")).toBeTruthy();
    await user.click(within(dialog).getByRole("button", { name: "恢复" }));
    expect((await within(dialog).findByRole("alert")).textContent).toContain("已有名为“计划.docx”的项目");
    await user.click(within(dialog).getByRole("button", { name: /^改用/ }));
    const name = within(dialog).getByRole("textbox", { name: "恢复后的名称" }) as HTMLInputElement;
    expect(name.value).toMatch(/^计划 \(恢复 \d\d-\d\d \d\d\.\d\d\)\.docx$/);
    await user.click(within(dialog).getByRole("button", { name: "恢复" }));

    expect(await screen.findByText(/已恢复“计划 \(恢复/)).toBeTruthy();
    expect(screen.queryByRole("dialog", { name: "恢复文件" })).toBeNull();
    const restores = calls.filter((call) => call.url.endsWith("/restore"));
    expect(restores.map((call) => call.body)).toEqual([
      { parentId: liveDocs.id, name: "计划.docx" },
      { parentId: liveDocs.id, name: name.value },
    ]);
  });

  it("saves a schedule only when asked, and keeps the shared one read-only for members", async () => {
    const calls = serve({ schedules: { [personal.id]: schedule(personal), [shared.id]: schedule(shared, { canManage: false }) } });
    const user = userEvent.setup();
    render(<BackupCenter userId={owner} isAdmin={false} />);
    await user.click(within(await screen.findByRole("navigation", { name: "备份中心导航" })).getByRole("button", { name: "定时快照" }));

    const personalCard = await screen.findByRole("region", { name: "个人空间的定时快照" });
    await user.click(within(personalCard).getByRole("switch", { name: "开启个人空间的定时快照" }));
    await user.click(within(personalCard).getByRole("radio", { name: "每周" }));
    expect((within(personalCard).getByRole("spinbutton", { name: "保留份数" }) as HTMLInputElement).value).toBe("8");
    await user.click(within(personalCard).getByRole("radio", { name: "周五" }));
    expect(calls.some((call) => call.method === "PUT")).toBe(false);
    await user.click(within(personalCard).getByRole("button", { name: "保存" }));
    await waitFor(() => expect(calls.filter((call) => call.method === "PUT").map((call) => call.body)).toEqual([
      { enabled: true, frequency: "weekly", time: "03:00", weekday: 5, keep: 8, retention: "count", tiers: { hourly: 0, daily: 0, weekly: 0, monthly: 0 } },
    ]));
    expect(await screen.findByText("个人空间将每周五 03:00拍摄快照")).toBeTruthy();

    const sharedCard = screen.getByRole("region", { name: "共享空间的定时快照" });
    expect((within(sharedCard).getByRole("switch") as HTMLButtonElement).disabled).toBe(true);
    expect(within(sharedCard).getByText(/管理员尚未为共享空间开启定时快照/)).toBeTruthy();
  });

  it("reaches far back with tiered retention, keeping fewer snapshots the older they are", async () => {
    const calls = serve({ schedules: { [personal.id]: schedule(personal, { enabled: true }), [shared.id]: schedule(shared) } });
    const user = userEvent.setup();
    render(<BackupCenter userId={owner} isAdmin />);
    await user.click(within(await screen.findByRole("navigation", { name: "备份中心导航" })).getByRole("button", { name: "定时快照" }));

    const card = await screen.findByRole("region", { name: "个人空间的定时快照" });
    expect(within(card).getByRole("img").getAttribute("aria-label")).toBe("保留 14 份定时快照，最早约 14 天前");
    await user.click(within(card).getByRole("radio", { name: "智能保留" }));
    const value = (name: string) => (within(card).getByRole("spinbutton", { name }) as HTMLInputElement).value;
    expect([value("保留最近几天"), value("保留最近几周"), value("保留最近几个月")]).toEqual(["14", "8", "12"]);
    expect(within(card).queryByRole("spinbutton", { name: "保留最近几小时" })).toBeNull();
    expect(within(card).getByRole("img").getAttribute("aria-label")).toBe("保留约 31 份定时快照，最早约 1 年前");

    // Hourly snapshots add a day of hours to the suggested tiers.
    await user.click(within(card).getByRole("radio", { name: "每小时" }));
    expect(value("保留最近几小时")).toBe("24");
    await user.click(within(card).getByRole("button", { name: "保留最近几周减一" }));
    expect(within(card).getByText("越近越密，最远可回到约 12 个月前，最多 57 份")).toBeTruthy();
    await user.click(within(card).getByRole("button", { name: "保存" }));
    await waitFor(() => expect(calls.filter((call) => call.method === "PUT").map((call) => call.body)).toEqual([
      { enabled: true, frequency: "hourly", time: "03:00", weekday: 0, keep: 24, retention: "tiered", tiers: { hourly: 24, daily: 14, weekly: 7, monthly: 12 } },
    ]));
  });

  it("marks what differs from the space now and restores a whole snapshot into a new folder", async () => {
    const removedFile: SnapshotEntry = { id: "entry:photo", snapshotId: nightly.id, name: "照片.jpg", kind: "file", sizeBytes: 4096, modifiedAt: hoursAgo(60), change: "removed" };
    const readme: SnapshotEntry = { id: "entry:readme", snapshotId: nightly.id, name: "说明.txt", kind: "file", sizeBytes: 12, modifiedAt: hoursAgo(60) };
    const calls = serve({
      routes: [(url, init) => {
        if (url === `/api/v1/snapshots/${encodeURIComponent(nightly.id)}/entries?parentId=`) return json({ items: [{ ...folder, change: "inside" }, removedFile, readme] });
        if (url === `/api/v1/snapshots/${encodeURIComponent(nightly.id)}/restore`) {
          return json({ id: "file:whole", spaceId: personal.id, name: JSON.parse(String(init?.body)).name, kind: "directory", sizeBytes: 0, modifiedAt: hoursAgo(0) }, 201);
        }
        return undefined;
      }],
    });
    const user = userEvent.setup();
    render(<BackupCenter userId={owner} isAdmin />);
    await user.click(within(await screen.findByRole("navigation", { name: "备份中心导航" })).getByRole("button", { name: "快照" }));
    const browser = await screen.findByRole("region", { name: `快照 ${nightly.name}` });
    expect(await within(browser).findByText("内有变化")).toBeTruthy();
    expect(within(browser).getByText("已不在空间中")).toBeTruthy();
    expect(within(browser).getByText(/1 项已不在空间中/)).toBeTruthy();
    await user.click(within(browser).getByRole("radio", { name: /与现在不同/ }));
    expect(within(browser).queryByText("说明.txt")).toBeNull();
    expect(within(browser).getByText("照片.jpg")).toBeTruthy();

    await user.click(within(browser).getByRole("button", { name: "全部恢复" }));
    const dialog = await screen.findByRole("dialog", { name: "恢复整份快照" });
    const name = within(dialog).getByRole("textbox", { name: "恢复后的名称" }) as HTMLInputElement;
    expect(name.value).toMatch(/^定时快照 \d\d-\d\d \d\d\.\d\d$/);
    await user.click(within(dialog).getByRole("button", { name: "恢复" }));
    expect(await screen.findByText(`已恢复“${name.value}”到 个人空间`)).toBeTruthy();
    expect(calls.filter((call) => call.url.endsWith("/restore")).map((call) => call.body)).toEqual([{ parentId: "", name: name.value }]);
  });

  it("lists what is gone from the space before anything is typed", async () => {
    const contracts: RemovedEntry = {
      path: "合同", name: "合同", kind: "directory", sizeBytes: 3_145_728, fileCount: 2, snapshotId: nightly.id, snapshotName: nightly.name,
      origin: "scheduled", entryId: "entry:contracts", lastSeenAt: nightly.createdAt, inTrash: true,
    };
    const photo: RemovedEntry = {
      path: "相片/照片.jpg", name: "照片.jpg", kind: "file", sizeBytes: 4096, fileCount: 1, snapshotId: manual.id, snapshotName: manual.name,
      origin: "manual", entryId: "entry:photo", lastSeenAt: manual.createdAt, goneBy: nightly.createdAt,
    };
    const calls = serve({
      routes: [(url) => {
        if (url === `/api/v1/spaces/${encodeURIComponent(personal.id)}/removed-entries`) return json({ items: [contracts, photo], truncated: false });
        if (url.endsWith(`/entries/${encodeURIComponent(contracts.entryId)}/restore`)) {
          return json({ id: "file:contracts", spaceId: personal.id, name: "合同", kind: "directory", sizeBytes: 0, modifiedAt: hoursAgo(0) }, 201);
        }
        return undefined;
      }],
    });
    const user = userEvent.setup();
    render(<BackupCenter userId={owner} isAdmin />);
    await user.click(within(await screen.findByRole("navigation", { name: "备份中心导航" })).getByRole("button", { name: "找回文件" }));

    const list = await screen.findByRole("region", { name: "最近删除或移走" });
    expect(within(list).getByText("2 个文件 · 3.0 MiB")).toBeTruthy();
    expect(within(list).getAllByText("在回收站中")).toHaveLength(1);
    expect(within(list).getByText("个人空间 / 相片")).toBeTruthy();
    expect(within(list).getByRole("link", { name: "下载 照片.jpg" }).getAttribute("href"))
      .toBe(`/api/v1/snapshots/${encodeURIComponent(manual.id)}/entries/${encodeURIComponent(photo.entryId)}/content`);
    await user.click(within(list).getByRole("button", { name: "恢复 合同" }));
    const dialog = await screen.findByRole("dialog", { name: "恢复文件夹" });
    await waitFor(() => expect(within(dialog).getByText("原位置")).toBeTruthy());
    await user.click(within(dialog).getByRole("button", { name: "恢复" }));
    expect(await screen.findByText("已恢复“合同”到 个人空间")).toBeTruthy();
    expect(calls.filter((call) => call.url.endsWith("/restore")).map((call) => call.body)).toEqual([{ parentId: "", name: "合同" }]);
    // The restore reloads the list.
    await waitFor(() => expect(calls.filter((call) => call.url.endsWith("/removed-entries"))).toHaveLength(2));
  });

  it("finds every version of a file and says when it is gone from the space", async () => {
    const match: SnapshotMatch = {
      path: "文档/计划.docx", name: "计划.docx", kind: "file", inSpace: false,
      versions: [
        { snapshotId: nightly.id, snapshotName: nightly.name, origin: "scheduled", createdAt: nightly.createdAt, entryId: plan.id, sizeBytes: 2048, modifiedAt: hoursAgo(40), snapshots: 2, oldestAt: manual.createdAt },
        { snapshotId: "snapshot:old", snapshotName: "旧", origin: "manual", createdAt: hoursAgo(200), entryId: "entry:old", sizeBytes: 1024, snapshots: 1, oldestAt: hoursAgo(200) },
      ],
    };
    serve({ routes: [(url) => (url.includes("/snapshot-search?q=") ? json({ items: [match], truncated: false }) : undefined)] });
    const user = userEvent.setup();
    render(<BackupCenter userId={owner} isAdmin />);
    await user.click(within(await screen.findByRole("navigation", { name: "备份中心导航" })).getByRole("button", { name: "找回文件" }));
    await user.type(screen.getByRole("textbox", { name: "在快照中查找" }), "计划");

    const result = await screen.findByText("已不在空间中");
    const card = result.closest("li")!;
    expect(within(card).getByText("个人空间 / 文档")).toBeTruthy();
    expect(within(card).getByText("2 个版本")).toBeTruthy();
    expect(within(card).getByText(/保存在 2 份快照中/)).toBeTruthy();
    expect(within(card).getAllByRole("link", { name: "下载这个版本的 计划.docx" })[0].getAttribute("href"))
      .toBe(`/api/v1/snapshots/${encodeURIComponent(nightly.id)}/entries/${encodeURIComponent(plan.id)}/content`);
  });
});
