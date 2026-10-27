import { ArrowDown, LockKeyhole, Send, X } from "lucide-react";
import { Fragment, useEffect, useRef, useState } from "react";

import { usePeople } from "../people/people";
import { listMessages, type Message, markRead, requestKey, type Share, sendMessage } from "./api";
import { EmojiInput } from "./EmojiInput";
import { clock, dayLabel, errorText, isActive, receipt } from "./format";
import { Avatar, nameOf } from "./parts";
import { useShares } from "./SharesProvider";
import styles from "./shares.module.css";

// useThread keeps one private conversation of a share current. It runs while
// the conversation is collapsed too, so the header can show the newest line.
export function useThread(shareId: string, recipientId: string) {
  // Messages carry the conversation they came from, so a switch to another
  // recipient never shows, or marks read, the previous one for a render.
  const key = `${shareId}:${recipientId}`;
  const [state, setState] = useState<{ key: string; messages: Message[] }>({ key: "", messages: [] });
  const [error, setError] = useState("");
  const [errorKey, setErrorKey] = useState(key);
  if (errorKey !== key) {
    setErrorKey(key);
    setError("");
  }
  const [revision, setRevision] = useState(0);
  useEffect(() => {
    if (!recipientId) return;
    let stopped = false,
      loading = false;
    const poll = async () => {
      if (loading) return;
      loading = true;
      try {
        const collected: Message[] = [];
        let after = 0;
        while (!stopped) {
          const page = (await listMessages(shareId, recipientId, after)).items ?? [];
          const fresh = page.filter((message) => message.id > after);
          collected.push(...fresh);
          if (page.length < 100 || !fresh.length) break;
          after = fresh[fresh.length - 1].id;
        }
        if (!stopped) {
          setState({ key, messages: collected });
          setError("");
        }
      } catch (error) {
        if (!stopped) setError(errorText(error));
      } finally {
        loading = false;
      }
    };
    void poll();
    const timer = setInterval(() => void poll(), 3000);
    return () => {
      stopped = true;
      clearInterval(timer);
    };
  }, [key, shareId, recipientId, revision]);
  const loaded = state.key === key;
  return { messages: loaded ? state.messages : [], error, loaded, reload: () => setRevision((value) => value + 1) };
}
export type Thread = ReturnType<typeof useThread>;

// A run is consecutive messages from one person within five minutes; it
// shares one avatar and one time.
const sameRun = (a: Message | undefined, b: Message | undefined) =>
  !!a &&
  !!b &&
  a.sender.id === b.sender.id &&
  Math.abs(new Date(b.createdAt).getTime() - new Date(a.createdAt).getTime()) < 300_000 &&
  dayLabel(a.createdAt) === dayLabel(b.createdAt);

export function Conversation({
  share,
  recipientId,
  thread,
  shown,
  visible,
  docked,
  onRecipient,
  onClose,
}: {
  share: Share;
  recipientId: string;
  thread: Thread;
  // shown: the panel is open; visible: open and not covered by a dialog.
  shown: boolean;
  visible: boolean;
  docked: boolean;
  onRecipient: (id: string) => void;
  onClose: () => void;
}) {
  const context = useShares();
  const people = usePeople();
  const [text, setText] = useState("");
  const [sendError, setSendError] = useState("");
  const [busy, setBusy] = useState(false);
  const [documentVisible, setDocumentVisible] = useState(document.visibilityState !== "hidden");
  const log = useRef<HTMLDivElement>(null);
  const [atBottom, setAtBottom] = useState(true);
  const through = useRef(0);
  const attempt = useRef<{ text: string; key: string } | undefined>(undefined);
  const [readError, setReadError] = useState("");
  const { messages } = thread;
  const sender = share.sender.id === context.userId;
  const active = context.active && documentVisible && visible;
  const open = isActive(share);
  // Each conversation has its own draft and read position.
  const [recipientBefore, setRecipientBefore] = useState(recipientId);
  if (recipientBefore !== recipientId) {
    setRecipientBefore(recipientId);
    setText("");
    setSendError("");
    setReadError("");
    setAtBottom(true);
  }
  useEffect(() => {
    through.current = 0;
    attempt.current = undefined;
  }, [recipientId]);
  useEffect(() => {
    const change = () => setDocumentVisible(document.visibilityState !== "hidden");
    document.addEventListener("visibilitychange", change);
    return () => document.removeEventListener("visibilitychange", change);
  }, []);
  const { thread: setThread, refresh } = context;
  useEffect(() => {
    setThread(active ? { id: share.id, recipientId } : undefined);
    return () => setThread(undefined);
  }, [share.id, recipientId, active, setThread]);
  useEffect(() => {
    if (!active || !atBottom || !messages.length) return;
    const last = messages[messages.length - 1].id;
    if (last <= through.current) return;
    if (log.current) log.current.scrollTop = log.current.scrollHeight;
    let stopped = false;
    // React has committed the actual thread before its read cursor advances.
    void markRead(share.id, recipientId, last)
      .then(() => {
        if (!stopped) {
          through.current = last;
          setReadError("");
          refresh();
        }
      })
      .catch((error) => {
        if (!stopped) setReadError(errorText(error));
      });
    return () => {
      stopped = true;
    };
  }, [active, atBottom, messages, share.id, recipientId, refresh]);
  const send = async () => {
    if (busy || !text.trim() || !open) return;
    setBusy(true);
    setSendError("");
    try {
      if (attempt.current?.text !== text) attempt.current = { text, key: requestKey() };
      await sendMessage(share.id, { recipientId, requestId: attempt.current.key, text });
      setAtBottom(true);
      setText("");
      attempt.current = undefined;
      thread.reload();
      context.refresh();
    } catch (error) {
      setSendError(errorText(error));
    } finally {
      setBusy(false);
    }
  };
  const contact = share.recipients.find((item) => item.userId === recipientId);
  const otherId = sender ? recipientId : share.sender.id;
  const otherName = sender ? nameOf(people, recipientId) : nameOf(people, share.sender.id, share.sender.username);
  const otherRead = sender ? contact?.readThrough : contact?.senderReadThrough;
  const lastOwn = [...messages].reverse().find((message) => message.sender.id === context.userId);
  const state = sender && contact ? receipt(contact) : undefined;
  const error = thread.error || readError || sendError;
  return (
    <aside className={`${styles.shChat}${docked ? " docked" : ` ${styles.drawer}`}`} hidden={!shown} aria-label="私聊">
      <header className={styles.shChatHead}>
        {sender && share.recipients.length > 1 && (
          <div className={styles.shChatPeople} aria-label="选择接收者">
            {share.recipients.map((item) => (
              <button
                key={item.userId}
                aria-label={nameOf(people, item.userId)}
                title={nameOf(people, item.userId)}
                aria-pressed={item.userId === recipientId}
                onClick={() => onRecipient(item.userId)}
              >
                <Avatar people={people} id={item.userId} size="md" />
                {item.lastMessage > item.senderReadThrough && item.userId !== recipientId && (
                  <span className={styles.shDot} />
                )}
              </button>
            ))}
          </div>
        )}
        <div className={styles.shChatWho}>
          {!(sender && share.recipients.length > 1) && (
            <Avatar people={people} id={otherId} username={sender ? undefined : share.sender.username} size="md" />
          )}
          <span className={styles.shChatWhoText}>
            <strong>{otherName}</strong>
            {state ? (
              <small className={`${styles[`tone-${state.tone}`] ?? ""}`}>{state.text}</small>
            ) : (
              <small>
                <LockKeyhole />
                仅你们两人可见
              </small>
            )}
          </span>
          <button className={styles.shIconBtn} aria-label="收起私聊" title="收起" onClick={onClose}>
            <X />
          </button>
        </div>
      </header>
      <div
        ref={log}
        className={styles.shThread}
        role="log"
        aria-label="分享私聊"
        onScroll={() => {
          const list = log.current;
          if (list) setAtBottom(list.scrollHeight - list.scrollTop - list.clientHeight < 48);
        }}
      >
        {messages.map((message, index) => {
          const previous = messages[index - 1],
            next = messages[index + 1];
          const own = message.sender.id === context.userId;
          const first = !sameRun(previous, message),
            last = !sameRun(message, next);
          const newDay = !previous || dayLabel(previous.createdAt) !== dayLabel(message.createdAt);
          return (
            <Fragment key={message.id}>
              {newDay && (
                <div className={styles.shDay}>
                  <span>{dayLabel(message.createdAt)}</span>
                </div>
              )}
              <article
                className={`${styles.shMsg}${own ? ` ${styles.own}` : ""}${first ? ` ${styles.first}` : ""}${last ? ` ${styles.last}` : ""}`}
              >
                {!own && (
                  <span className={styles.shMsgFace}>
                    {first && (
                      <Avatar people={people} id={message.sender.id} username={message.sender.username} size="sm" />
                    )}
                  </span>
                )}
                <div className={styles.shMsgBody}>
                  <p
                    title={`${nameOf(people, message.sender.id, message.sender.username)} · ${clock(message.createdAt)}`}
                  >
                    {message.text}
                  </p>
                  {last && (
                    <small>
                      <time dateTime={message.createdAt}>{clock(message.createdAt)}</time>
                      {message === lastOwn && <span>{(otherRead ?? 0) >= message.id ? "已读" : "未读"}</span>}
                    </small>
                  )}
                </div>
              </article>
            </Fragment>
          );
        })}
        {!messages.length && thread.loaded && <p className={styles.shThreadEmpty}>还没有消息</p>}
      </div>
      {!atBottom && (
        <button
          className={styles.shLatest}
          onClick={() => {
            if (log.current) log.current.scrollTop = log.current.scrollHeight;
            setAtBottom(true);
          }}
        >
          <ArrowDown />
          回到最新消息
        </button>
      )}
      {error && (
        <p role="alert" className={`${styles.shAlert} ${styles.shChatAlert}`}>
          {error}
        </p>
      )}
      {open ? (
        <form
          className={styles.shChatCompose}
          onSubmit={(event) => {
            event.preventDefault();
            void send();
          }}
        >
          <EmojiInput
            variant="chat"
            label="回复"
            placeholder={`发消息给 ${otherName}`}
            value={text}
            onChange={setText}
            disabled={busy}
            onEnter={() => void send()}
            trailing={
              <button
                type="submit"
                className={styles.shSend}
                aria-label="发送回复"
                title="发送 (Enter)"
                disabled={busy || !text.trim()}
              >
                <Send />
              </button>
            }
          />
        </form>
      ) : (
        <p className={styles.shChatEnded}>分享已结束，不能再回复</p>
      )}
    </aside>
  );
}
