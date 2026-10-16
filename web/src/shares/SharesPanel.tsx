import { useEffect, useRef, useState } from "react";
import { ArrowLeft, CheckCheck, ChevronRight, Clock3, Download, FileArchive, FileAudio, FileImage, FileText, FileVideo, Folder, Inbox, MessageCircle, PanelRightClose, RefreshCw, Search, Send, Share2, X } from "lucide-react";
import { type FileEntry, type Space, listEntries, listSpaces } from "../api";
import { formatBytes } from "../bytes";
import { isHEIFImage, isPDFPreviewable, isPreviewableImage, isTextPreviewable, isVideoFile } from "../fileTypes";
import { displayName, person, usePeople } from "../people";
import { PersonAvatar } from "../PersonAvatar";
import { EmojiInput } from "./EmojiInput";
import { useShares } from "./SharesProvider";
import { contentURL, errorText, getShare, isActive, listJobs, listMessages, listShares, markRead, readText, requestKey, revokeShare, saveShare, sendMessage, statusLabel, viewShare, type Job, type Message, type Share, type ShareEntry } from "./api";

const date = (value?: string) => value ? new Date(value).toLocaleString("zh-CN") : "—";
const leaf = (path: string) => path.split("/").at(-1) ?? path;
const parent = (path: string) => path.split("/").slice(0, -1).join("/");
export function SharesPanel() {
  const { userId, focus, revision, open } = useShares();
  const [direction, setDirection] = useState("received");
  const [items, setItems] = useState<Share[]>([]);
  const [selected, setSelected] = useState<string>();
  const [next, setNext] = useState<number>();
  const [pageCount, setPageCount] = useState(1);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  useEffect(() => { if (!focus) return; setSelected(focus.id || undefined); if (!focus.id) { setDirection("received"); setPageCount(1); } }, [focus]);
  useEffect(() => {
    let stopped = false, busy = false;
    const load = async () => {
      if (busy) return; busy = true;
      try {
        const collected: Share[] = [];
        let offset = 0, nextOffset: number | undefined;
        for (let page = 0; page < pageCount && !stopped; page++) {
          const result = await listShares(direction, offset);
          collected.push(...(result.items ?? []).filter((item) => !collected.some((old) => old.id === item.id)));
          nextOffset = result.nextOffset;
          if (nextOffset === undefined || nextOffset <= offset) break;
          offset = nextOffset;
        }
        if (!stopped) { setItems(collected); setNext(nextOffset); setError(""); }
      } catch (error) { if (!stopped) setError(errorText(error)); }
      finally { busy = false; if (!stopped) setLoading(false); }
    };
    setLoading(true); void load(); const timer = setInterval(() => void load(), 5000);
    return () => { stopped = true; clearInterval(timer); };
  }, [direction, revision, pageCount]);
  if (selected) return <ShareDetail key={selected} id={selected} preferredRecipient={focus?.id === selected ? focus.recipientId : undefined} onBack={() => setSelected(undefined)} />;
  return <section className="shares-panel share-inbox">
    <header className="share-inbox-header"><div className="share-inbox-title"><span><Inbox /></span><div><h2>分享</h2><p>与你分享的文件，都在这里</p></div></div><span className="share-count">{items.length}{next !== undefined ? "+" : ""} 条分享</span></header>
    <nav className="share-tabs" aria-label="分享分类"><button aria-pressed={direction === "received"} onClick={() => { setDirection("received"); setPageCount(1); }}><Inbox />收到的分享</button><button aria-pressed={direction === "sent"} onClick={() => { setDirection("sent"); setPageCount(1); }}><Send />我发出的分享</button></nav>
    {error && <p role="alert" className="share-error">{error}</p>}
    <div className="share-records" aria-busy={loading}>{items.map((share) => {
      const unread = share.recipients.some((recipient) => (share.sender.id !== userId && !recipient.viewedAt) || recipient.lastMessage > (share.sender.id === userId ? recipient.senderReadThrough : recipient.readThrough));
      const roots = share.entries.filter((entry) => !parent(entry.path));
      return <button className="share-record" key={share.id} onClick={() => open(share.id)}>
        <span className={`share-record-icon ${roots[0]?.isDir ? "folder" : ""}`}><EntryIcon entry={roots[0]} /></span>
        <span><strong>{roots.length ? roots.map((entry) => leaf(entry.path)).join("、") : "正在准备文件副本"}</strong><small>{direction === "received" ? `来自 ${share.sender.username}` : `${share.recipients.length} 名接收者`}<i />{roots.length} 个项目<i />{date(share.createdAt)}</small>{share.error && <small className="share-error">{share.error}</small>}</span>
        <span className="share-record-status">{unread && <b>未读</b>}<span className={`share-status ${isActive(share) ? "active" : ""}`}>{statusLabel(share)}</span></span><ChevronRight className="share-record-arrow" />
      </button>;
    })}{!items.length && <div className="share-empty"><Inbox /><h3>{loading ? "正在读取分享…" : direction === "received" ? "还没有收到分享" : "还没有发出分享"}</h3><span>{direction === "received" ? "成员分享给你的文件会出现在这里。" : "在文件管理中选择文件，再点击分享。"}</span></div>}{next !== undefined && <button disabled={loading} onClick={() => setPageCount((value) => value + 1)}>加载更多</button>}</div>
  </section>;
}

function ShareDetail({ id, preferredRecipient, onBack }: { id: string; preferredRecipient?: string; onBack: () => void }) {
  const shares = useShares();
  const people = usePeople();
  const [share, setShare] = useState<Share>();
  const [jobs, setJobs] = useState<Job[]>([]);
  const [recipient, setRecipient] = useState(preferredRecipient ?? "");
  const [directory, setDirectory] = useState("");
  const [selected, setSelected] = useState<string[]>([]);
  const [preview, setPreview] = useState<ShareEntry>();
  const [saving, setSaving] = useState(false);
  const [saveIds, setSaveIds] = useState<string[]>([]);
  const [chatOpen, setChatOpen] = useState(window.innerWidth > 700);
  const chatToggle = useRef<HTMLButtonElement>(null);
  const [query, setQuery] = useState("");
  const [revoking, setRevoking] = useState(false);
  const [confirmRevoke, setConfirmRevoke] = useState(false);
  const [error, setError] = useState("");
  const activeRef = useRef(shares.active); activeRef.current = shares.active;
  const viewed = useRef(false);
  const [refresh, setRefresh] = useState(0);
  useEffect(() => {
    let stopped = false, busy = false;
    const load = async () => {
      if (busy) return; busy = true;
      try {
        const value = await getShare(id);
        if (stopped) return;
        setShare(value); setRecipient((current) => value.recipients.some((item) => item.userId === current) ? current : value.recipients[0]?.userId ?? "");
        let warning = "";
        if (value.sender.id !== shares.userId) {
          const shouldView = activeRef.current && document.visibilityState !== "hidden" && !viewed.current;
          // Neither an unavailable job list nor a failed view receipt blocks the other.
          const [savedJobs, receipt] = await Promise.allSettled([listJobs(id), shouldView ? viewShare(id) : Promise.resolve(undefined)]);
          if (stopped) return;
          if (savedJobs.status === "fulfilled") setJobs(savedJobs.value);
          else warning = errorText(savedJobs.reason);
          if (receipt.status === "fulfilled" && receipt.value) { viewed.current = true; setShare(receipt.value); shares.refresh(); }
          else if (receipt.status === "rejected") warning = [warning, errorText(receipt.reason)].filter(Boolean).join("；");
        }
        if (!stopped) setError(warning);
      } catch (error) { if (!stopped) setError(errorText(error)); }
      finally { busy = false; }
    };
    void load(); const timer = setInterval(() => void load(), 3000);
    return () => { stopped = true; clearInterval(timer); };
  }, [id, refresh, shares.userId]);
  useEffect(() => { if (preferredRecipient) { setRecipient(preferredRecipient); setChatOpen(true); } }, [preferredRecipient, shares.focus]);
  const available = share && isActive(share);
  useEffect(() => { if (!available) { setPreview(undefined); setSaving(false); } }, [available]);
  const sender = share?.sender.id === shares.userId;
  const revoke = async () => {
    setRevoking(true);
    try { await revokeShare(id); setConfirmRevoke(false); setRefresh((value) => value + 1); shares.refresh(); }
    catch (error) { setError(errorText(error)); } finally { setRevoking(false); }
  };
  const roots = share?.entries.filter((entry) => !parent(entry.path)) ?? [];
  const visibleEntries = share?.entries.filter((entry) => parent(entry.path) === directory && leaf(entry.path).toLocaleLowerCase().includes(query.trim().toLocaleLowerCase())) ?? [];
  const allVisibleSelected = visibleEntries.length > 0 && visibleEntries.every((entry) => selected.includes(entry.id));
  const owner = share && person(people, share.sender);
  const contact = share?.recipients.find((item) => item.userId === recipient);
  const unread = contact && contact.lastMessage > (sender ? contact.senderReadThrough : contact.readThrough);
  const navigate = (path: string) => { setDirectory(path); setQuery(""); };
  const startSave = (ids: string[]) => { setSaveIds(ids); setSaving(true); };
  return <section className="shares-panel share-detail">
    <header className="share-detail-topbar"><button className="share-back" onClick={onBack}><ArrowLeft />返回分享列表</button><div><button aria-label="刷新分享" onClick={() => setRefresh((value) => value + 1)}><RefreshCw /></button><button ref={chatToggle} className={chatOpen ? "share-chat-toggle active" : "share-chat-toggle"} aria-label="展开私聊" aria-expanded={chatOpen} onClick={() => setChatOpen(!chatOpen)}><MessageCircle />私聊{unread && <b>新消息</b>}</button></div></header>
    {error && <p role="alert" className="share-error">{error}</p>}
    {!share ? <div className="share-empty">正在读取分享…</div> : <>
      <div className="share-detail-heading"><div className="share-heading-content"><div className="share-owner"><PersonAvatar who={owner!} className="share-owner-avatar" /><span>{sender ? "我发出的分享" : `${displayName(owner!)} 的分享`}</span><span className={`share-status ${available ? "active" : ""}`}>{statusLabel(share)}</span></div><h2 title={roots.map((entry) => leaf(entry.path)).join("、")}>{roots.length ? roots.map((entry) => leaf(entry.path)).join("、") : "正在准备文件副本"}</h2><div className="share-meta"><span>{roots.length} 个项目 · {formatBytes(share.entries.filter((entry) => !entry.isDir).reduce((sum, entry) => sum + entry.size, 0))}</span><span><Clock3 />{share.status === "preparing" ? `已准备 ${formatBytes(share.preparedBytes)}` : `${date(share.expiresAt)} 到期`}</span></div></div><div className="share-heading-actions">{!sender && <button className="primary" disabled={!available || !roots.length} onClick={() => startSave(roots.map((entry) => entry.id))}><Download />保存全部</button>}{sender && share.status !== "revoked" && <button className="danger" disabled={revoking} onClick={() => setConfirmRevoke(true)}>撤回分享</button>}</div></div>
      {share.error && <p className="share-error">{share.error}</p>}{!available && <p className="share-state-notice">{share.status === "preparing" ? "文件副本准备完成后会自动发出，你可以先关闭窗口。" : "这次分享已结束，仍可查看文件清单与历史消息。"}</p>}
      <div className={`share-detail-workspace ${chatOpen ? "with-chat" : ""}`}>
        <section className="share-file-workspace" aria-label="分享内容">
          <div className="share-file-toolbar"><nav className="share-breadcrumbs" aria-label="分享内路径"><button onClick={() => navigate("")}>全部文件</button>{directory.split("/").filter(Boolean).map((name, index, parts) => <span key={index}><ChevronRight /><button onClick={() => navigate(parts.slice(0, index + 1).join("/"))}>{name}</button></span>)}</nav><label className="share-file-search"><Search /><input type="search" aria-label="搜索分享文件" placeholder="搜索当前文件夹" value={query} onChange={(event) => setQuery(event.target.value)} /></label></div>
          <div className="share-selection-bar">{!sender && <label><input type="checkbox" aria-label="全选当前文件夹" disabled={!available || !visibleEntries.length} checked={allVisibleSelected} onChange={(event) => setSelected((old) => event.target.checked ? [...new Set([...old, ...visibleEntries.map((entry) => entry.id)])] : old.filter((id) => !visibleEntries.some((entry) => entry.id === id)))} /><span>{selected.length ? `已选 ${selected.length} 项` : "全选"}</span></label>}<span className="share-visible-count">{visibleEntries.length} 个项目</span>{selected.length > 0 && <><button className="share-clear" onClick={() => setSelected([])}>清除选择（{selected.length}）</button><button className="share-save-selected" disabled={!available} onClick={() => startSave(selected)}><Download />保存所选 {selected.length}</button></>}</div>
          <div className="share-file-scroll"><div className="share-file-labels"><span>名称</span><span>大小</span></div>{visibleEntries.map((entry) => <div className={`share-file-row ${selected.includes(entry.id) ? "selected" : ""}`} key={entry.id}>
            {!sender && <input type="checkbox" aria-label={`选择 ${leaf(entry.path)}`} disabled={!available} checked={selected.includes(entry.id)} onChange={(event) => setSelected((previous) => event.target.checked ? [...previous, entry.id] : previous.filter((item) => item !== entry.id))} />}
            <button onClick={() => entry.isDir ? navigate(entry.path) : setPreview(entry)} disabled={!entry.isDir && !available}><span className={`share-file-symbol ${entry.isDir ? "folder" : ""}`}><EntryIcon entry={entry} /></span><span className="share-file-name"><strong>{leaf(entry.path)}</strong><small>{entry.isDir ? "文件夹" : fileKind(entry.path)}</small></span><span className="share-file-size">{entry.isDir ? "—" : formatBytes(entry.size)}</span>{entry.isDir && <ChevronRight />}</button>
          </div>)}{!visibleEntries.length && <div className="share-empty"><Folder /><p>{query ? "没有匹配的文件" : "文件夹为空"}</p>{query && <button onClick={() => setQuery("")}>清除搜索</button>}</div>}</div>
          {!sender && <JobList jobs={jobs} />}
          <footer className="share-file-footnote"><CheckCheck /><span>独立文件副本 · 同名保存时保留两份</span></footer>
        </section>
        <aside className="share-chat-panel" hidden={!chatOpen} aria-label="分享私聊面板"><header><div><MessageCircle /><strong>私聊</strong></div><button aria-label="收起私聊" onClick={() => { setChatOpen(false); chatToggle.current?.focus(); }}><PanelRightClose /></button></header>
          {sender && <label className="share-contact-picker">接收者<select aria-label="选择接收者讨论" value={recipient} onChange={(event) => setRecipient(event.target.value)}>{share.recipients.map((item) => <option key={item.userId} value={item.userId}>{people.byId(item.userId) ? displayName(people.byId(item.userId)!) : item.userId}{item.lastMessage > item.senderReadThrough ? " · 未读" : ""}</option>)}</select></label>}
          <p className="share-chat-context">{sender ? "与当前成员单独对话" : `仅你与 ${displayName(owner!)} 可见`}</p>
          {contact && <details className="share-receipt"><summary>{contact.viewedAt ? "已查看" : "尚未查看"} · {contact.saved ? `曾成功保存 ${contact.savedFiles} 个文件` : contact.saveUncertain ? "尚无确认成功的保存" : "尚未成功保存"}{contact.saveUncertain && " · 保存结果待确认（仅统计已确认的文件）"}</summary><p>查看时间：{date(contact.viewedAt)}。保存数量仅表示已确认保存过的文件。</p></details>}
          {recipient && <Discussion key={`${id}:${recipient}`} share={share} recipientId={recipient} visible={chatOpen && !preview && !saving && !confirmRevoke} />}
        </aside>
      </div>
    </>}
    {preview && share && available && <Preview key={preview.id} shareId={id} entry={preview} onClose={() => setPreview(undefined)} />}
    {saving && <SaveTarget shareId={id} entryIds={saveIds} onClose={() => setSaving(false)} onSaved={(job) => { setJobs((previous) => [job, ...previous.filter((item) => item.id !== job.id)]); setSaving(false); shares.refresh(); }} />}
    {confirmRevoke && <div className="share-modal-backdrop"><section className="share-modal" role="alertdialog" aria-modal="true" aria-label="撤回这次分享？"><h2>撤回这次分享？</h2><p>所有接收者将不能继续预览或回复，正在保存的任务会停止。已经保存的文件无法收回。</p><footer><button disabled={revoking} onClick={() => setConfirmRevoke(false)}>取消</button><button className="danger" disabled={revoking} onClick={() => void revoke()}>{revoking ? "正在撤回…" : "确认撤回"}</button></footer></section></div>}
  </section>;
}

function Discussion({ share, recipientId, visible }: { share: Share; recipientId: string; visible: boolean }) {
  const context = useShares();
  const [messages, setMessages] = useState<Message[]>([]);
  const [text, setText] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [refresh, setRefresh] = useState(0);
  const [documentVisible, setDocumentVisible] = useState(document.visibilityState !== "hidden");
  const messageList = useRef<HTMLDivElement>(null);
  const [atBottom, setAtBottom] = useState(true);
  const through = useRef(0);
  const attempt = useRef<{ text: string; key: string } | undefined>(undefined);
  const active = context.active && documentVisible && visible;
  useEffect(() => { const change = () => setDocumentVisible(document.visibilityState !== "hidden"); document.addEventListener("visibilitychange", change); return () => document.removeEventListener("visibilitychange", change); }, []);
  useEffect(() => {
    context.thread(active ? { id: share.id, recipientId } : undefined);
    return () => context.thread(undefined);
  }, [share.id, recipientId, active]);
  useEffect(() => {
    let stopped = false, loading = false;
    const poll = async () => {
      if (loading) return; loading = true;
      try {
        const collected: Message[] = []; let after = 0;
        while (!stopped) {
          const page = (await listMessages(share.id, recipientId, after)).items ?? [];
          const fresh = page.filter((message) => message.id > after);
          collected.push(...fresh);
          if (page.length < 100 || !fresh.length) break;
          after = fresh[fresh.length - 1].id;
        }
        if (!stopped) { setMessages(collected); setError(""); }
      } catch (error) { if (!stopped) setError(errorText(error)); }
      finally { loading = false; }
    };
    void poll(); const timer = setInterval(() => void poll(), 3000);
    return () => { stopped = true; clearInterval(timer); };
  }, [share.id, recipientId, refresh]);
  useEffect(() => {
    if (!active || !atBottom || !messages.length) return;
    const last = messages[messages.length - 1].id;
    if (last <= through.current) return;
    if (messageList.current) messageList.current.scrollTop = messageList.current.scrollHeight;
    let stopped = false;
    // React has committed the actual thread before its read cursor advances.
    void markRead(share.id, recipientId, last).then(() => { if (!stopped) { through.current = last; context.refresh(); } }).catch((error) => { if (!stopped) setError(errorText(error)); });
    return () => { stopped = true; };
  }, [active, atBottom, messages, share.id, recipientId]);
  const send = async () => {
    if (busy || !text.trim()) return;
    setBusy(true); setError("");
    try {
      if (attempt.current?.text !== text) attempt.current = { text, key: requestKey() };
      await sendMessage(share.id, { recipientId, requestId: attempt.current.key, text }); setAtBottom(true); setText(""); attempt.current = undefined; setRefresh((value) => value + 1); context.refresh(); }
    catch (error) { setError(errorText(error)); } finally { setBusy(false); }
  };
  const recipient = share.recipients.find((item) => item.userId === recipientId);
  const otherRead = share.sender.id === context.userId ? recipient?.readThrough : recipient?.senderReadThrough;
  return <><div ref={messageList} className="share-messages" role="log" aria-label="分享私聊" onScroll={() => { const list = messageList.current; if (list) setAtBottom(list.scrollHeight - list.scrollTop - list.clientHeight < 48); }}>{messages.map((message) => <article className={message.sender.id === context.userId ? "own" : ""} key={message.id}><header>{message.sender.username}<time>{date(message.createdAt)}</time></header><p>{message.text}</p>{message.sender.id === context.userId && <small>{(otherRead ?? 0) >= message.id ? "已读" : "未读"}</small>}</article>)}{!messages.length && <p className="share-muted">可以围绕这次分享留言。</p>}</div>{!atBottom && <button className="share-latest" onClick={() => { if (messageList.current) messageList.current.scrollTop = messageList.current.scrollHeight; setAtBottom(true); }}>回到最新消息</button>}{error && <p role="alert" className="share-error">{error}</p>}<form onSubmit={(event) => { event.preventDefault(); void send(); }}><EmojiInput label="回复" value={text} onChange={setText} disabled={busy || !isActive(share)} /><button className="primary" disabled={busy || !isActive(share) || !text.trim()}>{busy ? "发送中…" : "发送回复"}</button></form></>;
}

function fileKind(name: string) {
  if (isPreviewableImage(name)) return "图片";
  if (isVideoFile(name)) return "视频";
  if (/\.(mp3|flac|wav|m4a|aac|ogg|opus)$/i.test(name)) return "音频";
  if (/\.(zip|7z|rar|tar|gz)$/i.test(name)) return "压缩包";
  return name.includes(".") ? `${name.split(".").at(-1)?.toUpperCase()} 文件` : "文件";
}
function EntryIcon({ entry }: { entry?: ShareEntry }) {
  if (!entry) return <Share2 />;
  if (entry.isDir) return <Folder />;
  switch (fileKind(entry.path)) { case "图片": return <FileImage />; case "视频": return <FileVideo />; case "音频": return <FileAudio />; case "压缩包": return <FileArchive />; default: return <FileText />; }
}
function JobList({ jobs }: { jobs: Job[] }) {
  if (!jobs.length) return null;
  const job = jobs[0];
  const label = (job: Job) => ({ running: "正在保存", completed: "保存完成", failed: "保存失败", cancelled: "已停止", interrupted: "服务重启，任务中断" }[job.status] ?? job.status);
  return <section className="share-save-progress" aria-label="保存任务"><div className="share-save-progress-title"><Download /><strong>{label(job)}</strong><small>{formatBytes(job.bytesDone)} / {formatBytes(job.bytesTotal)}</small></div><progress value={job.status === "completed" ? 1 : job.bytesDone} max={job.status === "completed" ? 1 : Math.max(job.bytesTotal, 1)} aria-label="保存进度" />{job.error && <p className="share-error">{job.error}</p>}<details><summary>保存记录 · {jobs.length} 次</summary><p>关闭窗口后继续保存。到期前开始的任务可完成，撤回会停止任务。</p>{jobs.map((item) => <div key={item.id}><span>{label(item)} · 已完成 {item.completedIds?.length ?? 0} 项</span>{item.error && <small>{item.error}</small>}</div>)}</details></section>;
}
function SaveTarget({ shareId, entryIds, onClose, onSaved }: { shareId: string; entryIds: string[]; onClose: () => void; onSaved: (job: Job) => void }) {
  const [spaces, setSpaces] = useState<Space[]>([]);
  const [spaceId, setSpaceId] = useState("");
  const [trail, setTrail] = useState<FileEntry[]>([]);
  const [entries, setEntries] = useState<FileEntry[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(true);
  const attempt = useRef<{ signature: string; key: string } | undefined>(undefined);
  const parentId = trail.at(-1)?.id ?? "";
  useEffect(() => { let stopped = false; void listSpaces().then((items) => { if (stopped) return; const allowed = items.filter((space) => !space.viewing && (space as Space & { writable?: boolean }).writable !== false); setSpaces(allowed); setSpaceId(allowed[0]?.id ?? ""); if (!allowed.length) setLoading(false); }).catch((error) => { if (!stopped) { setError(errorText(error)); setLoading(false); } }); return () => { stopped = true; }; }, []);
  useEffect(() => { if (!spaceId) return; let stopped = false; setLoading(true); setEntries([]); void listEntries(spaceId, parentId).then((items) => { if (!stopped) { setEntries(items.filter((entry) => entry.kind === "directory" && (entry as FileEntry & { writable?: boolean }).writable !== false)); setError(""); } }).catch((error) => { if (!stopped) setError(errorText(error)); }).finally(() => { if (!stopped) setLoading(false); }); return () => { stopped = true; }; }, [spaceId, parentId]);
  const save = async () => {
    if (busy || loading || !spaceId) return;
    const target = { rootId: spaceId, parentId };
    const signature = JSON.stringify({ entryIds, target });
    setBusy(true); setError("");
    try {
      if (attempt.current?.signature !== signature) attempt.current = { signature, key: requestKey() };
      onSaved(await saveShare(shareId, { requestId: attempt.current.key, entryIds, target })); }
    catch (error) { setError(errorText(error)); } finally { setBusy(false); }
  };
  return <div className="share-modal-backdrop"><section className="share-modal" role="dialog" aria-modal="true" aria-label="保存到我的空间"><header><h2>保存到我的空间</h2><button aria-label="关闭保存位置" onClick={onClose}><X /></button></header><label>目标空间<select aria-label="目标空间" disabled={busy} value={spaceId} onChange={(event) => { setSpaceId(event.target.value); setTrail([]); }}>{spaces.map((space) => <option key={space.id} value={space.id}>{space.kind === "private" ? "个人空间" : space.name}</option>)}</select></label><p>保存到：{spaces.find((space) => space.id === spaceId)?.name} / {trail.map((entry) => entry.name).join(" / ")}</p><button disabled={!trail.length || busy} onClick={() => setTrail((previous) => previous.slice(0, -1))}><ArrowLeft />上一级</button><div className="share-target-folders">{loading ? <p>正在读取文件夹…</p> : !spaces.length ? <p>没有可写的个人空间或共享文件夹。</p> : entries.map((entry) => <button key={entry.id} disabled={busy} onClick={() => setTrail((previous) => [...previous, entry])}><Folder />{entry.name}</button>)}</div><p className="share-muted">同名文件自动改名，保留两份。目标权限将在保存时再次检查。</p>{error && <p role="alert" className="share-error">{error}</p>}<footer><button disabled={busy || loading || !spaceId} className="primary" onClick={() => void save()}>{busy ? "正在创建任务…" : "保存到此文件夹"}</button></footer></section></div>;
}

function Preview({ shareId, entry, onClose }: { shareId: string; entry: ShareEntry; onClose: () => void }) {
  const [text, setText] = useState<string>();
  const [error, setError] = useState("");
  const name = leaf(entry.path), url = contentURL(shareId, entry, isHEIFImage(name));
  useEffect(() => { if (!isTextPreviewable(name)) return; const abort = new AbortController(); void readText(shareId, entry, abort.signal).then(setText).catch((error) => { if (!abort.signal.aborted) setError(errorText(error)); }); return () => abort.abort(); }, [shareId, entry, name]);
  return <div className="share-preview" role="dialog" aria-modal="true" aria-label={`预览 ${name}`} onKeyDown={(event) => { if (event.key === "Escape") onClose(); }}><header><strong>{name}</strong><span>{formatBytes(entry.size)}</span><button autoFocus aria-label="关闭预览" onClick={onClose}><X /></button></header><div className="share-preview-body">{error ? <p role="alert">{error}</p> : isPreviewableImage(name) ? <img src={url} alt={name} onError={() => setError("无法预览这张图片，请保存后查看。")} /> : isPDFPreviewable(name) ? <iframe title={name} src={url} /> : isTextPreviewable(name) ? <div><pre>{text === undefined ? "正在读取…" : text}</pre>{entry.size > 262144 && <p>仅预览前 256 KiB，保存后可查看完整内容。</p>}</div> : isVideoFile(name) ? <video src={url} controls onError={() => setError("浏览器无法播放此视频，请保存后查看。")} /> : /\.(mp3|flac|wav|m4a|aac|ogg|opus)$/i.test(name) ? <audio src={url} controls onError={() => setError("浏览器无法播放此音频，请保存后查看。")} /> : <div className="share-empty"><FileText /><h3>{name}</h3><p>此格式暂不支持预览。选择文件并保存到你的空间后打开。</p></div>}</div></div>;
}
