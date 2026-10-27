import { useEffect, useState } from "react";

import { appURL } from "../appcenter/model";
import { AppShortcut, readShortcuts, shortcutIconURL } from "../appcenter/api";
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
// and onStopped (Docker, for administrators) shows why.
export function shortcutApp(shortcut: AppShortcut, onStopped?: () => void): DesktopApp {
  const stopped = shortcut.running === 0;
  return {
    id: `app:${shortcut.id}`,
    label: shortcut.title,
    ariaLabel: stopped
      ? `${shortcut.title}，没有在运行${onStopped ? "，在 Docker 中查看" : ""}`
      : `打开 ${shortcut.title}（新窗口）`,
    keywords: `应用 第三方 ${shortcut.id} ${shortcut.webPort}`,
    tone: "app",
    icon: <ShortcutIcon shortcut={shortcut} />,
    unavailable: stopped,
    note: stopped ? "未运行" : undefined,
    onClick: stopped
      ? onStopped
      : () => {
          const url = appURL(shortcut);
          if (url) window.open(url, "_blank", "noopener,noreferrer");
        },
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
