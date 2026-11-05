import type { MutableRefObject } from "react";
import { useEffect, useState } from "react";

import { assistantStatus, failureText } from "../assistant/api";
import { TasksView } from "../assistant/TasksView";
import styles from "./companion.module.css";
import type { AgentBridge, AgentSnapshot } from "./conversation";
import { MinutesPicker, type MinutesSource } from "./MinutesPicker";
import { SavedFile } from "./SavedFile";
import { TaskInbox, type TaskSignal } from "./TaskInbox";
import { minutesPrompt, personalTaskPrompt, saveMinutesPrompt, type TaskPhase } from "./taskState";
import { useCompanionTask } from "./useCompanionTask";
import type { CompanionView } from "./useCompanionView";

type Props = {
  view: CompanionView;
  userId: string;
  compact?: boolean;
  bridge?: MutableRefObject<AgentBridge | undefined>;
  onChatState?(state: AgentSnapshot): void;
  chatActive?: boolean;
  onOpenHome?(): void;
  onPhase(phase: TaskPhase): void;
  onSpeech?(signal: TaskSignal | undefined): void;
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
  return <ConnectedTask key={props.userId} {...props} />;
}
export function ConnectedTask({
  view,
  userId,
  compact,
  onPhase,
  onSpeech,
  onOpenConversation,
  onOpenHome,
  bridge: bridgeRef,
  onChatState,
  chatActive = true,
}: Props) {
  const task = useCompanionTask(view);
  const [source, setSource] = useState<MinutesSource>();
  const [request, setRequest] = useState("");
  const [error, setError] = useState("");
  const [instruction, setInstruction] = useState("");
  const [minutes, setMinutes] = useState(false);
  const [minutesResult, setMinutesResult] = useState(false);
  const [draftNotice, setDraftNotice] = useState("");
  const [signal, setSignal] = useState<TaskSignal>();
  const noticeEnabled = view.remote.connection === "online" && (!compact || view.remote.here);
  const announce =
    view.remote.here && !view.quiet && !task.thread.running && !task.waiting && task.phase !== "connecting";
  const presentation = announce ? signal : undefined;
  useEffect(() => {
    onPhase(presentation?.phase ?? task.phase);
    return () => onPhase("idle");
  }, [onPhase, task.phase, presentation]);
  useEffect(() => {
    onSpeech?.(presentation);
    return () => onSpeech?.(undefined);
  }, [onSpeech, presentation]);
  const open = () => {
    if (task.storedId) onOpenConversation?.(task.storedId);
  };
  useEffect(() => {
    if (!bridgeRef) return;
    const handler: AgentBridge = {
      send: (text) => {
        if (!task.canSend)
          return {
            accepted: false,
            message: !view.remote.here
              ? "大肥鱼现在不在这一端。先点“叫到这里”，再交代 NAS 工作；本机计时和待办不受影响。"
              : task.waiting
                ? "先处理 NAS 中的确认卡，再交代下一件事。"
                : "NAS 正在连接或处理上一件事，稍后再试。",
          };
        setMinutesResult(false);
        void task.thread.send(personalTaskPrompt(text));
        return { accepted: true };
      },
      open,
    };
    bridgeRef.current = handler;
    return () => {
      if (bridgeRef.current === handler) bridgeRef.current = undefined;
    };
  });
  const latestReply = task.thread.messages.at(-1);
  const chatAnswer =
    latestReply?.role === "assistant" && !latestReply.interim
      ? latestReply.parts
          .filter((part) => part.type === "text")
          .map((part) => part.text)
          .join("\n")
      : "";
  useEffect(() => {
    if (chatActive)
      onChatState?.({ phase: task.phase, answer: chatAnswer, running: task.thread.running, message: task.message });
  }, [chatActive, onChatState, task.phase, task.thread.running, task.message, chatAnswer]);
  return (
    <section
      className={compact ? styles.taskCompact : styles.task}
      aria-label="大肥鱼的任务"
      data-pet-interactive="true"
    >
      <p role="status" data-task-phase={task.phase}>
        {task.message}
      </p>
      <TaskInbox
        userId={userId}
        enabled={noticeEnabled}
        announce={announce}
        compact={compact}
        onSignal={setSignal}
        onOpen={onOpenConversation}
        onRemindAgain={
          compact
            ? undefined
            : (text) => {
                setInstruction(text);
                setMinutes(false);
                setDraftNotice("已填好交代，点“交给大肥鱼”后安排新提醒；当前结果仍未读，原任务不会重跑。");
              }
        }
      />
      {compact ? (
        <button
          type="button"
          onClick={() => (task.storedId ? open() : onOpenHome ? onOpenHome() : window.companionDesktop?.home())}
        >
          {task.storedId ? "打开这次任务" : "交代事情"}
        </button>
      ) : (
        <>
          <h2>有事交给我</h2>
          <p className={styles.note}>提醒、定时查看文件，或者现在帮你做一件事。先说清楚，做好了我来告诉你。</p>
          <form
            onSubmit={(event) => {
              event.preventDefault();
              if (!instruction.trim() || !task.canSend) return;
              setMinutesResult(false);
              setDraftNotice("");
              void task.thread.send(personalTaskPrompt(instruction.trim()));
            }}
          >
            <label>
              交代一件事
              <textarea
                value={instruction}
                maxLength={4000}
                disabled={!task.canSend}
                onChange={(event) => {
                  setInstruction(event.target.value);
                  setDraftNotice("");
                }}
                placeholder="例如：明早十点提醒我交材料"
              />
            </label>
            <div className={styles.actions}>
              <button type="submit" disabled={!instruction.trim() || !task.canSend}>
                交给大肥鱼
              </button>
              <button type="button" disabled={!task.canSend} onClick={() => setInstruction("明早十点提醒我交材料")}>
                试试提醒
              </button>
              <button
                type="button"
                disabled={!task.canSend}
                onClick={() => setInstruction("每天早上八点告诉我共享空间昨天新增了哪些文件")}
              >
                每日文件小结
              </button>
            </div>
          </form>
          {draftNotice && <p role="status">{draftNotice}</p>}
          <p className={styles.note}>
            时间按 NAS
            所在时区理解；下方显示服务端实际保存的任务，下一次运行时间按当前设备时区展示。回答结束不代表提醒已经保存。
          </p>
          <details className={styles.taskLedger}>
            <summary>我的提醒与后台任务</summary>
            <TasksView
              refreshToken={task.thread.running}
              onPlan={(text) => {
                setInstruction(text ?? "");
                setMinutes(false);
              }}
              onOpenRun={(id) => onOpenConversation?.(id)}
            />
          </details>
          <details open={minutes} onToggle={(event) => setMinutes(event.currentTarget.open)}>
            <summary>整理 NAS 里的会议记录</summary>
            <p className={styles.note}>选文件，整理成摘要和待办。保存前会请你确认。</p>
            <MinutesPicker userId={userId} disabled={!task.canSend} onSelect={setSource} />
            {source && <p>已选：{source.name}</p>}
            <form
              onSubmit={(e) => {
                e.preventDefault();
                if (!source || !task.canSend) return;
                setMinutesResult(true);
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
          </details>
          {!view.remote.here && <p className={styles.note}>先把伙伴叫到这里，再交代新事情。已有任务仍由 VNAS 处理。</p>}
          {task.answer && (
            <details className={styles.result}>
              <summary>查看这次回答</summary>
              <pre>{task.answer}</pre>
            </details>
          )}
          {minutesResult && task.phase === "success" && (
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
                      setInstruction("");
                      setMinutesResult(false);
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
