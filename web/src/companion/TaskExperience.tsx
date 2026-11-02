import { useEffect, useState } from "react";

import { assistantStatus, failureText } from "../assistant/api";
import styles from "./companion.module.css";
import { MinutesPicker, type MinutesSource } from "./MinutesPicker";
import { SavedFile } from "./SavedFile";
import { minutesPrompt, saveMinutesPrompt, type TaskPhase } from "./taskState";
import { useCompanionTask } from "./useCompanionTask";
import type { CompanionView } from "./useCompanionView";

type Props = {
  view: CompanionView;
  userId: string;
  compact?: boolean;
  onOpenHome?(): void;
  onPhase(phase: TaskPhase): void;
  onOpenConversation?(id: string): void;
};
// Do not open a gateway in the offline playground or for a member without VNAS.
export function TaskExperience(props: Props) {
  const [available, setAvailable] = useState(false);
  const [message, setMessage] = useState("正在查看 VNAS…");
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    let active = true;
    assistantStatus()
      .then((status) => {
        if (active) {
          setAvailable(status.available);
          setMessage(status.available ? "" : "你的账号还没有 VNAS，请先在 VNAS 中完成配置。");
        }
      })
      .catch((error) => {
        if (active) setMessage(failureText(error));
      });
    return () => {
      active = false;
    };
  }, [attempt]);
  if (!available)
    return (
      <div className={props.compact ? styles.taskCompact : styles.task} data-pet-interactive="true">
        <p role="status">{message}</p>
        <button type="button" onClick={() => setAttempt((n) => n + 1)}>
          重新查看
        </button>
      </div>
    );
  return <ConnectedTask {...props} />;
}
export function ConnectedTask({ view, userId, compact, onPhase, onOpenConversation, onOpenHome }: Props) {
  const task = useCompanionTask(view);
  const [source, setSource] = useState<MinutesSource>();
  const [request, setRequest] = useState("");
  const [error, setError] = useState("");
  useEffect(() => {
    onPhase(task.phase);
    return () => onPhase("idle");
  }, [onPhase, task.phase]);
  const open = () => {
    if (task.storedId) onOpenConversation?.(task.storedId);
  };
  return (
    <section className={compact ? styles.taskCompact : styles.task} aria-label="奶蛙的任务" data-pet-interactive="true">
      <p role="status" data-task-phase={task.phase}>
        {task.message}
      </p>
      {compact ? (
        <button
          type="button"
          onClick={() => (task.storedId ? open() : onOpenHome ? onOpenHome() : window.companionDesktop?.home())}
        >
          {task.storedId ? "打开这次任务" : "交代事情"}
        </button>
      ) : (
        <>
          <h2>一起把会议记录理清楚</h2>
          <p className={styles.note}>选文件，整理成摘要和待办。保存前会请你确认。</p>
          <MinutesPicker userId={userId} disabled={!task.canSend} onSelect={setSource} />
          {source && <p>已选：{source.name}</p>}
          <form
            onSubmit={(e) => {
              e.preventDefault();
              if (!source || !task.canSend) return;
              void task.thread.send(minutesPrompt(source.path, request));
            }}
          >
            <label>
              这次有什么特别要关注的？
              <textarea
                value={request}
                maxLength={1000}
                onChange={(e) => setRequest(e.target.value)}
                placeholder="例如：重点列出负责人和下周要交付的事情"
              />
            </label>
            <button type="submit" disabled={!source || !task.canSend}>
              整理这份记录
            </button>
          </form>
          {!view.remote.here && <p className={styles.note}>先把伙伴叫到这里，再交代新事情。已有任务仍由 VNAS 处理。</p>}
          {task.answer && (
            <div className={styles.result}>
              <h3>这次的回答</h3>
              <pre>{task.answer}</pre>
            </div>
          )}
          {task.phase === "success" && (
            <button type="button" disabled={!task.canSend} onClick={() => void task.thread.send(saveMinutesPrompt)}>
              保存到个人空间
            </button>
          )}
          {task.paths.length > 0 && (
            <div>
              <h3>已确认保存</h3>
              <ul>
                {task.paths.map((path) => (
                  <SavedFile key={path} path={path} userId={userId} />
                ))}
              </ul>
              <p className={styles.note}>
                这里记录本对话已确认保存的文件，也可能包含之前的任务。完整记录可在 VNAS 中查看。
              </p>
            </div>
          )}
          <div className={styles.actions}>
            {task.storedId && (
              <button type="button" onClick={open}>
                {task.waiting ? "去 VNAS 处理确认卡" : "打开完整对话"}
              </button>
            )}
            {task.storedId && (
              <button
                type="button"
                disabled={!task.canSend}
                onClick={() => {
                  void task
                    .newTask()
                    .then(() => {
                      setSource(undefined);
                      setError("");
                    })
                    .catch((e) => setError(failureText(e)));
                }}
              >
                另开一件事
              </button>
            )}
          </div>
        </>
      )}
      {task.thread.running && (
        <button type="button" onClick={task.thread.cancel}>
          停止这次回答
        </button>
      )}
      {error && <p role="alert">{error}</p>}
    </section>
  );
}
