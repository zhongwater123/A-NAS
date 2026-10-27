import { call, client } from "./api";
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

export const readApps = (signal?: AbortSignal) => call(client.GET("/api/v1/apps", { signal }));

export const readPlan = (appId: string, signal?: AbortSignal) =>
  call(client.GET("/api/v1/apps/{appId}/plan", { params: { path: { appId } }, signal }));

export const installApp = (appId: string, digest: string) =>
  call(client.POST("/api/v1/apps/{appId}/install", { params: { path: { appId } }, body: { digest } }));

export const uninstallApp = (appId: string) =>
  call(client.POST("/api/v1/apps/{appId}/uninstall", { params: { path: { appId } }, body: {} }));

export function iconURL(id: string): string {
  return `/api/v1/apps/${id}/icon`;
}

// readShortcuts lists the installed apps every signed-in user opens from the desktop.
export const readShortcuts = async (signal?: AbortSignal) =>
  (await call(client.GET("/api/v1/app-shortcuts", { signal }))).items;

export function shortcutIconURL(id: string): string {
  return `/api/v1/app-shortcuts/${id}/icon`;
}
