import {
  ArrowLeft,
  ChevronLeft,
  ChevronRight,
  CircleDot,
  Download,
  FolderPlus,
  Info,
  Play,
  Presentation,
  ScanSearch,
  Square,
  Trash2,
} from "lucide-react";
import { KeyboardEvent, useCallback, useEffect, useEffectEvent, useLayoutEffect, useRef, useState } from "react";
import { type ReactZoomPanPinchContentRef, TransformComponent, TransformWrapper } from "react-zoom-pan-pinch";

import { displayURL, getPhoto, motionURL, type PhotoAsset, previewURL, thumbnailURL } from "./api";
import { type Caller, canChange, dayLabel, momentOf, reducedMotion } from "./model";
import { type DetailsActions, PhotoDetails } from "./PhotoDetails";
import styles from "./photos.module.css";
import { type VideoControls, VideoPlayer } from "./VideoPlayer";

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
  image.className = styles.phFly;
  Object.assign(image.style, {
    left: `${to.left - frame.left}px`,
    top: `${to.top - frame.top}px`,
    width: `${to.width}px`,
    height: `${to.height}px`,
  });
  root.appendChild(image);
  const offset = `translate(${from.left - to.left}px, ${from.top - to.top}px) scale(${from.width / to.width}, ${from.height / to.height})`;
  const frames = [
    { transform: offset, borderRadius: "6px" },
    { transform: "none", borderRadius: "0px" },
  ];
  const animation = image.animate(reverse ? frames.reverse() : frames, {
    duration: 300,
    easing: "cubic-bezier(.2,.8,.2,1)",
    fill: "forwards",
  });
  return animation.finished.finally(() => image.remove());
}

// Viewer shows one photo or video at a time over the whole window.
export function Viewer({
  assets,
  index,
  onIndex,
  onClose,
  origin,
  returnRect,
  caller,
  info,
  onInfo,
  onChanged,
  actions,
}: Props) {
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

  // Another photo starts without the last one's details, zoom or Live Photo.
  const assetId = asset?.id;
  const [shownId, setShownId] = useState(assetId);
  if (shownId !== assetId) {
    setShownId(assetId);
    setDetails(undefined);
    setLivePlaying(false);
    setLiveError(false);
    setOriginalReady(false);
    setZoomed(false);
  }
  // The neighbours' originals load ahead.
  const preloadNeighbours = useEffectEvent(() => {
    for (const neighbour of [assets[index + 1], assets[index - 1]])
      if (neighbour) new Image().src = displayURL(neighbour);
  });
  useEffect(() => {
    if (assetId) preloadNeighbours();
  }, [assetId]);
  // Details carry tags and albums; lists leave them out. A photo that became
  // a Live Photo meanwhile is asked for again.
  const motionKind = asset?.motion?.kind;
  const liveVideoMissing = asset?.liveVideoMissing;
  useEffect(() => {
    if (!assetId) return;
    let live = true;
    getPhoto(assetId).then(
      (value) => {
        if (live) setDetails(value);
      },
      () => undefined,
    );
    return () => {
      live = false;
    };
  }, [assetId, motionKind, liveVideoMissing]);

  // Opening grows the photo from its tile.
  const growFromTile = useEffectEvent(() => {
    root.current?.focus();
    if (!origin || !asset || !motion || !stage.current || typeof HTMLElement.prototype.animate !== "function") {
      setEntering(false);
      return;
    }
    const target = containRect(stage.current.getBoundingClientRect(), asset.width / asset.height || 1);
    void fly(root.current!, previewURL(asset), origin, target, false).then(() => setEntering(false));
  });
  useLayoutEffect(() => growFromTile(), []);

  const close = useCallback(async () => {
    setLivePlaying(false);
    if (closing) return;
    if (!asset || !motion || zoomed || !stage.current || typeof HTMLElement.prototype.animate !== "function") {
      onClose();
      return;
    }
    setClosing(true);
    const from = containRect(stage.current.getBoundingClientRect(), asset.width / asset.height || 1);
    const to = await returnRect(asset.id);
    if (to && root.current) await fly(root.current, previewURL(asset), from, to, false).catch(() => undefined);
    onClose();
  }, [asset, closing, motion, zoomed, returnRect, onClose]);

  const go = useCallback(
    (delta: number) => {
      const next = index + delta;
      if (next < 0 || next >= assets.length) return;
      setLivePlaying(false);
      onIndex(next);
    },
    [index, assets.length, onIndex],
  );
  const slideshow = () => {
    setLivePlaying(false);
    actions.slideshow(asset);
  };

  // Controls step aside while the pointer rests.
  const idleTimer = useRef(0);
  const wake = useCallback(() => {
    setIdle(false);
    window.clearTimeout(idleTimer.current);
    idleTimer.current = window.setTimeout(() => setIdle(true), idleDelay);
  }, []);
  useEffect(() => {
    idleTimer.current = window.setTimeout(() => setIdle(true), idleDelay);
    return () => window.clearTimeout(idleTimer.current);
  }, []);

  // The filmstrip keeps the current photo in its middle.
  const strip = useRef<HTMLDivElement>(null);
  useEffect(() => {
    strip.current
      ?.querySelector(`.${styles.current}`)
      ?.scrollIntoView?.({ inline: "center", block: "nearest", behavior: motion ? "smooth" : "auto" });
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
    if (field) {
      if (event.key === "Escape") {
        event.stopPropagation();
        field.blur();
        root.current?.focus();
      }
      return;
    }
    const handled = () => {
      event.preventDefault();
      event.stopPropagation();
    };
    switch (event.key) {
      case "ArrowLeft":
        handled();
        go(-1);
        break;
      case "ArrowRight":
        handled();
        go(1);
        break;
      case "Escape":
        handled();
        if (livePlaying) setLivePlaying(false);
        else void close();
        break;
      case "i":
      case "I":
        handled();
        onInfo(!info);
        break;
      case "Delete":
      case "Backspace":
        if (editable) {
          handled();
          actions.trash(asset);
        }
        break;
    }
    // A video plays and pauses with the space bar; a photo starts the
    // slideshow, finds its likes and zooms.
    if (video) {
      switch (event.key) {
        case " ":
          handled();
          player.current?.toggle();
          break;
        case "m":
        case "M":
          handled();
          player.current?.mute();
          break;
        case "f":
        case "F":
          handled();
          player.current?.fullscreen();
          break;
      }
      return;
    }
    switch (event.key) {
      case " ":
        handled();
        slideshow();
        break;
      case "s":
      case "S":
        handled();
        actions.findSimilar(asset);
        break;
      case "+":
      case "=":
        handled();
        zoom.current?.zoomIn(0.5);
        break;
      case "-":
        handled();
        zoom.current?.zoomOut(0.5);
        break;
      case "0":
        handled();
        zoom.current?.resetTransform();
        break;
    }
  };

  return (
    <div
      ref={root}
      className={`${styles.phViewer}${idle && !info ? ` ${styles.idle}` : ""}${idle ? ` ${styles.resting}` : ""}${entering ? ` ${styles.entering}` : ""}${closing ? ` ${styles.closing}` : ""}${info ? ` ${styles.withInfo}` : ""}`}
      role="dialog"
      aria-label={`查看 ${asset.name}`}
      tabIndex={-1}
      onKeyDown={keyDown}
      onPointerMove={wake}
      onPointerDown={wake}
    >
      <div className={styles.phViewerMain}>
        <header className={styles.phViewerBar}>
          <button type="button" aria-label="关闭查看" title="关闭 (Esc)" onClick={() => void close()}>
            <ArrowLeft />
          </button>
          <div className={styles.phViewerTitle}>
            <strong>{dayLabel(moment)}</strong>
            <small>
              {moment.toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit" })} · {asset.name}
            </small>
          </div>
          <div className={styles.phViewerTools}>
            {asset.motion && (
              <button
                type="button"
                className="ph-viewer-live"
                aria-label={livePlaying ? "停止动态照片" : "播放动态照片"}
                aria-pressed={livePlaying}
                onClick={() => {
                  setLiveError(false);
                  setLivePlaying(!livePlaying);
                }}
              >
                {livePlaying ? <Square /> : <CircleDot />}动态
              </button>
            )}
            <button
              type="button"
              aria-label="播放幻灯片"
              title={video ? "幻灯片（只放照片）" : "幻灯片 (空格)"}
              onClick={slideshow}
            >
              <Presentation />
            </button>
            {!video && (
              <button
                type="button"
                aria-label="找相似"
                title="找相似的照片 (S)"
                onClick={() => actions.findSimilar(asset)}
              >
                <ScanSearch />
              </button>
            )}
            {!libraryReadOnly(caller, asset) && (
              <button type="button" aria-label="加入相册" title="加入相册" onClick={() => actions.addToAlbum([asset])}>
                <FolderPlus />
              </button>
            )}
            <button
              type="button"
              aria-label={video ? "下载视频" : asset.motion ? "下载动态照片原件" : "下载原图"}
              title={video ? "下载视频原件" : asset.motion ? "下载动态照片原件" : "下载原图"}
              onClick={() => actions.download([asset])}
            >
              <Download />
            </button>
            {editable && (
              <button
                type="button"
                aria-label="移到回收站"
                title="移到回收站 (Delete)"
                onClick={() => actions.trash(asset)}
              >
                <Trash2 />
              </button>
            )}
            <button
              type="button"
              aria-label={info ? "隐藏信息" : "显示信息"}
              aria-pressed={info}
              title="信息 (I)"
              className={info ? styles.on : ""}
              onClick={() => onInfo(!info)}
            >
              <Info />
            </button>
          </div>
        </header>

        <div ref={stage} className={`${styles.phViewerStage}${video ? ` ${styles.video}` : ""}`}>
          {video ? (
            <VideoPlayer
              key={asset.id}
              ref={player}
              asset={asset}
              stage={stage}
              onDownload={() => actions.download([asset])}
            />
          ) : (
            <TransformWrapper
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
              <TransformComponent wrapperClass={styles.phZoomWrapper} contentClass={styles.phZoomContent}>
                <div className={styles.phViewerImage}>
                  {asset.thumbnail === "ready" && !originalReady && (
                    <img className={styles.phViewerLow} src={thumbnailURL(asset.id)} alt="" draggable={false} />
                  )}
                  <img
                    className={`${styles.phViewerFull}${originalReady ? ` ${styles.ready}` : ""}`}
                    src={displayURL(asset)}
                    alt={asset.name}
                    draggable={false}
                    onLoad={() => setOriginalReady(true)}
                  />
                </div>
              </TransformComponent>
            </TransformWrapper>
          )}
          {livePlaying && (
            <video
              key={asset.id}
              className={styles.phMotionVideo}
              src={motionURL(asset.id)}
              aria-label="动态照片视频"
              autoPlay
              muted
              playsInline
              controls
              onEnded={() => setLivePlaying(false)}
              onError={() => {
                setLivePlaying(false);
                setLiveError(true);
              }}
            />
          )}
          {liveError && (
            <p className={styles.phMotionError} role="status">
              动态片段暂时无法播放，仍可查看照片或下载原件。
            </p>
          )}
          {index > 0 && (
            <button
              type="button"
              className={`${styles.phViewerNav} ${styles.previous}`}
              aria-label="上一张"
              onClick={() => go(-1)}
            >
              <ChevronLeft />
            </button>
          )}
          {index < assets.length - 1 && (
            <button
              type="button"
              className={`${styles.phViewerNav} ${styles.next}`}
              aria-label="下一张"
              onClick={() => go(1)}
            >
              <ChevronRight />
            </button>
          )}
        </div>

        <div ref={strip} className={styles.phFilmstrip} aria-label="胶片条">
          {film.map((item, offset) => (
            <button
              type="button"
              key={item.id}
              className={start + offset === index ? styles.current : ""}
              aria-label={`跳到 ${item.name}`}
              aria-current={start + offset === index}
              onClick={() => onIndex(start + offset)}
              style={{ aspectRatio: `${Math.min(2, Math.max(0.5, (item.width || 1) / (item.height || 1)))}` }}
            >
              <img src={previewURL(item)} alt="" loading="lazy" draggable={false} />
              {item.video && (
                <span className={styles.phFilmVideo} aria-hidden="true">
                  <Play />
                </span>
              )}
            </button>
          ))}
        </div>
      </div>

      <aside className={styles.phViewerInfo} aria-label="照片信息" aria-hidden={!info}>
        {info && (
          <PhotoDetails
            asset={asset}
            details={details}
            caller={caller}
            actions={actions}
            onChanged={(value) => {
              setDetails(value);
              onChanged(value);
            }}
          />
        )}
      </aside>
    </div>
  );
}

const libraryReadOnly = (caller: Caller, asset: PhotoAsset) =>
  Boolean(caller.libraries.find((library) => library.id === asset.libraryId)?.viewing);
