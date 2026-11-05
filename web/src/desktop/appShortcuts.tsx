import { useEffect, useState } from "react";

import { AppShortcut, readShortcuts, shortcutIconURL } from "../appcenter/api";
import { appURL } from "../appcenter/model";
import type { DesktopApp } from "./DesktopGrid";

// Installed apps are read again while the desktop is in view, so an app
// installed, stopped or removed elsewhere shows up without a reload.
const refreshEvery = 15_000;

export function useAppShortcuts(): AppShortcut[] {
  const [shortcuts, setShortcuts] = useState<AppShortcut[]>([]);
  useEffect(() => {
    let live = true;
    let timer = 0;
    let controller: AbortController | undefined;
    const load = async () => {
      window.clearTimeout(timer);
      controller?.abort();
      controller = new AbortController();
      try {
        const items = await readShortcuts(controller.signal);
        if (live) setShortcuts(items);
      } catch {
        // Keep the last list: the container agent may be restarting.
      }
      if (!live || document.visibilityState !== "visible") return;
      window.clearTimeout(timer);
      timer = window.setTimeout(() => void load(), refreshEvery);
    };
    const visible = () => {
      if (document.visibilityState === "visible") void load();
    };
    void load();
    document.addEventListener("visibilitychange", visible);
    return () => {
      live = false;
      window.clearTimeout(timer);
      controller?.abort();
      document.removeEventListener("visibilitychange", visible);
    };
  }, []);
  return shortcuts;
}

// shortcutApp is an installed app on the desktop and in the launcher. It
// opens the app's own web interface in a new window; a stopped app says so,
// and onStopped (Docker, for administrators) shows why. Through the internet
// entry the app's port cannot be reached, so the app says it opens only on
// the LAN (ADR 0032).
export function shortcutApp(shortcut: AppShortcut, onStopped?: () => void): DesktopApp {
  const stopped = shortcut.running === 0;
  const url = appURL(shortcut);
  const lanOnly = !stopped && !url;
  return {
    id: `app:${shortcut.id}`,
    label: shortcut.title,
    ariaLabel: stopped
      ? `${shortcut.title}，没有在运行${onStopped ? "，在 Docker 中查看" : ""}`
      : lanOnly
        ? `${shortcut.title}，只能在局域网中打开`
        : `打开 ${shortcut.title}（新窗口）`,
    keywords: `应用 第三方 ${shortcut.id} ${shortcut.webPort}`,
    tone: "app",
    icon: <ShortcutIcon shortcut={shortcut} />,
    unavailable: stopped || lanOnly,
    note: stopped ? "未运行" : lanOnly ? "仅局域网" : undefined,
    onClick: stopped ? onStopped : url ? () => window.open(url, "_blank", "noopener,noreferrer") : undefined,
  };
}

function ShortcutIcon({ shortcut }: { shortcut: AppShortcut }) {
  const [failed, setFailed] = useState(false);
  return failed ? (
    <b>{shortcut.title.slice(0, 1).toUpperCase()}</b>
  ) : (
    <img src={shortcutIconURL(shortcut.id)} alt="" draggable={false} onError={() => setFailed(true)} />
  );
}
