// dsh-pet's animation chain (dsh-pet/client/pet/animation, patched) where a clip does not come: dsh-pet
// read its clips from disk, A-NAS fetches them from the NAS, so a fetch can fail, time out or answer an
// HTTP error, and a clip can fail to decode or to play. Driven here without the card: two videos, a
// loader the test answers, and the card's part (what plays next) left to each test.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { createOwnAnimation, type OwnAnimation } from "./dsh-pet/client/pet/animation/animation";
import { type AssetHandlers, createAssetLoader } from "./dsh-pet/client/pet/animation/asset-loader";
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

/** The clip in front is still playing (jsdom's videos never play: they are always paused). */
const playing = (video: HTMLVideoElement) =>
  Object.defineProperty(video, "paused", { configurable: true, value: false });

beforeEach(() => {
  vi.useFakeTimers();
  vi.stubGlobal("matchMedia", (query: string) => ({ matches: false, media: query }));
  vi.spyOn(console, "warn").mockImplementation(() => {});
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

  it("that does not come is answered failed and not fetched again; the chain goes on after a second", () => {
    const c = chain();
    c.play("点击回应-元气挥手", true, "pb-3");
    c.loads[0].handlers.onError(new Error("asset HTTP 503"));
    expect(c.reports).toEqual([["pb-3", "failed", "asset-load"]]);
    vi.advanceTimersByTime(999);
    expect(c.ended).toEqual([]);
    vi.advanceTimersByTime(1);
    expect(c.loads).toHaveLength(1);
    expect(c.ended).toEqual([{ kind: "interaction-done" }]);
  });
});

describe("a clip of the chain that does not come", () => {
  it("is fetched once more after a second, and the chain goes on two seconds after that one fails too", () => {
    const c = chain();
    c.play("待机呼吸休闲");
    c.loads[0].handlers.onError(new TypeError("Failed to fetch"));
    vi.advanceTimersByTime(999);
    expect(c.loads).toHaveLength(1);
    vi.advanceTimersByTime(1);
    expect(c.loads.map((load) => load.clip)).toEqual(["待机呼吸休闲", "待机呼吸休闲"]);
    c.loads[1].handlers.onError(new Error("asset HTTP 503"));
    vi.advanceTimersByTime(1_999);
    expect(c.ended).toEqual([]);
    vi.advanceTimersByTime(1);
    // As if it had played to its end: the card picks what comes next.
    expect(c.ended).toEqual([{ kind: "random" }]);
  });

  it("plays when its second fetch arrives", async () => {
    const c = chain();
    c.play("待机呼吸休闲");
    c.loads[0].handlers.onError(new TypeError("Failed to fetch"));
    vi.advanceTimersByTime(1_000);
    await c.arrive();
    expect(c.front().src).toBe("blob:clip-1");
    vi.advanceTimersByTime(60_000);
    expect(c.ended).toEqual([]);
  });

  it("waits twice as long for every clip in a row that does not come, up to 30 s, and a second again after one plays", async () => {
    const c = chain();
    // The NAS is out of reach: every fetch fails, and the card plays the next clip whenever the chain goes on.
    const asked: number[] = [];
    let goneOn = 0;
    c.play("待机呼吸休闲");
    for (let step = 0; step < 2_000 && asked.length < 8; step++) {
      if (c.loads.length > asked.length) {
        asked.push(Date.now());
        c.loads.at(-1)?.handlers.onError(new TypeError("Failed to fetch"));
      }
      if (c.ended.length > goneOn) {
        goneOn = c.ended.length;
        c.play("待机呼吸休闲");
        continue;
      }
      vi.advanceTimersByTime(100);
    }
    const waits = asked.slice(1).map((at, i) => at - asked[i]);
    expect(waits).toEqual([1_000, 2_000, 4_000, 8_000, 16_000, 30_000, 30_000]);
    // The NAS is back: a clip plays, and the next one that does not come is fetched again a second on.
    c.play("待机呼吸休闲");
    await c.arrive();
    c.play("点击回应-元气挥手");
    c.loads.at(-1)?.handlers.onError(new TypeError("Failed to fetch"));
    const before = c.loads.length;
    vi.advanceTimersByTime(1_000);
    expect(c.loads).toHaveLength(before + 1);
  });

  it("does not decode: it is let go as a failed fetch is", () => {
    const c = chain();
    c.play("待机呼吸休闲");
    c.loads[0].handlers.onObjectUrl("blob:broken");
    c.videos[1].dispatchEvent(new Event("error"));
    vi.advanceTimersByTime(1_000);
    expect(c.loads).toHaveLength(2);
  });

  it("will not play: the chain does not stay on its first frame", async () => {
    const c = chain();
    vi.mocked(c.videos[1].play).mockRejectedValue(new DOMException("not allowed", "NotAllowedError"));
    c.play("点击回应-元气挥手", true, "pb-4");
    await c.arrive();
    expect(c.reports).toEqual([["pb-4", "failed", "play-rejected"]]);
    vi.advanceTimersByTime(1_000);
    expect(c.ended).toEqual([{ kind: "interaction-done" }]);
  });

  it("goes on from a looping clip in front, which never ends by itself (sleep's loop)", () => {
    const c = chain();
    const loop = c.videos[0];
    loop.classList.add("is-front");
    loop.loop = true;
    playing(loop);
    c.play("睡觉-起来");
    c.loads[0].handlers.onError(new TypeError("Failed to fetch"));
    vi.advanceTimersByTime(1_000);
    c.loads[1].handlers.onError(new TypeError("Failed to fetch"));
    vi.advanceTimersByTime(2_000);
    expect(c.ended).toHaveLength(1);
  });

  it("leaves a clip in front that still plays to go on at its own end", () => {
    const c = chain();
    const shown = c.videos[0];
    shown.classList.add("is-front");
    playing(shown);
    shown.onended = (event) => c.ended.push(c.anim.handleEnded(event));
    c.play("点击回应-元气挥手");
    c.loads[0].handlers.onError(new TypeError("Failed to fetch"));
    vi.advanceTimersByTime(1_000);
    c.loads[1].handlers.onError(new TypeError("Failed to fetch"));
    vi.advanceTimersByTime(60_000);
    expect(c.ended).toEqual([]);
    shown.dispatchEvent(new Event("ended"));
    expect(c.ended).toEqual([{ kind: "interaction-done" }]);
  });

  it("that an earlier switch put in the video does not fail the switch that took the video over", async () => {
    const c = chain();
    c.play("待机呼吸休闲");
    // The idle clip is in the back video, still decoding, when a command's switch takes that video over.
    c.loads[0].handlers.onObjectUrl("blob:first");
    c.play("点击回应-元气挥手", true, "pb-9");
    c.videos[1].dispatchEvent(new Event("error"));
    expect(c.reports).toEqual([]);
    await c.arrive();
    expect(c.front()).toBe(c.videos[1]);
    expect(c.front().src).toBe("blob:clip-1");
    expect(c.reports).toEqual([["pb-9", "playing"]]);
    vi.advanceTimersByTime(60_000);
    expect(c.loads.map((load) => load.clip)).toEqual(["待机呼吸休闲", "点击回应-元气挥手"]);
  });

  it("is not fetched again while the card is frozen, and is once the freeze ends", () => {
    const c = chain();
    c.play("待机呼吸休闲");
    c.loads[0].handlers.onError(new TypeError("Failed to fetch"));
    // A lock or the page's pause freezes the card before the wait is over.
    c.anim.setFrozen(true);
    vi.advanceTimersByTime(60_000);
    expect(c.loads).toHaveLength(1);
    expect(c.ended).toEqual([]);
    c.anim.setFrozen(false);
    vi.advanceTimersByTime(999);
    expect(c.loads).toHaveLength(1);
    vi.advanceTimersByTime(1);
    expect(c.loads.map((load) => load.clip)).toEqual(["待机呼吸休闲", "待机呼吸休闲"]);
  });

  it("does not go on while the card is frozen, and does once the freeze ends", () => {
    const c = chain();
    c.play("点击回应-元气挥手", true, "pb-5");
    c.loads[0].handlers.onError(new TypeError("Failed to fetch"));
    c.anim.setFrozen(true);
    vi.advanceTimersByTime(60_000);
    expect(c.ended).toEqual([]);
    c.anim.setFrozen(false);
    vi.advanceTimersByTime(1_000);
    expect(c.ended).toEqual([{ kind: "interaction-done" }]);
  });

  it("is not fetched again when the card's resume switches to another clip", () => {
    const c = chain();
    c.play("待机呼吸休闲");
    c.loads[0].handlers.onError(new TypeError("Failed to fetch"));
    c.anim.setFrozen(true);
    vi.advanceTimersByTime(1_000);
    c.anim.setFrozen(false);
    c.play("点击回应-元气挥手");
    vi.advanceTimersByTime(60_000);
    expect(c.loads.map((load) => load.clip)).toEqual(["待机呼吸休闲", "点击回应-元气挥手"]);
  });

  it("is not fetched again once another switch took over", () => {
    const c = chain();
    c.play("待机呼吸休闲");
    c.loads[0].handlers.onError(new TypeError("Failed to fetch"));
    c.play("点击回应-元气挥手");
    vi.advanceTimersByTime(60_000);
    expect(c.loads.map((load) => load.clip)).toEqual(["待机呼吸休闲", "点击回应-元气挥手"]);
    expect(c.ended).toEqual([]);
  });
});

describe("the clip loader", () => {
  it("gives up on a clip that takes longer than its 10 s, and on an HTTP error", async () => {
    const asked: AbortSignal[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string, init: RequestInit) => {
        if (url.endsWith("broken")) return Promise.resolve(new Response(null, { status: 503 }));
        asked.push(init.signal as AbortSignal);
        return new Promise((_, reject) =>
          init.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError"))),
        );
      }),
    );
    const loader = createAssetLoader(10_000);
    const errors: unknown[] = [];
    const handlers = { onObjectUrl: () => {}, onError: (error: unknown) => errors.push(error) };
    loader.load("/clip/broken", handlers);
    loader.load("/clip/slow", handlers);
    await vi.advanceTimersByTimeAsync(9_999);
    expect(errors.map(String)).toEqual(["Error: asset HTTP 503"]);
    await vi.advanceTimersByTimeAsync(1);
    expect(asked[0].aborted).toBe(true);
    expect(errors).toHaveLength(2);
  });
});
