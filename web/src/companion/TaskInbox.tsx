import { useEffect, useRef, useState } from "react";

import { plainText } from "../assistant/TaskToasts";
import { acknowledgeTaskNotice, loadTaskNotices, useTaskNotices } from "../assistant/taskNotices";
import { whenText } from "../assistant/taskTime";
import styles from "./companion.module.css";
import { remindAgainRequest, type TaskPhase } from "./taskState";

export type TaskSignal = { id: string; message: string; phase: TaskPhase };

type Props = {
  userId: string;
  enabled: boolean;
  announce: boolean;
  compact?: boolean;
  onSignal(signal: TaskSignal | undefined): void;
  onOpen?(id: string): void;
  onRemindAgain?(request: string): void;
};

export function TaskInbox(props: Props) {
  return <MemberTaskInbox key={props.userId} {...props} />;
}

function MemberTaskInbox({ userId, enabled, announce, compact, onSignal, onOpen, onRemindAgain }: Props) {
  const notices = useTaskNotices(userId);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState("");
  const [reading, setReading] = useState<string>();
  const seen = useRef<Set<string> | undefined>(undefined);
  const [signal, setSignal] = useState<TaskSignal>();
  useEffect(() => {
    if (!enabled) return;
    let active = true;
    const load = async () => {
      if (document.hidden) return;
      await loadTaskNotices();
      if (active) setLoaded(true);
    };
    void load();
    const timer = window.setInterval(() => void load(), 60_000);
    const back = () => {
      if (!document.hidden) void load();
    };
    document.addEventListener("visibilitychange", back);
    return () => {
      active = false;
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", back);
    };
  }, [enabled, userId]);
  useEffect(() => {
    if (!loaded) return;
    // Existing unread results remain visible after reopening, without replaying
    // celebrations. Only a new server notice can start a new reaction.
    if (!seen.current) {
      seen.current = new Set(notices.map((item) => item.id));
      return;
    }
    if (!enabled || !announce || document.hidden) return;
    const latest = notices.findLast((item) => !seen.current?.has(item.id));
    for (const item of notices) seen.current.add(item.id);
    if (seen.current.size > 256) seen.current = new Set([...seen.current].slice(-256));
    if (!latest?.task) return;
    const run = latest.task;
    setSignal({
      id: latest.id,
      phase: run.status === "ok" ? "success" : "error",
      message: run.status === "ok" ? `有新结果啦 · ${run.name}` : `这项任务没完成 · ${run.name}`,
    });
  }, [loaded, notices, enabled, announce]);
  useEffect(() => {
    if (!signal) return;
    const timer = window.setTimeout(() => setSignal(undefined), 6500);
    return () => window.clearTimeout(timer);
  }, [signal]);
  const visibleSignal = enabled && announce && notices.some((item) => item.id === signal?.id) ? signal : undefined;
  useEffect(() => {
    onSignal(visibleSignal);
    return () => onSignal(undefined);
  }, [visibleSignal, onSignal]);
  if (!enabled || !loaded || !notices.length) return null;
  return (
    <div className={styles.inbox} aria-label="奶蛙带来的任务结果" aria-live="polite">
      {(compact ? notices.slice(-1) : notices.slice(-3)).reverse().map((item) => {
        const run = item.task!;
        return (
          <section key={item.id} aria-label={`任务结果 ${run.name}`}>
            <strong>
              {run.status === "ok" ? "有新结果" : "任务没有完成"} · {run.name}
            </strong>
            <small>{whenText(Date.parse(run.ranAt) / 1000)}</small>
            {!compact && run.summary && <p>{plainText(run.summary)}</p>}
            <div className={styles.actions}>
              {run.sessionId && onOpen && (
                <button type="button" onClick={() => onOpen(run.sessionId!)}>
                  查看结果
                </button>
              )}
              {onRemindAgain && (
                <button type="button" onClick={() => onRemindAgain(remindAgainRequest(run.name))}>
                  十分钟后再提醒
                </button>
              )}
              <button
                type="button"
                disabled={reading === item.id}
                onClick={async () => {
                  setReading(item.id);
                  setError("");
                  if (!(await acknowledgeTaskNotice(item.id))) setError("没能同步已读，结果仍会保留，请稍后再试。");
                  setReading(undefined);
                }}
              >
                知道了
              </button>
            </div>
          </section>
        );
      })}
      {notices.length > (compact ? 1 : 3) && (
        <p className={styles.note}>还有 {notices.length - (compact ? 1 : 3)} 项未读结果，可在 VNAS 定时任务中查看。</p>
      )}
      {error && <p role="alert">{error}</p>}
    </div>
  );
}
