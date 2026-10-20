import { useCallback, useEffect, useRef, useState } from "react";

import { readLocalDisplay } from "./api";

export const localConsoleScreenSaverIdleMs = 180_000;
export const localConsoleScreenSaverManifest = "/local-console/screensavers";
// defaultSleepAfterMinutes applies when the setting cannot be read, so the
// screen never stays lit just because the service was unreachable.
export const defaultSleepAfterMinutes = 30;

const screensaverVideoPrefix = "/local-console/screensavers/";
const screensaverVideoPattern = /^\/local-console\/screensavers\/[0-9a-f]{64}\.mp4$/;
const activityEvents = ["pointerdown", "pointermove", "keydown", "wheel", "touchstart"] as const;

// awake shows the page; saver plays a video over it; asleep is plain black.
type Phase = "awake" | "saver" | "asleep";

export function isLocalConsole(search = window.location.search): boolean {
  return new URLSearchParams(search).get("local-console") === "1";
}

export async function loadScreenSaverVideos(): Promise<string[]> {
  const response = await fetch(localConsoleScreenSaverManifest, { cache: "no-store" });
  if (!response.ok) throw new Error(`screen saver manifest returned ${response.status}`);
  const manifest = (await response.json()) as { videos?: unknown };
  if (!Array.isArray(manifest.videos)) throw new Error("screen saver manifest is invalid");
  return [...new Set(manifest.videos.filter((video): video is string =>
    typeof video === "string" && video.startsWith(screensaverVideoPrefix) && screensaverVideoPattern.test(video),
  ))];
}

export async function loadSleepAfterMinutes(): Promise<number> {
  return (await readLocalDisplay()).sleepAfterMinutes;
}

export function selectScreenSaverVideo(
  videos: readonly string[],
  random: () => number = Math.random,
): string | undefined {
  if (videos.length === 0) return undefined;
  const index = Math.min(videos.length - 1, Math.max(0, Math.floor(random() * videos.length)));
  return videos[index];
}

// LocalConsoleScreenSaver plays a video after idleMs without input and turns
// the screen black once the video has played the minutes set in 设置 →
// 个性化. Without videos (or on the sign-in page, where videos is false) the
// screen goes black at the same moment. The first input after either only
// wakes the page.
export function LocalConsoleScreenSaver({
  enabled,
  videos = true,
  idleMs = localConsoleScreenSaverIdleMs,
  loadVideos = loadScreenSaverVideos,
  loadSleepAfter = loadSleepAfterMinutes,
  random = Math.random,
}: {
  enabled: boolean;
  videos?: boolean;
  idleMs?: number;
  loadVideos?: () => Promise<string[]>;
  loadSleepAfter?: () => Promise<number>;
  random?: () => number;
}) {
  const idleTimer = useRef<number | undefined>(undefined);
  const sleepTimer = useRef<number | undefined>(undefined);
  // idlePeriod changes with every input, so a setting read for an idle period
  // that has since ended schedules nothing.
  const idlePeriod = useRef(0);
  const video = useRef<HTMLVideoElement>(null);
  const playlist = useRef<string[]>([]);
  const current = useRef<string | undefined>(undefined);
  const phaseRef = useRef<Phase>("awake");
  const [phase, setPhaseState] = useState<Phase>("awake");
  const [currentSrc, setCurrentSrc] = useState<string>();

  const setPhase = useCallback((next: Phase) => {
    phaseRef.current = next;
    setPhaseState(next);
  }, []);

  const selectVideo = useCallback(() => {
    const selected = selectScreenSaverVideo(playlist.current, random);
    if (!selected) return false;
    current.current = selected;
    setCurrentSrc(selected);
    return true;
  }, [random]);

  // A failed video leaves the pool for this page; the idle period, and so
  // the moment the screen goes black, carries on.
  const disableVideo = useCallback((failed: string) => {
    if (current.current !== failed) return;
    playlist.current = playlist.current.filter((candidate) => candidate !== failed);
    if (selectVideo()) return;
    current.current = undefined;
    setCurrentSrc(undefined);
    if (phaseRef.current === "saver") setPhase("awake");
  }, [selectVideo, setPhase]);

  useEffect(() => {
    if (!enabled || !videos) return;
    let cancelled = false;
    void loadVideos().then((loaded) => {
      if (cancelled) return;
      playlist.current = loaded;
      current.current = undefined;
      setCurrentSrc(undefined);
    }).catch(() => undefined);
    return () => {
      cancelled = true;
      playlist.current = [];
      current.current = undefined;
    };
  }, [enabled, videos, loadVideos]);

  useEffect(() => {
    if (!enabled) return;

    const clearTimers = () => {
      window.clearTimeout(idleTimer.current);
      window.clearTimeout(sleepTimer.current);
      idleTimer.current = undefined;
      sleepTimer.current = undefined;
      idlePeriod.current += 1;
    };
    const idle = () => {
      const period = idlePeriod.current;
      const since = Date.now();
      if (videos && selectVideo()) setPhase("saver");
      void loadSleepAfter().catch(() => defaultSleepAfterMinutes).then((minutes) => {
        if (period !== idlePeriod.current || !Number.isFinite(minutes) || minutes <= 0) return;
        sleepTimer.current = window.setTimeout(() => setPhase("asleep"), Math.max(0, minutes * 60_000 - (Date.now() - since)));
      });
    };
    const schedule = () => {
      clearTimers();
      if (!document.hidden) idleTimer.current = window.setTimeout(idle, idleMs);
    };
    const activity = (event: Event) => {
      if (phaseRef.current !== "awake") {
        event.preventDefault();
        event.stopPropagation();
        event.stopImmediatePropagation();
        setPhase("awake");
      }
      schedule();
    };
    const visibilityChanged = () => {
      setPhase("awake");
      schedule();
    };

    activityEvents.forEach((name) => window.addEventListener(name, activity, { capture: true, passive: false }));
    document.addEventListener("visibilitychange", visibilityChanged);
    schedule();
    return () => {
      clearTimers();
      activityEvents.forEach((name) => window.removeEventListener(name, activity, { capture: true }));
      document.removeEventListener("visibilitychange", visibilityChanged);
      setPhase("awake");
    };
  }, [enabled, videos, idleMs, loadSleepAfter, selectVideo, setPhase]);

  useEffect(() => {
    if (phase !== "saver" || !currentSrc || !video.current) return;
    const attempted = currentSrc;
    void video.current.play().catch(() => disableVideo(attempted));
  }, [phase, currentSrc, disableVideo]);

  if (!enabled) return null;

  if (phase === "asleep") {
    return (
      <section className="local-console-sleep" role="dialog" aria-label="息屏">
        <p className="sr-only">按任意键、移动鼠标或触摸屏幕即可唤醒</p>
      </section>
    );
  }

  if (phase !== "saver" || !currentSrc) return null;

  return (
    <section className="local-console-screensaver" role="dialog" aria-label="屏幕保护程序">
      <video
        ref={video}
        src={currentSrc}
        autoPlay
        loop
        muted
        playsInline
        preload="auto"
        onError={() => disableVideo(currentSrc)}
      />
      <p className="sr-only">移动鼠标、按键或触摸屏幕即可返回桌面</p>
    </section>
  );
}
