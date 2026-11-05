import { type MutableRefObject, useEffect, useRef, useState } from "react";

import { acknowledgeTaskNotice, useTaskNotices } from "../assistant/taskNotices";
import type { AgentBridge, useConversation } from "./conversation";
import styles from "./conversation.module.css";
import { focusDue, type PersonalDeskState } from "./deskStore";

export function CompanionChat({
  desk,
  conversation,
  bridge,
  owner,
  connected = false,
  onTool,
  onOpenConversation,
  compact = false,
  focusInput = false,
}: {
  desk: PersonalDeskState;
  conversation: ReturnType<typeof useConversation>;
  bridge: MutableRefObject<AgentBridge | undefined>;
  owner: string;
  connected?: boolean;
  compact?: boolean;
  focusInput?: boolean;
  onTool(view: "focus" | "todos"): void;
  onOpenConversation?(id: string): void;
}) {
  const [input, setInput] = useState("");
  const [noticeError, setNoticeError] = useState("");
  const notices = useTaskNotices(owner);
  const log = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLTextAreaElement>(null);
  useEffect(() => {
    if (focusInput) inputRef.current?.focus();
  }, [focusInput]);
  const { chat, state } = conversation;
  const newest = state.lines.at(-1)?.id;
  const focusAlert = focusDue(desk.value);
  useEffect(() => {
    if (log.current) log.current.scrollTop = log.current.scrollHeight;
  }, [state.lines]);
  const send = (text: string) => {
    if (!text.trim()) return;
    chat.send(text, desk, bridge.current);
    setInput("");
  };
  return (
    <section
      className={`${styles.chat} ${compact ? styles.compactChat : ""}`}
      aria-label="和大肥鱼聊天"
      data-pet-interactive="true"
    >
      <div
        ref={log}
        className={styles.log}
        role="log"
        aria-label="和大肥鱼的对话"
        aria-live="polite"
        aria-relevant="additions"
      >
        {!state.lines.length && (
          <div className={styles.welcome}>
            <strong>有什么小事，跟我说就好。</strong>
            <p>“专注25分钟”，或者“帮我记一件事”。</p>
            <span>计时和待办我可以直接处理。</span>
          </div>
        )}
        {state.lines.map((line) => (
          <div key={line.id} className={line.role === "user" ? styles.userLine : styles.assistantLine}>
            <small>{line.role === "user" ? "你" : "大肥鱼"}</small>
            <p>{line.text}</p>
            {line.id === newest && line.role === "assistant" && line.kind === "focus" && (
              <button type="button" onClick={() => onTool("focus")}>
                查看计时
              </button>
            )}
            {line.id === newest && line.role === "assistant" && line.kind === "todo" && (
              <button type="button" onClick={() => onTool("todos")}>
                查看待办
              </button>
            )}
            {line.id === newest && line.role === "assistant" && line.kind === "agent" && !state.pending && (
              <button type="button" onClick={() => bridge.current?.open()}>
                打开完整 NAS 对话
              </button>
            )}
          </div>
        ))}
      </div>
      {focusAlert && (
        <div className={styles.attention} role="status">
          <span>{desk.value.focus.mode === "break" ? "休息到点了" : "这一轮专注完成了"}</span>
          {desk.value.focus.mode === "focus" && (
            <button type="button" onClick={() => desk.store.start(5, "break")}>
              休息5分钟
            </button>
          )}
          <button type="button" onClick={desk.store.acknowledgeFocus}>
            知道了
          </button>
        </div>
      )}
      {connected &&
        notices.slice(-1).map((notice) => (
          <div key={notice.id} className={styles.notice}>
            <span>NAS 有新结果：{notice.task?.name}</span>
            {notice.task?.sessionId && onOpenConversation && (
              <button type="button" onClick={() => onOpenConversation(notice.task!.sessionId!)}>
                查看结果
              </button>
            )}
            <button
              type="button"
              onClick={async () => {
                setNoticeError("");
                if (!(await acknowledgeTaskNotice(notice.id))) setNoticeError("已读没有同步成功，请稍后重试。");
              }}
            >
              知道了
            </button>
          </div>
        ))}
      {noticeError && <p role="alert">{noticeError}</p>}
      <div className={styles.suggestions}>
        <button type="button" onClick={() => send("专注25分钟")}>
          开始25分钟
        </button>
        <button type="button" onClick={() => send("帮我记一件事")}>
          记一件事
        </button>
        {state.awaiting && (
          <button type="button" onClick={() => send("取消")}>
            取消这次补充
          </button>
        )}
      </div>
      <form
        className={styles.composer}
        onSubmit={(event) => {
          event.preventDefault();
          send(input);
        }}
      >
        <textarea
          ref={inputRef}
          aria-label="跟大肥鱼说句话"
          placeholder={state.awaiting === "todo" ? "要记下什么事？" : state.awaiting ? "例如25分钟" : "跟大肥鱼说句话…"}
          rows={1}
          maxLength={1000}
          value={input}
          onChange={(event) => setInput(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) {
              event.preventDefault();
              send(input);
            }
          }}
        />
        <button type="submit" aria-label="发送给大肥鱼" disabled={!input.trim()}>
          发送
        </button>
      </form>
      {!compact && (
        <p className={styles.privacy}>
          计时与待办在此设备处理；其他对话使用已配置的 NAS AI。页面关闭后不会弹出计时提醒。
        </p>
      )}
    </section>
  );
}
