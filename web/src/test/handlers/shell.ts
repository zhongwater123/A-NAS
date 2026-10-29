// The reads the desktop shell makes on its own: sign-in, the status bar, the
// rail, the icons, notifications and what the signed-in desktop loads around
// the windows. Tests that render <App /> serve these and add what they open.
import type {
  AppShortcut,
  AssistantStatus,
  HostStateResponse,
  MetricsResponse,
  Notification,
  Profile,
  Session,
  SetupStatus,
  Wallpaper,
} from "../../api.generated";
import { healthyState, metricsState, session as sessionFixture } from "../fixtures";
import { http, server } from "../server";

export const session = sessionFixture();

export interface ShellOptions {
  setup?: Partial<SetupStatus>;
  session?: Session;
  hostState?: HostStateResponse;
  metrics?: MetricsResponse;
  notifications?: Notification[];
  shortcuts?: AppShortcut[];
  profiles?: Profile[];
  wallpaper?: Wallpaper;
  assistant?: AssistantStatus;
}

export function shellHandlers(options: ShellOptions = {}) {
  return [
    http.get("/api/v1/setup/status", ({ response }) =>
      response(200).json({ setupRequired: false, setupAllowed: true, ...options.setup }),
    ),
    http.get("/api/v1/session", ({ response }) => response(200).json(options.session ?? session)),
    http.get("/api/v1/host-state", ({ response }) => response(200).json(options.hostState ?? healthyState)),
    http.get("/api/v1/metrics", ({ response }) => response(200).json(options.metrics ?? metricsState)),
    http.get("/api/v1/notifications", ({ response }) => response(200).json({ items: options.notifications ?? [] })),
    http.get("/api/v1/app-shortcuts", ({ response }) => response(200).json({ items: options.shortcuts ?? [] })),
    http.get("/api/v1/profiles", ({ response }) => response(200).json({ items: options.profiles ?? [] })),
    http.get("/api/v1/wallpaper", ({ response }) => response(200).json(options.wallpaper ?? { style: "fill" })),
    http.get("/api/v1/assistant/status", ({ response }) =>
      response(200).json(options.assistant ?? { available: false }),
    ),
    // The rail's count of new shares, the transfer indicator and 问题反馈.
    http.get("/api/v1/shares", ({ response }) => response(200).json({ items: [] })),
    http.get("/api/v1/shares/notifications", ({ response }) => response(200).json({ items: [] })),
    http.get("/api/v1/transfers", ({ response }) => response(200).json({ items: [] })),
    http.get("/api/v1/feedback", ({ response }) => response(200).json({ items: [] })),
    // 家庭中心's hints after signing in.
    http.get("/api/v1/family/hints", ({ response }) => response(200).json({ items: [] })),
  ];
}

// serveDesktop serves the shell's reads; a test's own server.use comes after
// and wins.
export function serveDesktop(options: ShellOptions = {}) {
  server.use(...shellHandlers(options));
}
