import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import type { Space, TrashItem } from "../api.generated";
import { filesHandlers, folder } from "../test/handlers/files";
import { type Restore, restoredAs, trashHandlers, trashItem } from "../test/handlers/trash";
import { server } from "../test/server";
import { expiryOf, groupItems, restoredName } from "./model";
import { TrashPanel } from "./TrashPanel";

const personal: Space = {
  id: "space:personal",
  kind: "private",
  name: "owner",
  ownerUserId: "user:owner",
  createdAt: "2026-10-01T00:00:00Z",
};
const shared: Space = { id: "space:shared", kind: "shared", name: "Shared", createdAt: "2026-10-01T00:00:00Z" };
const day = 86_400_000;
const at = (offset: number) => new Date(Date.now() + offset).toISOString();

function item(overrides: Partial<TrashItem>): TrashItem {
  return trashItem("计划.docx", {
    id: "trash:plan",
    entryId: "file:plan",
    spaceId: personal.id,
    sizeBytes: 2.3 * 1024 ** 2,
    originalPath: "文档/项目",
    deletedAt: at(-60_000),
    purgeAt: at(30 * day - 60_000),
    ...overrides,
  });
}

const plan = item({});
const photos = item({
  id: "trash:photos",
  entryId: "dir:photos",
  spaceId: shared.id,
  name: "旅行照片",
  kind: "directory",
  sizeBytes: 3 * 1024 ** 3,
  originalPath: "",
  deletedAt: at(-27 * day),
  purgeAt: at(3 * day),
});
const orphan = item({
  id: "trash:notes",
  entryId: "file:notes",
  name: "笔记.txt",
  sizeBytes: 900,
  originalPath: "旧项目",
  originalGone: true,
  deletedAt: at(-29.5 * day),
  purgeAt: at(0.5 * day),
});

// serve answers the recycle bin with items, or with what items returns at each
// read; the two spaces, the personal one holding 文档 but no 文档/项目; and
// restores through restore. It returns the restores and the items deleted
// for good.
function serve(items: TrashItem[] | (() => TrashItem[]), restore?: Restore) {
  const trash = trashHandlers(typeof items === "function" ? items : () => items, restore);
  server.use(
    ...filesHandlers({
      spaces: [personal, shared],
      entries: [folder("文档", { id: "dir:docs", spaceId: personal.id, modifiedAt: at(0) })],
    }),
    ...trash.handlers,
  );
  return trash;
}

const rows = () => screen.getAllByRole("listitem");
const row = (name: string) => rows().find((element) => element.querySelector("strong")?.textContent === name)!;
const names = () => rows().map((element) => element.querySelector("strong")?.textContent);

describe("TrashPanel", () => {
  it("says where each item came from, what it holds, when it was deleted and when it goes", async () => {
    serve([plan, photos, orphan]);
    render(<TrashPanel onShowInFiles={() => undefined} />);
    const card = await screen.findByRole("region", { name: "回收站项目" });
    const totals = screen.getByLabelText("回收站概况");
    expect(totals.textContent).toContain("项目3");
    expect(totals.textContent).toContain("占用空间3.0 GiB");
    expect(totals.textContent).toContain("7 天内清理2");

    // Grouped by the day they were deleted, newest first.
    expect(
      within(card)
        .getAllByRole("heading", { level: 3 })
        .map((heading) => heading.textContent),
    ).toEqual([
      expect.stringMatching(/^(今天|昨天)1 项$/),
      expect.stringMatching(/^\d+月\d+日.*1 项$/),
      expect.stringMatching(/^\d+月\d+日.*1 项$/),
    ]);
    expect(row("计划.docx").textContent).toContain("个人空间 / 文档 / 项目");
    expect(row("计划.docx").textContent).toContain("文档");
    expect(row("计划.docx").textContent).toContain("2.3 MiB");
    expect(row("计划.docx").textContent).toMatch(/删除于 \d\d:\d\d/);
    expect(row("计划.docx").textContent).toContain("剩余 30 天");
    expect(row("旅行照片").textContent).toContain("共享空间");
    expect(row("旅行照片").textContent).toContain("3.0 GiB");
    expect(row("旅行照片").textContent).toContain("剩余 3 天");
    expect(row("笔记.txt").textContent).toContain("个人空间 / 旧项目已不在");
    expect(row("笔记.txt").textContent).toContain("剩余 1 天内");

    // Sorting by expiry puts what goes first on top.
    const user = userEvent.setup();
    await user.click(within(card).getByRole("radio", { name: "即将清理" }));
    expect(names()).toEqual(["笔记.txt", "旅行照片", "计划.docx"]);
    expect(
      within(card)
        .getAllByRole("heading", { level: 3 })
        .map((heading) => heading.textContent),
    ).toEqual(["7 天内清理2 项", "更晚清理1 项"]);
  });

  it("finds items by name and by space", async () => {
    serve([plan, photos, orphan]);
    const user = userEvent.setup();
    render(<TrashPanel onShowInFiles={() => undefined} />);
    await screen.findByRole("region", { name: "回收站项目" });
    await user.click(within(screen.getByRole("radiogroup", { name: "空间" })).getByRole("radio", { name: "共享空间" }));
    expect(names()).toEqual(["旅行照片"]);
    await user.click(within(screen.getByRole("radiogroup", { name: "空间" })).getByRole("radio", { name: "全部" }));
    await user.type(screen.getByRole("searchbox", { name: "搜索回收站" }), "笔记");
    expect(names()).toEqual(["笔记.txt"]);
    await user.type(screen.getByRole("searchbox", { name: "搜索回收站" }), "不存在");
    expect(screen.getByText("没有匹配的项目")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "清除筛选" }));
    expect(names()).toHaveLength(3);
  });

  it("restores to the original folder, keeps both when the name is taken and shows where it went", async () => {
    const shown = vi.fn();
    const { restores } = serve([plan], (_id, body) =>
      body.conflict === "ask"
        ? "name_conflict"
        : restoredAs(plan, "计划 (恢复).docx", { parentId: "dir:project", modifiedAt: at(0) }),
    );
    const user = userEvent.setup();
    render(<TrashPanel onShowInFiles={shown} />);
    await user.click(await screen.findByRole("button", { name: "恢复 计划.docx" }));

    const dialog = await screen.findByRole("dialog", { name: "恢复文件" });
    expect(within(dialog).getByText("个人空间 / 文档 / 项目")).toBeTruthy();
    expect(within(dialog).getByText("原位置")).toBeTruthy();
    await user.click(within(dialog).getByRole("button", { name: "恢复" }));
    const choices = await within(dialog).findByRole("group", { name: "同名项目" });
    expect(choices.textContent).toContain("这里已有“计划.docx”");
    const keep = within(choices).getByRole("button", { name: /保留两者/ });
    expect(keep.textContent).toContain("“计划 (恢复).docx”");
    expect(document.activeElement).toBe(keep);
    await user.click(keep);

    expect(await screen.findByText("已恢复“计划 (恢复).docx”到 个人空间 / 文档 / 项目")).toBeTruthy();
    expect(restores).toEqual([
      { trashId: plan.id, parentId: "", name: "计划.docx", conflict: "ask" },
      { trashId: plan.id, parentId: "", name: "计划.docx", conflict: "rename" },
    ]);
    expect(screen.getByText("回收站是空的")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "在文件管理中显示" }));
    await waitFor(() =>
      expect(shown).toHaveBeenCalledWith({
        spaceId: personal.id,
        spaceKind: "private",
        folders: [{ id: "dir:docs", name: "文档" }],
      }),
    );
  });

  it("replaces the file of that name, which goes to the recycle bin, or takes a name of the user's own", async () => {
    let listed = [plan];
    const { restores } = serve(
      () => listed,
      (_id, body) => {
        if (body.conflict === "ask") return "name_conflict";
        listed = [
          item({
            id: "trash:displaced",
            entryId: "file:displaced",
            name: "计划.docx",
            deletedAt: at(0),
            purgeAt: at(30 * day),
          }),
        ];
        return restoredAs(plan, "计划.docx", { modifiedAt: at(0), replaced: true });
      },
    );
    const user = userEvent.setup();
    render(<TrashPanel onShowInFiles={() => undefined} />);
    await user.click(await screen.findByRole("button", { name: "恢复 计划.docx" }));
    const dialog = await screen.findByRole("dialog", { name: "恢复文件" });
    await user.click(within(dialog).getByRole("button", { name: "恢复" }));
    const choices = await within(dialog).findByRole("group", { name: "同名项目" });

    // Another name of one's own goes back through the name field.
    await user.click(within(choices).getByRole("button", { name: "自己起个名称" }));
    expect((within(dialog).getByRole("textbox", { name: "恢复后的名称" }) as HTMLInputElement).value).toBe(
      "计划 (恢复).docx",
    );
    await user.clear(within(dialog).getByRole("textbox", { name: "恢复后的名称" }));
    await user.type(within(dialog).getByRole("textbox", { name: "恢复后的名称" }), "计划.docx");
    await user.click(within(dialog).getByRole("button", { name: "恢复" }));
    await user.click(
      within(await within(dialog).findByRole("group", { name: "同名项目" })).getByRole("button", { name: /替换/ }),
    );

    expect(
      await screen.findByText("已恢复“计划.docx”到 个人空间 / 文档 / 项目，原来的同名文件已移到回收站"),
    ).toBeTruthy();
    expect(restores).toEqual([
      { trashId: plan.id, parentId: "", name: "计划.docx", conflict: "ask" },
      { trashId: plan.id, parentId: "", name: "计划.docx", conflict: "ask" },
      { trashId: plan.id, parentId: "", name: "计划.docx", conflict: "replace" },
    ]);
    // The replaced file is listed in the recycle bin.
    await waitFor(() => expect(names()).toEqual(["计划.docx"]));
    expect(within(row("计划.docx")).getByRole("button", { name: "恢复 计划.docx" })).toBeTruthy();
  });

  it("never offers to replace with a folder", async () => {
    serve([photos], () => "name_conflict");
    const user = userEvent.setup();
    render(<TrashPanel onShowInFiles={() => undefined} />);
    await user.click(await screen.findByRole("button", { name: "恢复 旅行照片" }));
    const dialog = await screen.findByRole("dialog", { name: "恢复文件夹" });
    await user.click(within(dialog).getByRole("button", { name: "恢复" }));
    const choices = await within(dialog).findByRole("group", { name: "同名项目" });
    expect(within(choices).getByRole("button", { name: /保留两者/ }).textContent).toContain("“旅行照片 (恢复)”");
    expect(within(choices).queryByRole("button", { name: /替换/ })).toBeNull();
    expect(choices.textContent).toContain("文件夹不会被替换或合并。");
  });

  it("opens an item's original folder in the file manager", async () => {
    const shown = vi.fn();
    serve([plan, orphan]);
    const user = userEvent.setup();
    render(<TrashPanel onShowInFiles={shown} />);
    await user.click(await screen.findByRole("button", { name: "在文件管理中打开 个人空间 / 文档 / 项目" }));
    await waitFor(() =>
      expect(shown).toHaveBeenCalledWith({
        spaceId: personal.id,
        spaceKind: "private",
        folders: [{ id: "dir:docs", name: "文档" }],
      }),
    );
    // A folder that is gone cannot be opened.
    expect(within(row("笔记.txt")).queryByRole("button", { name: /在文件管理中打开/ })).toBeNull();
  });

  it("says beforehand that an item whose folder is gone goes to the space root", async () => {
    serve([orphan]);
    const user = userEvent.setup();
    render(<TrashPanel onShowInFiles={() => undefined} />);
    await user.click(await screen.findByRole("button", { name: "恢复 笔记.txt" }));
    const dialog = await screen.findByRole("dialog", { name: "恢复文件" });
    expect(within(dialog).getByText("个人空间")).toBeTruthy();
    expect(within(dialog).queryByText("原位置")).toBeNull();
    expect(within(dialog).getByText("原文件夹“旧项目”已不在，将恢复到个人空间的根目录。")).toBeTruthy();
  });

  it("restores a selection and asks once what to do with the items whose name is taken", async () => {
    const { restores } = serve([plan, photos, orphan], (id, body) => {
      if (id !== orphan.id && body.conflict === "ask") return "name_conflict";
      if (id === plan.id) return restoredAs(plan, "计划.docx", { replaced: true });
      if (id === photos.id) return restoredAs(photos, "旅行照片 (恢复)");
      return restoredAs(orphan, "笔记.txt");
    });
    const user = userEvent.setup();
    render(<TrashPanel onShowInFiles={() => undefined} />);
    await screen.findByRole("region", { name: "回收站项目" });
    await user.click(screen.getByRole("checkbox", { name: "全选" }));
    const toolbar = screen.getByRole("toolbar", { name: "所选项目的操作" });
    expect(toolbar.textContent).toContain("已选 3 项");
    await user.click(within(toolbar).getByRole("button", { name: "恢复" }));

    const dialog = await screen.findByRole("dialog", { name: "同名项目" });
    expect(within(dialog).getByText("2 个项目的原位置已有同名项目")).toBeTruthy();
    expect(
      within(dialog)
        .getAllByRole("listitem")
        .map((element) => element.querySelector("strong")?.textContent),
    ).toEqual(["计划.docx", "旅行照片"]);
    expect(dialog.textContent).toContain("文件夹不会被替换，仍然保留两者");
    await user.click(within(dialog).getByRole("button", { name: "替换" }));

    expect(
      await screen.findByText("已恢复 3 项，其中 1 项改用了新名称、1 项替换了同名文件，原来的文件已移到回收站"),
    ).toBeTruthy();
    expect(restores.map(({ trashId, conflict }) => [trashId, conflict])).toEqual([
      ["trash:plan", "ask"],
      ["trash:photos", "ask"],
      ["trash:notes", "ask"],
      ["trash:plan", "replace"],
      ["trash:photos", "rename"],
    ]);
  });

  it("leaves the skipped items in the recycle bin, still selected", async () => {
    serve([plan, photos, orphan], (id, body) =>
      id === photos.id && body.conflict === "ask"
        ? "name_conflict"
        : restoredAs(id === plan.id ? plan : orphan, body.name, { modifiedAt: at(0) }),
    );
    const user = userEvent.setup();
    render(<TrashPanel onShowInFiles={() => undefined} />);
    await screen.findByRole("region", { name: "回收站项目" });
    await user.click(screen.getByRole("checkbox", { name: "全选" }));
    await user.click(
      within(screen.getByRole("toolbar", { name: "所选项目的操作" })).getByRole("button", { name: "恢复" }),
    );

    const dialog = await screen.findByRole("dialog", { name: "同名项目" });
    expect(within(dialog).getByText("“旅行照片”的原位置已有同名项目")).toBeTruthy();
    expect(within(dialog).queryByRole("button", { name: "替换" })).toBeNull();
    await user.click(within(dialog).getByRole("button", { name: "跳过" }));

    expect(await screen.findByText("已恢复 2 项；跳过 1 项，仍在回收站中")).toBeTruthy();
    expect(names()).toEqual(["旅行照片"]);
    expect(screen.getByRole("checkbox", { name: "选择 旅行照片" }).getAttribute("aria-checked")).toBe("true");
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("toolbar", { name: "所选项目的操作" })).toBeNull();
  });

  it("asks before deleting for good, one item, a selection or everything", async () => {
    const { purged } = serve([plan, photos, orphan]);
    const user = userEvent.setup();
    render(<TrashPanel onShowInFiles={() => undefined} />);
    await user.click(await screen.findByRole("button", { name: "永久删除 计划.docx" }));
    let dialog = await screen.findByRole("dialog", { name: "永久删除？" });
    expect(within(dialog).getByText(/“计划.docx”将被立即删除，无法再恢复/)).toBeTruthy();
    expect(purged).toEqual([]);
    await user.click(within(dialog).getByRole("button", { name: "永久删除" }));
    expect(await screen.findByText("已永久删除“计划.docx”")).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "清空回收站" }));
    dialog = await screen.findByRole("dialog", { name: "清空回收站？" });
    expect(within(dialog).getByText(/全部 2 个项目（共 3.0 GiB）将被立即删除/)).toBeTruthy();
    await user.click(within(dialog).getByRole("button", { name: "清空回收站" }));
    expect(await screen.findByText("回收站是空的")).toBeTruthy();
    expect(purged).toEqual([plan, photos, orphan].map((entry) => entry.id));
  });

  it("is empty with a word on how long items stay", async () => {
    serve([]);
    render(<TrashPanel onShowInFiles={() => undefined} />);
    expect(await screen.findByText("回收站是空的")).toBeTruthy();
    expect(screen.queryByText(/保留 30 天/)).toBeNull();
    expect(screen.getByText(/到期前随时可以恢复到原来的位置/)).toBeTruthy();
    expect((screen.getByRole("button", { name: /清空回收站/ }) as HTMLButtonElement).disabled).toBe(true);
  });
});

describe("trash model", () => {
  it("words the time left and how urgent it is", () => {
    const now = Date.parse("2026-10-13T10:00:00Z");
    expect(expiryOf({ deletedAt: "2026-10-13T10:00:00Z", purgeAt: "2026-11-12T10:00:00Z" }, now)).toMatchObject({
      label: "30 天后清理",
      short: "30 天",
      tone: "calm",
      left: 1,
    });
    expect(expiryOf({ deletedAt: "2026-09-18T10:00:00Z", purgeAt: "2026-10-18T10:00:00Z" }, now)).toMatchObject({
      short: "5 天",
      tone: "soon",
    });
    expect(expiryOf({ deletedAt: "2026-09-13T20:00:00Z", purgeAt: "2026-10-13T20:00:00Z" }, now)).toMatchObject({
      short: "1 天内",
      tone: "urgent",
    });
    expect(expiryOf({ deletedAt: "2026-09-13T09:00:00Z", purgeAt: "2026-10-13T09:00:00Z" }, now)).toMatchObject({
      short: "即将清理",
      tone: "urgent",
      left: 0,
    });
  });

  it("groups by the day of deletion", () => {
    const now = new Date(2026, 9, 13, 18, 0);
    const groups = groupItems(
      [
        item({ id: "a", deletedAt: new Date(2026, 9, 13, 9).toISOString() }),
        item({ id: "b", deletedAt: new Date(2026, 9, 13, 8).toISOString() }),
        item({ id: "c", deletedAt: new Date(2026, 9, 12, 23).toISOString() }),
        item({ id: "d", deletedAt: new Date(2026, 9, 2, 10).toISOString() }),
      ],
      "deleted",
      now,
    );
    expect(groups.map((group) => [group.label, group.items.map((entry) => entry.id)])).toEqual([
      ["今天", ["a", "b"]],
      ["昨天", ["c"]],
      ["10月2日 周五", ["d"]],
    ]);
  });

  it("suggests names beside a taken one", () => {
    expect(restoredName("计划.docx", "file")).toBe("计划 (恢复).docx");
    expect(restoredName("计划.docx", "file", 2)).toBe("计划 (恢复 2).docx");
    expect(restoredName("v1.2 资料", "directory")).toBe("v1.2 资料 (恢复)");
    expect(restoredName(".bashrc", "file")).toBe(".bashrc (恢复)");
  });
});
