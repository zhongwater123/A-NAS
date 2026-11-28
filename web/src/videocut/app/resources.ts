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

/** The desktop's one hover preview and one song preview (桌面级, one each). */
export const hoverPreview = singlePlayer();
export const songPreview = singlePlayer();

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
