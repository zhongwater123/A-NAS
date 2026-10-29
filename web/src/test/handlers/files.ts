// Files: the spaces, their folders, the data volume and what a file holds;
// the writes the file manager sends; and downloads, which the NAS prepares
// for the transfer center.
import { HttpResponse } from "msw";

import type { FileEntry, FileTransfer, Space, Transfer, Volume } from "../../api.generated";
import { http } from "../server";
import { transfer } from "./transfers";
import { trashItem } from "./trash";

// The file manager shows the data volume's usage; storage.ts builds it.
export { volume } from "./storage";

// space is a space everyone shares unless overrides say otherwise.
export function space(id: string, overrides: Partial<Space> = {}): Space {
  return { id, kind: "shared", name: "Shared", createdAt: "2026-10-07T10:00:00Z", ...overrides };
}

// ownSpace is the signed-in owner's private space; sharedSpace is everyone's.
export const ownSpace = space("space:owner", { kind: "private", name: "owner", ownerUserId: "user:owner" });
export const sharedSpace = space("space:shared");

// entry is a 12-byte file at the root of the shared space.
export function entry(name: string, overrides: Partial<FileEntry> = {}): FileEntry {
  return {
    id: `file:${name}`,
    spaceId: sharedSpace.id,
    name,
    kind: "file",
    sizeBytes: 12,
    modifiedAt: "2026-10-08T10:00:00Z",
    ...overrides,
  };
}

// folder is a folder at the root of the shared space.
export function folder(name: string, overrides: Partial<FileEntry> = {}): FileEntry {
  return entry(name, { id: `folder:${name}`, kind: "directory", sizeBytes: 0, ...overrides });
}

export interface FilesOptions {
  spaces?: Space[];
  // The files and folders of every space; a listing answers those in the
  // space and folder it asks for.
  entries?: FileEntry[];
  volumes?: Volume[];
  // What reading any file returns, as the details pane's text preview does.
  text?: string;
  // onList hears each listing: the space, and the folder or "" for its root.
  onList?: (spaceId: string, parentId: string) => void;
}

// filesHandlers answers the file manager's reads. Writes are left to the test
// (fileWrites, downloads), which sees them.
export function filesHandlers(options: FilesOptions = {}) {
  const { spaces = [], entries = [], volumes = [], text = "" } = options;
  return [
    http.get("/api/v1/spaces", ({ response }) => response(200).json({ items: spaces })),
    http.get("/api/v1/spaces/{spaceId}/entries", ({ params, query, response }) => {
      const parentId = query.get("parentId") ?? "";
      options.onList?.(params.spaceId, parentId);
      return response(200).json({
        items: entries.filter((item) => item.spaceId === params.spaceId && (item.parentId ?? "") === parentId),
      });
    }),
    http.get("/api/v1/volumes", ({ response }) => response(200).json({ items: volumes })),
    // The contract types a file's content as any media type (*/*), which the
    // typed helpers cannot answer.
    http.get("/api/v1/files/{fileId}/content", ({ response }) => response.untyped(HttpResponse.text(text))),
  ];
}

export type FileWrite = { fileId: string } & FileTransfer;

// fileWrites accepts the file manager's writes and records them: copies,
// moves and renames (both a PATCH), new folders and files sent to the
// recycle bin. Each answers the entry as the server would leave it.
export function fileWrites() {
  const copies: FileWrite[] = [];
  const moves: FileWrite[] = [];
  const directories: Array<{ spaceId: string; parentId?: string; name: string }> = [];
  const trashed: string[] = [];
  const placed = (id: string, { spaceId = sharedSpace.id, parentId, name }: FileTransfer) =>
    entry(name, { id, spaceId, parentId: parentId || undefined });
  const handlers = [
    http.post("/api/v1/files/{fileId}/copies", async ({ params, request, response }) => {
      const body = await request.json();
      copies.push({ fileId: params.fileId, ...body });
      return response(201).json(placed(`${params.fileId}:copy`, body));
    }),
    http.patch("/api/v1/files/{fileId}", async ({ params, request, response }) => {
      const body = await request.json();
      moves.push({ fileId: params.fileId, ...body });
      return response(200).json(placed(params.fileId, body));
    }),
    http.post("/api/v1/spaces/{spaceId}/directories", async ({ params, request, response }) => {
      const body = await request.json();
      directories.push({ spaceId: params.spaceId, ...body });
      return response(201).json(folder(body.name, { spaceId: params.spaceId, parentId: body.parentId || undefined }));
    }),
    http.delete("/api/v1/files/{fileId}", ({ params, response }) => {
      trashed.push(params.fileId);
      return response(200).json(trashItem(params.fileId, { id: `trash:${params.fileId}`, entryId: params.fileId }));
    }),
  ];
  return { copies, moves, directories, trashed, handlers };
}

// download is the transfer the NAS prepares for a download named name, which
// the browser then fetches from its contentUrl.
export function download(name: string, overrides: Partial<Transfer> = {}): Transfer {
  return transfer({
    kind: name.endsWith(".zip") ? "archive" : "file",
    name,
    location: { spaceId: ownSpace.id, spaceKind: "private", folders: [] },
    totalBytes: 12,
    transferredBytes: 0,
    state: "pending",
    createdAt: "2026-10-12T10:00:00Z",
    updatedAt: "2026-10-12T10:00:00Z",
    finishedAt: undefined,
    ...overrides,
  });
}

// downloads prepares every download asked for and records the entries of
// each: one file downloads by its name, a folder or several items as a ZIP.
export function downloads(entries: FileEntry[] = []) {
  const sent: string[][] = [];
  const handler = http.post("/api/v1/transfers/downloads", async ({ request, response }) => {
    const { entryIds } = await request.json();
    sent.push(entryIds);
    const only = entryIds.length === 1 ? entries.find((item) => item.id === entryIds[0]) : undefined;
    const name = only?.kind === "file" ? only.name : only ? `${only.name}.zip` : "下载.zip";
    return response(201).json(download(name));
  });
  return { sent, handler };
}
