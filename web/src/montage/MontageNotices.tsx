// 剪辑中心 tells a member on the desktop how their films go
// (docs/specs/montage-center.md, 桌面提示): a job waiting for VNAS to plan it
// and why, a round of VNAS planning it, a job needing them, or a film just
// saved into the photo library. The desktop only shows these; planning runs
// on the device whether it is open or not. Each notice shows once on this
// device until the job moves on, or the reason it waits changes.
import { Clapperboard } from "lucide-react";
import { useEffect, useState } from "react";

import { DeskNotice } from "../ui/DeskNotice";
import { listNotices, type MontageNotice } from "./api";
import styles from "./notices.module.css";

const POLL_MS = 60_000;
const SEEN = "montage-notices-seen";

const key = (notice: MontageNotice) => `${notice.jobId}:${notice.kind}:${notice.detail ?? ""}`;

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

export function MontageNotices({ onOpen }: { onOpen: () => void }) {
  const [notices, setNotices] = useState<MontageNotice[]>([]);

  // Asked once a minute while the page is visible, and when it becomes
  // visible again; a hidden page asks nothing.
  useEffect(() => {
    const leaving = new AbortController();
    const load = () => {
      if (document.visibilityState !== "visible") return;
      listNotices(leaving.signal).then(
        (items) => {
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
      leaving.abort();
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", load);
    };
  }, []);

  const dismiss = () => {
    remember(notices.map(key));
    setNotices([]);
  };

  return (
    <DeskNotice
      className={styles.mnNotice}
      label="剪辑中心提醒"
      source="剪辑中心"
      icon={<Clapperboard />}
      lines={summarize(notices)}
      onOpen={onOpen}
      onDismiss={dismiss}
    />
  );
}

// summarize says each notice in a line, the projects named; a job that still
// waits for VNAS says why, when the montage center gave a reason.
export function summarize(notices: MontageNotice[]): string[] {
  return notices.map((notice) => {
    const project = `「${notice.project}」`;
    switch (notice.kind) {
      case "waiting_for_planning":
        return `${project}在等 VNAS 规划${notice.detail ? `：${notice.detail}` : ""}`;
      case "planning":
        return `${project}正在由 VNAS 规划`;
      case "needs_input":
        return `${project}需要你处理一下`;
      case "saved":
        return `${project}的成片已存进相册`;
      default:
        return project;
    }
  });
}
