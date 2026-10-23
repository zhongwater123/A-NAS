import { APIError, call, client } from "./api";
import type { AppListResponse, AppPlan, AppStatus } from "./api.generated";

export type { AppCredential, AppJob, AppShortcut } from "./api.generated";
export type CatalogApp = AppStatus;
export type AppList = AppListResponse;
export type InstallPlan = AppPlan;
// failed: the last install failed and its containers could not be removed.
export type InstallState = CatalogApp["state"];
// How one service runs (ADR 0022): app starts as the app identity with no
// capabilities; app-after-init starts as root and switches to it; image keeps
// the image's own user, which may be root, and only reaches its app's data.
export type ServiceRuntime = NonNullable<InstallPlan["services"]>[number];

export class AppsAPIError extends Error {
  constructor(
    readonly code: string,
    readonly detail = "",
  ) {
    super(`app center request failed: ${code}`);
  }
}

export async function readApps(signal?: AbortSignal): Promise<AppList> {
  const value = await request(client.GET("/api/v1/apps", { signal }));
  if (!isRecord(value) || !Array.isArray(value.apps)) throw new AppsAPIError("malformed_response");
  return value;
}

export async function readPlan(appId: string, signal?: AbortSignal): Promise<InstallPlan> {
  const value = await request(client.GET("/api/v1/apps/{appId}/plan", { params: { path: { appId } }, signal }));
  if (!isRecord(value) || typeof value.digest !== "string") throw new AppsAPIError("malformed_response");
  return value;
}

export const installApp = (appId: string, digest: string) =>
  request(client.POST("/api/v1/apps/{appId}/install", { params: { path: { appId } }, body: { digest } }));

export const uninstallApp = (appId: string) =>
  request(client.POST("/api/v1/apps/{appId}/uninstall", { params: { path: { appId } }, body: {} }));

export function iconURL(id: string): string {
  return `/api/v1/apps/${id}/icon`;
}

// readShortcuts lists the installed apps every signed-in user opens from the desktop.
export async function readShortcuts(signal?: AbortSignal) {
  const value = await request(client.GET("/api/v1/app-shortcuts", { signal }));
  if (!isRecord(value) || !Array.isArray(value.items)) throw new AppsAPIError("malformed_response");
  return value.items;
}

export function shortcutIconURL(id: string): string {
  return `/api/v1/app-shortcuts/${id}/icon`;
}

// request unwraps an App Center call: a failed one throws an AppsAPIError
// with the server's code, network_error when the request went nowhere and
// malformed_response when the body was not JSON.
async function request<Data>(pending: Promise<{ data?: Data; error?: unknown; response: Response }>): Promise<Data> {
  try {
    return await call(pending);
  } catch (error) {
    if (error instanceof DOMException && error.name === "AbortError") throw error;
    if (error instanceof APIError) throw new AppsAPIError(error.code, error.message);
    throw new AppsAPIError(error instanceof TypeError ? "network_error" : "malformed_response");
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}
