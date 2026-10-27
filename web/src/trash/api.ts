import { call, client } from "../api/client";
import type { TrashRestore } from "../api.generated";

export const listTrash = async () => (await call(client.GET("/api/v1/trash"))).items;
// RestoreConflict is what a restore does when its name is taken: ask answers
// name_conflict, rename keeps both under "name (恢复)", replace (files only)
// sends the file of that name to the recycle bin.
export type RestoreConflict = TrashRestore["conflict"];
export const restoreTrash = (trashId: string, name: string, conflict: RestoreConflict = "ask") =>
  call(
    client.POST("/api/v1/trash/{trashId}/restore", {
      params: { path: { trashId } },
      body: { parentId: "", name, conflict },
    }),
  );
export const purgeTrash = (trashId: string) =>
  call(client.DELETE("/api/v1/trash/{trashId}", { params: { path: { trashId } } }));
