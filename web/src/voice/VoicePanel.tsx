import { AudioLines, CircleAlert, Clock3, Plus, Search, Settings2, Upload, UsersRound, X } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { Spinner, useToasts, VoiceContext, type VoiceContextValue, Waveform } from "./context";
import { duration, when } from "./format";
import { ImportDialog } from "./ImportDialog";
import { PeopleView } from "./PeopleView";
import { RecordingView } from "./RecordingView";
import { SettingsView } from "./SettingsView";
import { importQueue, isAudioName, type PendingImport, usePendingImports } from "./uploads";
import { getVoiceStatus, listRecordings, type Recording, type VoiceStatus } from "./voiceApi";
import "./voice.css";

type View = { kind: "recording"; id?: string } | { kind: "people" } | { kind: "settings" };

interface Props {
  userId: string;
  isAdmin: boolean;
  onOpenFiles: () => void;
}

const stageText: Record<string, string> = { preparing: "准备音频", recognizing: "识别文字", identifying: "识别发言人" };
const waitingText: Record<string, string> = { sign_in: "等待你登录后继续", recognizer: "等待管理员配置豆包语音", busy: "识别服务繁忙，稍后自动重试" };

// statusLine is what a recording's card says under its title.
function statusLine(recording: Recording): { text: string; tone: "" | "busy" | "wait" | "error" } {
  switch (recording.status) {
    case "queued": return { text: "排队中", tone: "busy" };
    case "processing": return { text: `${stageText[recording.stage ?? ""] ?? "处理中"}${recording.progress > 0.02 && recording.stage === "recognizing" ? ` ${Math.round(recording.progress / 0.8 * 100)}%` : ""}`, tone: "busy" };
    case "waiting": return { text: waitingText[recording.waiting ?? ""] ?? "等待中", tone: "wait" };
    case "failed": return { text: "处理失败", tone: "error" };
    default: return { text: recording.speakers ? `${recording.speakers} 位发言人` : "没有识别到说话", tone: "" };
  }
}

// VoicePanel is 语音处理: the member's recordings on the left, the chosen
// one, its speakers' voiceprints or the settings on the right.
export function VoicePanel({ userId, isAdmin, onOpenFiles }: Props) {
  const [status, setStatus] = useState<VoiceStatus>();
  const [recordings, setRecordings] = useState<Recording[]>();
  const [listError, setListError] = useState("");
  const [view, setView] = useState<View>({ kind: "recording" });
  const [query, setQuery] = useState("");
  const [importing, setImporting] = useState<File[] | true>();
  const [dragging, setDragging] = useState(false);
  const pending = usePendingImports();
  const { toast, view: toasts } = useToasts();

  const load = useCallback(async () => {
    try {
      setRecordings(await listRecordings());
      setListError("");
    } catch (caught) {
      setListError(caught instanceof Error ? caught.message : "无法读取录音");
    }
  }, []);
  useEffect(() => { void load(); }, [load]);
  useEffect(() => { void getVoiceStatus().then(setStatus).catch(() => undefined); }, []);
  useEffect(() => importQueue.onImported(() => void load()), [load]);
  // While anything is on its way the list follows it.
  const moving = recordings?.some((recording) => recording.status !== "ready" && recording.status !== "failed") || pending.length > 0;
  useEffect(() => {
    if (!moving) return;
    const timer = window.setInterval(() => void load(), 2500);
    return () => window.clearInterval(timer);
  }, [moving, load]);
  // The first recording opens by itself.
  useEffect(() => {
    if (view.kind === "recording" && !view.id && recordings?.length) setView({ kind: "recording", id: recordings[0].id });
  }, [recordings, view]);

  const visible = useMemo(() => {
    const words = query.trim().toLowerCase();
    if (!words) return recordings ?? [];
    return (recordings ?? []).filter((recording) =>
      [recording.title, recording.name, recording.preview ?? "", ...recording.people].some((text) => text.toLowerCase().includes(words)));
  }, [recordings, query]);

  const context = useMemo<VoiceContextValue>(() => ({
    userID: userId, admin: isAdmin, status, toast,
    changed: () => void load(),
    openSettings: () => setView({ kind: "settings" }),
  }), [userId, isAdmin, status, toast, load]);

  const selected = view.kind === "recording" ? view.id : undefined;
  const dragDepth = useRef(0);
  const onDrop = (event: React.DragEvent) => {
    event.preventDefault();
    dragDepth.current = 0;
    setDragging(false);
    const files = Array.from(event.dataTransfer.files).filter((file) => isAudioName(file.name));
    if (files.length) setImporting(files);
    else if (event.dataTransfer.files.length) toast("只能导入录音或带声音的视频文件", "error");
  };

  return (
    <VoiceContext.Provider value={context}>
      <div
        className="vo-app"
        onDragEnter={(event) => { if (event.dataTransfer.types.includes("Files")) { dragDepth.current += 1; setDragging(true); } }}
        onDragLeave={() => { dragDepth.current = Math.max(0, dragDepth.current - 1); if (!dragDepth.current) setDragging(false); }}
        onDragOver={(event) => { if (event.dataTransfer.types.includes("Files")) event.preventDefault(); }}
        onDrop={onDrop}
      >
        <aside className="vo-sidebar" aria-label="录音">
          <button type="button" className="vo-import" onClick={() => setImporting(true)}>
            <Plus /><span>导入录音</span>
          </button>
          <div className={`vo-search${query ? " active" : ""}`}>
            <Search />
            <input type="search" value={query} placeholder="搜索标题、发言人或内容" aria-label="搜索录音" onChange={(event) => setQuery(event.target.value)} />
            {query && <button type="button" aria-label="清除搜索" onClick={() => setQuery("")}><X /></button>}
          </div>
          <div className="vo-list" role="listbox" aria-label="录音列表">
            {pending.map((item) => <PendingCard key={item.key} item={item} />)}
            {recordings === undefined && !listError && <div className="vo-list-state"><Spinner /></div>}
            {listError && <div className="vo-list-state error"><CircleAlert /><span>{listError}</span><button type="button" onClick={() => void load()}>重试</button></div>}
            {recordings && !recordings.length && !pending.length && <p className="vo-list-empty">还没有录音</p>}
            {recordings && recordings.length > 0 && !visible.length && <p className="vo-list-empty">没有找到“{query.trim()}”</p>}
            {visible.map((recording) => {
              const line = statusLine(recording);
              return (
                <button
                  key={recording.id}
                  type="button"
                  role="option"
                  aria-selected={selected === recording.id}
                  className={`vo-card${selected === recording.id ? " selected" : ""}`}
                  onClick={() => setView({ kind: "recording", id: recording.id })}
                >
                  <span className="vo-card-head">
                    <strong>{recording.title}</strong>
                    <small>{when(recording.createdAt)}</small>
                  </span>
                  {recording.status === "ready" && recording.preview && <span className="vo-card-preview">{recording.preview}</span>}
                  <span className="vo-card-meta">
                    <span><Clock3 />{duration(recording.durationMs)}</span>
                    <span className={`vo-card-status ${line.tone}`}>
                      {line.tone === "busy" && <Waveform bars={4} />}
                      {line.text}
                    </span>
                  </span>
                  {recording.status === "processing" && <span className="vo-card-progress"><i style={{ width: `${Math.max(6, recording.progress * 100)}%` }} /></span>}
                  {recording.people.length > 0 && (
                    <span className="vo-card-people">{recording.people.slice(0, 4).join("、")}{recording.people.length > 4 ? ` 等 ${recording.people.length} 人` : ""}</span>
                  )}
                </button>
              );
            })}
          </div>
          <nav className="vo-nav" aria-label="语音处理">
            <button type="button" className={view.kind === "people" ? "active" : ""} onClick={() => setView({ kind: "people" })}><UsersRound /><span>发言人与声纹</span></button>
            <button type="button" className={view.kind === "settings" ? "active" : ""} onClick={() => setView({ kind: "settings" })}>
              <Settings2 /><span>服务与设置</span>
              {status && !status.recognizer.available && <i className="vo-nav-dot" aria-label="需要配置" />}
            </button>
          </nav>
        </aside>
        <main className="vo-main">
          {view.kind === "people" && <PeopleView recordings={recordings ?? []} onOpenRecording={(id) => setView({ kind: "recording", id })} />}
          {view.kind === "settings" && <SettingsView />}
          {view.kind === "recording" && (selected
            ? <RecordingView key={selected} id={selected} onDeleted={() => { setView({ kind: "recording" }); void load(); }} />
            : recordings !== undefined && <Welcome status={status} onImport={() => setImporting(true)} />)}
        </main>
        {dragging && (
          <div className="vo-drop" aria-hidden="true">
            <div><Upload /><strong>松开以导入录音</strong><span>上传到个人空间的“录音”文件夹，然后开始转写</span></div>
          </div>
        )}
        {importing && (
          <ImportDialog
            initialFiles={importing === true ? [] : importing}
            onClose={() => setImporting(undefined)}
            onImported={(count, opened) => {
              setImporting(undefined);
              void load();
              if (opened) setView({ kind: "recording", id: opened });
              toast(count > 1 ? `已加入 ${count} 段录音` : "已加入处理队列");
            }}
            onOpenFiles={onOpenFiles}
          />
        )}
        {toasts}
      </div>
    </VoiceContext.Provider>
  );
}

function PendingCard({ item }: { item: PendingImport }) {
  return (
    <div className={`vo-card pending${item.state === "failed" ? " failed" : ""}`}>
      <span className="vo-card-head"><strong>{item.name}</strong></span>
      <span className="vo-card-meta">
        <span className={`vo-card-status ${item.state === "failed" ? "error" : "busy"}`}>
          {item.state === "failed" ? item.error ?? "上传失败" : item.state === "importing" ? "正在导入" : `上传中 ${Math.round(item.progress * 100)}%`}
        </span>
        {item.state === "failed" && <button type="button" className="vo-link" onClick={() => importQueue.dismiss(item.key)}>移除</button>}
      </span>
      {item.state !== "failed" && <span className="vo-card-progress"><i style={{ width: `${Math.max(4, item.progress * 100)}%` }} /></span>}
    </div>
  );
}

function Welcome({ status, onImport }: { status?: VoiceStatus; onImport: () => void }) {
  return (
    <section className="vo-welcome" aria-label="开始使用语音处理">
      <div className="vo-welcome-art" aria-hidden="true"><AudioLines /><Waveform bars={13} /></div>
      <h2>把录音变成可以读、可以找、可以分享的文字</h2>
      <p>导入会议、课堂、访谈或通话录音，A-NAS 会在后台完成这些事：</p>
      <ol className="vo-welcome-steps">
        <li><strong>转写</strong><span>豆包语音识别普通话、英语和方言，带时间与标点</span></li>
        <li><strong>认出发言人</strong><span>分开每个人说的话；命名一次，之后的录音里自动认出</span></li>
        <li><strong>翻译</strong><span>逐句译成英语、日语等 15 种语言，与原文对照</span></li>
        <li><strong>按场景总结</strong><span>会议纪要、课堂笔记、访谈记录……按模板整理要点与待办</span></li>
      </ol>
      <button type="button" className="vo-button primary large" onClick={onImport}><Plus />导入第一段录音</button>
      {status && !status.recognizer.available && <p className="vo-welcome-note"><CircleAlert />管理员还没有配置豆包语音，导入的录音会先排队等待。</p>}
    </section>
  );
}
