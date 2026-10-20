import { ChevronLeft, ChevronRight, Pause, Play, RotateCcw, X } from "lucide-react";
import { KeyboardEvent, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";

import { dayLabel, momentOf, reducedMotion } from "./model";
import { displayURL, type PhotoAsset } from "./photosApi";

// How long each photo stays once shown, and how long two photos cross-fade.
const slideDelay = 4000;
const fadeTime = 900;
const controlsDelay = 2500;
// How long the end waits for the view below to load the photos after it.
const moreWait = 8000;
// A move still decoding after this long shows that it is on its way; one
// that never finishes shows the photo as the browser manages after decodeLimit.
const slowDecode = 1200;
const decodeLimit = 15000;

interface Props {
  // The photos and videos of the view the slideshow started from, in its
  // order; the list grows while the view loads more. Only photos are shown.
  assets: PhotoAsset[];
  startId: string;
  // All photos of the view, when known.
  total?: number;
  // Whether the view may still load photos after the last of assets.
  more: boolean;
  // Keeps the view below at the photo shown, so that it loads what follows.
  onReveal: (assetId: string) => void;
  // Leaves the slideshow at the photo last shown.
  onExit: (assetId: string) => void;
}

// prepared keeps the next photos decoded, so that a move never shows a
// photo still loading.
type Prepared = Map<string, { image: HTMLImageElement; ready: Promise<void> }>;

function prepare(prepared: Prepared, asset: PhotoAsset) {
  let entry = prepared.get(asset.id);
  if (!entry) {
    const image = new Image();
    image.src = displayURL(asset);
    // A photo that cannot be decoded is shown as the browser manages.
    const decoded = typeof image.decode === "function" ? image.decode().catch(() => undefined)
      : new Promise<void>((resolve) => { image.onload = image.onerror = () => resolve(); });
    const ready = Promise.race([decoded, new Promise<void>((resolve) => window.setTimeout(resolve, decodeLimit))]);
    entry = { image, ready };
    prepared.set(asset.id, entry);
  }
  return entry.ready;
}

// Slideshow plays the photos of a view over the whole screen: each stays
// slideDelay once shown, the next is decoded before it fades in over the
// last, and the controls appear only while the pointer moves. It runs past
// the photos loaded so far by keeping the view below at the photo shown.
// Videos are passed over; started from one, it starts at the photo after it.
export function Slideshow({ assets: all, startId, total, more, onReveal, onExit }: Props) {
  const root = useRef<HTMLDivElement>(null);
  const assets = useMemo(() => all.filter((asset) => !asset.video), [all]);
  const [current, setCurrent] = useState<PhotoAsset | undefined>(() => {
    const start = Math.max(0, all.findIndex((asset) => asset.id === startId));
    return all.slice(start).find((asset) => !asset.video) ?? assets[0];
  });
  const [leaving, setLeaving] = useState<PhotoAsset>();
  const [paused, setPaused] = useState(false);
  const [ended, setEnded] = useState(false);
  const [awaitingMore, setAwaitingMore] = useState(false);
  const [slow, setSlow] = useState(false);
  const [controls, setControls] = useState(true);
  const fade = !reducedMotion();

  // Everything the timers act on is read through refs, so that renders of
  // the view below never restart them.
  const latest = useRef({ assets, all, current, more, onReveal, onExit });
  latest.current = { assets, all, current, more, onReveal, onExit };
  const prepared = useRef<Prepared>(new Map());
  const move = useRef(0);

  const position = current ? assets.findIndex((asset) => asset.id === current.id) : -1;

  // go shows the photo delta places away once it is decoded. Moving past the
  // end waits for the view to load more, or ends.
  const go = useCallback(async (delta: number) => {
    const { assets: list, all: loaded, current: shown, more: loading, onReveal: reveal } = latest.current;
    const from = shown ? list.findIndex((asset) => asset.id === shown.id) : -1;
    const target = list[from + delta];
    if (!target) {
      if (delta < 0) return;
      if (loading && loaded.length) {
        setAwaitingMore(true);
        reveal(loaded[loaded.length - 1].id);
      } else setEnded(true);
      return;
    }
    const token = ++move.current;
    const timer = window.setTimeout(() => setSlow(true), slowDecode);
    await prepare(prepared.current, target);
    window.clearTimeout(timer);
    setSlow(false);
    if (token !== move.current) return;
    setLeaving(shown);
    setCurrent(target);
  }, []);

  // The next photo starts decoding as soon as one is shown; ones far behind
  // are let go.
  useEffect(() => {
    if (!current) return;
    latest.current.onReveal(current.id);
    const next = assets[position + 1];
    if (next) void prepare(prepared.current, next);
    const keep = new Set(assets.slice(Math.max(0, position - 1), position + 3).map((asset) => asset.id));
    for (const id of prepared.current.keys()) if (!keep.has(id)) prepared.current.delete(id);
  }, [current?.id]);

  useEffect(() => {
    if (!leaving) return;
    const timer = window.setTimeout(() => setLeaving(undefined), fade ? fadeTime : 0);
    return () => window.clearTimeout(timer);
  }, [leaving]);

  // Each photo stays slideDelay after its fade; pausing stops the clock and
  // playing again gives the photo its full time.
  useEffect(() => {
    if (paused || ended || awaitingMore || !current) return;
    const timer = window.setTimeout(() => void go(1), slideDelay + (fade ? fadeTime : 0));
    return () => window.clearTimeout(timer);
  }, [current?.id, paused, ended, awaitingMore, go]);

  // At the end of what is loaded, the view below loads the photos after it,
  // and past any videos after those.
  useEffect(() => {
    if (!awaitingMore) return;
    if (assets.length > position + 1) { setAwaitingMore(false); void go(1); return; }
    if (!more) { setAwaitingMore(false); setEnded(true); return; }
    if (all.length) latest.current.onReveal(all[all.length - 1].id);
    const timer = window.setTimeout(() => { setAwaitingMore(false); setEnded(true); }, moreWait);
    return () => window.clearTimeout(timer);
  }, [awaitingMore, assets.length, all.length, more]);

  // A view of videos alone has nothing to show.
  useEffect(() => { if (!current) onExit(startId); }, []);

  // The controls show while the pointer moves, and stay while paused.
  const idle = useRef(0);
  const wake = useCallback(() => {
    setControls(true);
    window.clearTimeout(idle.current);
    idle.current = window.setTimeout(() => setControls(false), controlsDelay);
  }, []);
  useEffect(() => { wake(); return () => window.clearTimeout(idle.current); }, [wake]);

  const exit = useCallback(() => {
    const shown = latest.current.current;
    if (shown) latest.current.onExit(shown.id);
  }, []);

  // Full screen when the browser allows it; leaving full screen leaves the
  // slideshow too.
  useLayoutEffect(() => {
    const element = root.current;
    element?.focus();
    let entered = false;
    const changed = () => {
      if (document.fullscreenElement === element) entered = true;
      else if (entered) exit();
    };
    document.addEventListener("fullscreenchange", changed);
    element?.requestFullscreen?.().catch(() => undefined);
    return () => {
      document.removeEventListener("fullscreenchange", changed);
      if (document.fullscreenElement === element) void document.exitFullscreen?.().catch(() => undefined);
    };
  }, [exit]);

  const step = (delta: number) => { wake(); setEnded(false); void go(delta); };
  const restart = () => {
    const first = latest.current.assets[0];
    if (!first) return;
    setEnded(false);
    setPaused(false);
    void prepare(prepared.current, first).then(() => { setLeaving(latest.current.current); setCurrent(first); });
  };
  const keyDown = (event: KeyboardEvent) => {
    const handled = () => { event.preventDefault(); event.stopPropagation(); };
    switch (event.key) {
      case "Escape": handled(); exit(); break;
      case " ": handled(); wake(); setPaused(!paused); break;
      case "ArrowRight": handled(); step(1); break;
      case "ArrowLeft": handled(); step(-1); break;
    }
  };

  if (!current) return null;
  const moment = momentOf(current);
  // The view's total counts its videos too; without them the count is known
  // once everything is loaded.
  const count = !more ? assets.length : all.some((asset) => asset.video) ? undefined : total;
  const shownChrome = controls || paused || ended;
  return createPortal(
    <div
      ref={root}
      className={`ph-slideshow${shownChrome ? " chrome" : ""}${fade ? "" : " still"}`}
      role="dialog"
      aria-label="幻灯片"
      tabIndex={-1}
      onKeyDown={keyDown}
      onPointerMove={wake}
      onPointerDown={wake}
    >
      {leaving && leaving.id !== current.id && <img key={`leaving-${leaving.id}`} className="ph-slide leaving" src={displayURL(leaving)} alt="" draggable={false} />}
      <img key={current.id} className="ph-slide" src={displayURL(current)} alt={current.name} draggable={false} />

      <header className="ph-slideshow-bar">
        <div className="ph-slideshow-caption">
          <strong>{dayLabel(moment)}</strong>
          <small>{moment.toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit" })} · {count ? `${position + 1} / ${count}` : `第 ${position + 1} 张`}</small>
        </div>
        <button type="button" aria-label="退出幻灯片" title="退出 (Esc)" onClick={exit}><X /></button>
      </header>
      {!ended && <div className="ph-slideshow-controls" role="toolbar" aria-label="幻灯片控制">
        <button type="button" aria-label="上一张" title="上一张 (←)" disabled={position <= 0} onClick={() => step(-1)}><ChevronLeft /></button>
        <button type="button" className="main" aria-label={paused ? "继续播放" : "暂停"} title={paused ? "继续 (空格)" : "暂停 (空格)"} onClick={() => { wake(); setPaused(!paused); }}>{paused ? <Play /> : <Pause />}</button>
        <button type="button" aria-label="下一张" title="下一张 (→)" onClick={() => step(1)}><ChevronRight /></button>
      </div>}
      {!ended && <div className="ph-slideshow-progress" aria-hidden="true"><span key={`${current.id}:${paused}`} className={paused || awaitingMore ? "paused" : ""} style={{ animationDuration: `${slideDelay + (fade ? fadeTime : 0)}ms` }} /></div>}
      {(slow || awaitingMore) && !ended && <div className="ph-slideshow-wait" role="status"><span className="ph-spinner" />{awaitingMore ? "正在载入后面的照片" : "正在载入下一张"}</div>}
      {ended && (
        <div className="ph-slideshow-end" role="status">
          <strong>放完了</strong>
          <small>{`共 ${assets.length} 张照片`}</small>
          <div>
            <button type="button" onClick={restart}><RotateCcw />从头再放</button>
            <button type="button" className="primary" onClick={exit}>退出</button>
          </div>
        </div>
      )}
    </div>,
    document.body,
  );
}
