import { ArrowLeft, ChevronLeft, ChevronRight, CircleDot, Download, FolderPlus, Info, Play, Presentation, ScanSearch, Square, Trash2 } from "lucide-react";
import { KeyboardEvent, useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { TransformComponent, TransformWrapper, type ReactZoomPanPinchContentRef } from "react-zoom-pan-pinch";

import { type Caller, canChange, dayLabel, momentOf, reducedMotion } from "./model";
import { PhotoDetails, type DetailsActions } from "./PhotoDetails";
import { getPhoto, displayURL, motionURL, previewURL, thumbnailURL, type PhotoAsset } from "./photosApi";
import { VideoPlayer, type VideoControls } from "./VideoPlayer";

export interface ViewerActions extends DetailsActions {
  trash: (asset: PhotoAsset) => void;
  addToAlbum: (assets: PhotoAsset[]) => void;
  download: (assets: PhotoAsset[]) => void;
  // Leaves the viewer for the photos that look like this one.
  findSimilar: (asset: PhotoAsset) => void;
  // Plays the view's photos full screen from this one.
  slideshow: (asset: PhotoAsset) => void;
}

interface Props {
  assets: PhotoAsset[];
  index: number;
  onIndex: (index: number) => void;
  onClose: () => void;
  // Where the photo opened from and where it would close to, for the
  // transition between grid and viewer.
  origin?: DOMRect;
  returnRect: (assetId: string) => Promise<DOMRect | undefined>;
  caller: Caller;
  info: boolean;
  onInfo: (open: boolean) => void;
  // A changed photo, such as a new name, for the views to show.
  onChanged: (asset: PhotoAsset) => void;
  actions: ViewerActions;
}

const idleDelay = 2600;
const filmstripReach = 40;

// containRect fits a photo of this aspect into a box, as object-fit does.
function containRect(box: DOMRect, aspect: number) {
  const width = Math.min(box.width, box.height * aspect);
  const height = width / aspect;
  return new DOMRect(box.left + (box.width - width) / 2, box.top + (box.height - height) / 2, width, height);
}

// fly animates a copy of the photo between two places on screen.
function fly(root: HTMLElement, src: string, from: DOMRect, to: DOMRect, reverse: boolean) {
  const frame = root.getBoundingClientRect();
  const image = document.createElement("img");
  image.src = src;
  image.className = "ph-fly";
  Object.assign(image.style, { left: `${to.left - frame.left}px`, top: `${to.top - frame.top}px`, width: `${to.width}px`, height: `${to.height}px` });
  root.appendChild(image);
  const offset = `translate(${from.left - to.left}px, ${from.top - to.top}px) scale(${from.width / to.width}, ${from.height / to.height})`;
  const frames = [{ transform: offset, borderRadius: "6px" }, { transform: "none", borderRadius: "0px" }];
  const animation = image.animate(reverse ? frames.reverse() : frames, { duration: 300, easing: "cubic-bezier(.2,.8,.2,1)", fill: "forwards" });
  return animation.finished.finally(() => image.remove());
}

// Viewer shows one photo or video at a time over the whole window.
export function Viewer({ assets, index, onIndex, onClose, origin, returnRect, caller, info, onInfo, onChanged, actions }: Props) {
  const asset = assets[index];
  const root = useRef<HTMLDivElement>(null);
  const stage = useRef<HTMLDivElement>(null);
  const zoom = useRef<ReactZoomPanPinchContentRef>(null);
  const player = useRef<VideoControls>(null);
  const [details, setDetails] = useState<PhotoAsset>();
  const [originalReady, setOriginalReady] = useState(false);
  const [entering, setEntering] = useState(Boolean(origin));
  const [closing, setClosing] = useState(false);
  const [idle, setIdle] = useState(false);
  const [livePlaying, setLivePlaying] = useState(false);
  const [liveError, setLiveError] = useState(false);
  const [zoomed, setZoomed] = useState(false);
  const motion = !reducedMotion();
  const editable = asset ? canChange(caller, asset) : false;

  useEffect(() => {
    setDetails(undefined);
    setLivePlaying(false);
    setLiveError(false);
    setOriginalReady(false);
    setZoomed(false);
    if (!asset) return;
    // The neighbours' originals load ahead.
    for (const neighbour of [assets[index + 1], assets[index - 1]]) if (neighbour) new Image().src = displayURL(neighbour);
  }, [asset?.id]);
  // Details carry tags and albums; lists leave them out. A photo that became
  // a Live Photo meanwhile is asked for again.
  useEffect(() => {
    if (!asset) return;
    let live = true;
    getPhoto(asset.id).then((value) => { if (live) setDetails(value); }, () => undefined);
    return () => { live = false; };
  }, [asset?.id, asset?.motion?.kind, asset?.liveVideoMissing]);

  // Opening grows the photo from its tile.
  useLayoutEffect(() => {
    root.current?.focus();
    if (!origin || !asset || !motion || !stage.current || typeof HTMLElement.prototype.animate !== "function") { setEntering(false); return; }
    const target = containRect(stage.current.getBoundingClientRect(), asset.width / asset.height || 1);
    void fly(root.current!, previewURL(asset), origin, target, false).then(() => setEntering(false));
  }, []);

  const close = useCallback(async () => {
    setLivePlaying(false);
    if (closing) return;
    if (!asset || !motion || zoomed || !stage.current || typeof HTMLElement.prototype.animate !== "function") { onClose(); return; }
    setClosing(true);
    const from = containRect(stage.current.getBoundingClientRect(), asset.width / asset.height || 1);
    const to = await returnRect(asset.id);
    if (to && root.current) await fly(root.current, previewURL(asset), from, to, false).catch(() => undefined);
    onClose();
  }, [asset, closing, motion, zoomed, returnRect, onClose]);

  const go = useCallback((delta: number) => {
    const next = index + delta;
    if (next < 0 || next >= assets.length) return;
    setLivePlaying(false);
    onIndex(next);
  }, [index, assets.length, onIndex]);
  const slideshow = () => { setLivePlaying(false); actions.slideshow(asset); };

  // Controls step aside while the pointer rests.
  const idleTimer = useRef(0);
  const wake = useCallback(() => {
    setIdle(false);
    window.clearTimeout(idleTimer.current);
    idleTimer.current = window.setTimeout(() => setIdle(true), idleDelay);
  }, []);
  useEffect(() => { wake(); return () => window.clearTimeout(idleTimer.current); }, [wake]);

  // The filmstrip keeps the current photo in its middle.
  const strip = useRef<HTMLDivElement>(null);
  useEffect(() => {
    strip.current?.querySelector(".current")?.scrollIntoView?.({ inline: "center", block: "nearest", behavior: motion ? "smooth" : "auto" });
  }, [index, motion]);

  if (!asset) return null;
  const start = Math.max(0, index - filmstripReach);
  const film = assets.slice(start, index + filmstripReach + 1);
  const moment = momentOf(asset);
  const video = Boolean(asset.video);
  const keyDown = (event: KeyboardEvent) => {
    // Typing a tag keeps its keys; Escape there only leaves the field.
    if ((event.target as HTMLElement).closest("video") && event.key !== "Escape") return;
    const field = (event.target as HTMLElement).closest<HTMLElement>("input, textarea");
    if (field) { if (event.key === "Escape") { event.stopPropagation(); field.blur(); root.current?.focus(); } return; }
    const handled = () => { event.preventDefault(); event.stopPropagation(); };
    switch (event.key) {
      case "ArrowLeft": handled(); go(-1); break;
      case "ArrowRight": handled(); go(1); break;
      case "Escape": handled(); if (livePlaying) setLivePlaying(false); else void close(); break;
      case "i": case "I": handled(); onInfo(!info); break;
      case "Delete": case "Backspace": if (editable) { handled(); actions.trash(asset); } break;
    }
    // A video plays and pauses with the space bar; a photo starts the
    // slideshow, finds its likes and zooms.
    if (video) {
      switch (event.key) {
        case " ": handled(); player.current?.toggle(); break;
        case "m": case "M": handled(); player.current?.mute(); break;
        case "f": case "F": handled(); player.current?.fullscreen(); break;
      }
      return;
    }
    switch (event.key) {
      case " ": handled(); slideshow(); break;
      case "s": case "S": handled(); actions.findSimilar(asset); break;
      case "+": case "=": handled(); zoom.current?.zoomIn(0.5); break;
      case "-": handled(); zoom.current?.zoomOut(0.5); break;
      case "0": handled(); zoom.current?.resetTransform(); break;
    }
  };

  return (
    <div
      ref={root}
      className={`ph-viewer${idle && !info ? " idle" : ""}${idle ? " resting" : ""}${entering ? " entering" : ""}${closing ? " closing" : ""}${info ? " with-info" : ""}`}
      role="dialog"
      aria-label={`查看 ${asset.name}`}
      tabIndex={-1}
      onKeyDown={keyDown}
      onPointerMove={wake}
      onPointerDown={wake}
    >
      <div className="ph-viewer-main">
        <header className="ph-viewer-bar">
          <button type="button" aria-label="关闭查看" title="关闭 (Esc)" onClick={() => void close()}><ArrowLeft /></button>
          <div className="ph-viewer-title">
            <strong>{dayLabel(moment)}</strong>
            <small>{moment.toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit" })} · {asset.name}</small>
          </div>
          <div className="ph-viewer-tools">
            {asset.motion && <button type="button" className="ph-viewer-live" aria-label={livePlaying ? "停止动态照片" : "播放动态照片"} aria-pressed={livePlaying} onClick={() => { setLiveError(false); setLivePlaying(!livePlaying); }}>{livePlaying ? <Square /> : <CircleDot />}动态</button>}
            <button type="button" aria-label="播放幻灯片" title={video ? "幻灯片（只放照片）" : "幻灯片 (空格)"} onClick={slideshow}><Presentation /></button>
            {!video && <button type="button" aria-label="找相似" title="找相似的照片 (S)" onClick={() => actions.findSimilar(asset)}><ScanSearch /></button>}
            {!libraryReadOnly(caller, asset) && <button type="button" aria-label="加入相册" title="加入相册" onClick={() => actions.addToAlbum([asset])}><FolderPlus /></button>}
            <button type="button" aria-label={video ? "下载视频" : asset.motion ? "下载动态照片原件" : "下载原图"} title={video ? "下载视频原件" : asset.motion ? "下载动态照片原件" : "下载原图"} onClick={() => actions.download([asset])}><Download /></button>
            {editable && <button type="button" aria-label="移到回收站" title="移到回收站 (Delete)" onClick={() => actions.trash(asset)}><Trash2 /></button>}
            <button type="button" aria-label={info ? "隐藏信息" : "显示信息"} aria-pressed={info} title="信息 (I)" className={info ? "on" : ""} onClick={() => onInfo(!info)}><Info /></button>
          </div>
        </header>

        <div ref={stage} className={`ph-viewer-stage${video ? " video" : ""}`}>
          {video ? <VideoPlayer key={asset.id} ref={player} asset={asset} stage={stage} onDownload={() => actions.download([asset])} /> : <TransformWrapper
            key={asset.id}
            ref={zoom}
            minScale={1}
            maxScale={8}
            centerOnInit
            limitToBounds
            doubleClick={{ mode: "toggle", step: 1.4 }}
            wheel={{ step: 0.18 }}
            panning={{ velocityDisabled: true }}
            onTransform={(_, state) => setZoomed(state.scale > 1.01)}
          >
            <TransformComponent wrapperClass="ph-zoom-wrapper" contentClass="ph-zoom-content">
              <div className="ph-viewer-image">
                {asset.thumbnail === "ready" && !originalReady && <img className="ph-viewer-low" src={thumbnailURL(asset.id)} alt="" draggable={false} />}
                <img
                  className={`ph-viewer-full${originalReady ? " ready" : ""}`}
                  src={displayURL(asset)}
                  alt={asset.name}
                  draggable={false}
                  onLoad={() => setOriginalReady(true)}
                />
              </div>
            </TransformComponent>
          </TransformWrapper>}
          {livePlaying && <video key={asset.id} className="ph-motion-video" src={motionURL(asset.id)} aria-label="动态照片视频" autoPlay muted playsInline controls onEnded={() => setLivePlaying(false)} onError={() => { setLivePlaying(false); setLiveError(true); }} />}
          {liveError && <p className="ph-motion-error" role="status">动态片段暂时无法播放，仍可查看照片或下载原件。</p>}
          {index > 0 && <button type="button" className="ph-viewer-nav previous" aria-label="上一张" onClick={() => go(-1)}><ChevronLeft /></button>}
          {index < assets.length - 1 && <button type="button" className="ph-viewer-nav next" aria-label="下一张" onClick={() => go(1)}><ChevronRight /></button>}
        </div>

        <div ref={strip} className="ph-filmstrip" aria-label="胶片条">
          {film.map((item, offset) => (
            <button type="button" key={item.id} className={start + offset === index ? "current" : ""} aria-label={`跳到 ${item.name}`} aria-current={start + offset === index} onClick={() => onIndex(start + offset)} style={{ aspectRatio: `${Math.min(2, Math.max(0.5, (item.width || 1) / (item.height || 1)))}` }}>
              <img src={previewURL(item)} alt="" loading="lazy" draggable={false} />
              {item.video && <span className="ph-film-video" aria-hidden="true"><Play /></span>}
            </button>
          ))}
        </div>
      </div>

      <aside className="ph-viewer-info" aria-label="照片信息" aria-hidden={!info}>
        {info && <PhotoDetails asset={asset} details={details} caller={caller} actions={actions} onChanged={(value) => { setDetails(value); onChanged(value); }} />}
      </aside>
    </div>
  );
}

const libraryReadOnly = (caller: Caller, asset: PhotoAsset) => Boolean(caller.libraries.find((library) => library.id === asset.libraryId)?.viewing);
