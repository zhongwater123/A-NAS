// 大肥鱼 on the desktop is dsh-pet's own pet card (client/pet/card in dsh-pet, copied to
// dsh-pet/ with the patches in dsh-pet-patches/): its animation chain, walks, drags,
// throws, bubbles, sleep and lock. This file only wires it into the page, the way dsh-pet's
// container (client/pet/multi.ts) wires it into a DSH page:
//
// - The card gets A-NAS's React (createPetCard's PetRuntime) and dsh-pet's configuration of
//   its main pet, narrowed to the animations this NAS has installed (animations.ts), at
//   A-NAS's size and place: the desktop's bottom right, above the dock.
// - What VNAS does and asks for comes from the page's GET /state store (companionState in
//   workStatus.ts): sections.workStatus and pets.main.{say,anim,position,throw,lock,sleep},
//   read with dsh-pet's readers by multi.ts's rule: the first read is a baseline, and after
//   it a changed counter hands the leaf to the card. lock, sleep and workStatus are states,
//   not events, so their value at the first read goes to the card as well (multi.ts does
//   that for sleep).
// - The page answers the card's own actions as dsh-pet's host does, with its stores (host.ts):
//   a click wakes the pet when it sleeps, a grab releases its lock, and the receipt of an
//   animation a VNAS command asked for settles the command (commands.ts).
//
// What is A-NAS's own: the companion stops while the member is away (a pause, not sleep),
// remembers where it was left on this device, curtsies when the member stops a turn here,
// waves at a new result of a scheduled task, and shows beside its head that VNAS is at work
// (companion.module.css). The character's credit is in Settings → 概览 (credit.ts), not on
// the companion.
import { type CSSProperties, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { jsx } from "react/jsx-runtime";

import { currentTaskNotices, loadTaskNotices, taskNoticesLoaded, useTaskNotices } from "../assistant/taskNotices";
import { focusedWindowBox, subscribeFocusedWindow } from "../desktop/focusedWindow";
import { isLocalConsole, localConsoleCovered, onLocalConsoleDismissed } from "../desktop/LocalConsoleScreenSaver";
import { companionPhysics, companionWeights, installedAnimations, workStatusTexts } from "./animations";
import { BEHAVIORS, behaviorLine, shortDetail } from "./behaviors";
import type { InstalledAnimations } from "./Companion";
import styles from "./companion.module.css";
import { prefersMov } from "./dsh-pet/client/pet/animation/media";
import { createPetCard } from "./dsh-pet/client/pet/card/pet-card";
import type { PetCardProps } from "./dsh-pet/client/pet/card/props";
import { injectPetCss } from "./dsh-pet/client/pet/css";
import {
  type BoxPoint,
  boxToRatio,
  type Center,
  centerToBox,
  derivePetMetrics,
  type Viewport,
} from "./dsh-pet/client/pet/motion/geometry";
import { makePlaybackReporter } from "./dsh-pet/client/pet/playback-report";
import { BUBBLE_DURATION_MS } from "./dsh-pet/client/pet/timings";
import type { RuntimePet } from "./dsh-pet/client/pet/types";
import { parsePlaybackPath } from "./dsh-pet/host/playback/parse";
import { isTerminalStatus } from "./dsh-pet/host/playback/status";
import { PET_REF_WIDTH, ROUTE_PATH } from "./dsh-pet/shared/constants";
import { bodyPixelBox } from "./dsh-pet/shared/physics";
import { pick } from "./dsh-pet/shared/pickers";
import {
  flattenCounters,
  type PetLeafName,
  type PollState,
  readAnim,
  readLock,
  readPosition,
  readSay,
  readSleep,
  readThrow,
  readWorkStatus,
  resolveLeafPath,
  type StateLeaf,
  setActionHandler,
  takeChanged,
} from "./dsh-pet/shared/state";
import type { Animations } from "./dsh-pet/shared/types";
import { companionLock, companionPlayback, companionSleep } from "./host";
import { clearBox, withinArea } from "./stepAside";
import {
  COMPANION_PET,
  COMPANION_SIZE,
  companionState,
  onTurnStopped,
  onTurns,
  setCompanionPaused,
  setCompanionReady,
  subscribeCompanionState,
} from "./workStatus";

// As dsh-pet's own page builds its card (client/pet.ts): React handed in, the styles put in once.
const PetCard = createPetCard({ h: jsx, useState, useEffect, useRef });
injectPetCss();

/** The companion stops after this long without the member, well before the screen saver (180 s). */
const IDLE_MS = 60_000;
/**
 * The member's input. The local console's screen saver keeps the input that takes it away to itself (stopped
 * on the way down) and says so (onLocalConsoleDismissed), so the companion goes on as the screen saver goes,
 * not at the next input after it; the release of a press, which does reach the page, is input too.
 */
const ACTIVITY = ["pointerdown", "pointerup", "pointermove", "keydown", "keyup", "wheel", "touchstart"] as const;
const POSITION_KEY = "a-nas.companion.position";
const WAVE = "点击回应-元气挥手";
/**
 * Above the windows (72) and the status bar (70); below the rail and notices (80), menus, the shelf and the
 * screen saver; dsh-pet's own is 40.
 */
const layer = { display: "contents", "--dsh-pet-z": 76 } as CSSProperties;
/**
 * dsh-pet's clips play this much faster than recorded, so the character reads as busy at a glance. Walks
 * follow the clip's own time, so they keep step; most clips last about 6.7 s instead of 10.
 */
export const PLAYBACK_RATE = 1.5;

/** Every clip the card loads into either of its two videos plays at PLAYBACK_RATE (a load resets the rate). */
function useClipSpeed() {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const root = ref.current;
    if (!root) return;
    const speed = (event: Event) => {
      if (!(event.target instanceof HTMLMediaElement)) return;
      event.target.defaultPlaybackRate = PLAYBACK_RATE;
      event.target.playbackRate = PLAYBACK_RATE;
    };
    // Media events do not bubble: catch them on the way down to the video.
    root.addEventListener("loadstart", speed, true);
    root.addEventListener("play", speed, true);
    return () => {
      root.removeEventListener("loadstart", speed, true);
      root.removeEventListener("play", speed, true);
    };
  }, []);
  return ref;
}

const pet: Omit<RuntimePet, "animations"> = {
  id: COMPANION_PET,
  name: "大肥鱼",
  assetRoot: COMPANION_PET,
  size: COMPANION_SIZE,
  position: { corner: "bottom-right", marginX: 24, marginY: 88 },
  display: "web",
  workStatusEnabled: true,
  workStatusTexts,
  // No balance, and no whispers of its own (dsh-pet asks a model for those): it says what VNAS says.
  balanceEnabled: false,
  whisperEnabled: false,
  fixedEnabled: false,
  animationWeights: companionWeights,
  eventsRefreshSec: {},
  physics: companionPhysics,
};

/** What of the page's state the card takes, as dsh-pet's container passes it (multi.ts). */
type Leaves = Pick<
  PetCardProps,
  | "workStatus"
  | "workStatusTick"
  | "say"
  | "sayTick"
  | "animCue"
  | "animCueTick"
  | "posCue"
  | "posCueTick"
  | "throwCue"
  | "throwCueTick"
  | "lock"
  | "lockTick"
  | "sleep"
  | "sleepTick"
>;
type LeafName = "workStatus" | PetLeafName;

const noLeaves: Leaves = {
  workStatus: null,
  workStatusTick: 0,
  sayTick: 0,
  animCueTick: 0,
  posCueTick: 0,
  throwCueTick: 0,
  lockTick: 0,
  sleepTick: 0,
};

/**
 * A changed leaf handed to the card as dsh-pet's container hands it (multi.ts applySection and
 * applyPetLeaf): a command takes the next seq of its kind, a state its new value, and the tick of
 * its kind moves. A leaf of the wrong shape changes nothing.
 */
function withLeaf(leaves: Leaves, name: LeafName, leaf: StateLeaf): Leaves {
  switch (name) {
    case "workStatus": {
      const workStatus = readWorkStatus(leaf);
      return workStatus ? { ...leaves, workStatus, workStatusTick: leaves.workStatusTick + 1 } : leaves;
    }
    case "say": {
      const read = readSay(leaf);
      if (!read) return leaves;
      // The bubble's picture goes: A-NAS serves none of dsh-pet's meme pictures, and a picture from
      // elsewhere is not the companion's to show.
      const say = read.kind === "say" ? { ...read, image: undefined, url: undefined } : read;
      return { ...leaves, say: { ...say, seq: (leaves.say?.seq ?? 0) + 1 }, sayTick: leaves.sayTick + 1 };
    }
    case "anim": {
      const anim = readAnim(leaf);
      // Only an animation of this NAS's pool, by its name: an address would play whatever it points at.
      if (!anim?.name) return leaves;
      return {
        ...leaves,
        animCue: { ...anim, url: undefined, seq: (leaves.animCue?.seq ?? 0) + 1 },
        animCueTick: leaves.animCueTick + 1,
      };
    }
    case "position": {
      const position = readPosition(leaf);
      if (!position) return leaves;
      return {
        ...leaves,
        posCue: { ...position, seq: (leaves.posCue?.seq ?? 0) + 1 },
        posCueTick: leaves.posCueTick + 1,
      };
    }
    case "throw": {
      const thrown = readThrow(leaf);
      if (!thrown) return leaves;
      return {
        ...leaves,
        throwCue: { ...thrown, seq: (leaves.throwCue?.seq ?? 0) + 1 },
        throwCueTick: leaves.throwCueTick + 1,
      };
    }
    case "lock": {
      const lock = readLock(leaf);
      return lock ? { ...leaves, lock, lockTick: leaves.lockTick + 1 } : leaves;
    }
    case "sleep": {
      const sleep = readSleep(leaf);
      return sleep ? { ...leaves, sleep, sleepTick: leaves.sleepTick + 1 } : leaves;
    }
  }
}

/** The first read: the baseline later changes are told by, and the states the card starts in. */
function firstRead(state: PollState): { baseline: Record<string, number>; leaves: Leaves } {
  const own = state.pets[COMPANION_PET];
  const states: [LeafName, StateLeaf | undefined][] = [
    ["workStatus", state.sections.workStatus],
    ["lock", own?.lock],
    ["sleep", own?.sleep],
  ];
  let leaves = noLeaves;
  for (const [name, leaf] of states) if (leaf && leaf.data !== null) leaves = withLeaf(leaves, name, leaf);
  return { baseline: flattenCounters(state), leaves };
}

/** The lock leaf holds the pet now: a stale one whose time is past does not (as lock-freeze.ts reads it). */
function lockHeld(leaf: StateLeaf | undefined): boolean {
  const lock = leaf ? readLock(leaf) : null;
  return lock?.locked === true && lock.expiresAt > Date.now();
}

/**
 * The stage the sign beside the pet's head shows (companion.module.css), undefined for none: none when
 * idle, and none while the pet sleeps or is locked, as the card then does not follow the stages either.
 * A success or error shows as long as its bubble line (BUBBLE_DURATION_MS), not the 60 s the stage stays.
 */
function useWorkSign(leaves: Leaves): string | undefined {
  const stage = leaves.workStatus?.state ?? null;
  const tick = leaves.workStatusTick;
  const terminal = stage === "success" || stage === "error";
  /** The stage write whose check or cross has had its time. */
  const [over, setOver] = useState(-1);
  useEffect(() => {
    if (!terminal) return;
    const timer = window.setTimeout(() => setOver(tick), BUBBLE_DURATION_MS);
    return () => window.clearTimeout(timer);
  }, [terminal, tick]);
  // Nothing tells the page that a lock ran out: the end of the lock that has passed.
  const lockEnds = leaves.lock?.locked ? leaves.lock.expiresAt : 0;
  const [unlocked, setUnlocked] = useState(0);
  useEffect(() => {
    if (lockEnds === 0) return;
    const timer = window.setTimeout(() => setUnlocked(lockEnds), Math.max(0, lockEnds - Date.now()));
    return () => window.clearTimeout(timer);
  }, [lockEnds]);
  if (stage === null || (terminal && over === tick)) return undefined;
  if (leaves.sleep?.sleeping || (lockEnds !== 0 && unlocked !== lockEnds)) return undefined;
  return stage;
}

/** How long what happened while the member was away waits to be told when they come back. */
const AWAY_KEEP_MS = 12 * 60 * 60 * 1000;

/** What happened while the member was away: how a turn ended, and new results by their notice's id. */
interface Away {
  ended: { state: "success" | "error"; at: number } | null;
  results: Map<string, number>;
}

const nothingAway = (): Away => ({ ended: null, results: new Map() });

/** Whether the member is away from the companion: it rests, the page is hidden, or the screen saver is up. */
function awayFrom(paused: boolean): boolean {
  return paused || document.hidden || localConsoleCovered();
}

/** Where the member last left the companion on this device, as a position command's data. */
function rememberedPlace(): unknown {
  try {
    const saved = JSON.parse(localStorage.getItem(POSITION_KEY) ?? "null") as { rx?: unknown; ry?: unknown } | null;
    if (typeof saved?.rx !== "number" || typeof saved.ry !== "number") return null;
    // Wholly within the part of the page it may use: a place left on a larger page, or hanging off its
    // edge, comes back on it. Where it lands, it then keeps clear of the window on top as ever.
    const page = { width: window.innerWidth, height: window.innerHeight };
    const { rx, ry } = withinArea({ rx: saved.rx, ry: saved.ry }, pet.size, page);
    return { x: rx, y: ry, mode: "instant" };
  } catch {
    // An unreadable position leaves the companion in its corner.
    return null;
  }
}

/** The companion's box in its corner, where it is until it is first moved (dsh-pet's css.ts). */
function cornerBox(page: Viewport): BoxPoint {
  const { corner, marginX, marginY } = pet.position;
  const height = (pet.size * 9) / 16;
  return {
    x: corner.endsWith("left") ? marginX : page.width - marginX - pet.size,
    y: corner.startsWith("top") ? marginY : page.height - marginY - height,
  };
}

/** How long a step aside of its own may take before the companion checks that it got clear. */
const SETTLE_MS = 1_500;
/** How long a throw may fly before the companion checks the window on top again without its landing. */
const FLIGHT_MS = 10_000;
/** How many times a step aside goes again that did not get there: cut short, or the page's frames held back. */
const RETRIES = 2;
/** How long the companion must hold still after a release or a lock before it is checked again. */
const STILL_MS = 150;

/** A move under way: until when (performance.now()), and the centre it goes to; null for a throw. */
interface Hold {
  until: number;
  target: Center | null;
}

/** The companion's box where it was last left, or in its corner. */
function placedBox(placed: Center | null, page: Viewport): BoxPoint {
  const { halfW, halfH } = derivePetMetrics(pet.size);
  return placed ? centerToBox(placed, page, { w: halfW, h: halfH }) : cornerBox(page);
}

/** Whether a place is a move's target, within a pixel. */
function reached(center: Center, target: Center, page: Viewport): boolean {
  return Math.abs(center.rx - target.rx) * page.width < 1 && Math.abs(center.ry - target.ry) * page.height < 1;
}

const pageSize = (): Viewport => ({ width: window.innerWidth, height: window.innerHeight });
const stillMotion = () => window.matchMedia?.("(prefers-reduced-motion: reduce)").matches === true;

/** The farthest a walk of its own can take the companion: dsh-pet's moves, scaled to its size. */
function walkReach(animations: Animations): number {
  const { moves } = animations;
  const farthest = Math.max(0, ...moves.actions.map((action) => ({ ...moves.default, ...action.params }).maxDist));
  return (farthest * pet.size) / PET_REF_WIDTH;
}

function remember(center: Center): void {
  try {
    localStorage.setItem(POSITION_KEY, JSON.stringify({ rx: center.rx, ry: center.ry }));
  } catch {
    // The position is a convenience of this device.
  }
}

/**
 * The card's own actions, answered as dsh-pet's host answers them (routes/playback.ts, routes/sleep.ts
 * and routes/interaction.ts, over the stores in host.ts): a playback receipt settles the animation a
 * command asked for, a click on the sleeping pet wakes it, and grabbing it releases a lock. As there, a
 * receipt for no known animation, or with no status the card may report, is not delivered.
 */
async function answer(url: string, body?: unknown): Promise<boolean> {
  const route = new URL(url, window.location.origin).pathname;
  const rest = route.startsWith(`${ROUTE_PATH}/`) ? route.slice(ROUTE_PATH.length + 1) : "";
  const playback = parsePlaybackPath(rest);
  if (playback?.isReport) {
    const receipt = (body ?? {}) as { status?: unknown; reason?: unknown };
    const outcome = companionPlayback.report(playback.playbackId, receipt.status, receipt.reason);
    return outcome === "ok" || outcome === "ignored";
  }
  if (rest === "wake") companionSleep.wake(COMPANION_PET);
  if (rest === "lock/release") companionLock.release({ petId: COMPANION_PET, force: true });
  return true;
}

/**
 * Whether a command's animation was answered before it showed: the VNAS window stopped waiting for it to
 * start (commands.ts) and told VNAS failed/timeout, so the card leaves it out rather than play it after all.
 */
function playbackSettled(playbackId: string): boolean {
  const record = companionPlayback.get(playbackId);
  return record !== undefined && isTerminalStatus(record.status);
}

export function CompanionCard({ installed, pool }: { installed: InstalledAnimations; pool?: string }) {
  // Safari draws no alpha in VP9: it needs dsh-pet's HEVC animations, which this pool may lack.
  const format = prefersMov() ? "mov" : "webm";
  const animations = useMemo(
    () => installedAnimations((name) => Boolean(installed[name]?.[format])),
    [installed, format],
  );
  return animations ? <Card animations={animations} pool={pool} /> : null;
}

function Card({ animations, pool }: { animations: Animations; pool?: string }) {
  /** Whether a walk of its own could not end on the window on top: where one could, it does not walk. */
  const [walkable, setWalkable] = useState(true);
  const cfg = useMemo<RuntimePet>(
    () => ({
      ...pet,
      animations,
      animationWeights: walkable ? companionWeights : { ...companionWeights, move: 0 },
      ...(pool ? { assetVersion: pool } : {}),
    }),
    [animations, pool, walkable],
  );
  const arena = useRef<PetCardProps["arena"]["current"]>({ slots: {} });
  const clipSpeed = useClipSpeed();
  const [first] = useState(() => firstRead(companionState.read()));
  const [leaves, setLeaves] = useState(first.leaves);
  const [paused, setPaused] = useState(() => document.hidden);
  const pausedRef = useRef(paused);
  /**
   * A sleep or wake written while the page is hidden or under the local console's screen saver: it is
   * applied when the member is back, its animations being videos too.
   */
  const heldSleep = useRef<StateLeaf | null>(null);
  /** Ends a pause the member's absence started (the page is not hidden), as their next input would. */
  const resume = useRef(() => {});
  /**
   * What happened while the member was away (awayFrom): told once when they come back (comeBack). A
   * turn's end goes when the next turn starts; anything goes after AWAY_KEEP_MS.
   */
  const away = useRef<Away>(nothingAway());
  /** When the companion last told the member something on return: a result listed just after waits. */
  const toldAt = useRef(Number.NEGATIVE_INFINITY);
  /** A turn ends with the member there and something waiting: the card does not show its end, which is told. */
  const telling = useRef(false);
  /** A turn's check or cross shown after the member came back, for as long as a turn's own. */
  const [told, setTold] = useState<{ sign: "success" | "error"; at: number } | null>(null);
  useEffect(() => {
    if (told === null) return;
    const timer = window.setTimeout(() => setTold(null), BUBBLE_DURATION_MS);
    return () => window.clearTimeout(timer);
  }, [told]);
  const comeBack = useRef(() => {});
  /** Where the companion was last left, null while it is in its corner. */
  const placed = useRef<Center | null>(null);
  /**
   * A move under way, whose landing is checked rather than the window on the way: VNAS's position (to its
   * target), VNAS's throw, or a step aside of its own (to where it steps). Until when, by performance.now().
   */
  const hold = useRef<Hold | null>(null);
  /** Checks again when the hold ends, though the move never landed (an animation, a whisper or sleep cut it). */
  const holdTimer = useRef<ReturnType<typeof setTimeout>>(undefined);
  /** The step aside about to go has just put the card's box where the companion is, for its spring to start. */
  const seeded = useRef(false);
  /** Step asides sent since the companion was last clear of the window. */
  const sent = useRef(0);
  /** A stillness check after a release or a lock, so that a flight is left to land. */
  const stillTimer = useRef<ReturnType<typeof setTimeout>>(undefined);
  const reach = useMemo(() => walkReach(animations), [animations]);

  /** Where the companion's box is now: where a move has drawn it, else its last place or its corner. */
  const liveBox = useCallback(
    (page: Viewport): BoxPoint => {
      const root = clipSpeed.current?.querySelector<HTMLElement>(".dsh-pet-root");
      if (root?.style.left && root.style.top)
        return { x: Number.parseFloat(root.style.left), y: Number.parseFloat(root.style.top) };
      return placedBox(placed.current, page);
    },
    [clipSpeed],
  );
  const moveTo = useCallback((box: BoxPoint, mode: "instant" | "spring") => {
    const { halfW, halfH } = derivePetMetrics(pet.size);
    const center = boxToRatio(box, pageSize(), { w: halfW, h: halfH });
    const data = { x: center.rx, y: center.ry, mode };
    setLeaves((current) => withLeaf(current, "position", { counter: 0, data }));
  }, []);

  // The companion stays above the windows but keeps clear of the one on top (stepAside.ts): when that window
  // changes, the page is resized, or a move of the companion's ends (a drag let go, a walk, a flight, VNAS's
  // position or throw), a hit area that covers the window goes to the nearest free place. It goes by the
  // card's own position command, as VNAS's does: no clip changes, and it moves resting, asleep or locked
  // too. The member who drops it on the window sees it step aside: the window comes first. A move under
  // way is left to land, and checked then, or when its hold runs out. A lock that starts or a press on the
  // companion stops a move: once it holds still, what it stopped is checked again.
  /** The check again, for the timers. */
  const again = useRef(() => {});
  const startHold = useCallback((next: Hold | null) => {
    hold.current = next;
    globalThis.clearTimeout(holdTimer.current);
    if (next === null) return;
    holdTimer.current = globalThis.setTimeout(
      () => {
        if (hold.current !== next) return;
        hold.current = null;
        again.current();
      },
      Math.max(0, next.until - performance.now()),
    );
  }, []);
  const keepClear = useCallback(() => {
    const held = hold.current;
    if (held !== null) {
      if (performance.now() < held.until) return;
      startHold(null);
    }
    const page = pageSize();
    const box = liveBox(page);
    const onTop = focusedWindowBox();
    const hit = bodyPixelBox({ ...box, size: pet.size, bottomPad: derivePetMetrics(pet.size).bottomPad });
    const walkable =
      onTop === null ||
      hit.right + reach <= onTop.left ||
      hit.left - reach >= onTop.right ||
      hit.bottom <= onTop.top ||
      hit.top >= onTop.bottom;
    setWalkable(walkable);
    // A walk of its own under way (drawn off its last place at the same height) that could end on the window
    // stops where it is: an instant move there, whose landing comes back here.
    const last = placedBox(placed.current, page);
    if (!walkable && Math.abs(box.x - last.x) >= 1 && Math.abs(box.y - last.y) < 1) {
      moveTo(box, "instant");
      return;
    }
    const to = clearBox(box, pet.size, page);
    if (to === null) {
      seeded.current = false;
      sent.current = 0;
      return;
    }
    if (stillMotion()) {
      moveTo(to, "instant");
      return;
    }
    // A spring starts from the card's box, which is not known in the corner and is stale after a resize: an
    // instant move to where the companion is first, whose landing comes back here.
    if (!seeded.current) {
      seeded.current = true;
      moveTo(box, "instant");
      return;
    }
    seeded.current = false;
    // Sent at most RETRIES more times when it does not get there (cut short, or the page's frames held back).
    if (sent.current > RETRIES) return;
    sent.current += 1;
    const { halfW, halfH } = derivePetMetrics(pet.size);
    startHold({ until: performance.now() + SETTLE_MS, target: boxToRatio(to, page, { w: halfW, h: halfH }) });
    moveTo(to, "spring");
  }, [liveBox, moveTo, reach, startHold]);
  useEffect(() => {
    again.current = keepClear;
  }, [keepClear]);
  /** Checks again once the companion holds still for a moment, its hold let go: not while it flies. */
  const whenStill = useCallback(() => {
    const before = liveBox(pageSize());
    globalThis.clearTimeout(stillTimer.current);
    stillTimer.current = globalThis.setTimeout(() => {
      const after = liveBox(pageSize());
      if (Math.abs(after.x - before.x) >= 1 || Math.abs(after.y - before.y) >= 1) return;
      startHold(null);
      keepClear();
    }, STILL_MS);
  }, [keepClear, liveBox, startHold]);
  const place = useCallback(
    (center: Center) => {
      remember(center);
      placed.current = center;
      const held = hold.current;
      if (held === null) {
        keepClear();
        return;
      }
      // dsh-pet's own re-layout after a resize sets a place too, which is not the move's end: a move to a
      // target ends there, and a throw once the companion holds still.
      if (held.target === null) whenStill();
      else if (reached(center, held.target, pageSize())) {
        startHold(null);
        keepClear();
      }
    },
    [keepClear, startHold, whenStill],
  );
  useEffect(
    () => () => {
      globalThis.clearTimeout(holdTimer.current);
      globalThis.clearTimeout(stillTimer.current);
    },
    [],
  );
  // A lock that starts stops the move under way (lock-freeze.ts). It starts when none was held: none before,
  // or one whose time had run out.
  const lock = leaves.lock ?? null;
  const lastLock = useRef(lock);
  useEffect(() => {
    const before = lastLock.current;
    lastLock.current = lock;
    const now = Date.now();
    const held = lock?.locked === true && lock.expiresAt > now;
    const heldBefore = before?.locked === true && before.expiresAt > now;
    if (held && !heldBefore) whenStill();
  }, [lock, whenStill]);
  // So does a press on the companion. Once it is let go, unless it is on its way (thrown, or going on to
  // where a drag left it, which is checked where it lands).
  useEffect(() => {
    const layer = clipSpeed.current;
    if (!layer) return;
    layer.addEventListener("pointerup", whenStill, true);
    layer.addEventListener("pointercancel", whenStill, true);
    return () => {
      layer.removeEventListener("pointerup", whenStill, true);
      layer.removeEventListener("pointercancel", whenStill, true);
    };
  }, [clipSpeed, whenStill]);
  useEffect(() => {
    let resized: number | undefined;
    const resize = () => {
      window.clearTimeout(resized);
      resized = window.setTimeout(keepClear, 200);
    };
    const stop = subscribeFocusedWindow(keepClear);
    window.addEventListener("resize", resize);
    return () => {
      stop();
      window.clearTimeout(resized);
      window.removeEventListener("resize", resize);
    };
  }, [keepClear]);

  // The companion stops while the member is away: after IDLE_MS without input, which is always
  // before the local console's screen saver (180 s without input) covers the page, or while the
  // page is hidden. It freezes on its frame (the card's paused prop) rather than going to sleep,
  // whose loop is a video too. The next input, the page shown again, or a command VNAS sends
  // while the page is shown brings it back.
  useEffect(() => {
    let timer: number | undefined;
    const rest = () => {
      window.clearTimeout(timer);
      pausedRef.current = true;
      // Only a hidden page's pause is one a command does not end (VNAS hears paused, commands.ts).
      setCompanionPaused(document.hidden);
      setPaused(true);
    };
    const wake = () => {
      if (document.hidden) {
        rest();
        return;
      }
      setCompanionPaused(false);
      window.clearTimeout(timer);
      timer = window.setTimeout(rest, IDLE_MS);
      // A held sleep goes on even if the companion was not resting: the screen saver can come up
      // within a minute of a command.
      const sleep = heldSleep.current;
      heldSleep.current = null;
      if (sleep) setLeaves((current) => withLeaf(current, "sleep", sleep));
      // What waited is told once the card goes on: back from a pause, or from under the screen saver,
      // which can come up while the companion has not paused (a command of VNAS's put that off).
      const { ended, results } = away.current;
      if (pausedRef.current || ended !== null || results.size > 0) window.setTimeout(() => comeBack.current(), 0);
      if (!pausedRef.current) return;
      pausedRef.current = false;
      setPaused(false);
    };
    resume.current = wake;
    setCompanionPaused(document.hidden);
    if (!document.hidden) timer = window.setTimeout(rest, IDLE_MS);
    for (const type of ACTIVITY) window.addEventListener(type, wake, { passive: true });
    document.addEventListener("visibilitychange", wake);
    const stopDismissed = onLocalConsoleDismissed(wake);
    return () => {
      resume.current = () => {};
      setCompanionPaused(false);
      window.clearTimeout(timer);
      stopDismissed();
      for (const type of ACTIVITY) window.removeEventListener(type, wake);
      document.removeEventListener("visibilitychange", wake);
    };
  }, []);

  useEffect(() => {
    const baseline = { ...first.baseline };
    const report = makePlaybackReporter({ petId: COMPANION_PET });
    const take = () => {
      const taken: [LeafName, StateLeaf][] = [];
      for (const { path, leaf } of takeChanged(companionState.read(), baseline)) {
        if (leaf.data === null) continue;
        const target = resolveLeafPath(path);
        const name =
          target?.where === "pet" && target.petId === COMPANION_PET
            ? target.name
            : target?.where === "section" && target.name === "workStatus"
              ? target.name
              : null;
        if (name === null) continue;
        // States go to the card paused or not: it shows them when it goes on. All but a turn's end while
        // the member is away: the card idles rather than play it to nobody, and the member is told how the
        // turn ended when they come back (onTurns, comeBack).
        if (name === "workStatus") {
          const status = readWorkStatus(leaf);
          const stage = status?.state ?? null;
          const ends = stage === "success" || stage === "error";
          if (status && ends && (awayFrom(pausedRef.current) || telling.current)) {
            telling.current = false;
            taken.push([name, { ...leaf, data: { ...status, state: null, anim: null } }]);
          } else taken.push([name, leaf]);
        } else if (name === "lock") taken.push([name, leaf]);
        // Under the local console's screen saver nobody sees the page either (D12): nothing written
        // then ends a rest, not even a sleep, a wake or the wake a lock brings.
        else if (!document.hidden && !localConsoleCovered()) {
          // A command is VNAS speaking to the member in a turn of this page, who may well be reading
          // its reply without touching anything: it ends a pause their absence started, and counts
          // as their input, so that a pause does not start in the middle of what it shows.
          resume.current();
          // VNAS's move lands before the companion checks the window on top again: at its target, or for a
          // throw where it comes to rest. It takes over from a step aside under way.
          const position = name === "position" ? readPosition(leaf) : null;
          if (position) {
            const until = performance.now() + position.duration + SETTLE_MS;
            startHold({ until, target: { rx: position.x, ry: position.y } });
          }
          if (name === "throw") startHold({ until: performance.now() + FLIGHT_MS, target: null });
          if (position || name === "throw") sent.current = 0;
          taken.push([name, leaf]);
        } else if (name === "sleep") heldSleep.current = leaf;
        else {
          // Nobody sees a hidden or covered page: a command sent to it is dropped, not kept for later, and a
          // caller waiting on an animation hears that a freeze cut it short, as the card reports one.
          const playbackId = name === "anim" ? readAnim(leaf)?.playbackId : undefined;
          if (playbackId) report(playbackId, "cancelled", "frozen");
        }
      }
      if (taken.length > 0)
        setLeaves((current) => taken.reduce((next, [name, leaf]) => withLeaf(next, name, leaf), current));
    };
    const stop = subscribeCompanionState(take);
    // What was written between the first read (during the render) and now.
    take();
    // Where the member left it on this device goes back as an instant position command, once the
    // card's own mount has put it in its corner (twice in development, under StrictMode).
    let placing = true;
    queueMicrotask(() => {
      if (!placing) return;
      const remembered = rememberedPlace();
      // From where it was left it steps aside when it gets there; from its corner, now.
      if (remembered) setLeaves((current) => withLeaf(current, "position", { counter: 0, data: remembered }));
      else keepClear();
    });
    setActionHandler(answer);
    setCompanionReady(true);
    return () => {
      placing = false;
      stop();
      // A card that leaves the page cuts short what a command asked it to play, as a freeze does:
      // the command hears so now rather than when its wait runs out (an animation already over is left as it was).
      const own = companionState.read().pets[COMPANION_PET];
      const playbackId = own ? readAnim(own.anim)?.playbackId : undefined;
      if (playbackId) companionPlayback.report(playbackId, "cancelled", "frozen");
      setActionHandler(null);
      setCompanionReady(false);
    };
  }, [first, keepClear, startHold]);

  // A new result of a scheduled task: a wave, on any screen (its content stays in VNAS). A result is
  // new by its notice's id: a task's next result replaces its notice under a new id, so the number of
  // notices may not change. The wave goes through the page's state, as a host's animation command,
  // and only while the companion is idle: not showing VNAS's stage, asleep, locked or paused.
  const notices = useTaskNotices();
  const seen = useRef<ReadonlySet<string> | null>(null);
  useEffect(() => {
    // Until the notices are first listed, none means nothing: that first list, whether it came before
    // or after the card, is what the member already had.
    if (!taskNoticesLoaded()) return;
    const before = seen.current;
    seen.current = new Set(notices.map((notice) => notice.id));
    if (!before || notices.every((notice) => before.has(notice.id))) return;
    // The member is away: told when they come back, unless they read it elsewhere first.
    if (awayFrom(pausedRef.current)) {
      for (const notice of notices) if (!before.has(notice.id)) away.current.results.set(notice.id, Date.now());
      return;
    }
    // Just told something on return: a result listed a moment later is not a second thing to tell.
    if (Date.now() - toldAt.current < BUBBLE_DURATION_MS) return;
    const state = companionState.read();
    const own = state.pets[COMPANION_PET];
    if (readWorkStatus(state.sections.workStatus)?.state || lockHeld(own?.lock)) return;
    if (own && readSleep(own.sleep)?.sleeping) return;
    // The wave by its name, or another click response: a click may also play a category action.
    const responses = animations.clicks.filter((name) => name.startsWith("点击回应-"));
    if (responses.length === 0) return;
    companionState.writePet(COMPANION_PET, "anim", { name: responses.includes(WAVE) ? WAVE : pick(responses) });
  }, [notices, animations]);

  // How a turn of this window ends while the member is away waits for them; an error is not overwritten by
  // another conversation's success. A turn that starts forgets it: what it ends in is the news then. New
  // results of scheduled tasks have nothing to do with turns and stay. A turn that ends with the member there
  // but something still waiting (they came back during it) is told with what waited, as one thing: the card
  // does not show the turn's own end then.
  useEffect(
    () =>
      onTurns({
        started: () => {
          away.current.ended = null;
          setTold(null);
        },
        ended: (_sessionId, state) => {
          const present = !awayFrom(pausedRef.current);
          const waiting = away.current.ended !== null || away.current.results.size > 0;
          if (present && !waiting) return;
          if (state === "error" || away.current.ended?.state !== "error")
            away.current.ended = { state, at: Date.now() };
          if (!present) return;
          telling.current = true;
          window.setTimeout(() => comeBack.current(), 0);
        },
      }),
    [],
  );

  // The member comes back to the page (an input after a pause, the page shown again, the local console's
  // screen saver gone): the companion tells the one thing that most needs them, once, with one clip and one
  // line. VNAS waiting for them first, which its stage shows as it is; then a turn that ended in an error
  // (a sigh, its cross for as long as a turn's own); then a new result of a scheduled task still unread (a
  // wave), by the list read again now, as another device may have read it meanwhile; then a turn that ended
  // well (only its check, no fireworks again). Nothing during a turn, which keeps it for the turn's end;
  // nothing asleep or locked. On the screen the family shares, no task's name.
  useEffect(() => {
    comeBack.current = () => {
      if (document.hidden || localConsoleCovered()) return;
      // Back during a turn: what waits stays until the turn ends, and is told then.
      const state = companionState.read();
      const stage = readWorkStatus(state.sections.workStatus)?.state ?? null;
      if (stage !== null && stage !== "success" && stage !== "error") return;
      const { ended, results } = away.current;
      away.current = nothingAway();
      const now = Date.now();
      const recent = (at: number) => now - at < AWAY_KEEP_MS;
      const waiting = new Set([...results].filter(([, at]) => recent(at)).map(([id]) => id));
      const outcome = ended && recent(ended.at) ? ended.state : null;
      if (outcome === null && waiting.size === 0) return;
      const own = state.pets[COMPANION_PET];
      if (lockHeld(own?.lock) || (own && readSleep(own.sleep)?.sleeping)) return;
      const tell = () => {
        toldAt.current = Date.now();
      };
      if (outcome === "error") {
        tell();
        setTold({ sign: "error", at: now });
        const line = behaviorLine("error");
        if (line) companionState.writePet(COMPANION_PET, "say", { text: line, anim: false });
        const { clip } = BEHAVIORS.error;
        if ([...animations.events.workStatus.flat(), ...(animations.events.steps ?? [])].includes(clip))
          companionState.writePet(COMPANION_PET, "anim", { name: clip });
        return;
      }
      const done = () => {
        if (outcome !== "success") return;
        tell();
        setTold({ sign: "success", at: now });
      };
      if (waiting.size === 0) {
        done();
        return;
      }
      void loadTaskNotices().finally(() => {
        const unread = currentTaskNotices().filter((notice) => waiting.has(notice.id));
        if (unread.length === 0) {
          done();
          return;
        }
        tell();
        const task = unread.at(-1)?.task?.name;
        const text = isLocalConsole() || !task ? "有新结果" : `${shortDetail(task)} 有新结果`;
        companionState.writePet(COMPANION_PET, "say", { text, anim: false });
        const responses = animations.clicks.filter((name) => name.startsWith("点击回应-"));
        if (responses.length > 0)
          companionState.writePet(COMPANION_PET, "anim", { name: responses.includes(WAVE) ? WAVE : pick(responses) });
      });
    };
  }, [animations]);

  // The member stopped a turn here: its stage is gone, and the companion curtsies once with a line, then
  // idles, through the page's state as the wave does. Only when it can be seen and is free to move: not
  // paused, covered, asleep or locked.
  useEffect(
    () =>
      onTurnStopped(() => {
        if (pausedRef.current || document.hidden || localConsoleCovered()) return;
        const own = companionState.read().pets[COMPANION_PET];
        if (lockHeld(own?.lock) || (own && readSleep(own.sleep)?.sleeping)) return;
        const line = behaviorLine("stopped");
        if (line) companionState.writePet(COMPANION_PET, "say", { text: line, anim: false });
        const { clip } = BEHAVIORS.stopped;
        if (animations.events.steps?.includes(clip)) companionState.writePet(COMPANION_PET, "anim", { name: clip });
      }),
    [animations],
  );

  // dsh-pet's card says nothing when no whisper animation is installed to play with it (speak in
  // card/animation-flow.ts); on such a NAS its bubble shows on its own.
  const say =
    leaves.say?.kind === "say" && !animations.events.whisper?.length ? { ...leaves.say, anim: false } : leaves.say;
  const sign = useWorkSign(leaves);
  const work = told?.sign ?? sign;
  // VNAS moves the companion with its own tools: the desktop it reads leaves the companion out, as it
  // does its toasts, since a click through the desktop tools would wake it or take its lock back.
  return (
    <div
      ref={clipSpeed}
      style={layer}
      className={styles.layer}
      data-companion-work={work}
      data-companion-rest={paused ? "" : undefined}
      data-vnas-overlay=""
    >
      <PetCard
        cfg={cfg}
        {...leaves}
        say={say}
        arena={arena}
        paused={paused}
        playbackSettled={playbackSettled}
        onPlace={place}
      />
    </div>
  );
}
