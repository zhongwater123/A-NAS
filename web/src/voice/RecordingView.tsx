import {
  CircleAlert, Download, Ellipsis, FileText, Gauge, Languages, Loader2, Pause, Play, RefreshCw, RotateCcw, RotateCw, ScrollText, Sparkles, Trash2,
} from "lucide-react";
import { type KeyboardEvent, type PointerEvent, useCallback, useEffect, useMemo, useRef, useState } from "react";

import { messageOf, Spinner, useVoice, VoiceMark, Waveform } from "./context";
import { clock, download, duration, type ExportKind, exportText, voiceColor } from "./format";
import { Popover } from "./Popover";
import { rates, usePlayer, type Player } from "./player";
import { ProcessingCard } from "./ProcessingCard";
import { SpeakerMenu } from "./SpeakerMenu";
import { SummaryView } from "./SummaryView";
import { Transcript } from "./Transcript";
import { TranslationView } from "./TranslationView";
import { busy, deleteRecording, getRecording, type RecordingDetail, renameRecording, type Speaker } from "./voiceApi";

type Tab = "transcript" | "translation" | "summary";

// RecordingView is one recording: its player and speakers above the
// transcript, translations or summaries.
export function RecordingView({ id, onDeleted }: { id: string; onDeleted: () => void }) {
  const voice = useVoice();
  const [detail, setDetail] = useState<RecordingDetail>();
  const [error, setError] = useState("");
  const [tab, setTab] = useState<Tab>("transcript");
  const player = usePlayer(detail);

  const load = useCallback(async () => {
    try {
      setDetail(await getRecording(id));
      setError("");
    } catch (caught) {
      setError(messageOf(caught, "无法读取这段录音"));
    }
  }, [id]);
  useEffect(() => { void load(); }, [load]);
  const moving = detail ? busy(detail) : false;
  useEffect(() => {
    if (!moving) return;
    const timer = window.setInterval(() => void load(), 2000);
    return () => window.clearInterval(timer);
  }, [moving, load]);
  // When processing ends the list shows the new title and speakers.
  const wasMoving = useRef(moving);
  useEffect(() => {
    if (wasMoving.current && !moving) voice.changed();
    wasMoving.current = moving;
  }, [moving, voice]);

  const update = useCallback((next: RecordingDetail) => setDetail(next), []);

  if (error && !detail) {
    return <div className="vo-center"><CircleAlert /><p>{error}</p><button type="button" className="vo-button" onClick={() => void load()}>重试</button></div>;
  }
  if (!detail) return <div className="vo-center"><Spinner /></div>;
  const ready = detail.status === "ready";

  return (
    <div className="vo-recording">
      <Header detail={detail} onRenamed={(title) => { setDetail({ ...detail, title, titleEdited: true }); voice.changed(); }} onDeleted={onDeleted} />
      <PlayerBar detail={detail} player={player} />
      {ready && detail.speakerList.length > 0 && <SpeakerStrip detail={detail} onChange={update} />}
      {ready ? (
        <>
          <div className="vo-tabs" role="tablist" aria-label="内容">
            <TabButton id="transcript" tab={tab} onSelect={setTab} icon={<ScrollText />} label="转写" />
            <TabButton id="translation" tab={tab} onSelect={setTab} icon={<Languages />} label="翻译"
              working={detail.translations.some((item) => item.status === "queued" || item.status === "running")} count={detail.translations.length} />
            <TabButton id="summary" tab={tab} onSelect={setTab} icon={<Sparkles />} label="总结"
              working={detail.summaries.some((item) => item.status === "queued" || item.status === "running")} count={detail.summaries.length} />
          </div>
          <div className="vo-body">
            {tab === "transcript" && <Transcript detail={detail} player={player} onChange={update} />}
            {tab === "translation" && <TranslationView detail={detail} player={player} onChange={update} />}
            {tab === "summary" && <SummaryView detail={detail} player={player} onChange={update} />}
          </div>
        </>
      ) : (
        <div className="vo-body"><ProcessingCard detail={detail} onChange={() => void load()} /></div>
      )}
    </div>
  );
}

function TabButton({ id, tab, onSelect, icon, label, working, count }: { id: Tab; tab: Tab; onSelect: (tab: Tab) => void; icon: React.ReactNode; label: string; working?: boolean; count?: number }) {
  return (
    <button type="button" role="tab" aria-selected={tab === id} className={tab === id ? "active" : ""} onClick={() => onSelect(id)}>
      {icon}<span>{label}</span>
      {working ? <Loader2 className="vo-spin" aria-label="处理中" /> : count ? <small>{count}</small> : null}
    </button>
  );
}

function Header({ detail, onRenamed, onDeleted }: { detail: RecordingDetail; onRenamed: (title: string) => void; onDeleted: () => void }) {
  const voice = useVoice();
  const [editing, setEditing] = useState(false);
  const [title, setTitle] = useState(detail.title);
  const [confirming, setConfirming] = useState(false);
  useEffect(() => { if (!editing) setTitle(detail.title); }, [detail.title, editing]);
  const save = async () => {
    setEditing(false);
    const next = title.trim();
    if (!next || next === detail.title) { setTitle(detail.title); return; }
    try {
      const saved = await renameRecording(detail.id, next);
      onRenamed(saved.title);
    } catch (caught) {
      setTitle(detail.title);
      voice.toast(messageOf(caught, "改名失败"), "error");
    }
  };
  const remove = async () => {
    try {
      await deleteRecording(detail.id);
      voice.toast("已从语音处理中移除，录音文件仍在原处");
      onDeleted();
    } catch (caught) {
      voice.toast(messageOf(caught, "移除失败"), "error");
    }
  };
  const exportAs = (kind: ExportKind) => download(exportText(detail, kind));
  const created = new Date(detail.createdAt).toLocaleString("zh-CN", { year: "numeric", month: "long", day: "numeric", hour: "2-digit", minute: "2-digit", hour12: false });
  return (
    <header className="vo-header">
      <div className="vo-title">
        {editing ? (
          <input
            className="vo-title-input" value={title} maxLength={120} autoFocus aria-label="录音标题"
            onChange={(event) => setTitle(event.target.value)} onBlur={() => void save()}
            onKeyDown={(event: KeyboardEvent<HTMLInputElement>) => {
              if (event.key === "Enter") event.currentTarget.blur();
              if (event.key === "Escape") { setTitle(detail.title); setEditing(false); }
            }}
          />
        ) : (
          <h2><button type="button" title="点击修改标题" onClick={() => setEditing(true)}>{detail.title}</button></h2>
        )}
        <p>
          <span>{created}</span>
          <span>{duration(detail.durationMs)}</span>
          {detail.status === "ready" && <span>{detail.speakerList.length} 位发言人</span>}
          <span className="vo-file" title={detail.name}><FileText />{detail.name}</span>
        </p>
      </div>
      <div className="vo-header-actions">
        {detail.status === "ready" && (
          <Popover label="导出" trigger={<><Download /><span>导出</span></>} className="vo-button">
            {(close) => (
              <div className="vo-menu" role="menu">
                <p className="vo-menu-title">转写</p>
                <button type="button" role="menuitem" onClick={() => { exportAs("transcript-txt"); close(); }}>纯文本 (.txt)</button>
                <button type="button" role="menuitem" onClick={() => { exportAs("transcript-md"); close(); }}>Markdown (.md)</button>
                <button type="button" role="menuitem" onClick={() => { exportAs("transcript-srt"); close(); }}>字幕 (.srt)</button>
                <p className="vo-menu-hint">翻译与总结在各自的页面里导出</p>
              </div>
            )}
          </Popover>
        )}
        <Popover label="更多" trigger={<Ellipsis />} className="vo-icon-button">
          {(close) => (
            <div className="vo-menu" role="menu">
              <button type="button" role="menuitem" className="danger" onClick={() => { close(); setConfirming(true); }}><Trash2 />从语音处理中移除</button>
            </div>
          )}
        </Popover>
      </div>
      {confirming && (
        <div className="vo-dialog-backdrop" onPointerDown={(event) => { if (event.target === event.currentTarget) setConfirming(false); }}>
          <div className="vo-dialog small" role="alertdialog" aria-modal="true" aria-label="移除录音">
            <h3>从语音处理中移除“{detail.title}”？</h3>
            <p>转写、翻译、总结和这段录音教给声纹的内容都会删除，录音文件 <strong>{detail.name}</strong> 仍留在原处。</p>
            <div className="vo-dialog-actions">
              <button type="button" className="vo-button" onClick={() => setConfirming(false)}>取消</button>
              <button type="button" className="vo-button danger" onClick={() => { setConfirming(false); void remove(); }}>移除</button>
            </div>
          </div>
        </div>
      )}
    </header>
  );
}

// PlayerBar plays the recording; its track shows who speaks when.
function PlayerBar({ detail, player }: { detail: RecordingDetail; player: Player }) {
  const track = useRef<HTMLDivElement>(null);
  const [hover, setHover] = useState<number>();
  const total = Math.max(1, detail.durationMs);
  const numbers = useMemo(() => new Map(detail.speakerList.map((speaker) => [speaker.key, speaker.number])), [detail.speakerList]);
  const at = (event: PointerEvent<HTMLDivElement>) => {
    const box = track.current?.getBoundingClientRect();
    if (!box) return 0;
    return Math.min(1, Math.max(0, (event.clientX - box.left) / box.width)) * total;
  };
  const position = Math.min(player.timeMs, total);
  return (
    <div className="vo-player">
      <button type="button" className="vo-icon-button" aria-label="后退 15 秒" title="后退 15 秒" onClick={() => player.skip(-15000)}><RotateCcw /></button>
      <button type="button" className="vo-play" aria-label={player.playing ? "暂停" : "播放"} onClick={player.toggle}>
        {player.loading && player.playing ? <Loader2 className="vo-spin" /> : player.playing ? <Pause /> : <Play />}
      </button>
      <button type="button" className="vo-icon-button" aria-label="前进 15 秒" title="前进 15 秒" onClick={() => player.skip(15000)}><RotateCw /></button>
      <span className="vo-time">{clock(position)}</span>
      <div
        ref={track}
        className="vo-track"
        role="slider"
        tabIndex={0}
        aria-label="播放位置"
        aria-valuemin={0}
        aria-valuemax={Math.round(total / 1000)}
        aria-valuenow={Math.round(position / 1000)}
        aria-valuetext={clock(position)}
        onPointerMove={(event) => setHover(at(event))}
        onPointerLeave={() => setHover(undefined)}
        onPointerDown={(event) => player.seek(at(event))}
        onKeyDown={(event) => {
          if (event.key === "ArrowRight") player.skip(5000);
          if (event.key === "ArrowLeft") player.skip(-5000);
        }}
      >
        <div className="vo-track-lanes" aria-hidden="true">
          {detail.utterances.map((utterance) => (
            <i key={utterance.seq} style={{
              left: `${utterance.startMs / total * 100}%`, width: `${Math.max(0.15, (utterance.endMs - utterance.startMs) / total * 100)}%`,
              background: voiceColor(numbers.get(utterance.speaker) ?? 1),
            }} />
          ))}
        </div>
        <div className="vo-track-played" style={{ width: `${position / total * 100}%` }} />
        <div className="vo-track-knob" style={{ left: `${position / total * 100}%` }} />
        {hover !== undefined && <span className="vo-track-hint" style={{ left: `${hover / total * 100}%` }}>{clock(hover)}</span>}
      </div>
      <span className="vo-time end">{clock(detail.durationMs)}</span>
      <Popover label="播放速度" trigger={<><Gauge /><span>{player.rate}×</span></>} className="vo-rate">
        {(close) => (
          <div className="vo-menu narrow" role="menu">
            {[...rates].sort((a, b) => a - b).map((rate) => (
              <button key={rate} type="button" role="menuitemradio" aria-checked={player.rate === rate} className={player.rate === rate ? "checked" : ""} onClick={() => { player.setRate(rate); close(); }}>{rate}×</button>
            ))}
          </div>
        )}
      </Popover>
      {player.failed && <span className="vo-player-error" role="alert"><CircleAlert />无法播放，文件可能已被移动或删除</span>}
    </div>
  );
}

// SpeakerStrip lists the voices with how much each spoke; a click names one.
function SpeakerStrip({ detail, onChange }: { detail: RecordingDetail; onChange: (detail: RecordingDetail) => void }) {
  const total = detail.speakerList.reduce((sum, speaker) => sum + speaker.speechMs, 0) || 1;
  return (
    <div className="vo-speakers" aria-label="发言人">
      {detail.speakerList.map((speaker: Speaker) => (
        <SpeakerMenu key={speaker.key} detail={detail} speaker={speaker} onChange={onChange}>
          <span className="vo-speaker-chip-inner">
            <VoiceMark speaker={speaker} size={22} />
            <span className="vo-speaker-name">{speaker.label}</span>
            <small>{Math.round(speaker.speechMs / total * 100)}%</small>
            {speaker.suggestion && <em className="vo-suggest">可能是 {speaker.suggestion.name}？</em>}
            {speaker.assigned === "auto" && <em className="vo-auto" title="按声纹自动识别">声纹</em>}
          </span>
        </SpeakerMenu>
      ))}
      {!detail.voiceprints && <span className="vo-speakers-note" title="声纹组件不可用时，发言人只按识别服务分开，不会跨录音认出">未使用声纹</span>}
    </div>
  );
}

export function RefreshButton({ onClick, label }: { onClick: () => void; label: string }) {
  return <button type="button" className="vo-button subtle" onClick={onClick}><RefreshCw />{label}</button>;
}

export { Waveform };
