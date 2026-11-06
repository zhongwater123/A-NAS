// 大肥鱼 on the desktop, played as dsh-pet plays its pet (dsh-pet/README.md):
// its animation chain swaps two videos, picks the next animation at random
// while VNAS is idle, follows VNAS's stage (workStatus.ts) and answers clicks
// and drags through dsh-pet's own drag driver. A throw is not wired yet: a drag
// ends where the member lets go. The companion freezes on its frame while the
// member is away or the page is hidden, so an idle desktop does not keep
// decoding video (the screen saver comes later still).
import { type Dispatch, type SetStateAction, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";

import { useTaskNotices } from "../assistant/taskNotices";
import { companionWeights, installedAnimations } from "./animations";
import type { InstalledAnimations } from "./Companion";
import styles from "./companion.module.css";
import { createOwnAnimation, type EndedCue, type OwnAnimation } from "./dsh-pet/client/pet/animation/animation";
import { prefersMov } from "./dsh-pet/client/pet/animation/media";
import { decideNextAnimation } from "./dsh-pet/client/pet/animation/next-animation";
import { createDrag, type Drag } from "./dsh-pet/client/pet/motion/drag";
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
}

// What plays next, after dsh-pet's card (client/pet/card/animation-flow.ts in
// dsh-pet) without the sleep, chatter and walking A-NAS does not have.
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

  const companion: Companion = {
    chain: undefined as unknown as OwnAnimation,
    drag: undefined as unknown as Drag,
    play(name, once) {
      show(name);
      set.setOnce(once);
      bump();
    },
    resumeWork() {
      const { state } = latest.current.work;
      if (companion.chain.isFrozen() || !state || state === "success" || state === "error") return false;
      const slot = latest.current.animations.events.workStatus?.[WORK_STATUS_INDEX[state]];
      if (slot === undefined) return false;
      companion.play(pickSlot(slot, latest.current.anim), Array.isArray(slot) && slot.length > 1);
      return true;
    },
    pickNext() {
      if (companion.chain.isFrozen()) return;
      // The companion does not walk yet: what dsh-pet spends on a walk goes to an action.
      const kind = rollKind(Math.random(), companionWeights);
      const decision = decideNextAnimation({
        kind: kind === "move" ? "action" : kind,
        animations: latest.current.animations,
        facing: latest.current.facing,
        current: latest.current.anim,
      });
      if (decision.play && decision.anim !== null) show(decision.anim);
      set.setOnce(true);
      bump();
    },
    wake: () => undefined,
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
    stopAutonomy: () => undefined,
    stopFlight: () => undefined,
    startFlight: (x, y) => place({ x, y }),
    isFrozen: () => companion.chain.isFrozen(),
    releaseLock: () => companion.wake(),
    grabVelocity: () => null,
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
    if (slot === undefined) return;
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

  // Rest while the member is away or the page is hidden; come back on their next move.
  useEffect(() => {
    let timer = 0;
    const rest = () => companion.current?.chain.setFrozen(true);
    const wake = () => {
      window.clearTimeout(timer);
      if (document.hidden) {
        rest();
        return;
      }
      timer = window.setTimeout(rest, IDLE_MS);
      const current = companion.current;
      if (current?.chain.setFrozen(false) && !current.resumeWork()) current.pickNext();
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
