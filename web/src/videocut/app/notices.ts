// The desktop's notices of 剪辑中心 (docs/specs/videocut-center.md, 桌面提示),
// for ui/VideoCutNotices. Each notice shows once on this device until the job
// moves on, or the reason it waits changes.
import { useEffect, useState } from "react";

import { listNotices, type VideoCutNotice } from "../api/client";

export type { VideoCutNotice };

const POLL_MS = 60_000;
const SEEN = "videocut-notices-seen";

const key = (notice: VideoCutNotice) => `${notice.jobId}:${notice.kind}:${notice.detail ?? ""}`;

function seen(): Set<string> {
  try {
    return new Set(JSON.parse(localStorage.getItem(SEEN) ?? "[]") as string[]);
  } catch {
    return new Set();
  }
}

function remember(keys: string[]) {
  try {
    const all = [...seen(), ...keys].slice(-200);
    localStorage.setItem(SEEN, JSON.stringify(all));
  } catch {
    // Without storage a notice may show again on the next visit.
  }
}

// useNotices is the notices not told on this device yet, and dismiss, which
// marks them told.
export function useNotices(): { notices: VideoCutNotice[]; dismiss: () => void } {
  const [notices, setNotices] = useState<VideoCutNotice[]>([]);

  // Asked once a minute while the page is visible, and when it becomes
  // visible again; a hidden page asks nothing. Only the latest request is
  // listened to: one still on its way when the next is sent is given up, so
  // that an older answer arriving late does not put back what the newer one
  // replaced (waiting for planning, after the film was saved).
  useEffect(() => {
    let latest: AbortController | undefined;
    const load = () => {
      if (document.visibilityState !== "visible") return;
      latest?.abort();
      const asking = new AbortController();
      latest = asking;
      listNotices(asking.signal).then(
        (items) => {
          if (asking !== latest) return;
          const told = seen();
          setNotices(items.filter((notice) => !told.has(key(notice))));
        },
        () => undefined,
      );
    };
    load();
    const timer = window.setInterval(load, POLL_MS);
    document.addEventListener("visibilitychange", load);
    return () => {
      latest?.abort();
      latest = undefined;
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", load);
    };
  }, []);

  const dismiss = () => {
    remember(notices.map(key));
    setNotices([]);
  };
  return { notices, dismiss };
}
