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
import { type CSSProperties, useEffect, useMemo, useRef, useState } from "react";
import { jsx } from "react/jsx-runtime";

import { taskNoticesLoaded, useTaskNotices } from "../assistant/taskNotices";
import { localConsoleCovered, onLocalConsoleDismissed } from "../desktop/LocalConsoleScreenSaver";
import { companionPhysics, companionWeights, installedAnimations, workStatusTexts } from "./animations";
import { BEHAVIORS, behaviorLine } from "./behaviors";
import type { InstalledAnimations } from "./Companion";
import styles from "./companion.module.css";
import { prefersMov } from "./dsh-pet/client/pet/animation/media";
import { createPetCard } from "./dsh-pet/client/pet/card/pet-card";
import type { PetCardProps } from "./dsh-pet/client/pet/card/props";
import { injectPetCss } from "./dsh-pet/client/pet/css";
import type { Center } from "./dsh-pet/client/pet/motion/geometry";
import { makePlaybackReporter } from "./dsh-pet/client/pet/playback-report";
import { BUBBLE_DURATION_MS } from "./dsh-pet/client/pet/timings";
import type { RuntimePet } from "./dsh-pet/client/pet/types";
import { parsePlaybackPath } from "./dsh-pet/host/playback/parse";
import { ROUTE_PATH } from "./dsh-pet/shared/constants";
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
import {
  COMPANION_PET,
  companionState,
  onTurnStopped,
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
  size: 240,
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

/** Where the member last left the companion on this device, as a position command's data. */
function rememberedPlace(): unknown {
  try {
    const saved = JSON.parse(localStorage.getItem(POSITION_KEY) ?? "null") as { rx?: unknown; ry?: unknown } | null;
    if (typeof saved?.rx !== "number" || typeof saved.ry !== "number") return null;
    // Within the page, as dsh-pet's host keeps a position command (its centre on the page).
    const within = (value: number) => Math.min(1, Math.max(0, value));
    return { x: within(saved.rx), y: within(saved.ry), mode: "instant" };
  } catch {
    // An unreadable position leaves the companion in its corner.
    return null;
  }
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
  const cfg = useMemo<RuntimePet>(
    () => ({ ...pet, animations, ...(pool ? { assetVersion: pool } : {}) }),
    [animations, pool],
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
        // States go to the card paused or not: it shows them when it goes on.
        if (name === "workStatus" || name === "lock") taken.push([name, leaf]);
        // Under the local console's screen saver nobody sees the page either (D12): nothing written
        // then ends a rest, not even a sleep, a wake or the wake a lock brings.
        else if (!document.hidden && !localConsoleCovered()) {
          // A command is VNAS speaking to the member in a turn of this page, who may well be reading
          // its reply without touching anything: it ends a pause their absence started, and counts
          // as their input, so that a pause does not start in the middle of what it shows.
          resume.current();
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
      const place = placing ? rememberedPlace() : null;
      if (place) setLeaves((current) => withLeaf(current, "position", { counter: 0, data: place }));
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
  }, [first]);

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
    if (!before || notices.every((notice) => before.has(notice.id)) || pausedRef.current) return;
    const state = companionState.read();
    const own = state.pets[COMPANION_PET];
    if (readWorkStatus(state.sections.workStatus)?.state || lockHeld(own?.lock)) return;
    if (own && readSleep(own.sleep)?.sleeping) return;
    // The wave by its name, or another click response: a click may also play a category action.
    const responses = animations.clicks.filter((name) => name.startsWith("点击回应-"));
    if (responses.length === 0) return;
    companionState.writePet(COMPANION_PET, "anim", { name: responses.includes(WAVE) ? WAVE : pick(responses) });
  }, [notices, animations]);

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
  const work = useWorkSign(leaves);
  // VNAS moves the companion with its own tools: the desktop it reads leaves the companion out, as it
  // does its toasts, since a click through the browser tools would wake it or take its lock back.
  return (
    <div
      ref={clipSpeed}
      style={layer}
      className={styles.layer}
      data-companion-work={work}
      data-companion-rest={paused ? "" : undefined}
      data-vnas-overlay=""
    >
      <PetCard cfg={cfg} {...leaves} say={say} arena={arena} paused={paused} onPlace={remember} />
    </div>
  );
}
