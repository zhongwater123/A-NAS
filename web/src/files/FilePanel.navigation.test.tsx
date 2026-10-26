import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import App from "../App";
import type { FileEntry } from "../api";
import { json } from "../test/api";
import { installAPI, resetDesktop } from "../test/desktop";

const space = { id: "space:shared", kind: "shared" as const, name: "Shared", createdAt: "2026-10-26T00:00:00Z" };
const file: FileEntry = {
  id: "file:a",
  spaceId: space.id,
  name: "A.bin",
  kind: "file",
  sizeBytes: 1,
  modifiedAt: "2026-10-26T00:00:00Z",
};
const folder: FileEntry = { ...file, id: "folder:b", name: "B", kind: "directory" };

beforeEach(() => localStorage.clear());
afterEach(resetDesktop);

describe("file operations across navigation", () => {
  it("keeps the current folder visible when a paste in the previous folder finishes", async () => {
    let finishCopy!: (response: Response) => void;
    const copied = new Promise<Response>((resolve) => {
      finishCopy = resolve;
    });
    const currentFile = { ...file, id: "file:b", parentId: folder.id, name: "B.bin" };
    const fetchMock = installAPI({ spaces: [space] });
    const fallback = fetchMock.getMockImplementation()!;
    const listed: string[] = [];
    fetchMock.mockImplementation(async (input, init) => {
      const url = new URL(String(input), "http://localhost");
      if (url.pathname.endsWith("/entries")) {
        const parent = url.searchParams.get("parentId") ?? "";
        listed.push(parent);
        return json({ items: parent === folder.id ? [currentFile] : [file, folder] });
      }
      if (url.pathname.endsWith("/copies")) return copied;
      return fallback(input, init);
    });
    const user = userEvent.setup();
    render(<App />);
    await user.click(await screen.findByRole("button", { name: "打开文件管理" }));
    const dialog = await screen.findByRole("dialog", { name: "文件管理" });
    await user.click(await within(dialog).findByRole("row", { name: /A.bin/ }));
    await user.click(within(dialog).getByRole("button", { name: "复制" }));
    await user.click(within(dialog).getByRole("button", { name: "粘贴" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([path]) => String(path).endsWith("/copies"))).toBe(true));
    await user.dblClick(within(dialog).getByRole("row", { name: /B.*文件夹/ }));
    await within(dialog).findByRole("row", { name: /B.bin/ });
    await act(async () => finishCopy(json({ ...file, id: "copy:a", name: "A - 副本.bin" })));
    await waitFor(() => expect(listed).toHaveLength(3));
    expect(listed).toEqual(["", folder.id, folder.id]);
    expect(await within(dialog).findByRole("row", { name: /B.bin/ })).toBeTruthy();
    expect(within(dialog).queryByRole("row", { name: /A.bin/ })).toBeNull();
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/files/file%3Aa/copies",
      expect.objectContaining({ body: JSON.stringify({ spaceId: space.id, parentId: "", name: "A - 副本.bin" }) }),
    );
  });

  it.each([undefined, "folder:original"])("renames in the entry's own parent (%s)", async (parentId) => {
    // A stale entry may differ from the breadcrumb: rename must never turn
    // that mismatch into a move, even independently of the listing guard.
    const original = { ...file, parentId };
    const fetchMock = installAPI({ spaces: [space], entries: [original] });
    const user = userEvent.setup();
    render(<App />);
    await user.click(await screen.findByRole("button", { name: "打开文件管理" }));
    const dialog = await screen.findByRole("dialog", { name: "文件管理" });
    await user.click(await within(dialog).findByRole("row", { name: /A.bin/ }));
    await user.click(within(dialog).getByRole("button", { name: "重命名" }));
    const form = within(dialog).getByRole("form", { name: "重命名 A.bin" });
    await user.clear(within(form).getByLabelText("新名称"));
    await user.type(within(form).getByLabelText("新名称"), "renamed.bin");
    await user.click(within(form).getByRole("button", { name: "保存" }));
    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        "/api/v1/files/file%3Aa",
        expect.objectContaining({
          method: "PATCH",
          body: JSON.stringify({ parentId: parentId ?? "", name: "renamed.bin" }),
        }),
      ),
    );
  });
});
