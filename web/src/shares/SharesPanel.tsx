import { useEffect, useRef, useState } from "react";
import { ArrowLeft, Download, FileText, Folder, RefreshCw, Share2, X } from "lucide-react";
import { type FileEntry, type Space, listEntries, listSpaces } from "../api";
import { formatBytes } from "../bytes";
import { isHEIFImage, isPDFPreviewable, isPreviewableImage, isTextPreviewable, isVideoFile } from "../fileTypes";
import { displayName, usePeople } from "../people";
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
  return <section className="shares-panel"><header className="share-panel-header"><div><h2><Share2 />成员分享</h2><p>查看收到和发出的文件，保存或回复分享。</p></div></header><nav className="share-tabs" aria-label="分享分类"><button aria-pressed={direction === "received"} onClick={() => { setDirection("received"); setPageCount(1); }}>收到的分享</button><button aria-pressed={direction === "sent"} onClick={() => { setDirection("sent"); setPageCount(1); }}>我发出的分享</button></nav>{error && <p role="alert" className="share-error">{error}</p>}<div className="share-records" aria-busy={loading}>{items.map((share) => {
    const unread = share.recipients.some((recipient) => (share.sender.id !== userId && !recipient.viewedAt) || recipient.lastMessage > (share.sender.id === userId ? recipient.senderReadThrough : recipient.readThrough));
    const roots = share.entries.filter((entry) => !parent(entry.path));
    return <button className="share-record" key={share.id} onClick={() => open(share.id)}><span className="share-record-icon"><Share2 /></span><span><strong>{roots.length ? roots.map((entry) => leaf(entry.path)).join("、") : "正在准备文件副本"}</strong><small>{direction === "received" ? `来自 ${share.sender.username}` : `${share.recipients.length} 名接收者`} · {date(share.createdAt)}</small>{share.error && <small className="share-error">{share.error}</small>}</span><span className="share-record-status">{unread && <b>未读</b>}{statusLabel(share)}</span></button>;
  })}{!items.length && <div className="share-empty"><Share2 /><p>{loading ? "正在读取分享…" : "这里还没有分享"}</p><span>在文件管理中选择文件或文件夹，再点击“分享”。</span></div>}{next !== undefined && <button disabled={loading} onClick={() => setPageCount((value) => value + 1)}>加载更多</button>}</div></section>;
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
  useEffect(() => { if (preferredRecipient) setRecipient(preferredRecipient); }, [preferredRecipient]);
  const available = share && isActive(share);
  useEffect(() => { if (!available) { setPreview(undefined); setSaving(false); } }, [available]);
  const sender = share?.sender.id === shares.userId;
  const revoke = async () => {
    setRevoking(true);
    try { await revokeShare(id); setConfirmRevoke(false); setRefresh((value) => value + 1); shares.refresh(); }
    catch (error) { setError(errorText(error)); } finally { setRevoking(false); }
  };
  return <section className="shares-panel share-detail"><header className="share-panel-header"><button onClick={onBack}><ArrowLeft />返回分享列表</button><button aria-label="刷新分享" onClick={() => setRefresh((value) => value + 1)}><RefreshCw /></button></header>{error && <p role="alert" className="share-error">{error}</p>}{!share ? <div className="share-empty">正在读取分享…</div> : <><div className="share-summary"><h2>{sender ? "我发出的分享" : `${share.sender.username} 的分享`}</h2><span className={`share-status ${available ? "active" : ""}`}>{statusLabel(share)}</span><p>{share.status === "preparing" ? `已准备 ${formatBytes(share.preparedBytes)}，完成后自动发出。关闭窗口不会停止准备。` : `发出 ${date(share.publishedAt)} · 到期 ${date(share.expiresAt)}`}</p>{share.error && <p className="share-error">{share.error}</p>}{!available && share.status !== "preparing" && <p>保留文件清单与历史消息；不能预览、回复或新建保存任务。</p>}{sender && share.status !== "revoked" && <button className="danger" disabled={revoking} onClick={() => setConfirmRevoke(true)}>撤回分享</button>}</div><div className="share-detail-columns"><section className="share-files"><header><h3>分享内容</h3>{!sender && <button disabled={!available || !selected.length} onClick={() => setSaving(true)}><Download />保存所选 {selected.length || ""}</button>}</header><div className="share-path"><button disabled={!directory} onClick={() => setDirectory(parent(directory))}><ArrowLeft />上一级</button><span>分享 / {directory}</span></div><div className="share-entry-list">{share.entries.filter((entry) => parent(entry.path) === directory).map((entry) => <div className="share-entry" key={entry.id}><input type="checkbox" aria-label={`选择 ${leaf(entry.path)}`} disabled={!available || sender} checked={selected.includes(entry.id)} onChange={(event) => setSelected((previous) => event.target.checked ? [...previous, entry.id] : previous.filter((item) => item !== entry.id))} /><button onClick={() => entry.isDir ? setDirectory(entry.path) : setPreview(entry)} disabled={!entry.isDir && !available}>{entry.isDir ? <Folder /> : <FileText />}<span>{leaf(entry.path)}</span><small>{entry.isDir ? "文件夹" : formatBytes(entry.size)}</small></button></div>)}</div>{selected.length > 0 && <button onClick={() => setSelected([])}>清除选择（{selected.length}）</button>}{!sender && <JobList jobs={jobs} />}</section><section className="share-discussion"><h3>私聊</h3>{sender && <label>接收者<select aria-label="选择接收者讨论" value={recipient} onChange={(event) => setRecipient(event.target.value)}>{share.recipients.map((item) => <option key={item.userId} value={item.userId}>{people.byId(item.userId) ? displayName(people.byId(item.userId)!) : item.userId}{item.lastMessage > item.senderReadThrough ? " · 未读" : ""}</option>)}</select></label>}{share.recipients.filter((item) => item.userId === recipient).map((item) => <p key={item.userId} className="share-muted">{item.viewedAt ? `已查看 · ${date(item.viewedAt)}` : "尚未查看"} · {item.saved ? `曾成功保存 ${item.savedFiles} 个文件` : item.saveUncertain ? "尚无确认成功的保存" : "尚未成功保存"}{item.saveUncertain && " · 保存结果待确认（仅统计已确认的文件）"}</p>)}{recipient && <Discussion key={`${id}:${recipient}`} share={share} recipientId={recipient} visible={!preview && !saving && !confirmRevoke} />}</section></div></>}{preview && share && available && <Preview key={preview.id} shareId={id} entry={preview} onClose={() => setPreview(undefined)} />}{saving && <SaveTarget shareId={id} entryIds={selected} onClose={() => setSaving(false)} onSaved={(job) => { setJobs((previous) => [job, ...previous.filter((item) => item.id !== job.id)]); setSaving(false); shares.refresh(); }} />}{confirmRevoke && <div className="share-modal-backdrop"><section className="share-modal" role="alertdialog" aria-modal="true" aria-label="撤回这次分享？"><h2>撤回这次分享？</h2><p>所有接收者将不能继续预览或回复，正在保存的任务会停止。已经保存的文件无法收回。</p><footer><button disabled={revoking} onClick={() => setConfirmRevoke(false)}>取消</button><button className="danger" disabled={revoking} onClick={() => void revoke()}>{revoking ? "正在撤回…" : "确认撤回"}</button></footer></section></div>}</section>;
}

function Discussion({ share, recipientId, visible }: { share: Share; recipientId: string; visible: boolean }) {
  const context = useShares();
  const [messages, setMessages] = useState<Message[]>([]);
  const [text, setText] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [refresh, setRefresh] = useState(0);
  const [documentVisible, setDocumentVisible] = useState(document.visibilityState !== "hidden");
  const bottom = useRef<HTMLDivElement>(null);
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
    if (!active || !messages.length) return;
    const last = messages[messages.length - 1].id;
    if (last <= through.current) return;
    bottom.current?.scrollIntoView?.({ block: "nearest" });
    let stopped = false;
    // React has committed the actual thread before its read cursor advances.
    void markRead(share.id, recipientId, last).then(() => { if (!stopped) { through.current = last; context.refresh(); } }).catch((error) => { if (!stopped) setError(errorText(error)); });
    return () => { stopped = true; };
  }, [active, messages, share.id, recipientId]);
  const send = async () => {
    if (busy || !text.trim()) return;
    setBusy(true); setError("");
    try {
      if (attempt.current?.text !== text) attempt.current = { text, key: requestKey() };
      await sendMessage(share.id, { recipientId, requestId: attempt.current.key, text }); setText(""); attempt.current = undefined; setRefresh((value) => value + 1); context.refresh(); }
    catch (error) { setError(errorText(error)); } finally { setBusy(false); }
  };
  const recipient = share.recipients.find((item) => item.userId === recipientId);
  const otherRead = share.sender.id === context.userId ? recipient?.readThrough : recipient?.senderReadThrough;
  return <><div className="share-messages" role="log" aria-label="分享私聊">{messages.map((message) => <article className={message.sender.id === context.userId ? "own" : ""} key={message.id}><header>{message.sender.username}<time>{date(message.createdAt)}</time></header><p>{message.text}</p>{message.sender.id === context.userId && <small>{(otherRead ?? 0) >= message.id ? "已读" : "未读"}</small>}</article>)}{!messages.length && <p className="share-muted">可以围绕这次分享留言。</p>}<div ref={bottom} /></div>{error && <p role="alert" className="share-error">{error}</p>}<form onSubmit={(event) => { event.preventDefault(); void send(); }}><EmojiInput label="回复" value={text} onChange={setText} disabled={busy || !isActive(share)} /><button className="primary" disabled={busy || !isActive(share) || !text.trim()}>{busy ? "发送中…" : "发送回复"}</button></form></>;
}

function JobList({ jobs }: { jobs: Job[] }) {
  return <section className="share-jobs" aria-label="保存任务"><h3>保存任务</h3><p className="share-muted">关闭窗口或刷新页面后，仍可在这里查看进度。到期前已开始的任务可继续；撤回会停止任务。</p>{!jobs.length && <p>尚无保存任务</p>}{jobs.map((job) => <div key={job.id}><strong>{{ running: "正在保存", completed: "保存完成", failed: "保存失败", cancelled: "已停止", interrupted: "服务重启，任务中断" }[job.status] ?? job.status}</strong><progress value={job.bytesDone} max={Math.max(job.bytesTotal, 1)} aria-label="保存进度" /><small>{formatBytes(job.bytesDone)} / {formatBytes(job.bytesTotal)} · 已完成 {job.completedIds?.length ?? 0} 项</small>{job.error && <p className="share-error">{job.error}</p>}</div>)}</section>;
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
