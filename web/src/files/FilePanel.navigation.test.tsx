import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { SharesProvider } from "../shares/SharesProvider";
import { entry, type FilesOptions, type FileWrite, filesHandlers, fileWrites, folder } from "../test/handlers/files";
import { http, server } from "../test/server";
import { transferStore } from "../transfers/store";
import { FilePanel } from "./FilePanel";

const space = { id: "space:shared", kind: "shared" as const, name: "Shared", createdAt: "2026-10-26T00:00:00Z" };
const file = entry("A.bin", { id: "file:a", spaceId: space.id, sizeBytes: 1, modifiedAt: "2026-10-26T00:00:00Z" });
const directory = folder("B", { id: "folder:b", spaceId: space.id, modifiedAt: "2026-10-26T00:00:00Z" });

// serve answers the file manager's reads, and the share provider's, which
// polls around every window: nothing is shared here.
function serve(options: FilesOptions) {
  server.use(
    ...filesHandlers(options),
    http.get("/api/v1/shares", ({ response }) => response(200).json({ items: [] })),
    http.get("/api/v1/shares/notifications", ({ response }) => response(200).json({ items: [] })),
  );
}

// renderFiles renders the file manager as the desktop does, inside the share provider.
function renderFiles() {
  render(
    <SharesProvider userId="user:owner" active={false} onOpen={() => undefined}>
      <FilePanel
        userID="user:owner"
        onIntentHandled={vi.fn()}
        onOpenTransfers={vi.fn()}
        onOpenPhotos={vi.fn()}
        onOpenMedia={vi.fn()}
        onOpenMusic={vi.fn()}
      />
    </SharesProvider>,
  );
}

beforeEach(() => localStorage.clear());
afterEach(() => transferStore.reset());

describe("file operations across navigation", () => {
  it("keeps the current folder visible when a paste in the previous folder finishes", async () => {
    let finishCopy!: () => void;
    const copied = new Promise<void>((resolve) => {
      finishCopy = resolve;
    });
    const currentFile = entry("B.bin", { ...file, id: "file:b", parentId: directory.id, name: "B.bin" });
    const listed: string[] = [];
    const copies: FileWrite[] = [];
    serve({ spaces: [space], entries: [file, directory, currentFile], onList: (_, parentId) => listed.push(parentId) });
    // The copy finishes only when the test says so.
    server.use(
      http.post("/api/v1/files/{fileId}/copies", async ({ params, request, response }) => {
        copies.push({ fileId: params.fileId, ...(await request.json()) });
        await copied;
        return response(201).json({ ...file, id: "copy:a", name: "A - 副本.bin" });
      }),
    );
    const user = userEvent.setup();
    renderFiles();
    await user.click(await screen.findByRole("row", { name: /A.bin/ }));
    await user.click(screen.getByRole("button", { name: "复制" }));
    await user.click(screen.getByRole("button", { name: "粘贴" }));
    await waitFor(() => expect(copies).toHaveLength(1));
    await user.dblClick(screen.getByRole("row", { name: /B.*文件夹/ }));
    await screen.findByRole("row", { name: /B.bin/ });
    await act(async () => finishCopy());
    await waitFor(() => expect(listed).toHaveLength(3));
    expect(listed).toEqual(["", directory.id, directory.id]);
    expect(await screen.findByRole("row", { name: /B.bin/ })).toBeTruthy();
    expect(screen.queryByRole("row", { name: /A.bin/ })).toBeNull();
    expect(copies).toEqual([{ fileId: "file:a", spaceId: space.id, parentId: "", name: "A - 副本.bin" }]);
  });

  it.each([undefined, "folder:original"])("renames in the entry's own parent (%s)", async (parentId) => {
    // A stale entry may differ from the breadcrumb: rename must never turn
    // that mismatch into a move, even independently of the listing guard.
    const original = { ...file, parentId };
    const writes = fileWrites();
    serve({ spaces: [space] });
    server.use(
      // The root lists the entry whatever parent it names.
      http.get("/api/v1/spaces/{spaceId}/entries", ({ response }) => response(200).json({ items: [original] })),
      ...writes.handlers,
    );
    const user = userEvent.setup();
    renderFiles();
    await user.click(await screen.findByRole("row", { name: /A.bin/ }));
    await user.click(screen.getByRole("button", { name: "重命名" }));
    const form = screen.getByRole("form", { name: "重命名 A.bin" });
    await user.clear(within(form).getByLabelText("新名称"));
    await user.type(within(form).getByLabelText("新名称"), "renamed.bin");
    await user.click(within(form).getByRole("button", { name: "保存" }));
    await waitFor(() =>
      expect(writes.moves).toEqual([{ fileId: "file:a", parentId: parentId ?? "", name: "renamed.bin" }]),
    );
  });
});
