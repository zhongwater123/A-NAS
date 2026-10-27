import type { HostStateResponse, LocalDisplay, MetricsResponse, Notification, WallpaperStyle } from "../api.generated";
import { call, client, rawBody } from "../api/client";

export type HostState = HostStateResponse;
export type HostMetrics = MetricsResponse;
// A run of one of the member's VNAS scheduled tasks that finished; at most one waits per task.
export type AssistantTaskNotice = NonNullable<Notification["task"]>;

export const readHostState = (signal?: AbortSignal): Promise<HostState> =>
  call(client.GET("/api/v1/host-state", { signal }));
export const readHostMetrics = (signal?: AbortSignal): Promise<HostMetrics> =>
  call(client.GET("/api/v1/metrics", { signal }));

// readLocalDisplay needs no session: the sign-in page on the local console reads it too.
export const readLocalDisplay = () => call(client.GET("/api/v1/local-display"));
export const updateLocalDisplay = (sleepAfterMinutes: LocalDisplay["sleepAfterMinutes"]) =>
  call(client.PUT("/api/v1/local-display", { body: { sleepAfterMinutes } }));
export const readWallpaper = () => call(client.GET("/api/v1/wallpaper"));
export const updateWallpaperStyle = (style: WallpaperStyle) =>
  call(client.PATCH("/api/v1/wallpaper", { body: { style } }));
export const uploadWallpaper = (picture: Blob) =>
  call(
    client.PUT("/api/v1/wallpaper/image", {
      ...rawBody(picture),
      headers: { "Content-Type": picture.type || "image/jpeg" },
    }),
  );
export const resetWallpaper = () => call(client.DELETE("/api/v1/wallpaper/image"));
// A version names one picture for good, so the browser keeps it cached.
export const wallpaperImageURL = (version: string) => `/api/v1/wallpaper/image?v=${encodeURIComponent(version)}`;

export const listNotifications = async () => (await call(client.GET("/api/v1/notifications"))).items;
export const acknowledgeNotification = (notificationId: string) =>
  call(client.POST("/api/v1/notifications/{notificationId}/acknowledge", { params: { path: { notificationId } } }));
