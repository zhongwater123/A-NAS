// A run of one of the member's VNAS scheduled tasks finished: a toast on the
// desktop, whatever window is open, that opens the run in VNAS. It hides itself
// after a while, unless pointed at; the result stays unread in VNAS's
// 定时任务 until the member reads it. The notices wait on the server, so a run
// while the member was away is told when they come back.
import { CalendarClock, ChevronRight, X } from "lucide-react";
import { useEffect, useState } from "react";

import type { AssistantTaskNotice } from "../api";
import { loadTaskNotices, readTaskNotice, useTaskNotices } from "./taskNotices";
import { whenText } from "./taskTime";
import "./taskToasts.css";

// The product service checks the member's tasks about once a minute.
const POLL_MS = 60_000;
const SHOWN = 3;
const HIDE_MS = 12_000;

export function TaskToasts({ enabled, onOpen }: { enabled: boolean; onOpen(notice: AssistantTaskNotice): void }) {
  const notices = useTaskNotices();
  // Toasts that hid themselves stay hidden on this page; the notices stay unread.
  const [hidden, setHidden] = useState<ReadonlySet<string>>(new Set());
  const [held, setHeld] = useState(false);

  useEffect(() => {
    if (!enabled) return;
    void loadTaskNotices();
    const timer = window.setInterval(() => void loadTaskNotices(), POLL_MS);
    const back = () => {
      if (document.visibilityState === "visible") void loadTaskNotices();
    };
    document.addEventListener("visibilitychange", back);
    return () => {
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", back);
    };
  }, [enabled]);

  const showing = enabled ? notices.filter((item) => !hidden.has(item.id)) : [];
  const key = showing.map((item) => item.id).join(" ");
  useEffect(() => {
    if (!key || held) return;
    const timer = window.setTimeout(() => setHidden((current) => new Set([...current, ...key.split(" ")])), HIDE_MS);
    return () => window.clearTimeout(timer);
  }, [key, held]);

  if (!showing.length) return null;
  // The newest on top.
  const shown = showing.slice(-SHOWN).reverse();
  return (
    <div
      className="vt-toasts"
      aria-live="polite"
      onPointerEnter={() => setHeld(true)}
      onPointerLeave={() => setHeld(false)}
      onFocus={() => setHeld(true)}
      onBlur={() => setHeld(false)}
    >
      {shown.map((item) => {
        const run = item.task as AssistantTaskNotice;
        const failed = run.status !== "ok";
        return (
          <section
            key={item.id}
            className={`vt-toast${failed ? " failed" : ""}`}
            role="status"
            aria-label={`定时任务 ${run.name}`}
          >
            <span className="vt-toast-mark">
              <CalendarClock />
            </span>
            <div className="vt-toast-copy">
              <small>VNAS 定时任务 · {whenText(Date.parse(run.ranAt) / 1000)}</small>
              <strong>
                {run.name}
                {failed && <em>没有完成</em>}
              </strong>
              {run.summary && <p>{plainText(run.summary)}</p>}
              <button
                type="button"
                className="vt-toast-open"
                onClick={() => {
                  readTaskNotice(item.id);
                  onOpen(run);
                }}
              >
                查看结果
                <ChevronRight />
              </button>
            </div>
            <button type="button" className="vt-toast-close" aria-label="关闭" onClick={() => readTaskNotice(item.id)}>
              <X />
            </button>
          </section>
        );
      })}
      {showing.length > SHOWN && (
        <p className="vt-toast-more">还有 {showing.length - SHOWN} 个任务的结果，在 VNAS 的定时任务里查看</p>
      )}
    </div>
  );
}

// The start of an answer is Markdown; a toast shows it as plain words.
export function plainText(markdown: string): string {
  return markdown
    .replace(/```[^`]*```/g, " ")
    .replace(/!?\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/(^|\s)#{1,6}\s+/g, "$1")
    .replace(/(\*\*|__|`)/g, "")
    .replace(/(^|\s)[-*>]\s+/g, "$1")
    .replace(/\s+/g, " ")
    .trim();
}
