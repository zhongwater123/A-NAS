import {
  type PointerEvent as ReactPointerEvent,
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";

import { type PetAction, taskPose } from "./behaviour";
import { Character } from "./Character";
import styles from "./companion.module.css";
import { advanceFlight, type Flight, inside, type Point, speechOffset, speechSide, speechSpot } from "./motion";
import type { Clip } from "./native";
import type { Character as CharacterKind } from "./preferences";
import "./native";

interface Press {
  pointer: number;
  start: Point;
  previous: Point;
  position: Point;
  at: number;
  velocity: Point;
  moved: boolean;
}

export function Stage({
  character,
  quiet,
  native = false,
  floating = false,
  interactive = true,
  taskPhase = "idle",
  taskMessage,
  taskEvent,
  companionMessage,
  companionAction,
  onInteract,
  hideSpeech = false,
  clip,
}: {
  character: CharacterKind;
  quiet: boolean;
  native?: boolean;
  floating?: boolean;
  interactive?: boolean;
  taskPhase?: import("./taskState").TaskPhase;
  taskMessage?: string;
  taskEvent?: string;
  companionMessage?: string;
  companionAction?: PetAction;
  onInteract?(): void;
  hideSpeech?: boolean;
  // Desktop only: how much of the window is off screen, so the speech bubble stays on it.
  clip?: Clip;
}) {
  const stage = useRef<HTMLDivElement>(null);
  const actor = useRef<HTMLDivElement>(null);
  const speech = useRef<HTMLParagraphElement>(null);
  const press = useRef<Press | undefined>(undefined);
  const flight = useRef<Flight | undefined>(undefined);
  const position = useRef<Point>({ x: 0, y: 0 });
  const frame = useRef(0);
  const ignoreClick = useRef(false);
  const clipped = useRef(clip);
  const [dragging, setDragging] = useState(false);
  const [reaction, setReaction] = useState("");
  const [active, setActive] = useState(() => !document.hidden);
  const [reduced, setReduced] = useState(false);
  const [settled, setSettled] = useState(true);
  const previousTask = useRef({ phase: taskPhase, event: taskEvent });
  const greeting = useRef({ at: 0, count: 0 });
  const quietNow = quiet || !active;
  const taskBusy = ["connecting", "thinking", "working", "waiting"].includes(taskPhase);
  const feeling = taskPose(taskPhase, settled);

  useEffect(() => {
    if (previousTask.current.phase === taskPhase && previousTask.current.event === taskEvent) return;
    const noticeEnded = previousTask.current.event !== undefined && taskEvent === undefined;
    const restoredOutcome =
      previousTask.current.phase === "connecting" &&
      taskEvent === undefined &&
      (taskPhase === "success" || taskPhase === "error");
    previousTask.current = { phase: taskPhase, event: taskEvent };
    setReaction("");
    // Restoring the underlying conversation after a notice is not another
    // completed turn. Keep its old outcome settled instead of celebrating it.
    if (noticeEnded || restoredOutcome) {
      setSettled(true);
      return;
    }
    setSettled(false);
    if (taskPhase !== "success" && taskPhase !== "error") return;
    const timer = window.setTimeout(() => setSettled(true), 6500);
    return () => window.clearTimeout(timer);
  }, [taskPhase, taskEvent]);

  const stop = useCallback(() => {
    cancelAnimationFrame(frame.current);
    frame.current = 0;
    flight.current = undefined;
  }, []);
  useEffect(() => {
    const media = window.matchMedia?.("(prefers-reduced-motion: reduce)");
    const update = () => {
      setActive(!document.hidden);
      setReduced(media?.matches ?? false);
    };
    update();
    document.addEventListener("visibilitychange", update);
    media?.addEventListener("change", update);
    return () => {
      stop();
      document.removeEventListener("visibilitychange", update);
      media?.removeEventListener("change", update);
    };
  }, [stop]);
  useEffect(() => {
    if (quietNow || reduced) stop();
  }, [quietNow, reduced, stop]);
  useEffect(() => {
    if (!reaction) return;
    const timer = window.setTimeout(() => setReaction(""), 1800);
    return () => window.clearTimeout(timer);
  }, [reaction]);

  const bounds = useCallback(() => {
    const width = stage.current?.clientWidth ?? 0;
    const height = stage.current?.clientHeight ?? 0;
    const size = floating ? 128 : 160;
    return { width: Math.max(0, width - size), height: Math.max(0, height - size - (floating ? 8 : 20)) };
  }, [floating]);
  const positionSpeech = useCallback(() => {
    const element = stage.current;
    const bubble = speech.current;
    if (!element || !bubble) return;
    const width = element.clientWidth || 320;
    const edges = clipped.current;
    if (edges) {
      const box = element.getBoundingClientRect();
      // The stage on screen; below it is the button row, which the bubble must not cover.
      const view = {
        left: Math.max(0, edges.left - box.left),
        top: Math.max(0, edges.top - box.top),
        right: Math.min(width, window.innerWidth - edges.right - box.left),
        bottom: Math.min(element.clientHeight, window.innerHeight - edges.bottom - box.top),
      };
      const pet = position.current;
      bubble.style.width = `${Math.min(240, Math.max(0, view.right - view.left - 12))}px`;
      let spot = speechSpot(pet, 128, view, { width: bubble.offsetWidth, height: bubble.offsetHeight });
      if (!spot) {
        // At the top of the screen there is no room above: speak from beside the pet instead.
        const side = speechSide(pet, 128, view);
        bubble.style.width = `${Math.min(240, Math.max(72, side.width))}px`;
        spot = {
          x: side.side === "right" ? pet.x + 128 + 6 : pet.x - 6 - bubble.offsetWidth,
          y: Math.max(view.top + 6, pet.y + 8),
        };
      }
      bubble.style.transform = `translate(${spot.x - pet.x}px, ${spot.y - pet.y}px)`;
      return;
    }
    bubble.style.width = `${Math.min(240, Math.max(0, width - 12))}px`;
    const offset = speechOffset(
      position.current,
      floating ? 128 : 160,
      { width, height: element.clientHeight || 300 },
      { width: bubble.offsetWidth, height: bubble.offsetHeight },
    );
    bubble.style.transform = `translate(${offset.x}px, ${offset.y}px)`;
  }, [floating]);
  const place = useCallback(
    (next: Point) => {
      position.current = inside(next, bounds());
      if (actor.current) actor.current.style.transform = `translate(${position.current.x}px, ${position.current.y}px)`;
      positionSpeech();
    },
    [bounds, positionSpeech],
  );
  useEffect(() => {
    const element = stage.current;
    if (!element) return;
    const reset = () => {
      stop();
      const space = bounds();
      place({ x: space.width / 2, y: floating ? space.height : space.height - 15 });
    };
    reset();
    const observer =
      typeof ResizeObserver === "undefined"
        ? undefined
        : new ResizeObserver(() => {
            stop();
            place(position.current);
          });
    observer?.observe(element);
    return () => observer?.disconnect();
  }, [bounds, place, stop, floating]);

  const down = (event: ReactPointerEvent<HTMLButtonElement>) => {
    if (event.button !== 0 || press.current) return;
    stop();
    event.currentTarget.setPointerCapture(event.pointerId);
    const point = { x: native ? event.screenX : event.clientX, y: native ? event.screenY : event.clientY };
    press.current = {
      pointer: event.pointerId,
      start: point,
      previous: point,
      position: position.current,
      velocity: { x: 0, y: 0 },
      at: event.timeStamp,
      moved: false,
    };
  };
  const move = (event: ReactPointerEvent<HTMLButtonElement>) => {
    const value = press.current;
    if (!value || value.pointer !== event.pointerId) return;
    const point = { x: native ? event.screenX : event.clientX, y: native ? event.screenY : event.clientY };
    const dx = point.x - value.start.x;
    const dy = point.y - value.start.y;
    if (!value.moved && Math.hypot(dx, dy) < 6) return;
    value.moved = true;
    setDragging(true);
    const elapsed = Math.max(8, event.timeStamp - value.at) / 1000;
    value.velocity = { x: (point.x - value.previous.x) / elapsed, y: (point.y - value.previous.y) / elapsed };
    if (native) window.companionDesktop?.move(point.x - value.previous.x, point.y - value.previous.y);
    else place({ x: value.position.x + dx, y: value.position.y + dy });
    value.previous = point;
    value.at = event.timeStamp;
  };
  const up = (event: ReactPointerEvent<HTMLButtonElement>, cancelled = false) => {
    const value = press.current;
    if (!value || value.pointer !== event.pointerId) return;
    press.current = undefined;
    setDragging(false);
    ignoreClick.current = value.moved;
    if (!value.moved) return;
    if (cancelled || quietNow || reduced || native) {
      setReaction("坐稳啦");
      return;
    }
    const fresh = event.timeStamp - value.at < 100;
    flight.current = {
      position: position.current,
      velocity: fresh
        ? {
            x: Math.max(-1200, Math.min(1200, value.velocity.x)),
            y: Math.max(-1200, Math.min(1200, value.velocity.y)),
          }
        : { x: 0, y: 0 },
    };
    let previous = performance.now();
    const tick = (now: number) => {
      if (!flight.current) return;
      flight.current = advanceFlight(flight.current, bounds(), (now - previous) / 1000);
      previous = now;
      place(flight.current.position);
      if (flight.current.velocity.x === 0 && flight.current.velocity.y === 0) {
        flight.current = undefined;
        setReaction("坐稳啦");
        return;
      }
      frame.current = requestAnimationFrame(tick);
    };
    frame.current = requestAnimationFrame(tick);
  };
  const localAllowed = !taskBusy && !taskEvent && (taskPhase === "idle" || settled);
  let action: PetAction = localAllowed && companionAction ? companionAction : feeling.action;
  if (reaction) action = reaction.startsWith("开心") ? "happy" : "wave";
  if (dragging) action = "waiting";
  const words = quiet
    ? "我在，安静陪你"
    : reaction || (localAllowed && companionMessage ? companionMessage : taskMessage || feeling.words);
  const emotion = quiet
    ? "安静"
    : reaction.startsWith("开心") || (localAllowed && companionAction === "happy")
      ? "开心"
      : reaction
        ? "亲近"
        : localAllowed && companionAction === "thinking"
          ? "专注"
          : feeling.label;

  useLayoutEffect(() => {
    clipped.current = clip;
    positionSpeech();
  }, [clip, positionSpeech]);
  useLayoutEffect(() => {
    positionSpeech();
    const observer = typeof ResizeObserver === "undefined" ? undefined : new ResizeObserver(positionSpeech);
    if (speech.current) observer?.observe(speech.current);
    return () => observer?.disconnect();
  }, [words, positionSpeech, hideSpeech]);

  return (
    <>
      <div
        ref={stage}
        className={`${styles.stage} ${floating ? styles.smallStage : ""} ${quietNow ? styles.paused : ""}`}
        data-emotion={emotion}
      >
        {!floating && (
          <div className={styles.nook} aria-hidden="true">
            <span />
            <span />
            <span />
          </div>
        )}
        <div ref={actor} className={styles.petAnchor}>
          <button
            type="button"
            data-pet-interactive="true"
            className={`${styles.pet} ${dragging ? styles.dragging : ""} ${reaction ? styles.greeting : ""}`}
            aria-label={onInteract ? "和大肥鱼聊聊，按住可拖动" : "和伙伴打招呼，按住可拖动"}
            disabled={!interactive}
            onPointerDown={down}
            onPointerMove={move}
            onPointerUp={(event) => up(event)}
            onPointerCancel={(event) => up(event, true)}
            onLostPointerCapture={(event) => {
              if (press.current) up(event, true);
            }}
            onClick={() => {
              if (ignoreClick.current) {
                ignoreClick.current = false;
                return;
              }
              if (onInteract) {
                onInteract();
                return;
              }
              if (taskPhase === "error") setSettled(true);
              const now = performance.now();
              greeting.current = { at: now, count: now - greeting.current.at < 1400 ? greeting.current.count + 1 : 1 };
              setReaction(
                quiet
                  ? "我在，安静陪你"
                  : taskPhase === "error"
                    ? "谢谢你安慰我，缓过来啦。我们慢慢来。"
                    : taskPhase === "success"
                      ? "开心 · 你也来击掌啦！一起看看结果。"
                      : greeting.current.count >= 3
                        ? "开心 · 哈哈，有点痒！"
                        : "嗨，我在这儿",
              );
            }}
          >
            <span>
              <Character kind={character} asleep={quietNow} action={action} />
            </span>
          </button>
          {!hideSpeech && (
            <p ref={speech} className={styles.reaction} role="status" aria-live="polite">
              {words}
            </p>
          )}
        </div>
      </div>
    </>
  );
}
