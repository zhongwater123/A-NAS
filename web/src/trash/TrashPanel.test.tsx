import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { FileEntry, Space, TrashItem } from "../api";
import { expiryOf, restoredName } from "./model";
import { TrashPanel } from "./TrashPanel";

const personal: Space = { id: "space:personal", kind: "private", name: "owner", ownerUserId: "user:owner", createdAt: "2026-10-01T00:00:00Z" };
const shared: Space = { id: "space:shared", kind: "shared", name: "Shared", createdAt: "2026-10-01T00:00:00Z" };
const day = 86_400_000;
const at = (offset: number) => new Date(Date.now() + offset).toISOString();

function item(overrides: Partial<TrashItem>): TrashItem {
  return {
    id: "trash:plan", entryId: "file:plan", spaceId: personal.id, name: "计划.docx", kind: "file", sizeBytes: 2.3 * 1024 ** 2,
    originalPath: "文档/项目", originalGone: false, deletedBy: "user:owner", deletedAt: at(-2 * 3_600_000), purgeAt: at(30 * day - 2 * 3_600_000),
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

function serve(items: TrashItem[], restore?: (body: { parentId: string; name: string }, attempt: number) => Response) {
  const calls: Call[] = [];
  let attempts = 0;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    const body = init?.body ? JSON.parse(String(init.body)) : undefined;
    calls.push({ url, method, body });
    if (url === "/api/v1/trash") return json({ items });
    if (url === "/api/v1/spaces") return json({ items: [personal, shared] });
    if (url.endsWith("/restore") && restore) return restore(body, ++attempts);
    if (method === "DELETE" && url.startsWith("/api/v1/trash/")) return json(null, 204);
    const docs: FileEntry = { id: "dir:docs", spaceId: personal.id, name: "文档", kind: "directory", sizeBytes: 0, modifiedAt: at(0) };
    if (url === `/api/v1/spaces/${encodeURIComponent(personal.id)}/entries?parentId=`) return json({ items: [docs] });
    if (url.includes("/entries?parentId=")) return json({ items: [] });
    return json({ error: { code: "not_found", message: "not found" } }, 404);
  }));
  return calls;
}

afterEach(() => vi.unstubAllGlobals());

describe("TrashPanel", () => {
  it("says where each item came from, what it holds, when it was deleted and when it goes", async () => {
    serve([plan, photos, orphan]);
    render(<TrashPanel onShowInFiles={() => undefined} />);
    const list = await screen.findByRole("region", { name: "回收站项目" });
    expect(within(list).getByText("3 个项目 · 共 3.0 GiB")).toBeTruthy();
    const [first, second, third] = within(list).getAllByRole("listitem");
    expect(first.textContent).toContain("计划.docx");
    expect(first.textContent).toContain("个人空间 / 文档 / 项目");
    expect(first.textContent).toContain("文档 · 2.3 MiB");
    expect(first.textContent).toMatch(/删除于 (今天|昨天) \d\d:\d\d/);
    expect(first.textContent).toContain("30 天后清理");
    expect(second.textContent).toContain("共享空间");
    expect(second.textContent).toContain("文件夹 · 3.0 GiB");
    expect(second.textContent).toContain("3 天后清理");
    expect(third.textContent).toContain("个人空间 / 旧项目已不在");
    expect(third.textContent).toContain("1 天内清理");

    // Sorting by expiry puts what goes first on top.
    await userEvent.setup().click(within(list).getByRole("radio", { name: "即将清理" }));
    expect(within(list).getAllByRole("listitem").map((row) => row.querySelector("strong")?.textContent)).toEqual(["笔记.txt", "旅行照片", "计划.docx"]);
  });

  it("restores to the original folder, offers another name when it is taken and shows where it went", async () => {
    const shown = vi.fn();
    const calls = serve([plan], (body, attempt) => attempt === 1
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
    expect(screen.queryByRole("listitem")).toBeNull();
    await user.click(screen.getByRole("button", { name: "在文件管理中显示" }));
    await waitFor(() => expect(shown).toHaveBeenCalledWith({ spaceId: personal.id, spaceKind: "private", folders: [{ id: "dir:docs", name: "文档" }] }));
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

  it("asks before deleting for good", async () => {
    const calls = serve([plan]);
    const user = userEvent.setup();
    render(<TrashPanel onShowInFiles={() => undefined} />);
    await user.click(await screen.findByRole("button", { name: "永久删除 计划.docx" }));
    const dialog = await screen.findByRole("dialog", { name: "永久删除？" });
    expect(within(dialog).getByText(/无法再恢复/)).toBeTruthy();
    expect(calls.some((call) => call.method === "DELETE")).toBe(false);
    await user.click(within(dialog).getByRole("button", { name: "永久删除" }));
    expect(await screen.findByText("已永久删除“计划.docx”")).toBeTruthy();
    expect(calls.filter((call) => call.method === "DELETE").map((call) => call.url)).toEqual([`/api/v1/trash/${encodeURIComponent(plan.id)}`]);
  });

  it("is empty with a word on how long items stay", async () => {
    serve([]);
    render(<TrashPanel onShowInFiles={() => undefined} />);
    expect(await screen.findByText("回收站是空的")).toBeTruthy();
    expect(screen.getByText(/保留 30 天，到期后自动永久删除/)).toBeTruthy();
    expect(screen.getByText(/到期前随时可以恢复到原来的位置/)).toBeTruthy();
  });
});

describe("trash model", () => {
  it("words the time left and how urgent it is", () => {
    const now = Date.parse("2026-10-13T10:00:00Z");
    expect(expiryOf({ purgeAt: "2026-11-12T10:00:00Z" }, now)).toEqual({ label: "30 天后清理", tone: "calm" });
    expect(expiryOf({ purgeAt: "2026-10-18T10:00:00Z" }, now)).toEqual({ label: "5 天后清理", tone: "soon" });
    expect(expiryOf({ purgeAt: "2026-10-13T20:00:00Z" }, now)).toEqual({ label: "1 天内清理", tone: "urgent" });
    expect(expiryOf({ purgeAt: "2026-10-13T09:00:00Z" }, now)).toEqual({ label: "即将清理", tone: "urgent" });
  });

  it("suggests names beside a taken one", () => {
    expect(restoredName("计划.docx", "file")).toBe("计划 (恢复).docx");
    expect(restoredName("计划.docx", "file", 2)).toBe("计划 (恢复 2).docx");
    expect(restoredName("v1.2 资料", "directory")).toBe("v1.2 资料 (恢复)");
    expect(restoredName(".bashrc", "file")).toBe(".bashrc (恢复)");
  });
});
