// dsh-pet's card animation hook (dsh-pet/client/pet/card/animation.ts, patched): the effect that switches the
// chain as the card's state changes. Driven here with the card's state as props, so that a test decides when
// each render happens; the page renders when React gets to it, which may be after a clip has already shown.
import { fireEvent, render, waitFor } from "@testing-library/react";
import { HttpResponse } from "msw";
import { useEffect, useRef } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { http, server } from "../test/server";
import { createCardAnimation } from "./dsh-pet/client/pet/card/animation";
import type { CardLive } from "./dsh-pet/client/pet/card/live";
import type { CardStateApi } from "./dsh-pet/client/pet/card/state";
import type { RuntimePet } from "./dsh-pet/client/pet/types";
import type { Animations } from "./dsh-pet/shared/types";

const origin = window.location.origin;
const IDLE = "待机呼吸休闲";
const WAVE = "点击回应-元气挥手";
const animations = {
  idle: [IDLE],
  turn: [],
  drag: [],
  clicks: [WAVE],
  moves: { default: {}, actions: [] },
  categories: [],
  events: { workStatus: [], whisper: [] },
} as unknown as Animations;

const useCardAnimation = createCardAnimation({ useEffect, useRef });

interface Shown {
  anim: string;
  once: boolean;
  seq: number;
  cuePlayback: string | null;
}

let fetched: string[] = [];
let reports: string[] = [];
const videos = () => [document.createElement("video"), document.createElement("video")];

function Card({ state, elements }: { state: Shown; elements: HTMLVideoElement[] }) {
  const card = {
    state,
    // What plays after a clip's end, which these tests do not reach.
    animRef: useRef(state.anim),
    facingRef: useRef("left"),
    actions: { bumpSeq: () => {}, setCustomPos: () => {} },
  } as unknown as CardStateApi;
  const cfgRef = useRef({ id: "main", animations } as unknown as RuntimePet);
  const live = useRef({
    squash: { start: () => {}, stop: () => {} },
    walk: { hasPending: () => false, start: () => {} },
    drag: { isActive: () => false, stopFollow: () => {} },
    flight: { stop: () => {} },
    flow: null,
    center: () => ({ x: 0, y: 0 }),
    stopMove: () => {},
  } as unknown as CardLive);
  useCardAnimation({
    card,
    cfgRef,
    workStatusRef: useRef(null),
    live,
    pendingSquash: useRef(false),
    videoA: () => elements[0],
    videoB: () => elements[1],
    report: (playbackId, status, reason) => reports.push([playbackId, status, reason].filter(Boolean).join(" ")),
    // The page's card clears it in a render of its own, later: each test renders that itself.
    setCuePlayback: () => {},
    petId: "main",
  });
  return null;
}

/** The clip the chain fetched arrives and decodes: it goes in front. */
const arrive = async (elements: HTMLVideoElement[], clip: string) => {
  await waitFor(() => expect(fetched).toContain(clip));
  const back = await waitFor(() => {
    const loaded = elements.find((video) => !video.classList.contains("is-front") && video.src.startsWith("blob:"));
    expect(loaded).toBeDefined();
    return loaded!;
  });
  fireEvent(back, new Event("loadeddata"));
  await waitFor(() => expect(back.classList.contains("is-front")).toBe(true));
};
const settle = () => new Promise((resolve) => setTimeout(resolve, 50));

beforeEach(() => {
  fetched = [];
  reports = [];
  vi.spyOn(HTMLMediaElement.prototype, "play").mockResolvedValue(undefined);
  vi.spyOn(HTMLMediaElement.prototype, "pause").mockReturnValue(undefined);
  vi.spyOn(HTMLMediaElement.prototype, "load").mockReturnValue(undefined);
  let made = 0;
  vi.spyOn(URL, "createObjectURL").mockImplementation(() => `blob:clip-${(made += 1)}`);
  vi.spyOn(URL, "revokeObjectURL").mockReturnValue(undefined);
  server.use(
    http.untyped.get(`${origin}/companion/thumb/main/:name`, ({ request }) => {
      fetched.push(decodeURIComponent(new URL(request.url).pathname).replace(/^.*\/|\.(webm|mov)$/g, ""));
      return new HttpResponse(new Blob(["webm"]));
    }),
  );
});
afterEach(() => vi.restoreAllMocks());

describe("the card's switches", () => {
  it("leaves a command's clip playing when the render that clears its playback id comes after the clip showed", async () => {
    const elements = videos();
    const view = render(<Card state={{ anim: IDLE, once: false, seq: 0, cuePlayback: null }} elements={elements} />);
    await arrive(elements, IDLE);
    view.rerender(<Card state={{ anim: WAVE, once: true, seq: 0, cuePlayback: "pb-1" }} elements={elements} />);
    await arrive(elements, WAVE);
    await waitFor(() => expect(reports).toEqual(["pb-1 playing"]));
    fetched = [];
    // The render the chain asked for when it took the playback id, after the clip has shown.
    view.rerender(<Card state={{ anim: WAVE, once: true, seq: 0, cuePlayback: null }} elements={elements} />);
    await settle();
    expect(fetched).toEqual([]);
    expect(reports).toEqual(["pb-1 playing"]);
    // A replay of the same clip (a click when it is the only one) still plays it again.
    view.rerender(<Card state={{ anim: WAVE, once: true, seq: 1, cuePlayback: null }} elements={elements} />);
    await arrive(elements, WAVE);
    expect(reports).toEqual(["pb-1 playing", "pb-1 cancelled superseded"]);
  });

  it("plays a second command for the clip in front", async () => {
    const elements = videos();
    const view = render(<Card state={{ anim: WAVE, once: true, seq: 0, cuePlayback: "pb-1" }} elements={elements} />);
    await arrive(elements, WAVE);
    view.rerender(<Card state={{ anim: WAVE, once: true, seq: 0, cuePlayback: null }} elements={elements} />);
    await settle();
    fetched = [];
    view.rerender(<Card state={{ anim: WAVE, once: true, seq: 0, cuePlayback: "pb-2" }} elements={elements} />);
    await arrive(elements, WAVE);
    await waitFor(() => expect(reports).toEqual(["pb-1 playing", "pb-1 cancelled superseded", "pb-2 playing"]));
  });
});
