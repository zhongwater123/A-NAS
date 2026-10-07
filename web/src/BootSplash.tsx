import { useEffect, useRef, useState } from "react";

import { startBootIdent, type BootIdentPlayer } from "./bootIdent";

export const bootIdentStorageKey = "a-nas.boot-ident.played.v1";

// The ident plays once per browser tab: a kiosk restart or a new tab shows it,
// a reload does not. Users who ask for reduced motion never see it.
export function shouldPlayBootIdent(): boolean {
  if (window.matchMedia?.("(prefers-reduced-motion: reduce)").matches) return false;
  try {
    return sessionStorage.getItem(bootIdentStorageKey) === null;
  } catch {
    return true;
  }
}

// Full-screen startup ident above the desktop. The desktop keeps loading
// underneath; any key or pointer press skips to the switch-off.
export function BootSplash() {
  const [active, setActive] = useState(shouldPlayBootIdent);
  const shellRef = useRef<HTMLDivElement>(null);
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const playerRef = useRef<BootIdentPlayer | null>(null);

  useEffect(() => {
    if (!active || !canvasRef.current) return;
    try {
      sessionStorage.setItem(bootIdentStorageKey, "1");
    } catch {
      // Storage can be unavailable; the ident then plays again on the next load.
    }
    const player = startBootIdent(canvasRef.current, {
      onFade: (opacity) => {
        if (shellRef.current) shellRef.current.style.opacity = String(opacity);
      },
      onDone: () => setActive(false),
    });
    if (!player) {
      setActive(false);
      return;
    }
    playerRef.current = player;
    // Keys must not reach the login form or the desktop underneath while the ident is up.
    const onKey = (event: KeyboardEvent) => {
      event.preventDefault();
      event.stopPropagation();
      player.skip();
    };
    window.addEventListener("keydown", onKey, true);
    return () => {
      window.removeEventListener("keydown", onKey, true);
      player.stop();
      playerRef.current = null;
    };
  }, [active]);

  if (!active) return null;
  return (
    <div ref={shellRef} className="boot-splash" aria-hidden="true" onPointerDown={() => playerRef.current?.skip()}>
      <canvas ref={canvasRef} />
    </div>
  );
}
