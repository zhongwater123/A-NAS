// 大肥鱼 on the desktop, played as dsh-pet plays its pet (dsh-pet/README.md):
// its animation chain swaps two videos, picks the next animation at random
// while VNAS is idle, walks when it rolls a move, follows VNAS's stage
// (workStatus.ts) and answers clicks and drags through dsh-pet's own drag
// driver; a drag let go with speed throws it, and it flies and lands with
// dsh-pet's physics. The companion freezes on its frame while the member is
// away or the page is hidden, so an idle desktop does not keep decoding video
// or running a frame loop (the screen saver comes later still); a walk stops
// where it is, and a flight carries on from there when the member is back.
import { type Dispatch, type SetStateAction, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";

import { useTaskNotices } from "../assistant/taskNotices";
import { companionWeights, installedAnimations } from "./animations";
import type { InstalledAnimations } from "./Companion";
import styles from "./companion.module.css";
import { createOwnAnimation, type EndedCue, type OwnAnimation } from "./dsh-pet/client/pet/animation/animation";
import { prefersMov } from "./dsh-pet/client/pet/animation/media";
import { decideNextAnimation } from "./dsh-pet/client/pet/animation/next-animation";
import { createDrag, type Drag } from "./dsh-pet/client/pet/motion/drag";
import { createFlight } from "./dsh-pet/client/pet/motion/flight";
import {
  type BoxPoint,
  boxToRatio,
  centerToBox,
  clampReachableBox,
  derivePetMetrics,
  PET_HIT_STYLE,
  petStageStyle,
} from "./dsh-pet/client/pet/motion/geometry";
import { createSquashDriver } from "./dsh-pet/client/pet/motion/squash";
import { createWalk } from "./dsh-pet/client/pet/motion/walk";
import { landingSquash } from "./dsh-pet/shared/physics";
import { DEFAULT_PHYSICS } from "./dsh-pet/shared/physics/constants";
import { pick, pickSlot, poolIncludes, rollKind } from "./dsh-pet/shared/pickers";
import type { Animations } from "./dsh-pet/shared/types";
import type { WorkStatusSnapshot } from "./dsh-pet/shared/work-status";
import { WORK_STATUS_INDEX } from "./dsh-pet/shared/work-status";
import { useCompanionWorkStatus } from "./workStatus";

/** The pet's width in px; its box is 16:9, the animations' canvas. */
const SIZE = 240;
/** The companion rests after this long without the member, well before the screen saver (180 s). */
const IDLE_MS = 60_000;
const POSITION_KEY = "a-nas.companion.position";
const ACTIVITY = ["pointerdown", "pointermove", "keydown", "wheel", "touchstart"] as const;
const metrics = derivePetMetrics(SIZE);
const motionMetrics = { ...metrics, size: SIZE };
// The only companion on the page: there is no other pet to bump into.
const physics = { ...DEFAULT_PHYSICS, petCollision: false };
const half = { w: metrics.halfW, h: metrics.halfH };
const body = { size: SIZE, bottomPad: metrics.bottomPad };
const viewport = () => ({ width: window.innerWidth, height: window.innerHeight });
// The credit dsh-pet asks for wherever the character shows.
const credit = "大肥鱼：社区二创形象，仅作非商业试验。动画来自 github.com/PC2005-cloud/dsh-pet";

/** Where the member last left the companion on this device, or the bottom right above the dock. */
function startBox(): BoxPoint {
  try {
    const saved = JSON.parse(localStorage.getItem(POSITION_KEY) ?? "null") as { rx?: unknown; ry?: unknown } | null;
    if (typeof saved?.rx === "number" && typeof saved.ry === "number" && Number.isFinite(saved.rx + saved.ry))
      return clampReachableBox(centerToBox({ rx: saved.rx, ry: saved.ry }, viewport(), half), viewport(), body);
  } catch {
    // An unreadable position starts in the corner.
  }
  return { x: window.innerWidth - SIZE - 24, y: window.innerHeight - metrics.halfH * 2 - 88 };
}

export function CompanionPet({ installed }: { installed: InstalledAnimations }) {
  // Safari draws no alpha in VP9: it needs dsh-pet's HEVC animations, which this pool may lack.
  const format = prefersMov() ? "mov" : "webm";
  const animations = useMemo(
    () => installedAnimations((name) => Boolean(installed[name]?.[format])),
    [installed, format],
  );
  return animations ? <Pet animations={animations} /> : null;
}

interface Latest {
  anim: string;
  facing: "left" | "right";
  work: WorkStatusSnapshot;
  animations: Animations;
}

interface Setters {
  setAnim: Dispatch<SetStateAction<string>>;
  setOnce: Dispatch<SetStateAction<boolean>>;
  setSeq: Dispatch<SetStateAction<number>>;
  setFacing: Dispatch<SetStateAction<"left" | "right">>;
}

/** The companion's animation chain, drag driver and choice of what plays next. */
interface Companion {
  chain: OwnAnimation;
  drag: Drag;
  play(name: string, once: boolean): void;
  resumeWork(): boolean;
  pickNext(): void;
  wake(): void;
  /** Stops a walk where it is and holds a flight, so no frame loop runs while frozen. */
  rest(): void;
  /** Carries on a flight held by rest. */
  resume(): void;
  /** Brings the companion back within a page that has changed size. */
  refit(): void;
}

// What plays next, after dsh-pet's card (client/pet/card/animation-flow.ts in
// dsh-pet) without the sleep and chatter A-NAS does not have. Walking and
// flying are wired as in its card/motion.ts and card/drag.ts.
function createCompanion(
  latest: { current: Latest },
  set: Setters,
  elements: { root(): HTMLDivElement | null; videoA(): HTMLVideoElement | null; videoB(): HTMLVideoElement | null },
  start: BoxPoint,
): Companion {
  let box = start;
  let squashNext = false;
  const squash = createSquashDriver({ facing: () => latest.current.facing });
  const show = (name: string) => {
    latest.current.anim = name;
    set.setAnim(name);
  };
  const bump = () => set.setSeq((value) => value + 1);
  // Drops the companion where it is, kept within reach, and remembers it on this device.
  const place = (next: BoxPoint) => {
    box = clampReachableBox(next, viewport(), body);
    const root = elements.root();
    if (root) {
      root.style.left = `${box.x}px`;
      root.style.top = `${box.y}px`;
    }
    try {
      localStorage.setItem(POSITION_KEY, JSON.stringify(boxToRatio(box, viewport(), half)));
    } catch {
      // The position is a convenience of this device.
    }
  };
  // A walk or a flight ends at a centre given as a share of the viewport.
  const settleAt = (center: { rx: number; ry: number }) => place(centerToBox(center, viewport(), half));

  const flight = createFlight({
    root: elements.root,
    metrics: () => motionMetrics,
    viewport,
    physics: () => physics,
    arena: () => ({ slots: {} }),
    petId: "dafeiyu",
    box: () => box,
    setBox: (next) => {
      box = next;
    },
    commitLive: () => undefined,
    commitFinal: settleAt,
    onLanding: (impactVy) => {
      const front = companion.chain.frontVideo();
      if (front) squash.start(front, landingSquash(impactVy));
    },
  });
  // A flight held while the companion rests, to carry on from where it stopped.
  let airborne: { vx: number; vy: number } | null = null;
  let walking = false;
  const walk = createWalk({
    root: elements.root,
    center: () => ({ x: box.x + metrics.halfW, y: box.y + metrics.halfH }),
    metrics: () => motionMetrics,
    moves: () => latest.current.animations.moves,
    facing: () => latest.current.facing,
    viewport,
    isFlying: () => flight.isFlying(),
    play: (name) => {
      show(name);
      set.setOnce(true);
    },
    setBox: (next) => {
      box = next;
    },
    commit: (center) => {
      walking = false;
      settleAt(center);
    },
  });
  // Stops a walk, remembering where it got to.
  const halt = () => {
    walk.stop();
    if (!walking) return;
    walking = false;
    place(box);
  };

  const companion: Companion = {
    chain: undefined as unknown as OwnAnimation,
    drag: undefined as unknown as Drag,
    play(name, once) {
      // Anything else that plays takes over from a walk: its frames move the companion.
      halt();
      show(name);
      set.setOnce(once);
      bump();
    },
    resumeWork() {
      const { state } = latest.current.work;
      if (companion.chain.isFrozen() || !state || state === "success" || state === "error") return false;
      const slot = latest.current.animations.events.workStatus?.[WORK_STATUS_INDEX[state]];
      if (slot === undefined || slot.length === 0) return false;
      companion.play(pickSlot(slot, latest.current.anim), Array.isArray(slot) && slot.length > 1);
      return true;
    },
    pickNext() {
      if (companion.chain.isFrozen()) return;
      const kind = rollKind(Math.random(), companionWeights);
      // A move that cannot fit (or has no installed animation) falls back to an action.
      const move = kind === "move" ? walk.tryMove() : undefined;
      const decision = decideNextAnimation({
        kind,
        animations: latest.current.animations,
        facing: latest.current.facing,
        current: latest.current.anim,
        move,
      });
      if (decision.play && decision.anim !== null) show(decision.anim);
      set.setOnce(true);
      bump();
    },
    wake: () => undefined,
    rest() {
      if (flight.isFlying()) {
        airborne = flight.velocity();
        flight.stop();
      }
      halt();
    },
    resume() {
      if (!airborne) return;
      const { vx, vy } = airborne;
      airborne = null;
      flight.start(box.x, box.y, vx, vy);
    },
    refit() {
      // A drag follows the pointer, which stays on the page.
      if (companion.drag.isActive()) return;
      // A walk and a flight worked out their bounds for the old size: stop them, bring
      // the companion back within reach, and let a flight carry on within the new bounds.
      const speed = flight.isFlying() ? flight.velocity() : null;
      flight.stop();
      halt();
      place(box);
      if (speed) flight.start(box.x, box.y, speed.vx, speed.vy);
    },
  };
  const settle = () => {
    if (!companion.resumeWork()) companion.play(pick(latest.current.animations.idle, latest.current.anim), true);
  };
  const ended = (cue: EndedCue) => {
    switch (cue.kind) {
      case "ignore":
        return;
      case "work-rotate":
        companion.play(cue.anim, true);
        return;
      case "work-replay":
        set.setOnce(false);
        bump();
        return;
      case "event-done":
      case "interaction-done":
        settle();
        return;
      case "turn": {
        const next = latest.current.facing === "left" ? "right" : "left";
        latest.current.facing = next;
        set.setFacing(next);
        companion.pickNext();
        return;
      }
      default:
        companion.pickNext();
    }
  };

  companion.chain = createOwnAnimation({
    videoA: elements.videoA,
    videoB: elements.videoB,
    cfg: () => ({ id: "dafeiyu", assetRoot: "/companion/animations", animations: latest.current.animations }),
    currentAnim: () => latest.current.anim,
    workStatus: () => latest.current.work,
    facing: () => latest.current.facing,
    report: () => undefined,
    clearCuePlayback: () => undefined,
    onFrontReady: (element) => {
      // A walk moves with its animation's frames, from when they show.
      if (walk.hasPending()) {
        walking = true;
        walk.start(element);
      }
      if (!squashNext) return;
      squashNext = false;
      squash.start(element);
    },
    onEnded: ended,
    isDragging: () => companion.drag.isActive(),
    initiallyFrozen: false,
  });
  companion.drag = createDrag(() => ({
    petId: "dafeiyu",
    size: SIZE,
    halfW: metrics.halfW,
    halfH: metrics.halfH,
    physics: DEFAULT_PHYSICS,
    dragAnims: latest.current.animations.drag,
    clickAnims: latest.current.animations.clicks,
    idleAnims: latest.current.animations.idle,
    currentAnim: latest.current.anim,
    setOnce: (once) => set.setOnce(once),
    playAnim: show,
    bumpSeq: bump,
    markSquash: () => {
      squashNext = true;
    },
    resumeWork: () => companion.resumeWork(),
    stopAutonomy: halt,
    stopFlight: () => {
      airborne = null;
      flight.stop();
    },
    startFlight: (x, y, vx, vy) => {
      halt();
      flight.start(x, y, vx, vy);
    },
    isFrozen: () => companion.chain.isFrozen(),
    releaseLock: () => companion.wake(),
    // Catching it in the air keeps its speed for the next throw.
    grabVelocity: () => (flight.isFlying() ? flight.velocity() : null),
    playClickSound: () => undefined,
    tipOn: () => false,
    placeTip: () => undefined,
    setTipOn: () => undefined,
    root: elements.root,
    box: () => box,
    setBox: (next) => {
      box = next;
    },
    commitPlace: place,
  }));
  const dispose = companion.chain.dispose;
  companion.chain.dispose = () => {
    walk.stop();
    flight.stop();
    companion.drag.dispose();
    squash.stop();
    dispose();
  };
  return companion;
}

function Pet({ animations }: { animations: Animations }) {
  const { workStatus, tick } = useCompanionWorkStatus();
  const unread = useTaskNotices().length;
  const [start] = useState(startBox);
  const [anim, setAnim] = useState(() => pick(animations.idle));
  const [once, setOnce] = useState(true);
  const [seq, setSeq] = useState(0);
  const [facing, setFacing] = useState<"left" | "right">("left");
  // The latest values, for dsh-pet's drivers, which read them through getters.
  const latest = useRef<Latest>({ anim, facing, work: workStatus, animations });
  useLayoutEffect(() => {
    latest.current = { anim, facing, work: workStatus, animations };
  });
  const root = useRef<HTMLDivElement>(null);
  const videoA = useRef<HTMLVideoElement>(null);
  const videoB = useRef<HTMLVideoElement>(null);
  const companion = useRef<Companion | null>(null);

  useEffect(() => {
    const created = createCompanion(
      latest,
      { setAnim, setOnce, setSeq, setFacing },
      { root: () => root.current, videoA: () => videoA.current, videoB: () => videoB.current },
      start,
    );
    companion.current = created;
    return () => {
      companion.current = null;
      created.chain.dispose();
    };
  }, [start]);

  useEffect(() => {
    companion.current?.chain.switchTo(anim, once, null);
  }, [anim, once, seq]);

  // VNAS's stage: a stage still going loops its animation, a finished one plays once.
  useEffect(() => {
    const current = companion.current;
    if (!current || current.chain.isFrozen() || current.drag.isActive()) return;
    const { state } = latest.current.work;
    const pool = latest.current.animations.events.workStatus ?? [];
    if (!state) {
      // Idle again: the loop playing a stage ends, and the random chain takes over.
      if (poolIncludes(pool, latest.current.anim)) current.chain.stopLoopingFront();
      return;
    }
    const slot = pool[WORK_STATUS_INDEX[state]];
    if (slot === undefined || slot.length === 0) return;
    const finished = state === "success" || state === "error";
    current.play(pickSlot(slot, latest.current.anim), finished || (Array.isArray(slot) && slot.length > 1));
  }, [tick]);

  // A new result of a scheduled task: a wave, on any screen (its content stays in VNAS).
  const seen = useRef(unread);
  useEffect(() => {
    const more = unread > seen.current;
    seen.current = unread;
    const current = companion.current;
    if (!more || !current || current.chain.isFrozen() || current.drag.isActive() || latest.current.work.state) return;
    const clicks = latest.current.animations.clicks;
    if (clicks.length > 0) current.play(clicks.find((name) => name.endsWith("挥手")) ?? pick(clicks), true);
  }, [unread]);

  // A smaller window must not leave the companion off the page.
  useEffect(() => {
    const refit = () => companion.current?.refit();
    window.addEventListener("resize", refit);
    return () => window.removeEventListener("resize", refit);
  }, []);

  // Rest while the member is away or the page is hidden; come back on their next move.
  useEffect(() => {
    let timer = 0;
    const rest = () => {
      companion.current?.chain.setFrozen(true);
      companion.current?.rest();
    };
    const wake = () => {
      window.clearTimeout(timer);
      if (document.hidden) {
        rest();
        return;
      }
      timer = window.setTimeout(rest, IDLE_MS);
      const current = companion.current;
      if (!current?.chain.setFrozen(false)) return;
      current.resume();
      if (!current.resumeWork()) current.pickNext();
    };
    if (companion.current) companion.current.wake = wake;
    wake();
    for (const type of ACTIVITY) window.addEventListener(type, wake, { passive: true });
    document.addEventListener("visibilitychange", wake);
    return () => {
      window.clearTimeout(timer);
      for (const type of ACTIVITY) window.removeEventListener(type, wake);
      document.removeEventListener("visibilitychange", wake);
    };
  }, []);

  return (
    <div
      ref={root}
      className={styles.root}
      style={{ left: start.x, top: start.y, width: SIZE, height: metrics.halfH * 2 }}
      data-anim={anim}
      data-work={workStatus.state ?? "idle"}
    >
      <div className={styles.stage} style={petStageStyle(metrics.bottomPad)}>
        <video ref={videoA} className={styles.video} muted playsInline preload="auto" />
        <video ref={videoB} className={styles.video} muted playsInline preload="auto" />
        <div
          className={styles.hit}
          style={PET_HIT_STYLE}
          role="button"
          tabIndex={0}
          aria-label="大肥鱼"
          title={credit}
          onPointerDown={(event) => companion.current?.drag.onPointerDown(event)}
          onPointerMove={(event) => companion.current?.drag.onPointerMove(event)}
          onPointerUp={(event) => companion.current?.drag.onPointerUp(event)}
          onPointerCancel={(event) => companion.current?.drag.onPointerUp(event)}
          onClick={() => companion.current?.drag.onClick()}
          onKeyDown={(event) => {
            if (event.key !== "Enter" && event.key !== " ") return;
            event.preventDefault();
            companion.current?.drag.onClick();
          }}
        />
      </div>
    </div>
  );
}
