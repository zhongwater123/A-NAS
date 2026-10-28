import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import type { FileEntry, Space } from "../api.generated";
import {
  type BackupOptions,
  backupHandlers,
  type Restore,
  restores,
  schedule,
  scheduleSaves,
} from "../test/handlers/backup";
import { filesHandlers } from "../test/handlers/files";
import { apiError, http, server } from "../test/server";
import type { RemovedEntry, Snapshot, SnapshotEntry, SnapshotMatch, SnapshotSchedule } from "./api";
import { BackupCenter } from "./BackupCenter";

const owner = "user:owner";
const personal: Space = {
  id: "space:personal",
  kind: "private",
  name: "owner",
  ownerUserId: owner,
  createdAt: "2026-10-01T00:00:00Z",
};
const shared: Space = { id: "space:shared", kind: "shared", name: "Shared", createdAt: "2026-10-01T00:00:00Z" };
const viewed: Space = {
  id: "space:alice",
  kind: "private",
  name: "alice",
  ownerUserId: "user:alice",
  createdAt: "2026-10-01T00:00:00Z",
  viewing: { grantId: "grant:1", expiresAt: "2026-10-12T12:00:00Z" },
};

const hoursAgo = (hours: number) => new Date(Date.now() - hours * 3_600_000).toISOString();
const nightly: Snapshot = {
  id: "snapshot:nightly",
  spaceId: personal.id,
  name: "定时快照 2026-10-12 03:00",
  origin: "scheduled",
  createdBy: "system",
  createdAt: hoursAgo(3),
  fileCount: 3,
  sizeBytes: 3_145_728,
};
const manual: Snapshot = {
  id: "snapshot:manual",
  spaceId: personal.id,
  name: "交付前",
  origin: "manual",
  createdBy: owner,
  createdAt: hoursAgo(30),
  fileCount: 2,
  sizeBytes: 2048,
};
const familyShot: Snapshot = {
  id: "snapshot:family",
  spaceId: shared.id,
  name: "整理家庭文件",
  origin: "manual",
  createdBy: owner,
  createdAt: hoursAgo(50),
  fileCount: 1,
  sizeBytes: 100,
};

const folder: SnapshotEntry = {
  id: "entry:docs",
  snapshotId: nightly.id,
  name: "文档",
  kind: "directory",
  sizeBytes: 0,
  modifiedAt: hoursAgo(40),
};
const plan: SnapshotEntry = {
  id: "entry:plan",
  snapshotId: nightly.id,
  parentId: folder.id,
  name: "计划.docx",
  kind: "file",
  sizeBytes: 2048,
  modifiedAt: hoursAgo(40),
};
const liveDocs: FileEntry = {
  id: "file:docs",
  spaceId: personal.id,
  name: "文档",
  kind: "directory",
  sizeBytes: 0,
  modifiedAt: hoursAgo(1),
};

// serve answers the backup center for the owner: the personal space with two
// snapshots and a daily schedule, the shared one with one snapshot and none,
// and alice's space, viewed through Administrative Viewing Mode. options
// replace any of that; entries are what the spaces hold now.
function serve({ entries = [liveDocs], ...options }: BackupOptions & { entries?: FileEntry[] } = {}) {
  server.use(
    ...filesHandlers({ spaces: [personal, shared, viewed], entries }),
    ...backupHandlers({
      snapshots: [nightly, manual, familyShot],
      schedules: [
        schedule(personal, { enabled: true, nextRunAt: new Date(Date.now() + 3_600_000).toISOString() }),
        schedule(shared),
      ],
      snapshotEntries: [folder, plan],
      ...options,
    }),
  );
}

// serveSchedules serves the backup center with schedules and returns the
// settings saved to them.
function serveSchedules(schedules: SnapshotSchedule[]) {
  const saves = scheduleSaves(schedules);
  serve({ schedules });
  server.use(saves.handler);
  return saves.sent;
}

describe("BackupCenter", () => {
  it("sums up how each of the user's own spaces is protected", async () => {
    serve();
    const user = userEvent.setup();
    render(<BackupCenter userId={owner} isAdmin />);
    expect(await screen.findByRole("heading", { name: "共享空间还没有开启定时快照" })).toBeTruthy();
    const personalCard = await screen.findByRole("region", { name: "个人空间" });
    await waitFor(() => expect(within(personalCard).getByText("定时 · 每天 03:00")).toBeTruthy());
    expect(within(personalCard).getByText("共 2 份快照")).toBeTruthy();
    const sharedCard = screen.getByRole("region", { name: "共享空间" });
    expect(within(sharedCard).getByText("未开启定时快照")).toBeTruthy();
    // A space viewed through Administrative Viewing Mode is not managed here.
    expect(screen.queryByText(/alice/)).toBeNull();
    // What snapshots cannot do is said beside the status, not below the fold.
    const coverage = screen.getByRole("region", { name: "能防范什么" });
    expect(within(coverage).getByRole("note", { name: "快照不是备份" }).textContent).toContain("尚未防护");
    // With a space unprotected, turning it on comes first and opens that space.
    const hero = screen.getByRole("region", { name: "保护状态" });
    await user.click(within(hero).getByRole("button", { name: "开启定时快照" }));
    expect(await screen.findByRole("heading", { name: "定时快照" })).toBeTruthy();
  });

  it("says nothing about protection until every space has answered", async () => {
    let release!: () => void;
    const pending = new Promise<void>((resolve) => {
      release = resolve;
    });
    serve();
    server.use(
      // Holds back the shared space's schedule; answering nothing, it then
      // passes the request on to serve's handler.
      http.get("/api/v1/spaces/{spaceId}/snapshot-schedule", async ({ params }) => {
        if (params.spaceId === shared.id) await pending;
      }),
    );
    render(<BackupCenter userId={owner} isAdmin />);
    const hero = await screen.findByRole("region", { name: "保护状态" });
    await waitFor(() => expect(within(hero).getByText("正在检查…")).toBeTruthy());
    expect(hero.getAttribute("aria-busy")).toBe("true");
    expect(within(hero).queryByRole("heading")).toBeNull();
    release();
    expect(await within(hero).findByRole("heading", { name: "共享空间还没有开启定时快照" })).toBeTruthy();
  });

  it("restores a file to its original folder and offers another name when it is taken", async () => {
    const sent: Restore[] = [];
    serve();
    server.use(
      http.post("/api/v1/snapshots/{snapshotId}/entries/{entryId}/restore", async ({ params, request, response }) => {
        const body = await request.json();
        sent.push({ snapshotId: params.snapshotId, entryId: params.entryId, body });
        // The original folder has a file of that name by the time of the first try.
        if (sent.length === 1)
          return response(409).json(apiError("name_conflict", "a file with this name already exists"));
        return response(201).json({
          id: "file:restored",
          spaceId: personal.id,
          parentId: liveDocs.id,
          name: body.name,
          kind: "file",
          sizeBytes: 2048,
          modifiedAt: hoursAgo(0),
        });
      }),
    );
    const user = userEvent.setup();
    render(<BackupCenter userId={owner} isAdmin />);
    await user.click(
      within(await screen.findByRole("navigation", { name: "备份中心导航" })).getByRole("button", { name: "快照" }),
    );
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
    expect(sent).toEqual([
      { snapshotId: nightly.id, entryId: plan.id, body: { parentId: liveDocs.id, name: "计划.docx" } },
      { snapshotId: nightly.id, entryId: plan.id, body: { parentId: liveDocs.id, name: name.value } },
    ]);
  });

  it("saves a schedule only when asked, and keeps the shared one read-only for members", async () => {
    const saved = serveSchedules([schedule(personal), schedule(shared, { canManage: false })]);
    const user = userEvent.setup();
    render(<BackupCenter userId={owner} isAdmin={false} />);
    await user.click(
      within(await screen.findByRole("navigation", { name: "备份中心导航" })).getByRole("button", { name: "定时快照" }),
    );

    const personalCard = await screen.findByRole("region", { name: "个人空间的定时快照" });
    await user.click(within(personalCard).getByRole("switch", { name: "开启个人空间的定时快照" }));
    // A new schedule starts on the default plan; the editor is behind 自定义.
    expect(within(personalCard).getByRole("radio", { name: /^标准/ }).getAttribute("aria-checked")).toBe("true");
    expect(within(personalCard).queryByRole("radio", { name: "每周" })).toBeNull();
    await user.click(within(personalCard).getByRole("radio", { name: /^自定义/ }));
    await user.click(within(personalCard).getByRole("radio", { name: "每周" }));
    expect((within(personalCard).getByRole("spinbutton", { name: "保留份数" }) as HTMLInputElement).value).toBe("8");
    await user.click(within(personalCard).getByRole("radio", { name: "周五" }));
    expect(saved).toEqual([]);
    await user.click(within(personalCard).getByRole("button", { name: "保存" }));
    await waitFor(() =>
      expect(saved).toEqual([
        {
          enabled: true,
          frequency: "weekly",
          time: "03:00",
          weekday: 5,
          keep: 8,
          retention: "count",
          tiers: { hourly: 0, daily: 0, weekly: 0, monthly: 0 },
        },
      ]),
    );
    expect(await screen.findByText("个人空间将每周五 03:00拍摄快照")).toBeTruthy();

    const sharedCard = screen.getByRole("region", { name: "共享空间的定时快照" });
    expect((within(sharedCard).getByRole("switch") as HTMLButtonElement).disabled).toBe(true);
    expect(within(sharedCard).getByText(/管理员尚未为共享空间开启定时快照/)).toBeTruthy();
  });

  it("reaches far back with tiered retention, keeping fewer snapshots the older they are", async () => {
    const saved = serveSchedules([schedule(personal, { enabled: true }), schedule(shared)]);
    const user = userEvent.setup();
    render(<BackupCenter userId={owner} isAdmin />);
    await user.click(
      within(await screen.findByRole("navigation", { name: "备份中心导航" })).getByRole("button", { name: "定时快照" }),
    );

    const card = await screen.findByRole("region", { name: "个人空间的定时快照" });
    expect(within(card).getByRole("img").getAttribute("aria-label")).toBe("保留 14 份定时快照，最早约 14 天前");
    // 长期 is tiered retention under its plain name, with nothing to tune.
    await user.click(within(card).getByRole("radio", { name: /^长期/ }));
    expect(within(card).queryByRole("spinbutton")).toBeNull();
    expect(within(card).getByRole("img").getAttribute("aria-label")).toBe("保留约 31 份定时快照，最早约 1 年前");
    await user.click(within(card).getByRole("radio", { name: /^自定义/ }));
    expect(within(card).getByRole("radio", { name: "智能保留" }).getAttribute("aria-checked")).toBe("true");
    const value = (name: string) => (within(card).getByRole("spinbutton", { name }) as HTMLInputElement).value;
    expect([value("保留最近几天"), value("保留最近几周"), value("保留最近几个月")]).toEqual(["14", "8", "12"]);
    expect(within(card).queryByRole("spinbutton", { name: "保留最近几小时" })).toBeNull();

    // Hourly snapshots add a day of hours to the suggested tiers.
    await user.click(within(card).getByRole("radio", { name: "每小时" }));
    expect(value("保留最近几小时")).toBe("24");
    await user.click(within(card).getByRole("button", { name: "保留最近几周减一" }));
    expect(within(card).getByText("越近越密，最远可回到约 12 个月前，最多 57 份")).toBeTruthy();
    await user.click(within(card).getByRole("button", { name: "保存" }));
    await waitFor(() =>
      expect(saved).toEqual([
        {
          enabled: true,
          frequency: "hourly",
          time: "03:00",
          weekday: 0,
          keep: 24,
          retention: "tiered",
          tiers: { hourly: 24, daily: 14, weekly: 7, monthly: 12 },
        },
      ]),
    );
  });

  it("saves a plan as its schedule and puts back what was saved", async () => {
    const saved = serveSchedules([schedule(personal, { enabled: true, time: "02:30" }), schedule(shared)]);
    const user = userEvent.setup();
    render(<BackupCenter userId={owner} isAdmin />);
    await user.click(
      within(await screen.findByRole("navigation", { name: "备份中心导航" })).getByRole("button", { name: "定时快照" }),
    );
    const card = await screen.findByRole("region", { name: "个人空间的定时快照" });

    await user.click(within(card).getByRole("radio", { name: /^频繁/ }));
    expect(within(card).getByRole("combobox", { name: "分钟" })).toBeTruthy();
    expect(within(card).queryByRole("combobox", { name: "小时" })).toBeNull();
    await user.click(within(card).getByRole("button", { name: "还原" }));
    expect(within(card).getByRole("radio", { name: /^标准/ }).getAttribute("aria-checked")).toBe("true");
    expect(within(card).queryByRole("button", { name: "保存" })).toBeNull();

    await user.click(within(card).getByRole("radio", { name: /^频繁/ }));
    await user.click(within(card).getByRole("button", { name: "保存" }));
    await waitFor(() =>
      expect(saved).toEqual([
        {
          enabled: true,
          frequency: "hourly",
          time: "02:30",
          weekday: 0,
          keep: 24,
          retention: "tiered",
          tiers: { hourly: 24, daily: 14, weekly: 8, monthly: 12 },
        },
      ]),
    );
  });

  it("marks what differs from the space now and restores a whole snapshot into a new folder", async () => {
    const removedFile: SnapshotEntry = {
      id: "entry:photo",
      snapshotId: nightly.id,
      name: "照片.jpg",
      kind: "file",
      sizeBytes: 4096,
      modifiedAt: hoursAgo(60),
      change: "removed",
    };
    const readme: SnapshotEntry = {
      id: "entry:readme",
      snapshotId: nightly.id,
      name: "说明.txt",
      kind: "file",
      sizeBytes: 12,
      modifiedAt: hoursAgo(60),
    };
    const restored = restores(({ body }) => ({
      id: "file:whole",
      spaceId: personal.id,
      name: body.name,
      kind: "directory",
      sizeBytes: 0,
      modifiedAt: hoursAgo(0),
    }));
    serve({ snapshotEntries: [{ ...folder, change: "inside" }, removedFile, readme, plan] });
    server.use(...restored.handlers);
    const user = userEvent.setup();
    render(<BackupCenter userId={owner} isAdmin />);
    await user.click(
      within(await screen.findByRole("navigation", { name: "备份中心导航" })).getByRole("button", { name: "快照" }),
    );
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
    expect(restored.sent).toEqual([{ snapshotId: nightly.id, body: { parentId: "", name: name.value } }]);
  });

  it("lists what is gone from every space before anything is typed", async () => {
    const contracts: RemovedEntry = {
      path: "合同",
      name: "合同",
      kind: "directory",
      sizeBytes: 3_145_728,
      fileCount: 2,
      snapshotId: nightly.id,
      snapshotName: nightly.name,
      origin: "scheduled",
      entryId: "entry:contracts",
      lastSeenAt: nightly.createdAt,
      inTrash: true,
    };
    const photo: RemovedEntry = {
      path: "相片/照片.jpg",
      name: "照片.jpg",
      kind: "file",
      sizeBytes: 4096,
      fileCount: 1,
      snapshotId: manual.id,
      snapshotName: manual.name,
      origin: "manual",
      entryId: "entry:photo",
      lastSeenAt: manual.createdAt,
      goneBy: nightly.createdAt,
    };
    const recipe: RemovedEntry = {
      path: "家庭/菜谱.docx",
      name: "菜谱.docx",
      kind: "file",
      sizeBytes: 100,
      fileCount: 1,
      snapshotId: familyShot.id,
      snapshotName: familyShot.name,
      origin: "manual",
      entryId: "entry:recipe",
      lastSeenAt: familyShot.createdAt,
    };
    const restored = restores(() => ({
      id: "file:contracts",
      spaceId: personal.id,
      name: "合同",
      kind: "directory",
      sizeBytes: 0,
      modifiedAt: hoursAgo(0),
    }));
    const listed: string[] = [];
    serve({ removed: { [personal.id]: [contracts, photo], [shared.id]: [recipe] } });
    server.use(
      ...restored.handlers,
      // Counts the lists asked for; answering nothing, it passes each on to serve's handler.
      http.get("/api/v1/spaces/{spaceId}/removed-entries", ({ params }) => {
        listed.push(params.spaceId);
      }),
    );
    const user = userEvent.setup();
    const openTrash = vi.fn();
    render(<BackupCenter userId={owner} isAdmin onOpenTrash={openTrash} />);
    await user.click(
      within(await screen.findByRole("navigation", { name: "备份中心导航" })).getByRole("button", { name: "找回文件" }),
    );

    const list = await screen.findByRole("region", { name: "最近删除或移走" });
    expect(within(list).getByText("2 个文件 · 3.0 MiB")).toBeTruthy();
    expect(within(list).getByText("个人空间 / 相片")).toBeTruthy();
    // Newest losses first, whichever space they were in.
    expect(
      within(list)
        .getAllByRole("listitem")
        .map((item) => item.querySelector("strong")?.firstChild?.textContent),
    ).toEqual(["合同", "照片.jpg", "菜谱.docx"]);
    expect(within(list).getByText("共享空间 / 家庭")).toBeTruthy();
    await user.click(within(list).getByRole("button", { name: "在回收站中" }));
    expect(openTrash).toHaveBeenCalledTimes(1);
    expect(within(list).getByRole("link", { name: "下载 照片.jpg" }).getAttribute("href")).toBe(
      `/api/v1/snapshots/${encodeURIComponent(manual.id)}/entries/${encodeURIComponent(photo.entryId)}/content`,
    );
    await user.click(within(list).getByRole("button", { name: "恢复 合同" }));
    const dialog = await screen.findByRole("dialog", { name: "恢复文件夹" });
    // The recycle bin holds what it was when deleted, newer than the snapshot.
    expect(within(dialog).getByText(/回收站里还有它/)).toBeTruthy();
    expect(within(dialog).getByRole("button", { name: "去回收站还原" })).toBeTruthy();
    await waitFor(() => expect(within(dialog).getByText("原位置")).toBeTruthy());
    await user.click(within(dialog).getByRole("button", { name: "恢复" }));
    expect(await screen.findByText("已恢复“合同”到 个人空间")).toBeTruthy();
    expect(restored.sent).toEqual([
      { snapshotId: nightly.id, entryId: contracts.entryId, body: { parentId: "", name: "合同" } },
    ]);
    // The restore reloads the list.
    await waitFor(() => expect(listed.filter((spaceId) => spaceId === personal.id)).toHaveLength(2));

    // Narrowed to one space, the others' items leave the list.
    await user.click(screen.getByRole("radio", { name: "个人空间" }));
    await waitFor(() => expect(screen.queryByText("菜谱.docx")).toBeNull());
    expect(await screen.findByText("照片.jpg")).toBeTruthy();
  });

  it("suggests a new name before restoring when the original folder already has one", async () => {
    const restored = restores(({ body }) => ({
      id: "file:copy",
      spaceId: personal.id,
      parentId: liveDocs.id,
      name: body.name,
      kind: "file",
      sizeBytes: 2048,
      modifiedAt: hoursAgo(0),
    }));
    serve({
      entries: [
        liveDocs,
        {
          id: "file:plan",
          spaceId: personal.id,
          parentId: liveDocs.id,
          name: "计划.DOCX",
          kind: "file",
          sizeBytes: 10,
          modifiedAt: hoursAgo(1),
        },
      ],
    });
    server.use(...restored.handlers);
    const showInFiles = vi.fn();
    const user = userEvent.setup();
    render(<BackupCenter userId={owner} isAdmin onShowInFiles={showInFiles} />);
    await user.click(
      within(await screen.findByRole("navigation", { name: "备份中心导航" })).getByRole("button", { name: "快照" }),
    );
    const browser = await screen.findByRole("region", { name: `快照 ${nightly.name}` });
    await user.click(await within(browser).findByRole("button", { name: "文档" }));
    await user.click(await within(browser).findByRole("button", { name: "恢复 计划.docx" }));

    const dialog = await screen.findByRole("dialog", { name: "恢复文件" });
    const name = within(dialog).getByRole("textbox", { name: "恢复后的名称" }) as HTMLInputElement;
    await waitFor(() => expect(name.value).toMatch(/^计划 \(恢复 \d\d-\d\d \d\d\.\d\d\)\.docx$/));
    expect(within(dialog).getByText(/原位置已有“计划.docx”/)).toBeTruthy();
    // Typing the taken name back is caught before anything is sent.
    await user.clear(name);
    await user.type(name, "计划.docx");
    expect(within(dialog).getByRole("alert").textContent).toContain("已有名为“计划.docx”的项目");
    expect((within(dialog).getByRole("button", { name: "恢复" }) as HTMLButtonElement).disabled).toBe(true);
    await user.click(within(dialog).getByRole("button", { name: /^改用/ }));
    await user.click(within(dialog).getByRole("button", { name: "恢复" }));

    const toast = await screen.findByText(/已恢复“计划 \(恢复/);
    expect(restored.sent).toEqual([
      { snapshotId: nightly.id, entryId: plan.id, body: { parentId: liveDocs.id, name: name.value } },
    ]);
    await user.click(
      within(toast.closest<HTMLElement>("[role=status]")!).getByRole("button", { name: "在文件管理中显示" }),
    );
    expect(showInFiles).toHaveBeenCalledWith({
      spaceId: personal.id,
      spaceKind: "private",
      folders: [{ id: liveDocs.id, name: "文档" }],
    });
  });

  it("finds every version of a file and says when it is gone from the space", async () => {
    const match: SnapshotMatch = {
      path: "文档/计划.docx",
      name: "计划.docx",
      kind: "file",
      inSpace: false,
      versions: [
        {
          snapshotId: nightly.id,
          snapshotName: nightly.name,
          origin: "scheduled",
          createdAt: nightly.createdAt,
          entryId: plan.id,
          sizeBytes: 2048,
          modifiedAt: hoursAgo(40),
          snapshots: 2,
          oldestAt: manual.createdAt,
        },
        {
          snapshotId: "snapshot:old",
          snapshotName: "旧",
          origin: "manual",
          createdAt: hoursAgo(200),
          entryId: "entry:old",
          sizeBytes: 1024,
          snapshots: 1,
          oldestAt: hoursAgo(200),
        },
      ],
    };
    const current: SnapshotMatch = {
      path: "共享资料/计划书.pdf",
      name: "计划书.pdf",
      kind: "file",
      inSpace: true,
      versions: [
        {
          snapshotId: familyShot.id,
          snapshotName: familyShot.name,
          origin: "manual",
          createdAt: familyShot.createdAt,
          entryId: "entry:book",
          sizeBytes: 64,
          snapshots: 1,
          oldestAt: familyShot.createdAt,
          current: true,
        },
      ],
    };
    serve({ matches: { [personal.id]: [match], [shared.id]: [current] } });
    const user = userEvent.setup();
    render(<BackupCenter userId={owner} isAdmin />);
    await user.click(
      within(await screen.findByRole("navigation", { name: "备份中心导航" })).getByRole("button", { name: "找回文件" }),
    );
    await user.type(screen.getByRole("textbox", { name: "在快照中查找" }), "计划");

    const result = await screen.findByText("已不在空间中");
    const card = result.closest("li")!;
    expect(within(card).getByText("个人空间 / 文档")).toBeTruthy();
    expect(within(card).getByText("2 个版本")).toBeTruthy();
    expect(within(card).getByText(/保存在 2 份快照中/)).toBeTruthy();
    expect(within(card).getAllByRole("link", { name: "下载这个版本的 计划.docx" })[0].getAttribute("href")).toBe(
      `/api/v1/snapshots/${encodeURIComponent(nightly.id)}/entries/${encodeURIComponent(plan.id)}/content`,
    );
    // The search covers every space; the version the space holds now has nothing to restore.
    const sharedCard = (await screen.findByText("共享空间 / 共享资料")).closest("li")!;
    expect(within(sharedCard).getByText("当前版本")).toBeTruthy();
    expect(within(sharedCard).queryByRole("button", { name: "恢复" })).toBeNull();
  });
});
