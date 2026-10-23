import { ArrowDownUp, FolderClosed, Music2, Settings, Share2, ShoppingBag, Trash2 } from "lucide-react";

import { AssistantGlyph } from "../AssistantGlyph";
import { BackupCenterGlyph } from "../BackupCenterGlyph";
import { DockerGlyph } from "../DockerGlyph";
import { type AppGlyph, MediaCenterGlyph } from "../MediaCenterGlyph";
import { PhotosGlyph } from "../PhotosGlyph";

export type WindowID =
  | "shares"
  | "files"
  | "transfers"
  | "photos"
  | "media"
  | "music"
  | "trash"
  | "backup"
  | "settings"
  | "docker"
  | "store"
  | "assistant";

export interface WindowModel {
  id: WindowID;
  title: string;
  open: boolean;
  minimized: boolean;
  maximized: boolean;
  x: number;
  y: number;
  width: number;
  height: number;
  z: number;
  // Sequence number of the latest open, so the dock lists apps in the order they were launched.
  opened: number;
}

export type WindowAction =
  | { type: "open"; id: WindowID }
  | { type: "close"; id: WindowID }
  | { type: "minimize"; id: WindowID }
  | { type: "focus"; id: WindowID }
  | { type: "maximize"; id: WindowID }
  | { type: "move"; id: WindowID; x: number; y: number }
  | { type: "resize"; id: WindowID; width: number; height: number }
  | { type: "show-desktop" }
  | { type: "restore"; ids: WindowID[] };

export const initialWindows: WindowModel[] = [
  {
    id: "shares",
    title: "分享",
    open: false,
    minimized: false,
    maximized: false,
    x: 214,
    y: 56,
    width: 1160,
    height: 700,
    z: 2,
    opened: 0,
  },
  {
    id: "files",
    title: "文件管理",
    open: false,
    minimized: false,
    maximized: false,
    x: 230,
    y: 70,
    width: 900,
    height: 620,
    z: 3,
    opened: 0,
  },
  {
    id: "transfers",
    title: "传输中心",
    open: false,
    minimized: false,
    maximized: false,
    x: 270,
    y: 84,
    width: 900,
    height: 600,
    z: 2,
    opened: 0,
  },
  {
    id: "photos",
    title: "相册",
    open: false,
    minimized: false,
    maximized: false,
    x: 250,
    y: 64,
    width: 960,
    height: 660,
    z: 2,
    opened: 0,
  },
  {
    id: "media",
    title: "影视中心",
    open: false,
    minimized: false,
    maximized: false,
    x: 210,
    y: 46,
    width: 1120,
    height: 720,
    z: 2,
    opened: 0,
  },
  {
    id: "music",
    title: "音乐",
    open: false,
    minimized: false,
    maximized: false,
    x: 240,
    y: 56,
    width: 1080,
    height: 700,
    z: 2,
    opened: 0,
  },
  {
    id: "trash",
    title: "回收站",
    open: false,
    minimized: false,
    maximized: false,
    x: 260,
    y: 76,
    width: 920,
    height: 620,
    z: 2,
    opened: 0,
  },
  {
    id: "backup",
    title: "备份中心",
    open: false,
    minimized: false,
    maximized: false,
    x: 220,
    y: 52,
    width: 1080,
    height: 700,
    z: 2,
    opened: 0,
  },
  {
    id: "settings",
    title: "设置",
    open: false,
    minimized: false,
    maximized: false,
    x: 300,
    y: 76,
    width: 920,
    height: 620,
    z: 1,
    opened: 0,
  },
  {
    id: "docker",
    title: "Docker",
    open: false,
    minimized: false,
    maximized: false,
    x: 300,
    y: 70,
    width: 960,
    height: 660,
    z: 0,
    opened: 0,
  },
  {
    id: "store",
    title: "应用中心",
    open: false,
    minimized: false,
    maximized: false,
    x: 300,
    y: 60,
    width: 940,
    height: 660,
    z: 0,
    opened: 0,
  },
  {
    id: "assistant",
    title: "VNAS",
    open: false,
    minimized: false,
    maximized: false,
    x: 260,
    y: 54,
    width: 1040,
    height: 700,
    z: 0,
    opened: 0,
  },
];

export const windowIcons: Record<WindowID, AppGlyph> = {
  shares: Share2,
  files: FolderClosed,
  transfers: ArrowDownUp,
  photos: PhotosGlyph,
  media: MediaCenterGlyph,
  music: Music2,
  trash: Trash2,
  backup: BackupCenterGlyph,
  settings: Settings,
  docker: DockerGlyph,
  store: ShoppingBag,
  assistant: AssistantGlyph,
};
// Apps on a white tile (相册, VNAS) keep it in their window's title bar too.
export const lightTitleIcons: ReadonlySet<WindowID> = new Set(["photos", "assistant"]);
// Dock icons reuse the desktop shortcut gradients so an app looks the same in both places.
export const windowTones: Record<WindowID, string> = {
  shares: "share",
  files: "files",
  transfers: "download",
  photos: "photos",
  media: "video",
  music: "music",
  trash: "trash",
  backup: "backup",
  settings: "settings",
  docker: "docker",
  store: "store",
  assistant: "ai",
};

export function windowReducer(windows: WindowModel[], action: WindowAction): WindowModel[] {
  if (action.type === "show-desktop")
    return windows.map((window) => (window.open ? { ...window, minimized: true } : window));
  if (action.type === "restore") {
    // Bring the windows back above the rest, keeping their stacking order.
    const base = Math.max(...windows.map((window) => window.z)) + 1;
    const order = windows
      .filter((window) => window.open && action.ids.includes(window.id))
      .sort((a, b) => a.z - b.z)
      .map((window) => window.id);
    return windows.map((window) =>
      order.includes(window.id) ? { ...window, minimized: false, z: base + order.indexOf(window.id) } : window,
    );
  }
  const top = Math.max(...windows.map((window) => window.z)) + 1;
  const nextOpened = Math.max(...windows.map((window) => window.opened)) + 1;
  return windows.map((window) => {
    if (window.id !== action.id) return window;
    switch (action.type) {
      case "open":
        return { ...window, open: true, minimized: false, z: top, opened: window.open ? window.opened : nextOpened };
      case "close":
        return { ...window, open: false, minimized: false };
      case "minimize":
        return { ...window, minimized: true };
      case "focus":
        return window.z === top - 1 ? window : { ...window, z: top };
      case "maximize":
        return { ...window, maximized: !window.maximized, z: top };
      case "move":
        return { ...window, x: action.x, y: action.y };
      case "resize":
        return { ...window, width: action.width, height: action.height };
    }
  });
}
