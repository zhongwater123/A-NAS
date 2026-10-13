import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { FileEntry, Space, TrashItem } from "../api";
import { expiryOf, groupItems, restoredName, sortItems } from "./model";
import { TrashPanel } from "./TrashPanel";

const personal: Space = { id: "space:personal", kind: "private", name: "owner", ownerUserId: "user:owner", createdAt: "2026-10-01T00:00:00Z" };
const shared: Space = { id: "space:shared", kind: "shared", name: "Shared", createdAt: "2026-10-01T00:00:00Z" };
const day = 86_400_000;
const at = (offset: number) => new Date(Date.now() + offset).toISOString();

function item(overrides: Partial<TrashItem>): TrashItem {
  return {
    id: "trash:plan", entryId: "file:plan", spaceId: personal.id, name: "计划.docx", kind: "file", sizeBytes: 2.3 * 1024 ** 2,
    originalPath: "文档/项目", originalGone: false, deletedBy: "user:owner", deletedAt: at(-60_000), purgeAt: at(30 * day - 60_000),
    ...overrides,
  };
}

const plan = item({});
const photos = item({ id: "trash:photos", entryId: "dir:photos", spaceId: shared.id, name: "旅行照片", kind: "directory", sizeBytes: 3 * 1024 ** 3, originalPath: "", deletedAt: at(-27 * day), purgeAt: at(3 * day) });
const orphan = item({ id: "trash:notes", entryId: "file:notes", name: "笔记.txt", sizeBytes: 900, originalPath: "旧项目", originalGone: true, deletedAt: at(-29.5 * day), purgeAt: at(0.5 * day) });

function json(body: unknown, status = 200) {
  return new Response(status === 204 ? null : JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

interface Call { url: string; method: string; body?: unknown }

function serve(items: TrashItem[], restore?: (id: string, body: { parentId: string; name: string }, attempt: number) => Response) {
  const calls: Call[] = [];
  let attempts = 0;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    const body = init?.body ? JSON.parse(String(init.body)) : undefined;
    calls.push({ url, method, body });
    if (url === "/api/v1/trash") return json({ items });
    if (url === "/api/v1/spaces") return json({ items: [personal, shared] });
    const restoring = url.match(/^\/api\/v1\/trash\/([^/]+)\/restore$/);
    if (restoring) {
      const id = decodeURIComponent(restoring[1]);
      return restore ? restore(id, body, ++attempts) : json({ id, spaceId: personal.id, name: body.name, kind: "file", sizeBytes: 1, modifiedAt: at(0) });
    }
    if (method === "DELETE" && url.startsWith("/api/v1/trash/")) return json(null, 204);
    const docs: FileEntry = { id: "dir:docs", spaceId: personal.id, name: "文档", kind: "directory", sizeBytes: 0, modifiedAt: at(0) };
    if (url === `/api/v1/spaces/${encodeURIComponent(personal.id)}/entries?parentId=`) return json({ items: [docs] });
    if (url.includes("/entries?parentId=")) return json({ items: [] });
    return json({ error: { code: "not_found", message: "not found" } }, 404);
  }));
  return calls;
}

const rows = () => screen.getAllByRole("listitem");
const row = (name: string) => rows().find((element) => element.querySelector("strong")?.textContent === name)!;
const names = () => rows().map((element) => element.querySelector("strong")?.textContent);
const status = () => document.querySelector(".tr-status")!.textContent;

afterEach(() => vi.unstubAllGlobals());

describe("TrashPanel", () => {
  it("lists where each item came from, its size, when it was deleted and how long it stays", async () => {
    serve([plan, photos, orphan]);
    render(<TrashPanel onShowInFiles={() => undefined} />);
    await screen.findByRole("button", { name: "恢复 计划.docx" });
    expect(screen.queryByText(/保留 30 天/)).toBeNull();
    expect(status()).toBe("3 个项目，共 3.0 GiB2 项将在 7 天内清理");

    // Grouped by the day they were deleted, newest first.
    expect(screen.getAllByRole("heading", { level: 3 }).map((heading) => heading.textContent)).toEqual([
      expect.stringMatching(/^(今天|昨天)$/), expect.stringMatching(/^\d+月\d+日/), expect.stringMatching(/^\d+月\d+日/),
    ]);
    expect(row("计划.docx").textContent).toContain("个人空间 / 文档 / 项目");
    expect(row("计划.docx").textContent).toContain("2.3 MiB");
    expect(row("计划.docx").textContent).toMatch(/删除于 \d\d:\d\d/);
    expect(row("计划.docx").textContent).toContain("剩余 30 天");
    expect(row("旅行照片").textContent).toContain("共享空间");
    expect(row("旅行照片").textContent).toContain("3.0 GiB");
    expect(row("旅行照片").textContent).toContain("剩余 3 天");
    expect(row("笔记.txt").textContent).toContain("个人空间 / 旧项目已不在");
    expect(row("笔记.txt").textContent).toContain("剩余 不到 1 天");

    // Column heads sort; time left starts with what goes first.
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "按剩余排序" }));
    expect(names()).toEqual(["笔记.txt", "旅行照片", "计划.docx"]);
    expect(screen.queryAllByRole("heading", { level: 3 })).toHaveLength(0);
    await user.click(screen.getByRole("button", { name: "按大小排序" }));
    expect(names()).toEqual(["旅行照片", "计划.docx", "笔记.txt"]);
  });

  it("finds items by name and by space", async () => {
    serve([plan, photos, orphan]);
    const user = userEvent.setup();
    render(<TrashPanel onShowInFiles={() => undefined} />);
    await screen.findByRole("button", { name: "恢复 计划.docx" });
    await user.selectOptions(screen.getByRole("combobox", { name: "空间" }), "共享空间");
    expect(names()).toEqual(["旅行照片"]);
    expect(status()).toContain("1 / 3 个项目");
    await user.selectOptions(screen.getByRole("combobox", { name: "空间" }), "全部空间");
    await user.type(screen.getByRole("searchbox", { name: "搜索回收站" }), "笔记");
    expect(names()).toEqual(["笔记.txt"]);
    await user.type(screen.getByRole("searchbox", { name: "搜索回收站" }), "不存在");
    expect(screen.getByText("没有匹配的项目")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "清除筛选" }));
    expect(names()).toHaveLength(3);
  });

  it("restores to the original folder, offers another name when it is taken and shows where it went", async () => {
    const shown = vi.fn();
    const calls = serve([plan], (_id, body, attempt) => attempt === 1
      ? json({ error: { code: "name_conflict", message: "a file with this name already exists" } }, 409)
      : json({ id: plan.entryId, spaceId: personal.id, parentId: "dir:project", name: body.name, kind: "file", sizeBytes: plan.sizeBytes, modifiedAt: at(0) }));
    const user = userEvent.setup();
    render(<TrashPanel onShowInFiles={shown} />);
    await user.click(await screen.findByRole("button", { name: "恢复 计划.docx" }));

    const dialog = await screen.findByRole("dialog", { name: "恢复文件" });
    expect(within(dialog).getByText("个人空间 / 文档 / 项目")).toBeTruthy();
    expect(within(dialog).getByText("原位置")).toBeTruthy();
    await user.click(within(dialog).getByRole("button", { name: "恢复" }));
    expect((await within(dialog).findByRole("alert")).textContent).toContain("“个人空间 / 文档 / 项目”中已有名为“计划.docx”的项目");
    await user.click(within(dialog).getByRole("button", { name: "改用“计划 (恢复).docx”" }));
    await user.click(within(dialog).getByRole("button", { name: "恢复" }));

    expect(await screen.findByText("已恢复“计划 (恢复).docx”到 个人空间 / 文档 / 项目")).toBeTruthy();
    expect(calls.filter((call) => call.url.endsWith("/restore")).map((call) => call.body)).toEqual([
      { parentId: "", name: "计划.docx" },
      { parentId: "", name: "计划 (恢复).docx" },
    ]);
    expect(screen.getByText("回收站是空的")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "在文件管理中显示" }));
    await waitFor(() => expect(shown).toHaveBeenCalledWith({ spaceId: personal.id, spaceKind: "private", folders: [{ id: "dir:docs", name: "文档" }] }));
  });

  it("opens an item's original folder in the file manager", async () => {
    const shown = vi.fn();
    serve([plan, orphan]);
    const user = userEvent.setup();
    render(<TrashPanel onShowInFiles={shown} />);
    await user.click(await screen.findByRole("button", { name: "在文件管理中打开 个人空间 / 文档 / 项目" }));
    await waitFor(() => expect(shown).toHaveBeenCalledWith({ spaceId: personal.id, spaceKind: "private", folders: [{ id: "dir:docs", name: "文档" }] }));
    // A folder that is gone cannot be opened.
    expect(within(row("笔记.txt")).queryByRole("button", { name: /在文件管理中打开/ })).toBeNull();
  });

  it("says beforehand that an item whose folder is gone goes to the space root", async () => {
    serve([orphan]);
    const user = userEvent.setup();
    render(<TrashPanel onShowInFiles={() => undefined} />);
    await user.dblClick(await screen.findByText("笔记.txt"));
    const dialog = await screen.findByRole("dialog", { name: "恢复文件" });
    expect(within(dialog).getByText("个人空间")).toBeTruthy();
    expect(within(dialog).queryByText("原位置")).toBeNull();
    expect(within(dialog).getByText("原文件夹“旧项目”已不在，将恢复到个人空间的根目录。")).toBeTruthy();
  });

  it("selects like a file list and restores a selection, keeping the items whose name is taken", async () => {
    const calls = serve([plan, photos, orphan], (id) => id === photos.id
      ? json({ error: { code: "name_conflict", message: "a file with this name already exists" } }, 409)
      : json({ id, spaceId: personal.id, name: "x", kind: "file", sizeBytes: 1, modifiedAt: at(0) }));
    const user = userEvent.setup();
    render(<TrashPanel onShowInFiles={() => undefined} />);
    // A click selects one item, Shift extends to a range.
    await user.click(await screen.findByText("计划.docx"));
    expect(screen.getByRole("toolbar", { name: "所选项目的操作" }).textContent).toContain("已选 1 项");
    await user.keyboard("{Shift>}");
    await user.click(screen.getByText("笔记.txt"));
    await user.keyboard("{/Shift}");
    const toolbar = screen.getByRole("toolbar", { name: "所选项目的操作" });
    expect(toolbar.textContent).toContain("已选 3 项");
    await user.click(within(toolbar).getByRole("button", { name: "恢复" }));

    expect(await screen.findByText(/已恢复 2 项；1 项的原位置已有同名项目/)).toBeTruthy();
    expect(calls.filter((call) => call.url.endsWith("/restore")).map((call) => call.body)).toEqual([
      { parentId: "", name: "计划.docx" }, { parentId: "", name: "旅行照片" }, { parentId: "", name: "笔记.txt" },
    ]);
    expect(names()).toEqual(["旅行照片"]);
    expect(screen.getByRole("checkbox", { name: "选择 旅行照片" }).getAttribute("aria-checked")).toBe("true");
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("toolbar", { name: "所选项目的操作" })).toBeNull();
  });

  it("asks before deleting for good, one item, a selection or everything", async () => {
    const calls = serve([plan, photos, orphan]);
    const user = userEvent.setup();
    render(<TrashPanel onShowInFiles={() => undefined} />);
    await user.click(await screen.findByRole("button", { name: "永久删除 计划.docx" }));
    let dialog = await screen.findByRole("dialog", { name: "永久删除？" });
    expect(within(dialog).getByText(/“计划.docx”将被立即删除，无法再恢复/)).toBeTruthy();
    expect(calls.some((call) => call.method === "DELETE")).toBe(false);
    await user.click(within(dialog).getByRole("button", { name: "永久删除" }));
    expect(await screen.findByText("已永久删除“计划.docx”")).toBeTruthy();

    // Delete asks for the selection.
    await user.click(screen.getByText("笔记.txt"));
    await user.keyboard("{Delete}");
    dialog = await screen.findByRole("dialog", { name: "永久删除？" });
    await user.click(within(dialog).getByRole("button", { name: "取消" }));

    await user.click(screen.getByRole("button", { name: "取消选择" }));
    await user.click(screen.getByRole("button", { name: "清空回收站" }));
    dialog = await screen.findByRole("dialog", { name: "清空回收站？" });
    expect(within(dialog).getByText(/全部 2 个项目（共 3.0 GiB）将被立即删除/)).toBeTruthy();
    await user.click(within(dialog).getByRole("button", { name: "清空回收站" }));
    expect(await screen.findByText("回收站是空的")).toBeTruthy();
    expect(calls.filter((call) => call.method === "DELETE").map((call) => call.url)).toEqual(
      [plan, photos, orphan].map((entry) => `/api/v1/trash/${encodeURIComponent(entry.id)}`),
    );
  });

  it("is empty with nothing else to say", async () => {
    serve([]);
    render(<TrashPanel onShowInFiles={() => undefined} />);
    expect(await screen.findByText("回收站是空的")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /清空回收站/ })).toBeNull();
  });
});

describe("trash model", () => {
  it("words the time left and how urgent it is", () => {
    const now = Date.parse("2026-10-13T10:00:00Z");
    expect(expiryOf({ purgeAt: "2026-11-12T10:00:00Z" }, now)).toEqual({ label: "30 天", tone: "calm" });
    expect(expiryOf({ purgeAt: "2026-10-18T10:00:00Z" }, now)).toEqual({ label: "5 天", tone: "soon" });
    expect(expiryOf({ purgeAt: "2026-10-13T20:00:00Z" }, now)).toEqual({ label: "不到 1 天", tone: "urgent" });
    expect(expiryOf({ purgeAt: "2026-10-13T09:00:00Z" }, now)).toEqual({ label: "即将清理", tone: "urgent" });
  });

  it("groups by the day of deletion only when sorted by it", () => {
    const now = new Date(2026, 9, 13, 18, 0);
    const items = [
      item({ id: "a", deletedAt: new Date(2026, 9, 13, 9).toISOString() }),
      item({ id: "b", deletedAt: new Date(2026, 9, 13, 8).toISOString() }),
      item({ id: "c", deletedAt: new Date(2026, 9, 12, 23).toISOString() }),
      item({ id: "d", deletedAt: new Date(2026, 9, 2, 10).toISOString() }),
    ];
    const byDeletion = groupItems(sortItems(items, { key: "deleted", ascending: false }), { key: "deleted", ascending: false }, now);
    expect(byDeletion.map((group) => [group.label, group.items.map((entry) => entry.id)])).toEqual([["今天", ["a", "b"]], ["昨天", ["c"]], ["10月2日 周五", ["d"]]]);
    expect(groupItems(items, { key: "name", ascending: true }, now)).toEqual([{ key: "all", items }]);
  });

  it("suggests names beside a taken one", () => {
    expect(restoredName("计划.docx", "file")).toBe("计划 (恢复).docx");
    expect(restoredName("计划.docx", "file", 2)).toBe("计划 (恢复 2).docx");
    expect(restoredName("v1.2 资料", "directory")).toBe("v1.2 资料 (恢复)");
    expect(restoredName(".bashrc", "file")).toBe(".bashrc (恢复)");
  });
});
