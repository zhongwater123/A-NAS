import { useCallback, useEffect, useRef, useState } from "react";

export const localConsoleScreenSaverIdleMs = 180_000;
export const localConsoleScreenSaverManifest = "/local-console/screensavers";

const screensaverVideoPrefix = "/local-console/screensavers/";
const screensaverVideoPattern = /^\/local-console\/screensavers\/[0-9a-f]{64}\.mp4$/;
const activityEvents = ["pointerdown", "pointermove", "keydown", "wheel", "touchstart"] as const;

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

export function selectScreenSaverVideo(
  videos: readonly string[],
  random: () => number = Math.random,
): string | undefined {
  if (videos.length === 0) return undefined;
  const index = Math.min(videos.length - 1, Math.max(0, Math.floor(random() * videos.length)));
  return videos[index];
}

export function LocalConsoleScreenSaver({
  enabled,
  idleMs = localConsoleScreenSaverIdleMs,
  loadVideos = loadScreenSaverVideos,
  random = Math.random,
}: {
  enabled: boolean;
  idleMs?: number;
  loadVideos?: () => Promise<string[]>;
  random?: () => number;
}) {
  const timer = useRef<number | undefined>(undefined);
  const video = useRef<HTMLVideoElement>(null);
  const playlist = useRef<string[]>([]);
  const current = useRef<string | undefined>(undefined);
  const activeRef = useRef(false);
  const [active, setActive] = useState(false);
  const [currentSrc, setCurrentSrc] = useState<string>();
  const [ready, setReady] = useState(false);

  const hide = useCallback(() => {
    activeRef.current = false;
    setActive(false);
  }, []);

  const selectVideo = useCallback(() => {
    const selected = selectScreenSaverVideo(playlist.current, random);
    if (!selected) return false;
    current.current = selected;
    setCurrentSrc(selected);
    return true;
  }, [random]);

  const disableVideo = useCallback((failed: string) => {
    if (current.current !== failed) return;
    playlist.current = playlist.current.filter((candidate) => candidate !== failed);
    if (playlist.current.length > 0 && selectVideo()) return;
    current.current = undefined;
    setCurrentSrc(undefined);
    setReady(false);
    hide();
  }, [hide, selectVideo]);

  useEffect(() => {
    if (!enabled) return;
    let cancelled = false;
    setReady(false);
    void loadVideos().then((videos) => {
      if (cancelled) return;
      playlist.current = videos;
      current.current = undefined;
      setCurrentSrc(undefined);
      setReady(videos.length > 0);
    }).catch(() => {
      if (!cancelled) setReady(false);
    });
    return () => {
      cancelled = true;
      playlist.current = [];
      current.current = undefined;
    };
  }, [enabled, loadVideos]);

  useEffect(() => {
    if (!enabled || !ready) return;

    const clearTimer = () => {
      if (timer.current !== undefined) window.clearTimeout(timer.current);
      timer.current = undefined;
    };
    const schedule = () => {
      clearTimer();
      if (document.hidden) return;
      timer.current = window.setTimeout(() => {
        if (!selectVideo()) return;
        activeRef.current = true;
        setActive(true);
      }, idleMs);
    };
    const activity = (event: Event) => {
      if (activeRef.current) {
        event.preventDefault();
        event.stopPropagation();
        event.stopImmediatePropagation();
        hide();
      }
      schedule();
    };
    const visibilityChanged = () => {
      hide();
      if (document.hidden) clearTimer();
      else schedule();
    };

    activityEvents.forEach((name) => window.addEventListener(name, activity, { capture: true, passive: false }));
    document.addEventListener("visibilitychange", visibilityChanged);
    schedule();
    return () => {
      clearTimer();
      activityEvents.forEach((name) => window.removeEventListener(name, activity, { capture: true }));
      document.removeEventListener("visibilitychange", visibilityChanged);
      activeRef.current = false;
    };
  }, [enabled, hide, idleMs, ready, selectVideo]);

  useEffect(() => {
    if (!active || !currentSrc || !video.current) return;
    const attempted = currentSrc;
    void video.current.play().catch(() => disableVideo(attempted));
  }, [active, currentSrc, disableVideo]);

  if (!enabled || !ready || !active || !currentSrc) return null;

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
