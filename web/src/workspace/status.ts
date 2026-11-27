// Whether this account may use 共享工作区 (ADR 0040): GET /api/v1/workspace
// answers 200 for administrators and the members they added, 404 for
// everyone else. The desktop imports this file eagerly, so it stays small;
// the window's own client is ./api.
import { client } from "../api/client";
import type { components } from "../api.generated";

export type WorkspaceAccess = components["schemas"]["WorkspaceAccess"];

// workspaceAccess is the account's access, or null when it has none. Any
// other answer, such as the service being down, throws: the desktop keeps
// what it knew.
export async function workspaceAccess(signal?: AbortSignal): Promise<WorkspaceAccess | null> {
  const { data, response } = await client.GET("/api/v1/workspace", { signal });
  if (response.ok && data) return data;
  if (response.status === 404) return null;
  throw new Error(`GET /api/v1/workspace answered ${response.status}`);
}
