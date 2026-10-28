// The App Center: the catalog with each app's state, install plans and jobs.
import type { AppJob, AppPlan, AppStatus } from "../../api.generated";
import { http } from "../server";

// app is a catalog app as the App Center lists it, not installed.
export function app(id: string, overrides: Partial<AppStatus> = {}): AppStatus {
  return {
    id,
    title: id[0].toUpperCase() + id.slice(1),
    tagline: "",
    description: "",
    notes: "",
    category: "notes",
    version: "1.0",
    author: "",
    developer: "",
    website: "",
    repo: "",
    docs: "",
    updated: "",
    scheme: "http",
    path: "/",
    state: "available",
    running: 0,
    total: 0,
    ...overrides,
  };
}

// job is an app's install job that has just started.
export function job(overrides: Partial<AppJob> = {}): AppJob {
  return {
    appId: "memos",
    action: "install",
    state: "running",
    startedAt: new Date().toISOString(),
    output: [],
    ...overrides,
  };
}

export const memosPlan: AppPlan = {
  appId: "memos",
  title: "Memos",
  version: "0.28.0",
  project: "a-nas-memos",
  identity: { username: "app-memos", uid: 30000, gid: 30000 },
  images: ["neosmemo/memos:0.28.0"],
  containers: ["memos"],
  ports: [{ hostPort: 5230, containerPort: 5230, protocol: "tcp", purpose: "WebUI 端口" }],
  mounts: [
    { hostPath: "/srv/a-nas/data/apps/memos/memos", containerPath: "/var/opt/memos", kind: "appdata", readOnly: false },
  ],
  networks: [],
  addressPools: [],
  digest: "d".repeat(64),
  compose: "name: a-nas-memos\n",
};

// appsHandlers answers the catalog from apps, read on every request so a test
// changes it as the server would, and every app's plan with plan.
export function appsHandlers(apps: () => AppStatus[], plan: AppPlan = memosPlan) {
  return [
    http.get("/api/v1/apps", ({ response }) => response(200).json({ dataSource: "simulated", apps: apps() })),
    http.get("/api/v1/apps/{appId}/plan", ({ response }) => response(200).json(plan)),
  ];
}
