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
  // Markdown from the vendored manifest; notes is its advice before first use, such as a default account.
  description: string;
  notes: string;
  // The manifest's own notes, set only when notes is written for A-NAS because they describe CasaOS.
  upstreamNotes?: string;
  // An A-NAS category, or "" from a container agent that predates them.
  category: string;
  version: string;
  author: string;
  developer: string;
  // Absolute http(s) links or "".
  website: string;
  repo: string;
  docs: string;
  // YYYY-MM-DD or "".
  updated: string;
  webPort?: number;
  scheme: string;
  path: string;
  state: InstallState;
  running: number;
  total: number;
  job?: AppJob;
  // Containers that are not running: restarting, exited or never started.
  problems?: Array<{ service: string; state: string; exitCode: number }>;
  // Logins generated for the app, such as its admin password.
  credentials?: AppCredential[];
  // Installed under older runtime rules; reinstalling applies the current ones (ADR 0022).
  runtimeOutdated?: boolean;
}

// upstream marks the manifest's published value, still used by an app
// installed before A-NAS generated its logins.
export interface AppCredential { label: string; username?: string; secret: string; upstream?: boolean }

// How one service runs (ADR 0022): app starts as the app identity with no
// capabilities; app-after-init starts as root and switches to it; image keeps
// the image's own user, which may be root, and only reaches its app's data.
export interface ServiceRuntime {
  service: string;
  runAs: "app" | "app-after-init" | "image";
  user?: string;
  groups?: string[];
  capDrop?: string[];
  shared?: "read" | "write";
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
  // The Linux account app-<id> allocated for the app; services says how each service uses it.
  identity: { username: string; uid: number; gid: number };
  // Absent from a container agent that predates runtime rules.
  services?: ServiceRuntime[];
  images: string[];
  containers: string[];
  ports: Array<{ hostPort: number; containerPort: number; protocol: string; purpose?: string }>;
  mounts: Array<{ hostPath: string; containerPath: string; kind: "appdata" | "shared" | "system"; readOnly: boolean; purpose?: string }>;
  // Docker networks the install creates and the address pools they come from.
  networks: string[];
  addressPools: string[];
  digest: string;
  compose: string;
  credentials?: AppCredential[];
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
