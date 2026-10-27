import {
  ArrowLeft,
  Captions,
  Check,
  ChevronRight,
  Clapperboard,
  Gauge,
  Languages,
  ListVideo,
  Maximize,
  Minimize,
  Pause,
  Play,
  RotateCcw,
  RotateCw,
  Settings2,
  SkipBack,
  SkipForward,
  Volume1,
  Volume2,
  VolumeX,
} from "lucide-react";
import {
  CSSProperties,
  FocusEvent,
  KeyboardEvent,
  PointerEvent as ReactPointerEvent,
  ReactNode,
  RefObject,
  useCallback,
  useEffect,
  useEffectEvent,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";

import { messageOf } from "./Browse";
import { episodeCode, episodeName, timecode } from "./format";
import {
  browserCapabilities,
  getPlayback,
  getVideo,
  saveProgress,
  streamURL,
  subtitleURL,
  type Playback,
  type Quality,
  type VideoDetail,
} from "./api";
import { PlayerPanel, type PanelTab } from "./PlayerPanel";
import { canFeed, StreamFeed } from "./streamFeed";

const qualityLabels: Record<Quality, string> = { original: "原画", "1080p": "1080p", "720p": "720p", "480p": "480p" };
const rates = [0.5, 0.75, 1, 1.25, 1.5, 2];
const saveEvery = 10_000;
const resumeFloor = 30;
const nextCountdown = 8;
// Skips in quick succession restart a converted stream once, where they end.
const settleSkips = 400;

// buffered tells whether the media element holds enough at a position to
// play from it at once.
function buffered(element: HTMLVideoElement, at: number): boolean {
  for (let index = 0; index < element.buffered.length; index++) {
    if (element.buffered.start(index) - 0.1 <= at && at <= element.buffered.end(index) - 1) return true;
  }
  return false;
}

interface Cue {
  start: number;
  end: number;
  text: string;
}

// playSafely starts playback; browsers refuse autoplay with sound until the
// user interacts, which leaves the player paused rather than failing.
function playSafely(element: HTMLVideoElement, onRefused?: () => void) {
  Promise.resolve()
    .then(() => element.play())
    .catch(() => onRefused?.());
}

function readStored(key: string, fallback: number): number {
  try {
    const value = Number(window.localStorage.getItem(key));
    return Number.isFinite(value) && window.localStorage.getItem(key) !== null ? value : fallback;
  } catch {
    return fallback;
  }
}
function store(key: string, value: number) {
  try {
    window.localStorage.setItem(key, String(value));
  } catch {
    /* private windows keep it for the session */
  }
}

type MenuName = "settings" | "audio" | "subtitles" | "quality" | "speed";

export interface PlayRequest {
  videoId: string;
  restart?: boolean;
}

// useHeldFocus keeps the keyboard on the player. Its keys work only while
// focus is inside it, and a control that goes away while focused, such as
// the episode button while the next episode loads, leaves focus on the page;
// the player then takes it back. Focus moved elsewhere by the viewer stays
// where they put it.
function useHeldFocus(container: RefObject<HTMLDivElement | null>) {
  const inside = useRef(false);
  const reclaim = useCallback(() => {
    const active = document.activeElement;
    if (!active || active === document.body) container.current?.focus({ preventScroll: true });
  }, [container]);
  useLayoutEffect(() => {
    if (inside.current) reclaim();
  });
  const onFocus = () => {
    inside.current = true;
  };
  const onBlur = (event: FocusEvent<HTMLDivElement>) => {
    const next = event.relatedTarget as Node | null;
    if (next && container.current?.contains(next)) return;
    const lost = event.target as Node;
    if (next || lost.isConnected) inside.current = false;
    // Some browsers report the blur before the control is gone.
    if (!next)
      window.requestAnimationFrame(() => {
        if (!lost.isConnected) {
          inside.current = true;
          reclaim();
        }
      });
  };
  return { onFocus, onBlur };
}

// Notice is the brief word a key press leaves on the picture.
interface Notice {
  id: number;
  icon: ReactNode;
  text: string;
}

// SkipIcon is the circular arrow of a ten-second skip with its seconds on it.
function SkipIcon({ forward }: { forward?: boolean }) {
  return (
    <span className="mc-skip-icon">
      {forward ? <RotateCw /> : <RotateCcw />}
      <small>10</small>
    </span>
  );
}

// Player plays one video over the media center. A converted stream cannot
// seek, so the player restarts it at the new position and adds that offset
// to the stream's own clock; positions it has already received play from
// where they are, and the last picture stays while a restart loads.
// How far ahead a converted stream is read. Remuxing only repackages, about
// eleven CPU-seconds on the device for five minutes of 1080p, so five
// minutes are buffered and a skip within them plays at once; the browser's
// buffer quota still bounds a high bitrate. Transcoding costs about five
// CPU-seconds per second of video, which a skip past the buffer throws
// away, so it stays half a minute ahead.
const reach = { remux: { ahead: 300, resume: 240 }, transcode: { ahead: 30, resume: 15 } };

export function Player({
  request,
  onClose,
  onNavigate,
  onSaved,
}: {
  request: PlayRequest;
  onClose: () => void;
  onNavigate: (videoId: string) => void;
  onSaved: () => void;
}) {
  const caps = useMemo(() => browserCapabilities(), []);
  const container = useRef<HTMLDivElement>(null);
  const video = useRef<HTMLVideoElement>(null);
  const [detail, setDetail] = useState<VideoDetail>();
  const [playback, setPlayback] = useState<Playback>();
  const [source, setSource] = useState("");
  const [offset, setOffset] = useState(0);
  const [failure, setFailure] = useState("");
  const [time, setTime] = useState(0);
  const [ranges, setRanges] = useState<[number, number][]>([]);
  const [paused, setPaused] = useState(true);
  const [waiting, setWaiting] = useState(true);
  const [volume, setVolume] = useState(() => readStored("a-nas.media.volume", 1));
  const [muted, setMuted] = useState(false);
  const [rate, setRate] = useState(1);
  const [subtitle, setSubtitle] = useState("off");
  const [cues, setCues] = useState<Cue[]>([]);
  const [menu, setMenu] = useState<MenuName>();
  const [panel, setPanel] = useState<PanelTab>();
  const [chrome, setChrome] = useState(true);
  const [fullscreen, setFullscreen] = useState(false);
  const [scrub, setScrub] = useState<number>();
  const [hover, setHover] = useState<{ x: number; time: number }>();
  const [countdown, setCountdown] = useState<number>();
  // upNext names the episode the countdown leads to.
  const [upNext, setUpNext] = useState<string>();
  const [resumed, setResumed] = useState<number>();
  // halted is a pause the viewer sees as one: their own, or a refused
  // autoplay, not the moment before a video starts.
  const [halted, setHalted] = useState(false);
  const [notice, setNotice] = useState<Notice>();
  const held = useHeldFocus(container);
  // jump is where a seek is going while the picture is not there yet.
  const [jump, setJump] = useState<number>();
  const [frozen, setFrozen] = useState(false);
  const still = useRef<HTMLCanvasElement>(null);
  const restart = useRef(0);
  const pendingSeek = useRef<number | undefined>(undefined);
  const fellBack = useRef(false);
  const pendingStart = useRef(0);
  const resumePlaying = useRef(true);
  const lastSaved = useRef(0);
  // Only a video that actually played goes into the history.
  const started = useRef(false);
  const hideTimer = useRef(0);

  const converted = playback ? playback.mode !== "direct" : false;
  const duration = playback?.duration || detail?.duration || 0;
  const absolute = useCallback(() => (converted ? offset : 0) + (video.current?.currentTime ?? 0), [converted, offset]);

  // Another video starts without the last one's state.
  const [requestBefore, setRequestBefore] = useState(request);
  if (requestBefore !== request) {
    setRequestBefore(request);
    setJump(undefined);
    setFrozen(false);
    setRanges([]);
    setFailure("");
    setDetail(undefined);
    setPlayback(undefined);
    setCountdown(undefined);
    setUpNext(undefined);
    setHalted(false);
    setCues([]);
  }

  // A converted stream plays through a feed that buffers it ahead, where
  // the browser allows; otherwise the element fetches it itself.
  const feed = useRef<StreamFeed | undefined>(undefined);
  const attach = (
    url: string,
    mime: string | undefined,
    remaining: number | undefined,
    mode: "remux" | "transcode",
  ) => {
    feed.current?.close();
    feed.current = undefined;
    if (!canFeed(mime)) {
      setSource(url);
      return;
    }
    const next: StreamFeed = new StreamFeed(url, mime, () => video.current, {
      ...reach[mode],
      duration: remaining,
      onError: (message) => {
        if (feed.current !== next) return;
        setWaiting(false);
        setFrozen(false);
        setJump(undefined);
        setFailure(message);
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
  useEffect(() => () => feed.current?.close(), []);

  // begin points the video at a decision, starting at an absolute position.
  const begin = (decision: Playback, start: number, play: boolean) => {
    resumePlaying.current = play;
    setPlayback(decision);
    setWaiting(true);
    setFailure("");
    if (decision.mode === "direct") {
      pendingStart.current = start;
      setOffset(0);
      feed.current?.close();
      feed.current = undefined;
      setSource(decision.url);
    } else {
      pendingStart.current = 0;
      setOffset(start);
      attach(
        `${decision.url}${decision.url.includes("?") ? "&" : "?"}start=${start.toFixed(3)}`,
        decision.mime,
        decision.duration ? Math.max(1, decision.duration - start) : undefined,
        decision.mode,
      );
    }
    setTime(start);
  };

  // Load the video and decide how to play it.
  const decided = useEffectEvent((value: VideoDetail, decision: Playback) => {
    const saved = value.progress;
    const start = !request.restart && saved && !saved.watched && saved.position > resumeFloor ? saved.position : 0;
    setDetail(value);
    setResumed(start > 0 ? start : undefined);
    const preferred =
      value.subtitles.find((track) => track.default) ??
      value.subtitles.find((track) => track.external && track.language?.startsWith("zh"));
    setSubtitle(preferred?.id ?? "off");
    begin(decision, start, true);
  });
  useEffect(() => {
    let live = true;
    window.clearTimeout(restart.current);
    pendingSeek.current = undefined;
    fellBack.current = false;
    started.current = false;
    lastSaved.current = 0;
    Promise.all([getVideo(request.videoId), getPlayback(request.videoId, { caps })]).then(
      ([value, decision]) => {
        if (live) decided(value, decision);
      },
      (caught) => {
        if (live) setFailure(messageOf(caught));
      },
    );
    return () => {
      live = false;
    };
  }, [request, caps]);

  const save = useCallback(
    (force = false) => {
      if (!detail || !playback || !started.current) return;
      const now = Date.now();
      if (!force && now - lastSaved.current < saveEvery) return;
      lastSaved.current = now;
      const position = absolute();
      if (position <= 0 && !force) return;
      saveProgress(detail.id, position, duration).then(onSaved, () => undefined);
    },
    [detail, playback, absolute, duration, onSaved],
  );

  // Save when the player closes or moves on to another episode.
  const saveOnClose = useEffectEvent(() => save(true));
  useEffect(() => () => saveOnClose(), []);
  // The control that moved on, a panel's episode or the countdown's button,
  // goes away with the video, so the player keeps the keyboard itself.
  const go = (videoId: string) => {
    save(true);
    setCountdown(undefined);
    container.current?.focus({ preventScroll: true });
    onNavigate(videoId);
  };

  // holdFrame keeps the last picture on screen while a restarted stream
  // loads, instead of a black frame.
  const holdFrame = () => {
    const element = video.current;
    const canvas = still.current;
    if (!element || !canvas || element.readyState < 2 || !element.videoWidth) return;
    try {
      canvas.width = element.videoWidth;
      canvas.height = element.videoHeight;
      const context = canvas.getContext("2d");
      if (!context) return;
      context.drawImage(element, 0, 0);
      setFrozen(true);
    } catch {
      /* the picture is a courtesy */
    }
  };
  useEffect(() => () => window.clearTimeout(restart.current), []);

  // seek moves to an absolute position. Skips pass settle, so a burst of
  // them restarts a converted stream once.
  const seek = (target: number, settle = 0) => {
    const element = video.current;
    if (!element || !playback) return;
    target = Math.max(0, Math.min(duration ? duration - 1 : target, target));
    setCountdown(undefined);
    setTime(target);
    if (!converted) {
      if (!buffered(element, target)) setJump(target);
      element.currentTime = target;
      return;
    }
    window.clearTimeout(restart.current);
    const local = target - offset;
    if (local >= 0 && buffered(element, local)) {
      pendingSeek.current = undefined;
      setJump(undefined);
      element.currentTime = local;
      return;
    }
    pendingSeek.current = target;
    setJump(target);
    const playing = !element.paused || resumePlaying.current;
    restart.current = window.setTimeout(() => {
      pendingSeek.current = undefined;
      holdFrame();
      begin(playback, target, playing);
    }, settle);
  };
  // Where the next skip starts: a pending seek's target, or the picture.
  const position = () => pendingSeek.current ?? absolute();

  const reconfigure = async (options: { audio?: number; quality?: Quality }) => {
    if (!detail || !playback) return;
    const position = absolute();
    const playing = !(video.current?.paused ?? true);
    setMenu(undefined);
    try {
      const decision = await getPlayback(detail.id, {
        caps,
        audio: options.audio ?? playback.audio,
        quality: options.quality ?? playback.quality,
      });
      holdFrame();
      begin(decision, position, playing);
    } catch (caught) {
      setFailure(messageOf(caught));
    }
  };

  const toggle = () => {
    const element = video.current;
    if (!element) return;
    if (element.paused) playSafely(element);
    else element.pause();
  };

  const toggleFullscreen = () => {
    if (document.fullscreenElement) void document.exitFullscreen().catch(() => undefined);
    else void container.current?.requestFullscreen?.().catch(() => undefined);
  };
  useEffect(() => {
    const change = () => setFullscreen(Boolean(document.fullscreenElement));
    document.addEventListener("fullscreenchange", change);
    return () => document.removeEventListener("fullscreenchange", change);
  }, []);

  useEffect(() => {
    if (video.current) {
      video.current.volume = volume;
      video.current.muted = muted;
    }
    store("a-nas.media.volume", volume);
  }, [volume, muted, source]);
  useEffect(() => {
    if (video.current) video.current.playbackRate = rate;
  }, [rate, source]);

  // Controls hide while playing and the pointer is still.
  const wake = useCallback(() => {
    setChrome(true);
    window.clearTimeout(hideTimer.current);
    hideTimer.current = window.setTimeout(() => setChrome(false), 2800);
  }, []);
  useEffect(() => () => window.clearTimeout(hideTimer.current), []);
  const showChrome = chrome || paused || Boolean(menu) || Boolean(panel) || scrub !== undefined || Boolean(failure);

  useEffect(() => {
    container.current?.focus();
  }, []);
  useEffect(() => {
    if (resumed === undefined) return;
    const timer = window.setTimeout(() => setResumed(undefined), 7000);
    return () => window.clearTimeout(timer);
  }, [resumed]);

  // The next episode starts after a short countdown that names it.
  useEffect(() => {
    if (countdown === undefined) return;
    if (countdown <= 0) {
      if (detail?.nextId) onNavigate(detail.nextId);
      return;
    }
    const timer = window.setTimeout(() => setCountdown((value) => (value === undefined ? undefined : value - 1)), 1000);
    return () => window.clearTimeout(timer);
  }, [countdown, detail?.nextId, onNavigate]);
  const counting = countdown !== undefined;
  useEffect(() => {
    if (!counting || !detail?.nextId) return;
    let live = true;
    getVideo(detail.nextId).then(
      (next) => {
        if (live) setUpNext(episodeName({ season: next.season ?? 0, episode: next.episode ?? 0, title: next.title }));
      },
      () => undefined,
    );
    return () => {
      live = false;
    };
  }, [counting, detail?.nextId]);

  // A key leaves a short notice of what it did; the volume and the skips
  // are otherwise invisible while the controls are hidden.
  const noticeTimer = useRef(0);
  const noticeCount = useRef(0);
  const announce = (icon: ReactNode, text: string) => {
    window.clearTimeout(noticeTimer.current);
    setNotice({ id: ++noticeCount.current, icon, text });
    noticeTimer.current = window.setTimeout(() => setNotice(undefined), 900);
  };
  useEffect(() => () => window.clearTimeout(noticeTimer.current), []);

  const events = {
    onLoadedMetadata: () => {
      const element = video.current;
      if (!element) return;
      if (pendingStart.current > 0) {
        element.currentTime = pendingStart.current;
        pendingStart.current = 0;
      }
      if (resumePlaying.current)
        playSafely(element, () => {
          setPaused(true);
          setHalted(true);
        });
      else setHalted(true);
    },
    onTimeUpdate: () => {
      const element = video.current;
      if (!element) return;
      if (scrub === undefined && pendingSeek.current === undefined) setTime(absolute());
      readRanges();
      if (!element.paused) save();
    },
    onProgress: () => readRanges(),
    onLoadedData: () => {
      setFrozen(false);
      if (pendingSeek.current === undefined) setJump(undefined);
    },
    onSeeked: () => {
      if (pendingSeek.current === undefined) setJump(undefined);
    },
    onPlay: () => {
      setPaused(false);
      setHalted(false);
      wake();
    },
    onPause: () => {
      setPaused(true);
      setHalted(true);
      save(true);
    },
    onWaiting: () => setWaiting(true),
    onPlaying: () => {
      started.current = true;
      setWaiting(false);
      setFrozen(false);
      if (pendingSeek.current === undefined) setJump(undefined);
    },
    onCanPlay: () => setWaiting(false),
    onEnded: () => {
      setPaused(true);
      if (detail) {
        started.current = false;
        saveProgress(detail.id, duration || absolute(), duration || absolute()).then(onSaved, () => undefined);
      }
      if (detail?.nextId) setCountdown(nextCountdown);
    },
    onError: () => {
      setFrozen(false);
      setJump(undefined);
      if (!detail || !playback) return;
      // The original did not play after all: convert it instead, once.
      if (playback.mode === "direct" && !fellBack.current) {
        fellBack.current = true;
        const mime = `video/mp4; codecs="avc1.640033${playback.audio >= 0 ? ",mp4a.40.2" : ""}"`;
        begin(
          {
            ...playback,
            mode: "transcode",
            url: streamURL(detail.id, "transcode", playback.audio, playback.quality, caps),
            mime,
            reason: "原文件无法直接播放，已切换为实时转码",
          },
          absolute(),
          true,
        );
        return;
      }
      setWaiting(false);
      setFailure(playback.mode === "direct" ? "这个视频无法播放。" : "转码失败，或设备正在为其他人转码。请稍后重试。");
    },
  };

  // The seek bar shows every stretch the browser holds, so a skip into one
  // of them is known to be instant.
  const readRanges = () => {
    const element = video.current;
    if (!element) return;
    const shift = converted ? offset : 0;
    const next: [number, number][] = [];
    for (let index = 0; index < element.buffered.length; index++)
      next.push([shift + element.buffered.start(index), shift + element.buffered.end(index)]);
    setRanges((current) =>
      current.length === next.length &&
      current.every(([start, end], index) => start === next[index][0] && Math.abs(end - next[index][1]) < 0.5)
        ? current
        : next,
    );
  };

  // Subtitles load into a hidden track; the overlay shows the cues for the
  // absolute position, so they stay in time after a converted restart.
  const trackElement = useRef<HTMLTrackElement>(null);
  const cuesFor = JSON.stringify([subtitle, source]);
  const [cuesBefore, setCuesBefore] = useState(cuesFor);
  if (cuesBefore !== cuesFor) {
    setCuesBefore(cuesFor);
    setCues([]);
  }
  useEffect(() => {
    const element = trackElement.current;
    if (!element?.track || subtitle === "off") return;
    element.track.mode = "hidden";
    const loaded = () => {
      const list = element.track.cues;
      const next: Cue[] = [];
      for (let index = 0; list && index < list.length; index++) {
        const cue = list[index] as VTTCue;
        next.push({ start: cue.startTime, end: cue.endTime, text: cue.text.replace(/<[^>]+>/g, "") });
      }
      setCues(next);
    };
    element.addEventListener("load", loaded);
    if (element.readyState === 2) loaded();
    return () => element.removeEventListener("load", loaded);
  }, [subtitle, source]);
  const activeCues = cues.filter((cue) => cue.start <= time && cue.end >= time);

  const changeVolume = (step: number) => {
    const next = Math.round(Math.max(0, Math.min(1, volume + step)) * 100) / 100;
    const silent = next === 0 || (muted && step < 0);
    setVolume(next);
    if (step > 0) setMuted(false);
    announce(
      silent ? <VolumeX /> : next < 0.5 ? <Volume1 /> : <Volume2 />,
      silent && next > 0 ? "已静音" : `音量 ${Math.round(next * 100)}%`,
    );
  };
  const skip = (seconds: number) => {
    seek(position() + seconds, settleSkips);
    announce(<SkipIcon forward={seconds > 0} />, seconds > 0 ? `快进 ${seconds} 秒` : `后退 ${-seconds} 秒`);
  };
  const keys = (event: KeyboardEvent<HTMLDivElement>) => {
    if ((event.target as HTMLElement).tagName === "INPUT" && event.key !== "Escape") return;
    let handled = true;
    switch (event.key) {
      case " ":
      case "k":
      case "K":
        toggle();
        break;
      case "ArrowLeft":
        skip(-10);
        break;
      case "ArrowRight":
        skip(10);
        break;
      case "ArrowUp":
        changeVolume(0.1);
        break;
      case "ArrowDown":
        changeVolume(-0.1);
        break;
      case "f":
      case "F":
        toggleFullscreen();
        break;
      case "m":
      case "M":
        setMuted(!muted);
        announce(muted ? <Volume2 /> : <VolumeX />, muted ? "取消静音" : "已静音");
        break;
      case "Escape":
        if (panel) setPanel(undefined);
        else if (menu) setMenu(undefined);
        else if (document.fullscreenElement) toggleFullscreen();
        else onClose();
        break;
      default:
        handled = false;
    }
    if (handled) {
      event.preventDefault();
      event.stopPropagation();
      wake();
    }
  };

  // The seek bar scrubs while dragged and seeks once released.
  const bar = useRef<HTMLDivElement>(null);
  const positionAt = (clientX: number) => {
    const box = bar.current?.getBoundingClientRect();
    if (!box || !duration) return 0;
    return Math.max(0, Math.min(1, (clientX - box.left) / box.width)) * duration;
  };
  const startScrub = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (!duration) return;
    event.preventDefault();
    (event.target as HTMLElement).setPointerCapture?.(event.pointerId);
    setScrub(positionAt(event.clientX));
  };
  const moveScrub = (event: ReactPointerEvent<HTMLDivElement>) => {
    const box = bar.current?.getBoundingClientRect();
    if (box)
      setHover({ x: Math.max(0, Math.min(box.width, event.clientX - box.left)), time: positionAt(event.clientX) });
    if (scrub !== undefined) setScrub(positionAt(event.clientX));
  };
  const endScrub = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (scrub === undefined) return;
    const target = positionAt(event.clientX);
    setScrub(undefined);
    seek(target);
  };

  const shown = scrub ?? time;
  const share = duration ? Math.min(1, shown / duration) : 0;
  const titleText = detail ? (detail.type === "episode" && detail.showTitle ? detail.showTitle : detail.title) : "";
  const subtitleText =
    detail?.type === "episode"
      ? [episodeCode(detail.season, detail.episode), detail.title?.startsWith("第 ") ? "" : detail.title]
          .filter(Boolean)
          .join(" · ")
      : "";
  const VolumeIcon = muted || volume === 0 ? VolumeX : volume < 0.5 ? Volume1 : Volume2;
  const episodic = detail?.type === "episode" && Boolean(detail.showId);
  const togglePanel = () => {
    setMenu(undefined);
    setPanel(panel ? undefined : episodic ? "episodes" : "library");
  };
  const modeLabel = playback
    ? playback.mode === "direct"
      ? "原画直连"
      : playback.mode === "remux"
        ? "转换封装"
        : `实时转码 · ${qualityLabels[playback.quality]}`
    : "";

  return (
    <div
      ref={container}
      className={`mc-player ${showChrome ? "chrome" : "bare"} ${panel ? "paneled" : ""}`}
      tabIndex={-1}
      role="dialog"
      aria-label={titleText ? `播放 ${titleText}` : "播放器"}
      onKeyDown={keys}
      onPointerMove={wake}
      onPointerDown={() => wake()}
      {...held}
    >
      <video
        ref={video}
        src={source || undefined}
        className="mc-video"
        playsInline
        preload="auto"
        onClick={() => (panel ? setPanel(undefined) : toggle())}
        onDoubleClick={toggleFullscreen}
        {...events}
      >
        {subtitle !== "off" && detail && (
          <track
            key={`${subtitle}-${source}`}
            ref={trackElement}
            kind="subtitles"
            default
            src={subtitleURL(detail.id, subtitle)}
          />
        )}
      </video>
      <canvas ref={still} className={`mc-still ${frozen ? "shown" : ""}`} aria-hidden="true" />
      {activeCues.length > 0 && (
        <div className="mc-cues" aria-live="off">
          {activeCues.map((cue, index) => (
            <span key={index}>{cue.text}</span>
          ))}
        </div>
      )}
      {(waiting || jump !== undefined) && !failure && <span className="mc-player-spinner" aria-label="正在载入" />}
      {jump !== undefined && !failure && (
        <span className="mc-jump" role="status">
          跳转到 {timecode(jump)}
        </span>
      )}
      {halted && !waiting && jump === undefined && !failure && countdown === undefined && (
        <button type="button" className="mc-player-center" aria-label="播放" onClick={toggle}>
          <Play />
        </button>
      )}
      {notice && (
        <span key={notice.id} className="mc-notice" role="status">
          {notice.icon}
          {notice.text}
        </span>
      )}
      {failure && (
        <div className="mc-player-failure" role="alert">
          <p>{failure}</p>
          <div>
            {detail && playback && (
              <button type="button" className="mc-primary" onClick={() => begin(playback, absolute(), true)}>
                <RotateCcw />
                重试
              </button>
            )}
            <button type="button" className="mc-glass" onClick={onClose}>
              关闭
            </button>
          </div>
        </div>
      )}
      {resumed !== undefined && !failure && (
        <div className="mc-resume" role="status">
          已从 {timecode(resumed)} 继续播放
          <button
            type="button"
            onClick={() => {
              setResumed(undefined);
              seek(0);
            }}
          >
            <RotateCcw />
            从头播放
          </button>
        </div>
      )}
      {countdown !== undefined && detail?.nextId && (
        <div className="mc-next" role="status">
          <span>{countdown} 秒后播放下一集</span>
          <strong>{upNext ?? "下一集"}</strong>
          <div>
            <button type="button" className="mc-primary small" onClick={() => go(detail.nextId!)}>
              <SkipForward />
              立即播放
            </button>
            <button type="button" className="mc-glass small" onClick={() => setCountdown(undefined)}>
              取消
            </button>
          </div>
        </div>
      )}

      <header className="mc-player-top">
        <button
          type="button"
          className="mc-player-button"
          aria-label="退出播放"
          data-tip="退出播放 (Esc)"
          data-tip-at="below start"
          onClick={onClose}
        >
          <ArrowLeft />
        </button>
        <div className="mc-player-title">
          <strong>{titleText}</strong>
          {subtitleText && <small>{subtitleText}</small>}
        </div>
        {playback && (
          <span className={`mc-player-mode ${playback.mode}`} title={playback.reason}>
            {modeLabel}
          </span>
        )}
      </header>

      <footer className="mc-player-bottom" onPointerDown={(event) => event.stopPropagation()}>
        <div
          ref={bar}
          className="mc-seek"
          role="slider"
          aria-label="播放进度"
          aria-valuemin={0}
          aria-valuemax={Math.round(duration)}
          aria-valuenow={Math.round(shown)}
          aria-valuetext={`${timecode(shown)} / ${timecode(duration)}`}
          tabIndex={0}
          onPointerDown={startScrub}
          onPointerMove={moveScrub}
          onPointerUp={endScrub}
          onPointerLeave={() => setHover(undefined)}
        >
          <span className="mc-seek-track">
            {duration > 0 &&
              ranges.map(([start, end]) => (
                <span
                  key={start}
                  className="mc-seek-buffer"
                  style={{
                    left: `${Math.min(100, (start / duration) * 100)}%`,
                    width: `${Math.max(0, Math.min(1, (end - start) / duration)) * 100}%`,
                  }}
                />
              ))}
            <span className="mc-seek-fill" style={{ width: `${share * 100}%` }} />
          </span>
          <span className="mc-seek-thumb" style={{ left: `${share * 100}%` }} />
          {hover && duration > 0 && (
            <span className="mc-seek-tip" style={{ left: hover.x }}>
              {timecode(hover.time)}
            </span>
          )}
        </div>
        <div className="mc-controls">
          {detail?.previousId && (
            <button
              type="button"
              className="mc-player-button"
              aria-label="上一集"
              data-tip="上一集"
              onClick={() => go(detail.previousId!)}
            >
              <SkipBack />
            </button>
          )}
          <button
            type="button"
            className="mc-player-button big"
            aria-label={paused ? "播放" : "暂停"}
            data-tip={paused ? "播放 (空格)" : "暂停 (空格)"}
            data-tip-at={detail?.previousId ? undefined : "start"}
            onClick={toggle}
          >
            {paused ? <Play /> : <Pause />}
          </button>
          {detail?.nextId && (
            <button
              type="button"
              className="mc-player-button"
              aria-label="下一集"
              data-tip="下一集"
              onClick={() => go(detail.nextId!)}
            >
              <SkipForward />
            </button>
          )}
          <button
            type="button"
            className="mc-player-button"
            aria-label="后退 10 秒"
            data-tip="后退 10 秒 (←)"
            onClick={() => seek(position() - 10, settleSkips)}
          >
            <SkipIcon />
          </button>
          <button
            type="button"
            className="mc-player-button"
            aria-label="前进 10 秒"
            data-tip="快进 10 秒 (→)"
            onClick={() => seek(position() + 10, settleSkips)}
          >
            <SkipIcon forward />
          </button>
          <div className="mc-volume">
            <button
              type="button"
              className="mc-player-button"
              aria-label={muted ? "取消静音" : "静音"}
              data-tip={muted ? "取消静音 (M)" : "静音 (M)"}
              onClick={() => setMuted(!muted)}
            >
              <VolumeIcon />
            </button>
            <input
              type="range"
              min={0}
              max={1}
              step={0.05}
              value={muted ? 0 : volume}
              aria-label="音量"
              onChange={(event) => {
                setVolume(Number(event.target.value));
                setMuted(false);
              }}
              style={{ "--mc-level": `${(muted ? 0 : volume) * 100}%` } as CSSProperties}
            />
          </div>
          <span className="mc-time">
            {timecode(shown)}
            <span> / {timecode(duration)}</span>
          </span>
          <span className="mc-controls-gap" />
          {detail && (
            <button
              type="button"
              className={`mc-player-button labeled ${panel ? "on" : ""}`}
              aria-label={episodic ? "选集" : "片库"}
              aria-expanded={Boolean(panel)}
              data-tip={episodic ? "选择其他集" : "换一部看"}
              onClick={togglePanel}
            >
              {episodic ? <ListVideo /> : <Clapperboard />}
              <span>{episodic ? "选集" : "片库"}</span>
            </button>
          )}
          {detail && detail.subtitles.length > 0 && (
            <button
              type="button"
              className={`mc-player-button ${subtitle !== "off" ? "on" : ""}`}
              aria-label="字幕"
              data-tip="字幕"
              aria-expanded={menu === "subtitles"}
              onClick={() => setMenu(menu === "subtitles" ? undefined : "subtitles")}
            >
              <Captions />
            </button>
          )}
          <button
            type="button"
            className="mc-player-button"
            aria-label="播放设置"
            data-tip="画质、倍速与音轨"
            data-tip-at="end"
            aria-expanded={Boolean(menu) && menu !== "subtitles"}
            onClick={() => {
              setPanel(undefined);
              setMenu(menu ? undefined : "settings");
            }}
          >
            <Settings2 />
          </button>
          <button
            type="button"
            className="mc-player-button"
            aria-label={fullscreen ? "退出全屏" : "全屏"}
            data-tip={fullscreen ? "退出全屏 (F)" : "全屏 (F)"}
            data-tip-at="end"
            onClick={toggleFullscreen}
          >
            {fullscreen ? <Minimize /> : <Maximize />}
          </button>
        </div>
        {menu && detail && playback && (
          <div className="mc-player-menu" role="menu" onPointerDown={(event) => event.stopPropagation()}>
            {menu === "settings" && (
              <>
                <button type="button" role="menuitem" onClick={() => setMenu("quality")}>
                  <Gauge />
                  <span>画质</span>
                  <small>{qualityLabels[playback.quality]}</small>
                  <ChevronRight />
                </button>
                {detail.audio.length > 1 && (
                  <button type="button" role="menuitem" onClick={() => setMenu("audio")}>
                    <Languages />
                    <span>音轨</span>
                    <small>{detail.audio[playback.audio]?.label ?? "默认"}</small>
                    <ChevronRight />
                  </button>
                )}
                <button type="button" role="menuitem" onClick={() => setMenu("speed")}>
                  <Play />
                  <span>倍速</span>
                  <small>{rate === 1 ? "正常" : `${rate}×`}</small>
                  <ChevronRight />
                </button>
                {playback.reason && <p className="mc-player-reason">{playback.reason}</p>}
              </>
            )}
            {menu === "quality" &&
              playback.qualities.map((quality) => (
                <button
                  key={quality}
                  type="button"
                  role="menuitemradio"
                  aria-checked={quality === playback.quality}
                  onClick={() => void reconfigure({ quality })}
                >
                  {quality === playback.quality ? <Check /> : <span className="mc-menu-blank" />}
                  <span>{qualityLabels[quality]}</span>
                  {quality !== "original" && <small>转码</small>}
                </button>
              ))}
            {menu === "audio" &&
              detail.audio.map((track) => (
                <button
                  key={track.index}
                  type="button"
                  role="menuitemradio"
                  aria-checked={track.index === playback.audio}
                  onClick={() => void reconfigure({ audio: track.index })}
                >
                  {track.index === playback.audio ? <Check /> : <span className="mc-menu-blank" />}
                  <span>{track.label}</span>
                </button>
              ))}
            {menu === "speed" &&
              rates.map((value) => (
                <button
                  key={value}
                  type="button"
                  role="menuitemradio"
                  aria-checked={value === rate}
                  onClick={() => {
                    setRate(value);
                    setMenu(undefined);
                  }}
                >
                  {value === rate ? <Check /> : <span className="mc-menu-blank" />}
                  <span>{value === 1 ? "正常" : `${value}×`}</span>
                </button>
              ))}
            {menu === "subtitles" &&
              [{ id: "off", label: "关闭字幕", external: false }, ...detail.subtitles].map((track) => (
                <button
                  key={track.id}
                  type="button"
                  role="menuitemradio"
                  aria-checked={track.id === subtitle}
                  onClick={() => {
                    setSubtitle(track.id);
                    setMenu(undefined);
                  }}
                >
                  {track.id === subtitle ? <Check /> : <span className="mc-menu-blank" />}
                  <span>{track.label}</span>
                  {track.id !== "off" && <small>{track.external ? "外挂" : "内嵌"}</small>}
                </button>
              ))}
          </div>
        )}
      </footer>
      {panel && detail && (
        <PlayerPanel
          detail={detail}
          tab={panel}
          onTab={setPanel}
          onClose={() => setPanel(undefined)}
          onPick={(videoId) => {
            setPanel(undefined);
            go(videoId);
          }}
        />
      )}
    </div>
  );
}
