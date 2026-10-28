// The recycle bin: its items, restores and deleting for good.
import type { RestoredEntry, TrashItem, TrashRestore } from "../../api.generated";
import { apiError, http } from "../server";

const day = 86_400_000;

// trashItem is a file deleted a minute ago from the root of the shared space,
// which the recycle bin keeps for 30 more days.
export function trashItem(name: string, overrides: Partial<TrashItem> = {}): TrashItem {
  const now = Date.now();
  return {
    id: `trash:${name}`,
    entryId: `file:${name}`,
    spaceId: "space:shared",
    name,
    kind: "file",
    sizeBytes: 12,
    originalPath: "",
    originalGone: false,
    deletedBy: "user:owner",
    deletedAt: new Date(now - 60_000).toISOString(),
    purgeAt: new Date(now + 30 * day - 60_000).toISOString(),
    ...overrides,
  };
}

// A Restore answers a restore of an item: the entry it came back as, or the
// name conflict the server reports when the name is taken.
export type Restore = (trashId: string, body: TrashRestore) => RestoredEntry | "name_conflict";

export type TrashRestoreRecord = { trashId: string } & TrashRestore;

// trashHandlers answers the recycle bin with items, read on every request so
// a test changes it as the server would. A restore answers what restore says,
// by default the item back under the name asked for; deleting for good
// succeeds. Both are recorded.
export function trashHandlers(items: () => TrashItem[], restore?: Restore) {
  const restores: TrashRestoreRecord[] = [];
  const purged: string[] = [];
  const handlers = [
    http.get("/api/v1/trash", ({ response }) => response(200).json({ items: items() })),
    http.post("/api/v1/trash/{trashId}/restore", async ({ params, request, response }) => {
      const body = await request.json();
      restores.push({ trashId: params.trashId, ...body });
      const item = items().find((candidate) => candidate.id === params.trashId);
      if (!restore && !item) return response(404).json(apiError("not_found", "trash item not found"));
      const answer = restore ? restore(params.trashId, body) : restoredAs(item!, body.name);
      return answer === "name_conflict"
        ? response(409).json(apiError("name_conflict", "a file with this name already exists"))
        : response(200).json(answer);
    }),
    http.delete("/api/v1/trash/{trashId}", ({ params, response }) => {
      purged.push(params.trashId);
      return response(204).empty();
    }),
  ];
  return { restores, purged, handlers };
}

// restoredAs is item back in its space under name.
export function restoredAs(item: TrashItem, name: string, overrides: Partial<RestoredEntry> = {}): RestoredEntry {
  return {
    id: item.entryId,
    spaceId: item.spaceId,
    name,
    kind: item.kind,
    sizeBytes: item.sizeBytes,
    modifiedAt: new Date().toISOString(),
    ...overrides,
  };
}
