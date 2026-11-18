// 剪辑中心 tells a member on the desktop how their films go
// (docs/specs/montage-center.md, 桌面提示): a job waiting for VNAS to plan it,
// planning, needing them, or a film just saved into the photo library. The
// desktop only shows these; planning runs on the device whether it is open or
// not. Each notice shows once on this device until the job moves on.
import { ChevronRight, Clapperboard, X } from "lucide-react";
import { useEffect, useState } from "react";

import { listNotices, type MontageNotice } from "./api";
import styles from "./notices.module.css";

const HIDE_MS = 15_000;
const POLL_MS = 60_000;
const SEEN = "montage-notices-seen";

const key = (notice: MontageNotice) => `${notice.jobId}:${notice.kind}`;

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
  const [held, setHeld] = useState(false);

  useEffect(() => {
    let live = true;
    const load = () =>
      listNotices().then(
        (items) => {
          if (!live) return;
          const told = seen();
          setNotices(items.filter((notice) => !told.has(key(notice))));
        },
        () => undefined,
      );
    void load();
    const timer = window.setInterval(() => void load(), POLL_MS);
    return () => {
      live = false;
      window.clearInterval(timer);
    };
  }, []);

  const dismiss = () => {
    remember(notices.map(key));
    setNotices([]);
  };

  useEffect(() => {
    if (!notices.length || held) return;
    const timer = window.setTimeout(dismiss, HIDE_MS);
    return () => window.clearTimeout(timer);
  });

  if (!notices.length) return null;
  return (
    <div
      className={styles.mnHolder}
      data-vnas-overlay=""
      onPointerEnter={() => setHeld(true)}
      onPointerLeave={() => setHeld(false)}
      onFocus={() => setHeld(true)}
      onBlur={() => setHeld(false)}
    >
      <section className={styles.mnToast} role="status" aria-label="剪辑中心提醒">
        <span className={styles.mnMark} aria-hidden="true">
          <Clapperboard />
        </span>
        <div className={styles.mnCopy}>
          <small>剪辑中心</small>
          {summarize(notices).map((line) => (
            <strong key={line}>{line}</strong>
          ))}
          <button
            type="button"
            className={styles.mnOpen}
            onClick={() => {
              dismiss();
              onOpen();
            }}
          >
            去看看
            <ChevronRight />
          </button>
        </div>
        <button type="button" className={styles.mnClose} aria-label="关闭" onClick={dismiss}>
          <X />
        </button>
      </section>
    </div>
  );
}

// summarize says each notice in a line, the projects named.
export function summarize(notices: MontageNotice[]): string[] {
  return notices.map((notice) => {
    const project = `「${notice.project}」`;
    switch (notice.kind) {
      case "waiting_for_planning":
        return `${project}在等 VNAS 规划`;
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
