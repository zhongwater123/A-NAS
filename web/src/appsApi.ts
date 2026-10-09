import { DataSource, apiFetch, csrfHeaders } from "./api";

export type InstallState = "available" | "installed" | "installing" | "uninstalling";

export interface AppJob {
  appId: string;
  action: "install" | "uninstall";
  state: "running" | "succeeded" | "failed";
  startedAt: string;
  finishedAt?: string;
  output: string[];
  error?: string;
}

export interface CatalogApp {
  id: string;
  title: string;
  tagline: string;
  description: string;
  category: string;
  version: string;
  author: string;
  website: string;
  webPort?: number;
  scheme: string;
  path: string;
  state: InstallState;
  running: number;
  total: number;
  job?: AppJob;
}

export interface AppList {
  dataSource: DataSource;
  apps: CatalogApp[];
}

export interface InstallPlan {
  appId: string;
  title: string;
  version: string;
  project: string;
  // The Linux account app-<id> the app's containers run as.
  identity: { username: string; uid: number; gid: number };
  images: string[];
  containers: string[];
  ports: Array<{ hostPort: number; containerPort: number; protocol: string; purpose?: string }>;
  mounts: Array<{ hostPath: string; containerPath: string; kind: "appdata" | "shared" | "system"; readOnly: boolean; purpose?: string }>;
  // Docker networks the install creates and the address pools they come from.
  networks: string[];
  addressPools: string[];
  digest: string;
  compose: string;
}

export class AppsAPIError extends Error {
  constructor(readonly code: string, readonly detail = "") {
    super(`app center request failed: ${code}`);
  }
}

export async function readApps(signal?: AbortSignal): Promise<AppList> {
  const value = await request("/api/v1/apps", { signal });
  if (!isRecord(value) || !Array.isArray(value.apps)) throw new AppsAPIError("malformed_response");
  return value as unknown as AppList;
}

export async function readPlan(id: string, signal?: AbortSignal): Promise<InstallPlan> {
  const value = await request(`/api/v1/apps/${id}/plan`, { signal });
  if (!isRecord(value) || typeof value.digest !== "string") throw new AppsAPIError("malformed_response");
  return value as unknown as InstallPlan;
}

export async function installApp(id: string, digest: string): Promise<AppJob> {
  return (await request(`/api/v1/apps/${id}/install`, post({ digest }))) as AppJob;
}

export async function uninstallApp(id: string): Promise<AppJob> {
  return (await request(`/api/v1/apps/${id}/uninstall`, post({}))) as AppJob;
}

export function iconURL(id: string): string {
  return `/api/v1/apps/${id}/icon`;
}

function post(body: unknown): RequestInit {
  return { method: "POST", headers: { "Content-Type": "application/json", ...csrfHeaders() }, body: JSON.stringify(body) };
}

async function request(path: string, init: RequestInit): Promise<unknown> {
  let response: Response;
  try {
    response = await apiFetch(path, { ...init, headers: { Accept: "application/json", ...init.headers } });
  } catch (error) {
    if (error instanceof DOMException && error.name === "AbortError") throw error;
    throw new AppsAPIError("network_error");
  }
  const body: unknown = await response.json().catch(() => undefined);
  if (!response.ok) {
    const failure = isRecord(body) && isRecord(body.error) ? body.error : {};
    throw new AppsAPIError(typeof failure.code === "string" ? failure.code : "request_failed", typeof failure.message === "string" ? failure.message : "");
  }
  return body;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}
