// 设置: what the Settings window reads on its own, the terminal's status and
// 个性化. Accounts are in users.ts, the storage section in storage.ts.
import type { HostStateResponse, LocalDisplay, Wallpaper, WallpaperStyleChange } from "../../api.generated";
import { healthyState } from "../fixtures";
import { apiError, http } from "../server";
import { type StorageOptions, storageHandlers } from "./storage";

export interface SettingsOptions extends StorageOptions {
  hostState?: HostStateResponse;
}

// settingsHandlers answers what Settings reads whatever section it shows: the
// host state the desktop shares with every window, and the data volumes,
// with the rest of the storage section. sent is what storageHandlers records.
export function settingsHandlers(options: SettingsOptions = {}) {
  const storage = storageHandlers(options);
  const handlers = [
    http.get("/api/v1/host-state", ({ response }) => response(200).json(options.hostState ?? healthyState)),
    ...storage.handlers,
  ];
  return { sent: storage.sent, handlers };
}

// terminalHandlers answers 终端 with whether the device's root turned the
// terminal on, and counts the checks.
export function terminalHandlers(enabled: boolean) {
  const sent = { checks: 0 };
  const handlers = [
    http.get("/api/v1/terminal", ({ response }) => {
      sent.checks += 1;
      return response(200).json({ enabled });
    }),
  ];
  return { sent, handlers };
}

export interface PersonalizationOptions {
  sleepAfterMinutes?: LocalDisplay["sleepAfterMinutes"];
  wallpaper?: Wallpaper;
  // The server refuses to save the screen and wallpaper settings.
  refuse?: boolean;
}

// personalizationHandlers answers 个性化 as the server keeps it: what is
// saved is read back. It records the writes with their CSRF token.
export function personalizationHandlers(options: PersonalizationOptions = {}) {
  let display: LocalDisplay = { sleepAfterMinutes: options.sleepAfterMinutes ?? 30 };
  let wallpaper: Wallpaper = options.wallpaper ?? { style: "fill" };
  const sent = {
    displays: [] as Array<{ body: LocalDisplay; csrf: string | null }>,
    styles: [] as WallpaperStyleChange[],
    images: [] as Array<{ type: string | null; csrf: string | null; bytes: string }>,
    resets: 0,
  };
  const refused = apiError("forbidden", "administrator permission is required");
  const handlers = [
    http.get("/api/v1/local-display", ({ response }) => response(200).json(display)),
    http.put("/api/v1/local-display", async ({ request, response }) => {
      const body = await request.json();
      sent.displays.push({ body, csrf: request.headers.get("X-CSRF-Token") });
      if (options.refuse) return response(403).json(refused);
      display = body;
      return response(200).json(display);
    }),
    http.get("/api/v1/wallpaper", ({ response }) => response(200).json(wallpaper)),
    http.patch("/api/v1/wallpaper", async ({ request, response }) => {
      const change = await request.json();
      sent.styles.push(change);
      if (options.refuse) return response(403).json(refused);
      wallpaper = { ...wallpaper, style: change.style };
      return response(200).json(wallpaper);
    }),
    http.put("/api/v1/wallpaper/image", async ({ request, response }) => {
      sent.images.push({
        type: request.headers.get("Content-Type"),
        csrf: request.headers.get("X-CSRF-Token"),
        bytes: await (await request.blob()).text(),
      });
      wallpaper = { ...wallpaper, image: { version: "0123456789abcdef", width: 800, height: 600 } };
      return response(200).json(wallpaper);
    }),
    http.delete("/api/v1/wallpaper/image", ({ response }) => {
      sent.resets += 1;
      wallpaper = { style: "fill" };
      return response(200).json(wallpaper);
    }),
  ];
  return { sent, handlers };
}
