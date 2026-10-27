// Fixtures and fetch stubs for tests that render the whole desktop (<App />).
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";

import type {
  AppListResponse,
  AppShortcut,
  AssistantStatus,
  ContainerSnapshotResponse,
  FileEntry,
  HostStateResponse,
  LocalDisplay,
  Notification,
  Profile,
  Session,
  Space,
  StoragePlan,
  Transfer,
  User,
  Volume,
  Wallpaper,
} from "../api.generated";
import { transferStore } from "../transfers/store";
import { apiError, json, stubAPI } from "./api";
import { healthyState, metricsState, session as sessionFixture, taskManagerDevice, taskManagerTasks } from "./fixtures";

export { healthyState, metricsState } from "./fixtures";
export const session = sessionFixture();

// What a fresh device answers to the reads the desktop makes on its own; any
// other read gets an empty list.
const fresh: Record<string, unknown> = {
  "/api/v1/wallpaper": { style: "fill" } satisfies Wallpaper,
  "/api/v1/local-display": { sleepAfterMinutes: 30 } satisfies LocalDisplay,
  "/api/v1/assistant/status": { available: false } satisfies AssistantStatus,
  "/api/v1/apps": { dataSource: "simulated", apps: [] } satisfies AppListResponse,
  "/api/v1/containers": {
    dataSource: "simulated",
    observedAt: "2026-10-07T10:00:00Z",
    engine: { version: "27.5.1", apiVersion: "1.47" },
    containers: [],
    images: [],
  } satisfies ContainerSnapshotResponse,
};

export function installAPI(
  options: {
    setupRequired?: boolean;
    setupAllowed?: boolean;
    spaces?: Space[];
    entries?: FileEntry[];
    volumes?: Volume[];
    storagePlan?: StoragePlan;
    unfinishedPlans?: StoragePlan[];
    operation?: StoragePlan;
    executeFails?: boolean;
    terminalEnabled?: boolean;
    session?: Session;
    notifications?: Notification[];
    users?: User[];
    textPreview?: string;
    profiles?: Profile[];
    shortcuts?: AppShortcut[];
    hostState?: HostStateResponse;
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
    if (path === "/api/v1/task-manager/device") return ok(taskManagerDevice);
    if (path === "/api/v1/task-manager/tasks") return ok(taskManagerTasks);
    if (path === "/api/v1/terminal") return ok({ enabled: options.terminalEnabled ?? false });
    if (path === "/api/v1/spaces") return ok({ items: options.spaces ?? [] });
    if (path.startsWith("/api/v1/spaces/") && path.includes("/entries")) return ok({ items: options.entries ?? [] });
    if (path.startsWith("/api/v1/files/") && path.includes("/content")) return new Response(options.textPreview ?? "");
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
    if (path.startsWith("/api/v1/volumes/") && path.endsWith("/conversion")) return new Response(null, { status: 202 });
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
        } satisfies Transfer,
        201,
      );
    }
    return ok(fresh[path] ?? { items: [] });
  });
  stubAPI(mock);
  return mock;
}

const ok = json;
const failure = apiError;

export async function openSettingsSection(
  user: ReturnType<typeof userEvent.setup>,
  desktop: HTMLElement,
  section: string,
) {
  await user.click(within(desktop).getByRole("button", { name: "打开设置" }));
  // The settings window loads its code when first opened.
  const sections = await within(screen.getByRole("dialog", { name: "设置" })).findByRole("navigation", {
    name: "设置分区",
  });
  await user.click(within(sections).getByRole("button", { name: section }));
}

export function routeFetch(routes: {
  hostState: () => Promise<Response>;
  metrics?: () => Promise<Response>;
  terminalEnabled?: boolean;
  containers?: (url: string, init?: RequestInit) => Promise<Response>;
  apps?: (url: string, init?: RequestInit) => Promise<Response>;
}) {
  stubAPI(
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input).startsWith("/api/v1/containers") && routes.containers)
        return routes.containers(String(input), init);
      if (String(input).startsWith("/api/v1/apps") && routes.apps) return routes.apps(String(input), init);
      switch (String(input)) {
        case "/api/v1/setup/status":
          return okResponse({ setupRequired: false, setupAllowed: true });
        case "/api/v1/session":
          return okResponse(session);
        case "/api/v1/host-state":
          return routes.hostState();
        case "/api/v1/metrics":
          return routes.metrics ? routes.metrics() : okResponse(metricsState);
        case "/api/v1/terminal":
          return okResponse({ enabled: routes.terminalEnabled ?? true });
        case "/api/v1/task-manager/device":
          return okResponse(taskManagerDevice);
        case "/api/v1/task-manager/tasks":
          return okResponse(taskManagerTasks);
        default:
          return okResponse(fresh[String(input)] ?? { items: [] });
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
  return json(state);
}

export function sessionExpiredResponse() {
  return apiError(401, "authentication_required", "authentication is required");
}

export function errorResponse(status: number) {
  return apiError(status, "state_unavailable", "host state is unavailable");
}

// resetDesktop undoes what a desktop test leaves behind: transfers, fake
// timers and stubbed globals.
export function resetDesktop() {
  transferStore.reset();
  vi.useRealTimers();
  vi.unstubAllGlobals();
}
