import { ChevronDown, ChevronUp, Copy, Crosshair, Pencil, Play, Search, UserRound, X } from "lucide-react";
import { type KeyboardEvent, type MouseEvent, ReactNode, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";

import { messageOf, useVoice, VoiceMark } from "./context";
import { clock, paragraphs } from "./format";
import type { Player } from "./player";
import { SpeakerMenu } from "./SpeakerMenu";
import { editUtterance, type RecordingDetail, type Speaker, type Utterance } from "./voiceApi";

interface Props {
  detail: RecordingDetail;
  player: Player;
  onChange: (detail: RecordingDetail) => void;
}

// activeSeq is the sentence being played, if any.
export function activeSeq(utterances: Utterance[], timeMs: number): number | undefined {
  let found: number | undefined;
  for (const utterance of utterances) {
    if (utterance.startMs > timeMs) break;
    if (timeMs < utterance.endMs + 400) found = utterance.seq;
  }
  return found;
}

// highlight marks every match of words in text.
export function highlight(text: string, words: string): ReactNode {
  if (!words) return text;
  const parts: ReactNode[] = [];
  const lower = text.toLowerCase();
  let at = 0;
  for (let found = lower.indexOf(words); found >= 0; found = lower.indexOf(words, found + words.length)) {
    if (found > at) parts.push(text.slice(at, found));
    parts.push(<mark key={found}>{text.slice(found, found + words.length)}</mark>);
    at = found + words.length;
  }
  parts.push(text.slice(at));
  return parts;
}

interface SentenceMenu { x: number; y: number; utterance: Utterance }

// Transcript is the recording's sentences by speaker turn. A click plays from
// a sentence, a double click corrects it, and the context menu moves it to
// another speaker.
export function Transcript({ detail, player, onChange }: Props) {
  const voice = useVoice();
  const [query, setQuery] = useState("");
  const [follow, setFollow] = useState(true);
  const [editing, setEditing] = useState<number>();
  const [menu, setMenu] = useState<SentenceMenu>();
  const [hit, setHit] = useState(0);
  const scroller = useRef<HTMLDivElement>(null);
  const speakers = useMemo(() => new Map(detail.speakerList.map((speaker) => [speaker.key, speaker])), [detail.speakerList]);
  const turns = useMemo(() => paragraphs(detail.utterances), [detail.utterances]);
  const words = query.trim().toLowerCase();
  const hits = useMemo(() => words ? detail.utterances.filter((utterance) => utterance.text.toLowerCase().includes(words)).map((utterance) => utterance.seq) : [], [detail.utterances, words]);
  const playing = player.playing ? activeSeq(detail.utterances, player.timeMs) : undefined;
  const current = playing ?? (player.timeMs > 0 ? activeSeq(detail.utterances, player.timeMs) : undefined);

  useEffect(() => { setHit(0); }, [words]);
  // Following keeps the sentence being played in view.
  useEffect(() => {
    if (!follow || playing === undefined || words) return;
    const element = scroller.current?.querySelector<HTMLElement>(`[data-seq="${playing}"]`);
    const box = scroller.current?.getBoundingClientRect();
    const own = element?.getBoundingClientRect();
    if (element && box && own && (own.top < box.top + 60 || own.bottom > box.bottom - 60)) element.scrollIntoView({ block: "center", behavior: "smooth" });
  }, [playing, follow, words]);
  useLayoutEffect(() => {
    if (!hits.length) return;
    scroller.current?.querySelector(`[data-seq="${hits[hit % hits.length]}"]`)?.scrollIntoView({ block: "center" });
  }, [hit, hits]);

  const save = async (utterance: Utterance, change: { text?: string; speaker?: string }) => {
    try {
      onChange(await editUtterance(detail.id, utterance.seq, change));
      if (change.speaker) voice.toast(`已改为${speakers.get(change.speaker)?.label ?? "另一位发言人"}说的`);
    } catch (caught) {
      voice.toast(messageOf(caught, "保存失败"), "error");
    }
  };

  if (!detail.utterances.length) {
    return <div className="vo-empty"><UserRound /><h3>没有识别到说话</h3><p>这段录音里可能只有音乐、噪音或很小的声音。</p></div>;
  }

  return (
    <div className="vo-transcript">
      <div className="vo-toolbar">
        <div className={`vo-search inline${query ? " active" : ""}`}>
          <Search />
          <input type="search" value={query} placeholder="在转写中查找" aria-label="在转写中查找" onChange={(event) => setQuery(event.target.value)}
            onKeyDown={(event) => { if (event.key === "Enter" && hits.length) setHit((value) => value + (event.shiftKey ? hits.length - 1 : 1)); }} />
          {words && <small>{hits.length ? `${(hit % hits.length) + 1}/${hits.length}` : "无结果"}</small>}
          {hits.length > 1 && <>
            <button type="button" aria-label="上一个" onClick={() => setHit((value) => value + hits.length - 1)}><ChevronUp /></button>
            <button type="button" aria-label="下一个" onClick={() => setHit((value) => value + 1)}><ChevronDown /></button>
          </>}
          {query && <button type="button" aria-label="清除" onClick={() => setQuery("")}><X /></button>}
        </div>
        <button type="button" className={`vo-toggle${follow ? " on" : ""}`} aria-pressed={follow} onClick={() => setFollow(!follow)} title="播放时自动滚动到正在播放的句子">
          <Crosshair />跟随播放
        </button>
        <span className="vo-toolbar-hint">点击句子从那里播放 · 双击修改</span>
      </div>
      <div ref={scroller} className="vo-scroll" onScroll={() => setMenu(undefined)}>
        {turns.map((turn) => {
          const speaker = speakers.get(turn.speaker) ?? fallbackSpeaker(turn.speaker);
          return (
            <article key={turn.utterances[0].seq} className="vo-turn">
              <SpeakerMenu detail={detail} speaker={speaker} onChange={onChange} className="vo-turn-mark">
                <VoiceMark speaker={speaker} size={32} />
              </SpeakerMenu>
              <div className="vo-turn-body">
                <header>
                  <strong>{speaker.label}</strong>
                  <button type="button" className="vo-stamp" onClick={() => player.play(turn.startMs)}>{clock(turn.startMs)}</button>
                </header>
                <p>
                  {turn.utterances.map((utterance) => editing === utterance.seq ? (
                    <SentenceEditor key={utterance.seq} utterance={utterance} onDone={(text) => {
                      setEditing(undefined);
                      if (text !== undefined && text !== utterance.text) void save(utterance, { text });
                    }} />
                  ) : (
                    <span
                      key={utterance.seq}
                      data-seq={utterance.seq}
                      className={`vo-sentence${current === utterance.seq ? " active" : ""}${hits[hit % (hits.length || 1)] === utterance.seq ? " hit" : ""}${utterance.edited ? " edited" : ""}`}
                      onClick={() => player.play(utterance.startMs)}
                      onDoubleClick={() => setEditing(utterance.seq)}
                      onContextMenu={(event: MouseEvent) => {
                        event.preventDefault();
                        const box = scroller.current?.getBoundingClientRect();
                        setMenu({ x: event.clientX - (box?.left ?? 0), y: event.clientY - (box?.top ?? 0) + (scroller.current?.scrollTop ?? 0), utterance });
                      }}
                    >
                      {highlight(utterance.text, words)}
                    </span>
                  ))}
                </p>
              </div>
            </article>
          );
        })}
        {menu && (
          <SentenceActions
            menu={menu}
            speakers={detail.speakerList}
            onClose={() => setMenu(undefined)}
            onPlay={() => player.play(menu.utterance.startMs)}
            onEdit={() => setEditing(menu.utterance.seq)}
            onMove={(key) => void save(menu.utterance, { speaker: key })}
            onCopy={() => { void navigator.clipboard?.writeText(menu.utterance.text); voice.toast("已复制"); }}
          />
        )}
      </div>
    </div>
  );
}

function fallbackSpeaker(key: string): Speaker {
  return { key, number: 1, label: "发言人", speechMs: 0, voiceprint: false };
}

function SentenceEditor({ utterance, onDone }: { utterance: Utterance; onDone: (text?: string) => void }) {
  const [text, setText] = useState(utterance.text);
  const ref = useRef<HTMLTextAreaElement>(null);
  useLayoutEffect(() => {
    const element = ref.current;
    if (!element) return;
    element.focus();
    element.select();
  }, []);
  useLayoutEffect(() => {
    const element = ref.current;
    if (element) { element.style.height = "auto"; element.style.height = `${element.scrollHeight}px`; }
  }, [text]);
  return (
    <textarea
      ref={ref} className="vo-sentence-editor" value={text} maxLength={4000} aria-label="修改这句话"
      onChange={(event) => setText(event.target.value)}
      onBlur={() => onDone(text.trim() || undefined)}
      onKeyDown={(event: KeyboardEvent<HTMLTextAreaElement>) => {
        if (event.key === "Enter" && !event.shiftKey) { event.preventDefault(); event.currentTarget.blur(); }
        if (event.key === "Escape") { event.preventDefault(); onDone(undefined); }
      }}
    />
  );
}

function SentenceActions({ menu, speakers, onClose, onPlay, onEdit, onMove, onCopy }: {
  menu: SentenceMenu; speakers: Speaker[]; onClose: () => void; onPlay: () => void; onEdit: () => void; onMove: (key: string) => void; onCopy: () => void;
}) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const outside = (event: Event) => { if (!ref.current?.contains(event.target as Node)) onClose(); };
    const escape = (event: globalThis.KeyboardEvent) => { if (event.key === "Escape") onClose(); };
    window.addEventListener("pointerdown", outside, true);
    window.addEventListener("keydown", escape);
    return () => { window.removeEventListener("pointerdown", outside, true); window.removeEventListener("keydown", escape); };
  }, [onClose]);
  const act = (run: () => void) => () => { run(); onClose(); };
  return (
    <div ref={ref} className="vo-menu floating" role="menu" style={{ left: menu.x, top: menu.y }}>
      <button type="button" role="menuitem" onClick={act(onPlay)}><Play />从这里播放</button>
      <button type="button" role="menuitem" onClick={act(onEdit)}><Pencil />修改文字</button>
      <button type="button" role="menuitem" onClick={act(onCopy)}><Copy />复制这句</button>
      {speakers.length > 1 && <>
        <p className="vo-menu-title">这句是谁说的</p>
        {speakers.map((speaker) => (
          <button key={speaker.key} type="button" role="menuitemradio" aria-checked={speaker.key === menu.utterance.speaker}
            className={speaker.key === menu.utterance.speaker ? "checked" : ""} onClick={act(() => { if (speaker.key !== menu.utterance.speaker) onMove(speaker.key); })}>
            <VoiceMark speaker={speaker} size={18} />{speaker.label}
          </button>
        ))}
      </>}
    </div>
  );
}
