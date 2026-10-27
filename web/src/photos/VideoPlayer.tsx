import {
  Download,
  Maximize,
  Minimize,
  Pause,
  Play,
  RotateCcw,
  TriangleAlert,
  Volume1,
  Volume2,
  VolumeX,
} from "lucide-react";
import {
  forwardRef,
  KeyboardEvent,
  PointerEvent as ReactPointerEvent,
  RefObject,
  useCallback,
  useEffect,
  useEffectEvent,
  useImperativeHandle,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";

import { browserCapabilities } from "../media/api";
import { canFeed, StreamFeed } from "../media/streamFeed";
import {
  displayURL,
  getVideoPlayback,
  type PhotoAsset,
  type VideoPlayback,
  videoOriginalURL,
  videoStreamURL,
} from "./api";
import { clock, messageOf } from "./model";

// What the viewer's keys do to the video it shows.
export interface VideoControls {
  toggle: () => void;
  mute: () => void;
  fullscreen: () => void;
}

interface Props {
  asset: PhotoAsset;
  // What goes full screen: the viewer's stage, so the controls come along.
  stage: RefObject<HTMLElement | null>;
  onDownload: () => void;
}

type Mode = VideoPlayback["mode"];

// How far ahead a converted stream is read, in seconds: remuxing only
// repackages and costs little, transcoding is about five CPU-seconds per
// second of video, which a skip past the buffer throws away.
const reach = { remux: { ahead: 120, resume: 90 }, transcode: { ahead: 30, resume: 15 } };
// Skips in quick succession restart a converted stream once, where they end.
const settleSkips = 350;
const volumeKey = "a-nas.photos.volume";

function storedVolume() {
  try {
    const value = Number(window.localStorage.getItem(volumeKey) ?? "1");
    return Number.isFinite(value) ? Math.min(1, Math.max(0, value)) : 1;
  } catch {
    return 1;
  }
}

// bufferedAt tells whether the element holds enough at a position to play
// from it at once.
function bufferedAt(element: HTMLVideoElement, at: number) {
  for (let index = 0; index < element.buffered.length; index++) {
    if (element.buffered.start(index) - 0.1 <= at && at <= element.buffered.end(index) - 0.5) return true;
  }
  return false;
}

// VideoPlayer plays a video asset in the viewer. The browser plays the
// original when it decodes it and seeks in it by ranges; otherwise the NAS
// converts it on the fly, and since a converted stream cannot seek, the
// player restarts it where a skip lands and adds that offset to the
// stream's own clock. A failure to play the original or a remux falls back
// to a full conversion before it is reported.
export const VideoPlayer = forwardRef<VideoControls, Props>(function VideoPlayer(
  { asset, stage: stageRef, onDownload },
  ref,
) {
  const caps = useMemo(() => browserCapabilities(), []);
  const duration = asset.video?.duration ?? 0;
  const video = useRef<HTMLVideoElement>(null);
  const feed = useRef<StreamFeed | undefined>(undefined);
  const [playback, setPlayback] = useState<VideoPlayback>();
  const [mode, setMode] = useState<Mode>();
  // forced marks a full conversion asked for after another way failed.
  const forced = useRef(false);
  const [source, setSource] = useState("");
  const [offset, setOffset] = useState(0);
  const [time, setTime] = useState(0);
  const [ranges, setRanges] = useState<[number, number][]>([]);
  const [paused, setPaused] = useState(true);
  const [waiting, setWaiting] = useState(true);
  const [started, setStarted] = useState(false);
  const [ended, setEnded] = useState(false);
  const [failure, setFailure] = useState("");
  const [volume, setVolume] = useState(storedVolume);
  const [muted, setMuted] = useState(false);
  const [fullscreen, setFullscreen] = useState(false);
  const [scrub, setScrub] = useState<number>();
  const [hover, setHover] = useState<{ x: number; time: number }>();
  const pendingStart = useRef(0);
  const wantPlay = useRef(true);
  const restart = useRef(0);
  const converted = mode !== undefined && mode !== "direct";

  const attach = (url: string, mime: string, remaining: number, kind: "remux" | "transcode") => {
    feed.current?.close();
    feed.current = undefined;
    if (!canFeed(mime)) {
      setSource(url);
      return;
    }
    const next: StreamFeed = new StreamFeed(url, mime, () => video.current, {
      ...reach[kind],
      duration: remaining,
      onError: (message) => {
        if (feed.current === next) fail(message);
      },
      onFallback: () => {
        if (feed.current !== next) return;
        next.close();
        feed.current = undefined;
        setSource(url);
      },
    });
    feed.current = next;
    setSource(next.url);
  };

  // begin plays the video one way from an absolute position.
  const begin = (decision: VideoPlayback, how: Mode, start: number, play: boolean) => {
    window.clearTimeout(restart.current);
    wantPlay.current = play;
    setMode(how);
    setFailure("");
    setWaiting(true);
    setEnded(false);
    setTime(start);
    if (how === "direct") {
      feed.current?.close();
      feed.current = undefined;
      pendingStart.current = start;
      setOffset(0);
      setSource(videoOriginalURL(asset.id));
      return;
    }
    pendingStart.current = 0;
    setOffset(start);
    // A conversion asked for after another way failed is always H.264.
    const mime =
      forced.current || !decision.mime
        ? `video/mp4; codecs="avc1.640033${decision.mime && !decision.mime.includes("mp4a") ? "" : ",mp4a.40.2"}"`
        : decision.mime;
    attach(videoStreamURL(asset.id, start, caps, forced.current), mime, Math.max(1, duration - start), how);
  };

  // The memoized controls below call the latest begin.
  const beginLatest = useRef(begin);
  useLayoutEffect(() => {
    beginLatest.current = begin;
  });

  const fail = (message: string) => {
    setWaiting(false);
    setFailure(message);
    setPaused(true);
  };

  const decided = useEffectEvent((decision: VideoPlayback) => {
    setPlayback(decision);
    begin(decision, decision.mode, 0, true);
  });
  const undecided = useEffectEvent((caught: unknown) => fail(messageOf(caught)));
  useEffect(() => {
    let live = true;
    getVideoPlayback(asset.id, caps).then(
      (decision) => {
        if (live) decided(decision);
      },
      (caught) => {
        if (live) undecided(caught);
      },
    );
    return () => {
      live = false;
      window.clearTimeout(restart.current);
      feed.current?.close();
    };
  }, [asset.id, caps]);

  // The element's own failure: what the browser could not play is tried
  // once more as a full conversion.
  const failed = () => {
    if (!playback || failure) return;
    if (forced.current) {
      fail("浏览器无法播放这段视频");
      return;
    }
    forced.current = true;
    begin(playback, "transcode", absolute(), wantPlay.current || !paused);
  };

  const absolute = useCallback(() => (converted ? offset : 0) + (video.current?.currentTime ?? 0), [converted, offset]);

  const play = useCallback(() => {
    const element = video.current;
    if (!element) return;
    wantPlay.current = true;
    Promise.resolve()
      .then(() => element.play())
      .catch(() => {
        wantPlay.current = false;
        setPaused(true);
        setWaiting(false);
      });
  }, []);

  // seek moves to an absolute position; a converted stream restarts there
  // unless it already holds it.
  const seek = useCallback(
    (target: number) => {
      const element = video.current;
      if (!element || !playback || !mode) return;
      target = Math.max(0, Math.min(duration - 0.2, target));
      setTime(target);
      setEnded(false);
      if (!converted) {
        element.currentTime = target;
        return;
      }
      const local = target - offset;
      if (local >= 0 && bufferedAt(element, local)) {
        element.currentTime = local;
        return;
      }
      window.clearTimeout(restart.current);
      const resume = !element.paused || wantPlay.current;
      restart.current = window.setTimeout(() => beginLatest.current(playback, mode, target, resume), settleSkips);
    },
    [playback, mode, converted, offset, duration],
  );

  const toggle = useCallback(() => {
    const element = video.current;
    if (!element || failure) return;
    if (ended) {
      if (converted) {
        if (playback && mode) beginLatest.current(playback, mode, 0, true);
      } else {
        element.currentTime = 0;
        play();
      }
      return;
    }
    if (paused) play();
    else {
      wantPlay.current = false;
      element.pause();
    }
  }, [failure, ended, paused, converted, playback, mode, play]);

  const toggleMute = useCallback(() => setMuted((value) => !value), []);
  const toggleFullscreen = useCallback(() => {
    if (document.fullscreenElement) void document.exitFullscreen?.().catch(() => undefined);
    else void stageRef.current?.requestFullscreen?.().catch(() => undefined);
  }, [stageRef]);
  useImperativeHandle(ref, () => ({ toggle, mute: toggleMute, fullscreen: toggleFullscreen }), [
    toggle,
    toggleMute,
    toggleFullscreen,
  ]);

  useEffect(() => {
    const element = stageRef.current;
    const changed = () =>
      setFullscreen(Boolean(document.fullscreenElement) && document.fullscreenElement === stageRef.current);
    document.addEventListener("fullscreenchange", changed);
    return () => {
      document.removeEventListener("fullscreenchange", changed);
      if (document.fullscreenElement && document.fullscreenElement === element)
        void document.exitFullscreen?.().catch(() => undefined);
    };
  }, [stageRef]);

  useEffect(() => {
    const element = video.current;
    if (!element) return;
    element.volume = volume;
    element.muted = muted;
    try {
      window.localStorage.setItem(volumeKey, String(volume));
    } catch {
      /* private windows keep it for the session */
    }
  }, [volume, muted, source]);

  const readRanges = () => {
    const element = video.current;
    if (!element) return;
    const base = converted ? offset : 0;
    const next: [number, number][] = [];
    for (let index = 0; index < element.buffered.length; index++)
      next.push([base + element.buffered.start(index), base + element.buffered.end(index)]);
    setRanges(next);
  };

  // The scrubber follows the pointer while it is held.
  const track = useRef<HTMLDivElement>(null);
  const at = (clientX: number) => {
    const box = track.current?.getBoundingClientRect();
    if (!box || !box.width) return 0;
    return Math.max(0, Math.min(1, (clientX - box.left) / box.width)) * duration;
  };
  const pointerDown = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (event.button !== 0) return;
    event.currentTarget.setPointerCapture?.(event.pointerId);
    setScrub(at(event.clientX));
  };
  const pointerMove = (event: ReactPointerEvent<HTMLDivElement>) => {
    const box = track.current?.getBoundingClientRect();
    if (box) setHover({ x: event.clientX - box.left, time: at(event.clientX) });
    if (scrub !== undefined) setScrub(at(event.clientX));
  };
  const pointerUp = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (scrub === undefined) return;
    const target = at(event.clientX);
    setScrub(undefined);
    seek(target);
  };
  const scrubKey = (event: KeyboardEvent) => {
    const step = { ArrowLeft: -5, ArrowRight: 5, PageDown: -30, PageUp: 30 }[event.key];
    if (step === undefined && event.key !== "Home" && event.key !== "End") return;
    event.preventDefault();
    event.stopPropagation();
    if (event.key === "Home") seek(0);
    else if (event.key === "End") seek(duration);
    else seek(time + (step ?? 0));
  };

  const shown = scrub ?? time;
  const fraction = duration ? Math.min(1, shown / duration) : 0;
  const quiet = muted || volume === 0;
  const resting = paused && !waiting && !failure;
  return (
    <div className={`ph-video${paused ? " paused" : ""}${started ? " started" : ""}`}>
      <video
        ref={video}
        className="ph-video-media"
        src={source || undefined}
        poster={displayURL(asset)}
        aria-label={asset.name}
        playsInline
        preload="auto"
        onClick={toggle}
        onDoubleClick={toggleFullscreen}
        onLoadedMetadata={(event) => {
          if (pendingStart.current > 0) {
            event.currentTarget.currentTime = pendingStart.current;
            pendingStart.current = 0;
          }
          if (wantPlay.current) play();
          else setWaiting(false);
        }}
        onTimeUpdate={(event) => {
          if (scrub === undefined) setTime((converted ? offset : 0) + event.currentTarget.currentTime);
        }}
        onProgress={readRanges}
        onWaiting={() => setWaiting(true)}
        onPlaying={() => {
          setWaiting(false);
          setStarted(true);
          setEnded(false);
        }}
        onCanPlay={() => setWaiting(false)}
        onPlay={() => setPaused(false)}
        onPause={() => setPaused(true)}
        onEnded={() => {
          setEnded(true);
          setPaused(true);
          setTime(duration);
          wantPlay.current = false;
        }}
        onError={() => {
          if (source) failed();
        }}
      />
      {resting && (
        <button type="button" className="ph-video-play" aria-label={ended ? "重新播放" : "播放视频"} onClick={toggle}>
          {ended ? <RotateCcw /> : <Play />}
        </button>
      )}
      {waiting && !failure && (started || source) && (
        <div className="ph-video-wait" role="status" aria-label="正在载入视频">
          <span className="ph-spinner" />
        </div>
      )}
      {failure && (
        <div className="ph-video-failure" role="status">
          <TriangleAlert />
          <strong>视频暂时无法播放</strong>
          <small>{failure}</small>
          <button type="button" onClick={onDownload}>
            <Download />
            下载原件
          </button>
        </div>
      )}
      {/* A focused button takes the space bar as a click; anywhere else in
          the controls it plays and pauses, as in the viewer. */}
      <div
        className="ph-video-controls"
        role="toolbar"
        aria-label="视频控制"
        onKeyDown={(event) => {
          if (event.key === " " && (event.target as HTMLElement).closest("button")) event.stopPropagation();
        }}
      >
        <button
          type="button"
          aria-label={paused ? "播放" : "暂停"}
          title={paused ? "播放 (空格)" : "暂停 (空格)"}
          disabled={Boolean(failure)}
          onClick={toggle}
        >
          {paused ? <Play /> : <Pause />}
        </button>
        <span className="ph-video-time">
          <strong>{clock(shown)}</strong> / {clock(duration)}
        </span>
        <div
          ref={track}
          className={`ph-video-scrub${scrub !== undefined ? " dragging" : ""}`}
          role="slider"
          tabIndex={0}
          aria-label="播放进度"
          aria-valuemin={0}
          aria-valuemax={Math.round(duration)}
          aria-valuenow={Math.round(shown)}
          aria-valuetext={`${clock(shown)} / ${clock(duration)}`}
          onPointerDown={pointerDown}
          onPointerMove={pointerMove}
          onPointerUp={pointerUp}
          onPointerLeave={() => setHover(undefined)}
          onKeyDown={scrubKey}
        >
          <span className="ph-video-rail">
            {ranges.map(([start, end]) => (
              <span
                key={start}
                className="ph-video-buffered"
                style={{
                  left: `${(start / duration) * 100}%`,
                  width: `${(Math.max(0, end - start) / duration) * 100}%`,
                }}
              />
            ))}
            <span className="ph-video-played" style={{ width: `${fraction * 100}%` }} />
          </span>
          <span className="ph-video-knob" style={{ left: `${fraction * 100}%` }} />
          {hover && (
            <span className="ph-video-tip" style={{ left: hover.x }}>
              {clock(hover.time)}
            </span>
          )}
        </div>
        {converted && (
          <span
            className="ph-video-mode"
            title={
              mode === "remux"
                ? "浏览器不能直接打开这种格式，NAS 正在转换封装"
                : "浏览器无法解码这段视频，NAS 正在实时转码"
            }
          >
            {mode === "remux" ? "转换播放" : "转码播放"}
          </span>
        )}
        <span className="ph-video-volume">
          <button
            type="button"
            aria-label={quiet ? "打开声音" : "静音"}
            title={quiet ? "打开声音 (M)" : "静音 (M)"}
            onClick={() => {
              if (volume === 0) setVolume(1);
              setMuted(quiet ? false : true);
            }}
          >
            {quiet ? <VolumeX /> : volume < 0.5 ? <Volume1 /> : <Volume2 />}
          </button>
          <input
            type="range"
            aria-label="音量"
            min={0}
            max={1}
            step={0.05}
            value={quiet ? 0 : volume}
            onChange={(event) => {
              const value = Number(event.target.value);
              setVolume(value);
              setMuted(value === 0);
            }}
            onKeyDown={(event) => {
              if (event.key.startsWith("Arrow")) event.stopPropagation();
            }}
          />
        </span>
        <button
          type="button"
          aria-label={fullscreen ? "退出全屏" : "全屏"}
          title={fullscreen ? "退出全屏 (F)" : "全屏 (F)"}
          onClick={toggleFullscreen}
        >
          {fullscreen ? <Minimize /> : <Maximize />}
        </button>
      </div>
    </div>
  );
});
