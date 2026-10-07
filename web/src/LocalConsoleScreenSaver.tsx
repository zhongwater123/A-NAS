import { useEffect, useRef, useState } from "react";

export const localConsoleScreenSaverIdleMs = 180_000;
export const localConsoleScreenSaverVideo = "/local-console/screensaver.mp4";

const activityEvents = ["pointerdown", "pointermove", "keydown", "wheel", "touchstart"] as const;

export function isLocalConsole(search = window.location.search): boolean {
  return new URLSearchParams(search).get("local-console") === "1";
}

export function LocalConsoleScreenSaver({
  enabled,
  idleMs = localConsoleScreenSaverIdleMs,
  videoSrc = localConsoleScreenSaverVideo,
}: {
  enabled: boolean;
  idleMs?: number;
  videoSrc?: string;
}) {
  const timer = useRef<number | undefined>(undefined);
  const video = useRef<HTMLVideoElement>(null);
  const activeRef = useRef(false);
  const [active, setActive] = useState(false);
  const [available, setAvailable] = useState(true);

  const hide = () => {
    activeRef.current = false;
    setActive(false);
  };

  useEffect(() => {
    if (!enabled || !available) return;

    const clearTimer = () => {
      if (timer.current !== undefined) window.clearTimeout(timer.current);
      timer.current = undefined;
    };
    const schedule = () => {
      clearTimer();
      if (document.hidden) return;
      timer.current = window.setTimeout(() => {
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
  }, [available, enabled, idleMs]);

  useEffect(() => {
    if (!active || !video.current) return;
    void video.current.play().catch(() => {
      hide();
      setAvailable(false);
    });
  }, [active]);

  if (!enabled || !available || !active) return null;

  return (
    <section className="local-console-screensaver" role="dialog" aria-label="屏幕保护程序">
      <video
        ref={video}
        src={videoSrc}
        autoPlay
        loop
        muted
        playsInline
        preload="auto"
        onError={() => {
          hide();
          setAvailable(false);
        }}
      />
      <p className="sr-only">移动鼠标、按键或触摸屏幕即可返回桌面</p>
    </section>
  );
}
