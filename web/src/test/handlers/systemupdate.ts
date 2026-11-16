// 系统更新: the update service as the Product Service shows it. An action
// moves the state straight to where the service would leave it, so a test
// sees the outcome on the next read.
import type { SystemUpdateSettings, SystemUpdateStatus } from "../../api.generated";
import { apiError, http } from "../server";

export const currentVersion = "1.0.1~rc.6+dev20261112.79+g83bb9db1c0de";
export const nextVersion = "1.0.1~rc.6+dev20261113.80+g4e842653a1b2";

export function systemUpdateStatus(overrides: Partial<SystemUpdateStatus> = {}): SystemUpdateStatus {
  return {
    phase: "up-to-date",
    running: false,
    current: currentVersion,
    downloadBytes: 0,
    downloadedBytes: 0,
    suites: ["release", "dev"],
    checkedAt: new Date().toISOString(),
    installedAt: "2026-11-12T06:03:00Z",
    records: [],
    settings: { autoDownload: true, autoInstall: false, windowStart: "03:00", windowEnd: "05:00" },
    waiting: [],
    ...overrides,
  };
}

export interface SystemUpdateOptions {
  status?: SystemUpdateStatus;
  // refuse answers every action with this error code, as a 409.
  refuse?: string;
}

// systemUpdateHandlers answers 系统更新 and records the actions and settings sent.
export function systemUpdateHandlers(options: SystemUpdateOptions = {}) {
  let status = options.status ?? systemUpdateStatus();
  const sent = { actions: [] as string[], settings: [] as SystemUpdateSettings[] };
  const handlers = [
    http.get("/api/v1/system/update", ({ response }) => response(200).json(status)),
    http.post("/api/v1/system/update/actions", async ({ request, response }) => {
      const { action } = await request.json();
      sent.actions.push(action);
      if (options.refuse) return response(409).json(apiError(options.refuse, "refused"));
      const at = new Date().toISOString();
      if (action === "install" && status.candidate) {
        status = {
          ...status,
          phase: "up-to-date",
          previous: status.current,
          current: status.candidate,
          candidate: undefined,
          records: [{ at, kind: "updated", version: status.candidate, from: status.current, by: "owner" }],
        };
      } else if (action === "revert" && status.previous) {
        status = {
          ...status,
          phase: "up-to-date",
          current: status.previous,
          previous: undefined,
          records: [{ at, kind: "reverted", version: status.previous, from: status.current, by: "owner" }],
        };
      } else if (action === "download") {
        status = { ...status, phase: "ready" };
      } else {
        status = { ...status, checkedAt: at };
      }
      return response(202).json(status);
    }),
    http.put("/api/v1/system/update/settings", async ({ request, response }) => {
      const settings = await request.json();
      sent.settings.push(settings);
      status = { ...status, settings };
      return response(200).json(status);
    }),
  ];
  return { sent, handlers };
}
