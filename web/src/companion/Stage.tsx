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
import { advanceFlight, type Flight, inside, type Point, speechOffset } from "./motion";
import { playWords, usePlaySession } from "./playSession";
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
}: {
  character: CharacterKind;
  quiet: boolean;
  native?: boolean;
  floating?: boolean;
  interactive?: boolean;
  taskPhase?: import("./taskState").TaskPhase;
  taskMessage?: string;
  taskEvent?: string;
}) {
  const stage = useRef<HTMLDivElement>(null);
  const actor = useRef<HTMLDivElement>(null);
  const speech = useRef<HTMLParagraphElement>(null);
  const press = useRef<Press | undefined>(undefined);
  const flight = useRef<Flight | undefined>(undefined);
  const position = useRef<Point>({ x: 0, y: 0 });
  const frame = useRef(0);
  const ignoreClick = useRef(false);
  const [dragging, setDragging] = useState(false);
  const [reaction, setReaction] = useState("");
  const [active, setActive] = useState(() => !document.hidden);
  const [reduced, setReduced] = useState(false);
  const [settled, setSettled] = useState(true);
  const previousTask = useRef({ phase: taskPhase, event: taskEvent });
  const greeting = useRef({ at: 0, count: 0 });
  const quietNow = quiet || !active;
  const taskBusy = ["connecting", "thinking", "working", "waiting"].includes(taskPhase);
  const { session, dispatch } = usePlaySession(
    quietNow || !interactive || taskBusy || dragging,
    taskPhase === "idle" || (taskPhase === "success" && settled),
  );
  const feeling = taskPose(taskPhase, settled);
  const playing = session.phase !== "idle";
  const beforePeek = useRef<Point | undefined>(undefined);

  useEffect(() => {
    if (previousTask.current.phase === taskPhase && previousTask.current.event === taskEvent) return;
    previousTask.current = { phase: taskPhase, event: taskEvent };
    setSettled(false);
    setReaction("");
    if (["success", "error", "waiting", "connecting", "stopped"].includes(taskPhase)) dispatch({ type: "stop" });
    if (taskPhase !== "success" && taskPhase !== "error") return;
    const timer = window.setTimeout(() => setSettled(true), 6500);
    return () => window.clearTimeout(timer);
  }, [taskPhase, taskEvent, dispatch]);

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

  useEffect(() => {
    if (session.phase === "hiding") {
      stop();
      ignoreClick.current = false;
      beforePeek.current ??= position.current;
      const space = bounds();
      place({
        x: session.side === "left" ? Math.min(16, space.width) : Math.max(0, space.width - 16),
        y: floating ? space.height : space.height - 15,
      });
    } else if (session.phase === "idle" && beforePeek.current) {
      place(beforePeek.current);
      beforePeek.current = undefined;
    }
  }, [session.phase, session.side, bounds, floating, place, stop]);

  const down = (event: ReactPointerEvent<HTMLButtonElement>) => {
    if (event.button !== 0 || press.current || playing) return;
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
  const invite = () => {
    if (quietNow || !interactive || taskBusy) return;
    setReaction("");
    dispatch({ type: "invite", side: session.side === "left" ? "right" : "left" });
  };
  let action: PetAction = feeling.action;
  if (session.phase === "hiding") action = session.side;
  if (session.phase === "peek") action = "wave";
  if (session.phase === "found" || session.phase === "gifted") action = "happy";
  if (reaction && !playing) action = reaction.startsWith("开心") ? "happy" : "wave";
  if (dragging) action = "waiting";
  const words = quiet
    ? "我在，安静陪你"
    : reaction ||
      (playing
        ? playWords(session.phase)
        : taskPhase === "idle" && session.offered
          ? "我在旁边安静陪你，想我时点一下就好。"
          : taskMessage || feeling.words);
  const emotion = quiet
    ? "安静"
    : session.phase === "found" || session.phase === "gifted" || reaction.startsWith("开心")
      ? "开心"
      : playing
        ? "想逗你玩"
        : reaction
          ? "亲近"
          : feeling.label;

  useLayoutEffect(() => {
    positionSpeech();
    const observer = typeof ResizeObserver === "undefined" ? undefined : new ResizeObserver(positionSpeech);
    if (speech.current) observer?.observe(speech.current);
    return () => observer?.disconnect();
  }, [words, positionSpeech]);

  return (
    <>
      <div
        ref={stage}
        className={`${styles.stage} ${floating ? styles.smallStage : ""} ${quietNow ? styles.paused : ""}`}
        data-emotion={emotion}
        data-play-phase={session.phase}
      >
        {!floating && (
          <div className={styles.nook} aria-hidden="true">
            <span />
            <span />
            <span />
          </div>
        )}
        <div ref={actor} className={`${styles.petAnchor} ${playing ? styles.peekActor : ""}`}>
          <button
            type="button"
            data-pet-interactive="true"
            className={`${styles.pet} ${dragging ? styles.dragging : ""} ${reaction ? styles.greeting : ""} ${playing ? styles.peekPet : ""}`}
            aria-label={session.phase === "peek" ? "找到躲起来的奶蛙" : "和伙伴打招呼，按住可拖动"}
            disabled={!interactive || session.phase === "hiding"}
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
              if (session.phase === "peek") {
                dispatch({ type: "find" });
                return;
              }
              if (playing) return;
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
            <span className={styles.peekFigure}>
              <Character kind={character} asleep={quietNow} action={action} />
            </span>
            {playing && (
              <span className={styles.peekCover} aria-hidden="true">
                ✦
              </span>
            )}
          </button>
          <p ref={speech} className={styles.reaction} role="status" aria-live="polite">
            {words}
          </p>
        </div>
        {(session.phase === "found" || session.phase === "gifted") && (
          <button
            type="button"
            data-pet-interactive="true"
            className={styles.flower}
            aria-label="收下奶蛙送的小花"
            disabled={session.phase === "gifted" || !interactive}
            onClick={() => dispatch({ type: "gift" })}
          >
            ❀
          </button>
        )}
      </div>
      <div className={styles.playDock} data-pet-interactive="true" aria-label="陪伴互动">
        {session.phase === "hiding" || session.phase === "peek" || session.phase === "found" ? (
          <button
            type="button"
            onClick={() => {
              dispatch({ type: "stop" });
              setReaction("好，你先忙。我在旁边待着。");
            }}
          >
            我先忙啦
          </button>
        ) : (
          <button type="button" disabled={quietNow || !interactive || taskBusy} onClick={invite}>
            陪我一下
          </button>
        )}
      </div>
    </>
  );
}
