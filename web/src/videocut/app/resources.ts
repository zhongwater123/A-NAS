// Resources by how long they live (docs/architecture/videocut-center.md,
// 资源分级). Register a new one by its level before writing it.
import { useEffect, useMemo } from "react";

// singlePlayer allows one playing element at a time: a new one stops the
// last and clears its source, so nothing decodes in the background.
export function singlePlayer() {
  let current: HTMLMediaElement | null = null;
  const stop = () => {
    if (!current) return;
    current.pause();
    current.removeAttribute("src");
    current.load();
    current = null;
  };
  return {
    play: (element: HTMLMediaElement) => {
      if (current !== element) stop();
      current = element;
      void element.play().catch(() => undefined);
    },
    stop,
  };
}

// One hover preview and one song preview in the whole page: a new one stops
// the last. A page uses them through usePreview, so they stop when it goes.
const hoverPreview = singlePlayer();
const songPreview = singlePlayer();

/** The hover preview or the song preview, stopped and cleared when the page using it goes (页面级). */
export function usePreview(which: "hover" | "song") {
  const player = which === "hover" ? hoverPreview : songPreview;
  useEffect(() => () => player.stop(), [player]);
  return player;
}

// pool runs at most parallel tasks at once, the rest in turn.
export function pool(parallel: number) {
  let running = 0;
  const waiting: (() => void)[] = [];
  return async function run<T>(task: () => Promise<T>): Promise<T> {
    if (running >= parallel) await new Promise<void>((resolve) => waiting.push(resolve));
    running += 1;
    try {
      return await task();
    } finally {
      running -= 1;
      waiting.shift()?.();
    }
  };
}

/** Reading files in the browser: 4 at a time (窗口级). */
export const importPool = pool(4);

/** An object URL for a page's own use (页面级), revoked when the page lets it go. */
export function useObjectURL(blob: Blob | undefined): string | undefined {
  const url = useMemo(() => (blob ? URL.createObjectURL(blob) : undefined), [blob]);
  useEffect(
    () => () => {
      if (url) URL.revokeObjectURL(url);
    },
    [url],
  );
  return url;
}
