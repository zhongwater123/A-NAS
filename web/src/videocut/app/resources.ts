// Resources by how long they live (docs/architecture/videocut-center.md,
// 资源分级). Register a new one by its level before writing it.
import { useEffect } from "react";

// singlePlayer allows one playing element at a time: a new one pauses the
// last, so nothing plays in the background. It does not clear the source:
// React set it and would not set it again, so the element could not play
// later. The element's page clears it by unmounting it.
export function singlePlayer() {
  let current: HTMLMediaElement | null = null;
  const stop = () => {
    current?.pause();
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

// pool runs at most parallel tasks at once, the rest in turn. A finished
// task hands its place straight to the next waiting one, so a new call
// cannot take it in between and run one too many. Written here rather than
// taken from a library: it is these lines, and the page needs nothing more.
export function pool(parallel: number) {
  let running = 0;
  const waiting: (() => void)[] = [];
  return async function run<T>(task: () => Promise<T>): Promise<T> {
    if (running < parallel) running += 1;
    else await new Promise<void>((resolve) => waiting.push(resolve));
    try {
      return await task();
    } finally {
      const next = waiting.shift();
      if (next) next();
      else running -= 1;
    }
  };
}

/** Reading files in the browser: 4 at a time (窗口级). */
export const importPool = pool(4);

// An object URL a blob's users share, revoked once none is left. Revoking
// waits a turn, so a component unmounted and mounted again at once, as
// React's StrictMode does, keeps the same live URL.
const shared = new WeakMap<Blob, { url: string; users: number }>();

function sharedURL(blob: Blob) {
  let entry = shared.get(blob);
  if (!entry) {
    entry = { url: URL.createObjectURL(blob), users: 0 };
    shared.set(blob, entry);
  }
  return entry;
}

/** An object URL for a page's own use (页面级), revoked when the page lets it go. */
export function useObjectURL(blob: Blob | undefined): string | undefined {
  const url = blob ? sharedURL(blob).url : undefined;
  useEffect(() => {
    if (!blob) return;
    const entry = sharedURL(blob);
    entry.users += 1;
    return () => {
      entry.users -= 1;
      setTimeout(() => {
        if (entry.users > 0 || shared.get(blob) !== entry) return;
        URL.revokeObjectURL(entry.url);
        shared.delete(blob);
      });
    };
  }, [blob]);
  return url;
}
