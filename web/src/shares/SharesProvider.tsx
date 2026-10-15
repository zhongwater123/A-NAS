import { createContext, useContext, useEffect, useRef, useState, type ReactNode } from "react";
import { X, Share2 } from "lucide-react";
import type { FileEntry, Profile } from "../api";
import { displayName } from "../people";
import { createShare, errorText, listMembers, listShareNotifications, requestKey, type ShareNotification } from "./api";
import { EmojiInput } from "./EmojiInput";
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
    {draft && <Composer entries={draft} onClose={() => setDraft(undefined)} onCreated={(id) => { setDraft(undefined); refresh(); open(id); }} />}
    {(notices.length > 0 || pollError) && <aside className="share-toasts" aria-label="分享通知" aria-live="polite">{pollError && <div className="share-toast"><span>分享通知暂不可用：{pollError}</span><button aria-label="关闭通知错误" onClick={() => setPollError("")}><X /></button></div>}{notices.slice(-3).reverse().map((item) => <div className="share-toast" key={item.id}><Share2 /><button onClick={() => { open(item.shareId, item.recipientId); setNotices((previous) => previous.filter((notice) => notice.id !== item.id)); }}>{item.kind === "message" ? "分享有新回复" : "收到新的文件分享"}<small>{new Date(item.createdAt).toLocaleString("zh-CN")} · 点击查看</small></button><button aria-label="关闭分享通知" onClick={() => setNotices((previous) => previous.filter((notice) => notice.id !== item.id))}><X /></button></div>)}{notices.length > 0 && <div className="share-notice-summary"><button aria-label="打开收到的分享" onClick={() => { open(""); setNotices([]); }}>{notices.length > 3 ? `还有 ${notices.length - 3} 条提醒` : "打开收到的分享"}</button><button onClick={() => setNotices([])}>关闭全部提醒</button></div>}</aside>}
  </Context.Provider>;
}

function Composer({ entries, onClose, onCreated }: { entries: FileEntry[]; onClose: () => void; onCreated: (id: string) => void }) {
  const [members, setMembers] = useState<Profile[]>([]);
  const [recipientIds, setRecipients] = useState<string[]>([]);
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const mounted = useRef(true);
  const attempt = useRef<{ signature: string; key: string } | undefined>(undefined);
  useEffect(() => { mounted.current = true; void listMembers().then((items) => { if (mounted.current) setMembers(items); }).catch((error) => { if (mounted.current) setError(errorText(error)); }); return () => { mounted.current = false; }; }, []);
  const submit = async () => {
    if (busy || !recipientIds.length) return;
    const sourceIds = entries.map((entry) => entry.id);
    const signature = JSON.stringify({ sourceIds, recipientIds, message });
    if (attempt.current?.signature !== signature) attempt.current = { signature, key: requestKey() };
    setBusy(true); setError("");
    try { const share = await createShare({ requestId: attempt.current.key, sourceIds, recipientIds, message }); if (mounted.current) onCreated(share.id); }
    catch (error) { if (mounted.current) setError(errorText(error)); }
    finally { if (mounted.current) setBusy(false); }
  };
  return <div className="share-modal-backdrop"><form className="share-modal" role="dialog" aria-modal="true" aria-label="分享给成员" onSubmit={(event) => { event.preventDefault(); void submit(); }}><header><h2><Share2 />分享给成员</h2><button type="button" aria-label="关闭分享" onClick={onClose}><X /></button></header><p>{entries.map((entry) => entry.name).join("、")}</p><p className="share-muted">准备独立副本后发出，有效期 7 天。每名接收者与你单独讨论。</p><fieldset disabled={busy}><legend>选择接收成员</legend>{members.length ? members.map((member) => <label className="share-member" key={member.id}><input type="checkbox" checked={recipientIds.includes(member.id)} onChange={(event) => setRecipients((ids) => event.target.checked ? [...ids, member.id] : ids.filter((id) => id !== member.id))} />{displayName(member)}</label>) : <p>暂无可选择的成员</p>}</fieldset><EmojiInput label="留言" value={message} onChange={setMessage} disabled={busy} />{error && <p role="alert" className="share-error">{error}</p>}<footer><span className="share-muted">关闭窗口不会取消已提交的准备任务。</span><button type="submit" className="primary" disabled={busy || !recipientIds.length}>{busy ? "正在提交…" : "发送分享"}</button></footer></form></div>;
}
