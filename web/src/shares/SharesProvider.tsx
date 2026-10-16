import { createContext, useContext, useEffect, useRef, useState, type ReactNode } from "react";
import { X, Share2 } from "lucide-react";
import type { FileEntry } from "../api";
import { errorText, listShareNotifications, type ShareNotification } from "./api";
import { ShareComposer } from "./ShareComposer";
import "./shares.css";

interface Focus { id: string; recipientId?: string }
interface SharesContext { userId: string; active: boolean; focus?: Focus; revision: number; open: (id: string, recipientId?: string) => void; compose: (entries: FileEntry[]) => void; refresh: () => void; thread: (focus?: Focus) => void }
const Context = createContext<SharesContext | undefined>(undefined);
export function useShares() { const context = useContext(Context); if (!context) throw new Error("SharesProvider is required"); return context; }
export function SharesProvider({ userId, active, onOpen, children }: { userId: string; active: boolean; onOpen: () => void; children: ReactNode }) {
  // A keyed inner provider makes account changes discard messages, recipients and jobs immediately.
  return <Provider key={userId} userId={userId} active={active} onOpen={onOpen}>{children}</Provider>;
}
function Provider({ userId, active, onOpen, children }: { userId: string; active: boolean; onOpen: () => void; children: ReactNode }) {
  const [focus, setFocus] = useState<Focus>();
  const [draft, setDraft] = useState<FileEntry[]>();
  const [revision, setRevision] = useState(0);
  const [notices, setNotices] = useState<ShareNotification[]>([]);
  const [pollError, setPollError] = useState("");
  const currentThread = useRef<Focus | undefined>(undefined);
  const visible = useRef(active); visible.current = active && !draft;
  const key = `anas:shares:notifications:${userId}`;
  const [delivered, setDelivered] = useState(0);
  useEffect(() => {
    if (!delivered) return;
    // Commit the cursor after the fetched notices have been rendered; never jump
    // to a server high-water mark while earlier pages remain unprocessed.
    try { localStorage.setItem(key, String(delivered)); } catch { /* Private browsing still gets in-memory reminders. */ }
  }, [delivered, key]);
  useEffect(() => {
    let stopped = false, busy = false, cursor = 0;
    try { cursor = Number(localStorage.getItem(key)) || 0; } catch { /* Storage unavailable. */ }
    const poll = async () => {
      if (busy) return; busy = true;
      try {
        let more = true;
        while (more && !stopped) {
          const items = await listShareNotifications(cursor);
          if (stopped) return;
          const fresh = items.filter((item) => item.id > cursor).sort((a, b) => a.id - b.id);
          if (!fresh.length) break;
          const reminders = fresh.filter((item) => !(visible.current && document.visibilityState !== "hidden" && currentThread.current?.id === item.shareId && currentThread.current?.recipientId === item.recipientId));
          setNotices((previous) => [...previous, ...reminders.filter((item) => !previous.some((other) => other.id === item.id))]);
          cursor = fresh[fresh.length - 1].id;
          setDelivered(cursor); setRevision((value) => value + 1);
          more = items.length === 100;
        }
        setPollError("");
      } catch (error) { if (!stopped) setPollError(errorText(error)); }
      finally { busy = false; }
    };
    void poll(); const timer = setInterval(() => void poll(), 5000);
    return () => { stopped = true; clearInterval(timer); };
  }, [key]);
  const open = (id: string, recipientId?: string) => { setFocus({ id, recipientId }); onOpen(); };
  const refresh = () => setRevision((value) => value + 1);
  return <Context.Provider value={{ userId, active: active && !draft, focus, revision, open, compose: setDraft, refresh, thread: (value) => { currentThread.current = value; } }}>{children}
    {draft && <ShareComposer entries={draft} onClose={() => setDraft(undefined)} onCreated={(id) => { setDraft(undefined); refresh(); open(id); }} />}
    {(notices.length > 0 || pollError) && <aside className="share-toasts" aria-label="分享通知" aria-live="polite">{pollError && <div className="share-toast"><span>分享通知暂不可用：{pollError}</span><button aria-label="关闭通知错误" onClick={() => setPollError("")}><X /></button></div>}{notices.slice(-3).reverse().map((item) => <div className="share-toast" key={item.id}><Share2 /><button onClick={() => { open(item.shareId, item.recipientId); setNotices((previous) => previous.filter((notice) => notice.id !== item.id)); }}>{item.kind === "message" ? "分享有新回复" : "收到新的文件分享"}<small>{new Date(item.createdAt).toLocaleString("zh-CN")} · 点击查看</small></button><button aria-label="关闭分享通知" onClick={() => setNotices((previous) => previous.filter((notice) => notice.id !== item.id))}><X /></button></div>)}{notices.length > 0 && <div className="share-notice-summary"><button aria-label="打开收到的分享" onClick={() => { open(""); setNotices([]); }}>{notices.length > 3 ? `还有 ${notices.length - 3} 条提醒` : "打开收到的分享"}</button><button onClick={() => setNotices([])}>关闭全部提醒</button></div>}</aside>}
  </Context.Provider>;
}
