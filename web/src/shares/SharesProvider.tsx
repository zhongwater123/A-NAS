import { Share2, X } from "lucide-react";
import {
  createContext,
  type ReactNode,
  Suspense,
  useCallback,
  useContext,
  useEffect,
  useEffectEvent,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { ErrorBoundary } from "react-error-boundary";

import type { FileEntry } from "../api.generated";
import { usePeople } from "../people/people";
import { CodeMissing, lazyCode } from "../ui/lazyCode";
import { getShare, listMessages, listShareNotifications, listShares, type Share, type ShareNotification } from "./api";
import { Avatar, nameOf } from "./avatars";
import { errorText, isUnread, shareTitle } from "./format";
import palette from "./palette.module.css";
import styles from "./toasts.module.css";

// The send dialog loads its code and the window's styles when first opened,
// as the windows do; the desktop starts with only the toasts.
const ShareComposer = lazyCode(() => import("./ShareComposer").then((module) => module.ShareComposer));

interface Focus {
  id: string;
  recipientId?: string;
}
export interface Unread {
  received: number;
  sent: number;
}
interface SharesContext {
  userId: string;
  active: boolean;
  focus?: Focus;
  revision: number;
  unread: Unread;
  open: (id: string, recipientId?: string) => void;
  compose: (entries: FileEntry[]) => void;
  refresh: () => void;
  thread: (focus?: Focus) => void;
}
const Context = createContext<SharesContext | undefined>(undefined);
export function useShares() {
  const context = useContext(Context);
  if (!context) throw new Error("SharesProvider is required");
  return context;
}
export function SharesProvider({
  userId,
  active,
  onOpen,
  children,
}: {
  userId: string;
  active: boolean;
  onOpen: () => void;
  children: ReactNode;
}) {
  // A keyed inner provider makes account changes discard messages, recipients and jobs immediately.
  return (
    <Provider key={userId} userId={userId} active={active} onOpen={onOpen}>
      {children}
    </Provider>
  );
}

// A toast stands for one conversation or one new share; later notices for
// it raise its count instead of stacking more toasts.
interface Toast {
  key: string;
  shareId: string;
  recipientId: string;
  kind: string;
  count: number;
  latest: number;
  share?: Share;
  author?: { id: string; username: string };
  text?: string;
}
const toastLife = 10_000;
const countUnread = (items: Share[] | null | undefined, userId: string) =>
  (items ?? []).filter((share) => isUnread(share, userId)).length;

function Provider({
  userId,
  active,
  onOpen,
  children,
}: {
  userId: string;
  active: boolean;
  onOpen: () => void;
  children: ReactNode;
}) {
  const [focus, setFocus] = useState<Focus>();
  const [draft, setDraft] = useState<FileEntry[]>();
  const [revision, setRevision] = useState(0);
  const [toasts, setToasts] = useState<Toast[]>([]);
  const [pollError, setPollError] = useState("");
  // Why the send dialog closed on its own: its code did not download, which
  // lasts until a reload, or it failed.
  const [composerFailure, setComposerFailure] = useState("");
  const [unread, setUnread] = useState<Unread>({ received: 0, sent: 0 });
  // What was unread when the desktop opened is summarised once instead of
  // replaying every notice since this browser last looked.
  const [summary, setSummary] = useState<Unread>();
  const summarised = useRef(false);
  const currentThread = useRef<Focus | undefined>(undefined);
  const visible = useRef(active);
  useLayoutEffect(() => {
    visible.current = active && !draft;
  });
  const key = `anas:shares:notifications:${userId}`;
  const [delivered, setDelivered] = useState(0);
  useEffect(() => {
    if (!delivered) return;
    // Commit the cursor after the fetched notices have been rendered; never jump
    // to a server high-water mark while earlier pages remain unprocessed.
    try {
      localStorage.setItem(key, String(delivered));
    } catch {
      /* Private browsing still gets in-memory reminders. */
    }
  }, [delivered, key]);
  useEffect(() => {
    let stopped = false,
      busy = false,
      cursor = 0,
      caughtUp = false;
    try {
      cursor = Number(localStorage.getItem(key)) || 0;
    } catch {
      /* Storage unavailable. */
    }
    const poll = async () => {
      if (busy) return;
      busy = true;
      try {
        let more = true;
        while (more && !stopped) {
          const items = await listShareNotifications(cursor);
          if (stopped) return;
          const fresh = items.filter((item) => item.id > cursor).sort((a, b) => a.id - b.id);
          if (!fresh.length) break;
          if (caughtUp) {
            const live = fresh.filter(
              (item) =>
                !(
                  visible.current &&
                  document.visibilityState !== "hidden" &&
                  currentThread.current?.id === item.shareId &&
                  currentThread.current?.recipientId === item.recipientId
                ),
            );
            setToasts((previous) => merge(previous, live));
          }
          cursor = fresh[fresh.length - 1].id;
          setDelivered(cursor);
          setRevision((value) => value + 1);
          more = items.length === 100;
        }
        caughtUp = true;
        setPollError("");
      } catch (error) {
        if (!stopped) setPollError(errorText(error));
      } finally {
        busy = false;
      }
    };
    void poll();
    const timer = setInterval(() => void poll(), 5000);
    return () => {
      stopped = true;
      clearInterval(timer);
    };
  }, [key]);
  // The rail and the list's tabs count unread shares; any notice or local
  // read changes the revision, and a slow timer covers another device.
  useEffect(() => {
    let stopped = false;
    const load = async () => {
      try {
        const [received, sent] = await Promise.all([listShares("received"), listShares("sent")]);
        if (stopped) return;
        const counts = { received: countUnread(received?.items, userId), sent: countUnread(sent?.items, userId) };
        setUnread(counts);
        if (!summarised.current) {
          summarised.current = true;
          if (counts.received + counts.sent > 0) setSummary(counts);
        }
      } catch {
        /* Keep the last counts; the notice poll reports connection trouble. */
      }
    };
    void load();
    const timer = setInterval(() => void load(), 30_000);
    return () => {
      stopped = true;
      clearInterval(timer);
    };
  }, [revision, userId]);
  const open = (id: string, recipientId?: string) => {
    setFocus({ id, recipientId });
    onOpen();
  };
  const refresh = useCallback(() => setRevision((value) => value + 1), []);
  const thread = useCallback((value?: Focus) => {
    currentThread.current = value;
  }, []);
  const dismiss = (key: string) => setToasts((previous) => previous.filter((item) => item.key !== key));
  return (
    <Context.Provider
      value={{
        userId,
        active: active && !draft,
        focus,
        revision,
        unread,
        open,
        compose: setDraft,
        refresh,
        thread,
      }}
    >
      {children}
      {draft && (
        <ErrorBoundary
          fallback={null}
          onError={(error) => {
            console.error("A-NAS share composer failed", error);
            setDraft(undefined);
            setComposerFailure(
              error instanceof CodeMissing
                ? "发送分享的窗口没有加载出来。A-NAS 可能刚刚更新过，重新载入页面后再分享。"
                : "发送分享的窗口出错了，请再试一次。",
            );
          }}
        >
          <Suspense fallback={null}>
            <ShareComposer
              entries={draft}
              onClose={() => setDraft(undefined)}
              onCreated={(id) => {
                setDraft(undefined);
                refresh();
                open(id);
              }}
            />
          </Suspense>
        </ErrorBoundary>
      )}
      {(toasts.length > 0 || pollError || composerFailure || summary) && (
        <aside className={`${palette.shPalette} ${styles.shToasts}`} aria-label="分享通知" aria-live="polite">
          {composerFailure && (
            <div className={`${styles.shToast} ${styles.shToastPlain}`} role="alert">
              <span className={styles.shToastMark}>
                <Share2 />
              </span>
              <p>{composerFailure}</p>
              <button className={styles.shToastClose} aria-label="关闭提示" onClick={() => setComposerFailure("")}>
                <X />
              </button>
            </div>
          )}
          {pollError && (
            <div className={`${styles.shToast} ${styles.shToastPlain}`} role="status">
              <span className={styles.shToastMark}>
                <Share2 />
              </span>
              <p>分享通知暂不可用：{pollError}</p>
              <button className={styles.shToastClose} aria-label="关闭通知错误" onClick={() => setPollError("")}>
                <X />
              </button>
            </div>
          )}
          {summary && (
            <SummaryToast
              unread={summary}
              onOpen={() => {
                open("");
                setSummary(undefined);
              }}
              onClose={() => setSummary(undefined)}
            />
          )}
          {toasts
            .slice(-3)
            .reverse()
            .map((toast) => (
              <NoticeToast
                key={toast.key}
                toast={toast}
                onEnrich={(value) =>
                  setToasts((previous) =>
                    previous.map((item) => (item.key === toast.key ? { ...item, ...value } : item)),
                  )
                }
                onOpen={() => {
                  open(toast.shareId, toast.kind === "message" ? toast.recipientId : undefined);
                  dismiss(toast.key);
                }}
                onClose={() => dismiss(toast.key)}
              />
            ))}
          {toasts.length > 3 && (
            <div className={styles.shToastMore}>
              <button
                onClick={() => {
                  open("");
                  setToasts([]);
                }}
              >
                还有 {toasts.length - 3} 条提醒
              </button>
              <button onClick={() => setToasts([])}>全部关闭</button>
            </div>
          )}
        </aside>
      )}
    </Context.Provider>
  );
}

function merge(previous: Toast[], notices: ShareNotification[]): Toast[] {
  const next = [...previous];
  for (const notice of notices) {
    const key = `${notice.kind}:${notice.shareId}:${notice.recipientId}`;
    const index = next.findIndex((item) => item.key === key);
    const old = index >= 0 ? next.splice(index, 1)[0] : undefined;
    // A changed toast moves to the top and fetches its newest text again,
    // showing the previous text meanwhile.
    next.push({
      ...old,
      key,
      shareId: notice.shareId,
      recipientId: notice.recipientId,
      kind: notice.kind,
      count: (old?.count ?? 0) + 1,
      latest: notice.messageId ?? notice.id,
    });
  }
  return next;
}

function NoticeToast({
  toast,
  onEnrich,
  onOpen,
  onClose,
}: {
  toast: Toast;
  onEnrich: (value: Partial<Toast>) => void;
  onOpen: () => void;
  onClose: () => void;
}) {
  const people = usePeople();
  const [paused, setPaused] = useState(false);
  const message = toast.kind === "message";
  const enrich = useEffectEvent(onEnrich);
  const close = useEffectEvent(onClose);
  useEffect(() => {
    let stopped = false;
    void (async () => {
      try {
        const share = await getShare(toast.shareId);
        if (stopped || !share?.sender) return;
        let author: Toast["author"] = share.sender,
          text: string | undefined;
        if (message && toast.latest > 0) {
          const found = (await listMessages(toast.shareId, toast.recipientId, toast.latest - 1)).items?.find(
            (item) => item.id === toast.latest,
          );
          if (found) {
            author = found.sender;
            text = found.text;
          }
        }
        if (!stopped) enrich({ share, author, text });
      } catch {
        /* The plain wording still says what happened. */
      }
    })();
    return () => {
      stopped = true;
    };
  }, [toast.shareId, toast.recipientId, toast.latest, message]);
  useEffect(() => {
    if (paused) return;
    const timer = setTimeout(() => close(), toastLife);
    return () => clearTimeout(timer);
  }, [paused, toast.latest]);
  const who = toast.author ? nameOf(people, toast.author.id, toast.author.username) : "";
  const title = message ? (who ? `${who} 回复了你` : "分享有新回复") : who ? `${who} 分享了文件` : "收到新的文件分享";
  const body = message ? toast.text : toast.share && shareTitle(toast.share);
  return (
    <div
      className={styles.shToast}
      onPointerEnter={() => setPaused(true)}
      onPointerLeave={() => setPaused(false)}
      onFocus={() => setPaused(true)}
      onBlur={() => setPaused(false)}
    >
      <button className={styles.shToastOpen} onClick={onOpen}>
        {toast.author ? (
          <Avatar
            people={people}
            id={toast.author.id}
            username={toast.author.username}
            size="md"
            className={styles.shToastAvatar}
          />
        ) : (
          <span className={styles.shToastMark}>
            <Share2 />
          </span>
        )}
        <span className={styles.shToastCopy}>
          <strong>
            {title}
            {toast.count > 1 && <em>{message ? `${toast.count} 条` : `${toast.count} 个`}</em>}
          </strong>
          {body && <span className={styles.shToastBody}>{body}</span>}
          <small>点击查看</small>
        </span>
      </button>
      <button className={styles.shToastClose} aria-label="关闭分享通知" onClick={onClose}>
        <X />
      </button>
    </div>
  );
}

function SummaryToast({ unread, onOpen, onClose }: { unread: Unread; onOpen: () => void; onClose: () => void }) {
  const [paused, setPaused] = useState(false);
  const close = useEffectEvent(onClose);
  useEffect(() => {
    if (paused) return;
    const timer = setTimeout(() => close(), toastLife);
    return () => clearTimeout(timer);
  }, [paused]);
  const parts = [unread.received && `${unread.received} 个收到的分享`, unread.sent && `${unread.sent} 个发出的分享`]
    .filter(Boolean)
    .join("、");
  return (
    <div className={styles.shToast} onPointerEnter={() => setPaused(true)} onPointerLeave={() => setPaused(false)}>
      <button className={styles.shToastOpen} onClick={onOpen}>
        <span className={styles.shToastMark}>
          <Share2 />
        </span>
        <span className={styles.shToastCopy}>
          <strong>有未读的分享</strong>
          <span className={styles.shToastBody}>{parts}有新内容</span>
          <small>点击查看</small>
        </span>
      </button>
      <button className={styles.shToastClose} aria-label="关闭分享通知" onClick={onClose}>
        <X />
      </button>
    </div>
  );
}
