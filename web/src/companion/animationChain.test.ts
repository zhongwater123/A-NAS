// dsh-pet's animation chain (dsh-pet/client/pet/animation, patched), driven without the card: two
// videos, a loader the test answers, and the card's part (what plays next) left to each test.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { createOwnAnimation, type OwnAnimation } from "./dsh-pet/client/pet/animation/animation";
import type { AssetHandlers } from "./dsh-pet/client/pet/animation/asset-loader";
import type { EndedCue } from "./dsh-pet/client/pet/animation/cues";
import type { Animations } from "./dsh-pet/shared/types";

const animations = {
  idle: ["待机呼吸休闲"],
  turn: [],
  drag: [],
  clicks: ["点击回应-元气挥手"],
  moves: { default: {}, actions: [] },
  categories: [],
  events: { workStatus: [], whisper: [] },
} as unknown as Animations;

interface Chain {
  anim: OwnAnimation;
  videos: HTMLVideoElement[];
  /** The fetches the chain asked for, by clip, in order, with how to answer each. */
  loads: { clip: string; handlers: AssetHandlers }[];
  reports: [string, string, string?][];
  ended: EndedCue[];
  /** Plays a clip as the card does: its name becomes the current animation, then the chain switches. */
  play: (clip: string, once?: boolean, playbackId?: string | null) => void;
  front: () => HTMLVideoElement;
  /** The last fetch arrives and decodes: its clip goes in front. */
  arrive: () => Promise<void>;
}

function chain(): Chain {
  const videos = [document.createElement("video"), document.createElement("video")];
  for (const video of videos) {
    video.play = vi.fn().mockResolvedValue(undefined);
    video.pause = vi.fn();
    video.load = vi.fn();
  }
  const loads: Chain["loads"] = [];
  const reports: Chain["reports"] = [];
  const ended: EndedCue[] = [];
  let current = "";
  let made = 0;
  const anim = createOwnAnimation({
    videoA: () => videos[0],
    videoB: () => videos[1],
    cfg: () => ({ id: "main", animations }),
    currentAnim: () => current,
    workStatus: () => null,
    facing: () => "left",
    report: (playbackId, status, reason) => reports.push(reason ? [playbackId, status, reason] : [playbackId, status]),
    clearCuePlayback: () => {},
    onFrontReady: () => {},
    onEnded: (cue) => ended.push(cue),
    isDragging: () => false,
    initiallyFrozen: false,
    loader: {
      load: (url, handlers) =>
        loads.push({ clip: decodeURIComponent(url).replace(/^.*\/|\.(webm|mov).*$/g, ""), handlers }),
      abortAll: () => {},
      release: () => {},
    },
  });
  const front = () => videos.find((video) => video.classList.contains("is-front")) as HTMLVideoElement;
  return {
    anim,
    videos,
    loads,
    reports,
    ended,
    front,
    play: (clip, once = true, playbackId = null) => {
      current = clip;
      anim.switchTo(clip, once, playbackId);
    },
    arrive: async () => {
      const last = loads.at(-1);
      if (!last) throw new Error("nothing is loading");
      last.handlers.onObjectUrl(`blob:clip-${++made}`);
      const loading = videos.find((video) => video.src === `blob:clip-${made}`) as HTMLVideoElement;
      loading.dispatchEvent(new Event("loadeddata"));
      await Promise.resolve();
    },
  };
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.stubGlobal("matchMedia", (query: string) => ({ matches: false, media: query }));
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("a command's clip", () => {
  it("is answered cancelled at once when another switch takes over while it loads", () => {
    const c = chain();
    c.play("点击回应-元气挥手", true, "pb-1");
    c.play("待机呼吸休闲");
    expect(c.reports).toEqual([["pb-1", "cancelled", "superseded"]]);
  });

  it("takes over the same clip the chain is loading, and is answered by how that clip goes", async () => {
    const c = chain();
    c.play("点击回应-元气挥手");
    c.play("点击回应-元气挥手", true, "pb-2");
    // Not playing before it shows.
    expect(c.reports).toEqual([]);
    await c.arrive();
    expect(c.reports).toEqual([["pb-2", "playing"]]);
    c.front().dispatchEvent(new Event("ended"));
    expect(c.reports).toEqual([
      ["pb-2", "playing"],
      ["pb-2", "ended"],
    ]);
  });

  it("hands the clip loading for one command to the next command for it, cutting the first short", async () => {
    const c = chain();
    c.play("点击回应-元气挥手", true, "pb-1");
    c.play("点击回应-元气挥手", true, "pb-2");
    expect(c.reports).toEqual([["pb-1", "cancelled", "superseded"]]);
    await c.arrive();
    c.front().dispatchEvent(new Event("ended"));
    expect(c.reports.slice(1)).toEqual([
      ["pb-2", "playing"],
      ["pb-2", "ended"],
    ]);
  });
});
