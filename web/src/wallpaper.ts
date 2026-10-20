import { useEffect, useSyncExternalStore, type CSSProperties } from "react";

import { readWallpaper, wallpaperImageURL, type Wallpaper, type WallpaperStyle } from "./api";
import defaultWallpaperURL from "./assets/a-nas-wallpaper.png";

// Every desktop draws the wallpaper an administrator chose in 设置 → 个性化.
// Open desktops read it again every minute and when they come back into view,
// so a change reaches the local console and other browsers without a reload.

export const wallpaperRefreshMs = 60_000;
// backdrop shows wherever the picture does not reach; it is the default
// wallpaper's navy, which the server also puts behind transparent pixels.
export const wallpaperBackdrop = "#031127";
const defaultWallpaperSize = { width: 1448, height: 1086 };

let current: Wallpaper | undefined;
const listeners = new Set<() => void>();

export function publishWallpaper(next: Wallpaper) {
  current = next;
  listeners.forEach((listener) => listener());
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => { listeners.delete(listener); };
}

// useWallpaper is undefined until the first read, so a chosen wallpaper does
// not flash the default one first; a failed read shows the default.
export function useWallpaper(): Wallpaper | undefined {
  const wallpaper = useSyncExternalStore(subscribe, () => current);
  useEffect(() => {
    let active = true;
    const load = () => {
      void readWallpaper()
        .then((next) => { if (active) publishWallpaper(next); })
        .catch(() => { if (active && !current) publishWallpaper({ style: "fill" }); });
    };
    load();
    const timer = window.setInterval(load, wallpaperRefreshMs);
    const visible = () => { if (!document.hidden) load(); };
    document.addEventListener("visibilitychange", visible);
    return () => {
      active = false;
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", visible);
    };
  }, []);
  return wallpaper;
}

// WallpaperPicture is a picture as the desktop draws it, at its own size.
export interface WallpaperPicture { url: string; width: number; height: number }

export function wallpaperPicture(wallpaper: Wallpaper): WallpaperPicture {
  const picture = wallpaper.image;
  return picture ? { url: wallpaperImageURL(picture.version), width: picture.width, height: picture.height } : { url: defaultWallpaperURL, ...defaultWallpaperSize };
}

// wallpaperBackground draws wallpaper as CSS backgrounds; before the first
// read it is plain backdrop.
export function wallpaperBackground(wallpaper: Wallpaper | undefined, scale = 1): CSSProperties {
  if (!wallpaper) return { backgroundColor: wallpaperBackdrop, backgroundImage: "none" };
  return drawWallpaper(wallpaper.style, wallpaperPicture(wallpaper), scale);
}

// drawWallpaper lays picture out in style. scale shrinks the picture's own
// size for 居中 and 平铺, so a small preview of the desktop shows them in
// proportion.
export function drawWallpaper(style: WallpaperStyle, picture: WallpaperPicture, scale = 1): CSSProperties {
  const natural = `${Math.max(1, picture.width * scale)}px ${Math.max(1, picture.height * scale)}px`;
  const layout: Record<WallpaperStyle, Pick<CSSProperties, "backgroundSize" | "backgroundRepeat" | "backgroundPosition">> = {
    fill: { backgroundSize: "cover", backgroundRepeat: "no-repeat", backgroundPosition: "center" },
    fit: { backgroundSize: "contain", backgroundRepeat: "no-repeat", backgroundPosition: "center" },
    stretch: { backgroundSize: "100% 100%", backgroundRepeat: "no-repeat", backgroundPosition: "center" },
    center: { backgroundSize: natural, backgroundRepeat: "no-repeat", backgroundPosition: "center" },
    tile: { backgroundSize: natural, backgroundRepeat: "repeat", backgroundPosition: "left top" },
  };
  return { backgroundColor: wallpaperBackdrop, backgroundImage: `url("${picture.url}")`, ...layout[style] };
}
