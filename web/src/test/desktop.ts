// Fixtures and fetch stubs for tests that render the whole desktop (<App />).
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";

import { transferStore } from "../transfers/store";

export const session = {
  csrfToken: "csrf-test",
  expiresAt: "2026-10-07T22:00:00Z",
  user: { id: "user:owner", username: "owner", role: "admin", status: "active", createdAt: "2026-10-07T10:00:00Z" },
};

export const healthyState = {
  dataSource: "simulated",
  productVersion: "v1.0.1-rc.1",
  observedAt: "2026-10-07T10:00:00Z",
  system: {
    id: "host:fake-01",
    hostname: "anas-fake",
    operatingSystem: { name: "Debian", version: "13" },
    architecture: "amd64",
    uptimeSeconds: 3661,
    health: "healthy",
  },
  disks: [
    {
      id: "disk:fake-data-01",
      model: "A-NAS Fake HDD",
      transport: "sata",
      capacityBytes: 512000000000,
      rotational: true,
      removable: false,
      inUse: false,
      filesystems: [],
      role: "unassigned",
      health: "healthy",
      smartStatus: "healthy",
      eligibleForDataVolume: true,
      ineligibleReasons: [],
      temperatureCelsius: 31,
    },
  ],
};

export const metricsState = {
  dataSource: "simulated",
  observedAt: "2026-10-06T00:00:00Z",
  cpu: { usagePercent: 23.5, logicalCores: 4 },
  memory: { totalBytes: 8589934592, usedBytes: 3435973837 },
  network: { receiveBytesPerSecond: 2516582, transmitBytesPerSecond: 327680 },
};

export function installAPI(
  options: {
    setupRequired?: boolean;
    setupAllowed?: boolean;
    spaces?: unknown[];
    entries?: unknown[];
    volumes?: unknown[];
    storagePlan?: unknown;
    unfinishedPlans?: unknown[];
    operation?: unknown;
    executeFails?: boolean;
    terminalEnabled?: boolean;
    session?: unknown;
    notifications?: unknown[];
    users?: unknown[];
    textPreview?: string;
    profiles?: unknown[];
    shortcuts?: unknown[];
    hostState?: unknown;
  } = {},
) {
  const mock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input);
    if (path === "/api/v1/app-shortcuts") return ok({ items: options.shortcuts ?? [] });
    const signedIn = (options.session ?? session) as { user: Record<string, unknown> };
    if (path === "/api/v1/profiles") return ok({ items: options.profiles ?? [] });
    if (path === "/api/v1/session/profile" && init?.method === "PATCH") {
      const { nickname } = JSON.parse(String(init.body)) as { nickname: string };
      if (nickname === "owner") return failure(409, "nickname_unavailable");
      return ok({ ...signedIn.user, nickname: nickname || undefined });
    }
    if (path === "/api/v1/session/avatar" && init?.method === "PUT")
      return ok({ ...signedIn.user, avatarVersion: "0123456789abcdef" });
    if (path === "/api/v1/session/avatar" && init?.method === "DELETE")
      return ok({ ...signedIn.user, avatarVersion: undefined });
    if (path === "/api/v1/setup/status")
      return ok({ setupRequired: options.setupRequired ?? false, setupAllowed: options.setupAllowed ?? true });
    if (path === "/api/v1/setup/admin" && init?.method === "POST") return ok(session, 201);
    if (path === "/api/v1/session") return ok(options.session ?? session);
    if (path === "/api/v1/notifications") return ok({ items: options.notifications ?? [] });
    if (path === "/api/v1/host-state") return ok(options.hostState ?? healthyState);
    if (path === "/api/v1/metrics") return ok(metricsState);
    if (path === "/api/v1/terminal") return ok({ enabled: options.terminalEnabled ?? false });
    if (path === "/api/v1/spaces") return ok({ items: options.spaces ?? [] });
    if (path.startsWith("/api/v1/spaces/") && path.includes("/entries")) return ok({ items: options.entries ?? [] });
    if (path.startsWith("/api/v1/files/") && path.includes("/content"))
      return { ok: true, status: 200, text: async () => options.textPreview ?? "" } as Response;
    if (path === "/api/v1/volumes") return ok({ items: options.volumes ?? [] });
    if (path === "/api/v1/storage/plans" && init?.method === "POST") return ok(options.storagePlan ?? {}, 201);
    if (path === "/api/v1/storage/plans") return ok({ items: options.unfinishedPlans ?? [] });
    if (path.startsWith("/api/v1/operations/"))
      return ok(options.operation ?? { ...(options.storagePlan as object), state: "succeeded" });
    if (path.startsWith("/api/v1/storage/plans/") && path.endsWith("/confirm"))
      return ok({ ...(options.storagePlan as object), state: "confirmed" });
    if (path.startsWith("/api/v1/storage/plans/") && path.endsWith("/execute"))
      return options.executeFails
        ? failure(503, "operation_needs_attention")
        : ok({ ...(options.storagePlan as object), state: "succeeded" });
    if (path.startsWith("/api/v1/storage/plans/") && path.endsWith("/cancel"))
      return ok({ ...(options.storagePlan as object), state: "cancelled" });
    if (path.startsWith("/api/v1/volumes/") && path.endsWith("/conversion"))
      return {
        ok: true,
        status: 202,
        headers: new Headers({ "Content-Length": "0" }),
        json: async () => undefined,
      } as Response;
    if (path === "/api/v1/users" && init?.method === "POST") {
      const { username } = JSON.parse(String(init.body)) as { username: string };
      return ok(
        {
          id: "user:new",
          username: username.trim().toLowerCase(),
          role: "member",
          status: "active",
          createdAt: "2026-10-07T10:00:00Z",
          mustChangePassword: false,
        },
        201,
      );
    }
    if (path === "/api/v1/users") return ok({ items: options.users ?? [session.user] });
    if (path === "/api/v1/trash") return ok({ items: [] });
    if (path === "/api/v1/transfers/downloads" && init?.method === "POST") {
      const { entryIds } = JSON.parse(String(init.body)) as { entryIds: string[] };
      return ok(
        {
          id: "download-1",
          direction: "download",
          kind: entryIds.length > 1 ? "archive" : "file",
          name: entryIds.length > 1 ? "下载.zip" : "notes.txt",
          location: { spaceId: "space:owner", spaceKind: "private", folders: [] },
          totalBytes: 12,
          transferredBytes: 0,
          bytesPerSecond: 0,
          state: "pending",
          contentUrl: "/api/v1/transfers/download-1/content",
          createdAt: "2026-10-12T10:00:00Z",
          updatedAt: "2026-10-12T10:00:00Z",
        },
        201,
      );
    }
    return ok({ items: [] });
  });
  vi.stubGlobal("fetch", mock);
  return mock;
}

function ok(body: unknown, status = 200) {
  return { ok: true, status, json: async () => body } as Response;
}
function failure(status: number, code: string) {
  return { ok: false, status, json: async () => ({ error: { code, message: code } }) } as Response;
}

export async function openSettingsSection(
  user: ReturnType<typeof userEvent.setup>,
  desktop: HTMLElement,
  section: string,
) {
  await user.click(within(desktop).getByRole("button", { name: "打开设置" }));
  const sections = within(screen.getByRole("dialog", { name: "设置" })).getByRole("navigation", { name: "设置分区" });
  await user.click(within(sections).getByRole("button", { name: section }));
}

export function routeFetch(routes: {
  hostState: () => Promise<Response>;
  metrics?: () => Promise<Response>;
  terminalEnabled?: boolean;
  containers?: (url: string, init?: RequestInit) => Promise<Response>;
  apps?: (url: string, init?: RequestInit) => Promise<Response>;
}) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input).startsWith("/api/v1/containers") && routes.containers)
        return routes.containers(String(input), init);
      if (String(input).startsWith("/api/v1/apps") && routes.apps) return routes.apps(String(input), init);
      switch (String(input)) {
        case "/api/v1/setup/status":
          return okResponse({ setupRequired: false });
        case "/api/v1/session":
          return okResponse(session);
        case "/api/v1/host-state":
          return routes.hostState();
        case "/api/v1/metrics":
          return routes.metrics ? routes.metrics() : okResponse(metricsState);
        case "/api/v1/terminal":
          return okResponse({ enabled: routes.terminalEnabled ?? true });
        default:
          return okResponse({ items: [] });
      }
    }),
  );
}

export function stubFetch(state: unknown) {
  routeFetch({ hostState: vi.fn().mockResolvedValue(okResponse(state)) });
}

export function stubDesktopFetch(state: unknown, terminalEnabled: boolean) {
  routeFetch({ hostState: vi.fn().mockResolvedValue(okResponse(state)), terminalEnabled });
}

export function okResponse(state: unknown) {
  return {
    ok: true,
    status: 200,
    json: async () => state,
  } as Response;
}

export function sessionExpiredResponse() {
  return {
    ok: false,
    status: 401,
    json: async () => ({ error: { code: "authentication_required", message: "authentication is required" } }),
  } as Response;
}

export function errorResponse(status: number) {
  return {
    ok: false,
    status,
    json: async () => ({ error: { code: "state_unavailable", message: "host state is unavailable" } }),
  } as Response;
}

// resetDesktop undoes what a desktop test leaves behind: transfers, fake
// timers and stubbed globals.
export function resetDesktop() {
  transferStore.reset();
  vi.useRealTimers();
  vi.unstubAllGlobals();
}
