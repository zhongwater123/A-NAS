import { type PointerEvent as ReactPointerEvent, useCallback, useEffect, useRef, useState } from "react";

import { type PetAction, taskPose } from "./behaviour";
import { Character } from "./Character";
import styles from "./companion.module.css";
import { advanceFlight, type Flight, inside, type Point } from "./motion";
import { type Beat, beatWords, playWords, usePlaySession } from "./playSession";
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
  playCount,
  onPlay,
  taskPhase = "idle",
}: {
  character: CharacterKind;
  quiet: boolean;
  native?: boolean;
  floating?: boolean;
  interactive?: boolean;
  playCount?: number;
  onPlay?: () => void;
  taskPhase?: import("./taskState").TaskPhase;
}) {
  const stage = useRef<HTMLDivElement>(null);
  const pet = useRef<HTMLButtonElement>(null);
  const ball = useRef<HTMLButtonElement>(null);
  const press = useRef<Press | undefined>(undefined);
  const flight = useRef<Flight | undefined>(undefined);
  const position = useRef<Point>({ x: 0, y: 0 });
  const animation = useRef<Animation | undefined>(undefined);
  const frame = useRef(0);
  const ignoreClick = useRef(false);
  const [dragging, setDragging] = useState(false);
  const [reaction, setReaction] = useState("");
  const [returns, setReturns] = useState(0);
  const [active, setActive] = useState(() => !document.hidden);
  const [reduced, setReduced] = useState(false);
  const [settled, setSettled] = useState(true);
  const previousTask = useRef(taskPhase);
  const [visits, setVisits] = useState(0);
  const [invited, setInvited] = useState(false);
  const [declined, setDeclined] = useState(false);
  const greeting = useRef({ at: 0, count: 0 });
  const quietNow = quiet || !active;
  const { session, dispatch } = usePlaySession(quietNow || !interactive, reduced, () => {
    setReturns((value) => value + 1);
    onPlay?.();
  });
  const feeling = taskPose(taskPhase, settled);
  const playing = session.kind !== "none";
  const rememberVisit = () => {
    setVisits((value) => value + 1);
    setInvited(false);
  };

  useEffect(() => {
    if (previousTask.current === taskPhase) return;
    previousTask.current = taskPhase;
    setSettled(false);
    setReaction("");
    if (["success", "error", "waiting", "connecting", "stopped"].includes(taskPhase)) dispatch({ type: "stop" });
    if (taskPhase !== "success" && taskPhase !== "error") return;
    const timer = window.setTimeout(() => setSettled(true), 6500);
    return () => window.clearTimeout(timer);
  }, [taskPhase, dispatch]);

  useEffect(() => {
    if (quietNow || !interactive || playing || taskPhase !== "idle" || declined) return;
    const timer = window.setTimeout(() => setInvited(true), 45000);
    return () => window.clearTimeout(timer);
  }, [quietNow, interactive, playing, taskPhase, visits, declined]);

  const stop = useCallback(() => {
    cancelAnimationFrame(frame.current);
    frame.current = 0;
    flight.current = undefined;
    animation.current?.cancel();
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

  useEffect(() => {
    const target = ball.current;
    if (!target || quietNow || reduced || !target.animate || session.kind !== "ball") return;
    animation.current?.cancel();
    const distance = position.current.x + (floating ? 20 : 38);
    if (session.phase !== "throw" && session.phase !== "return") return;
    const outward = session.phase === "throw";
    animation.current = target.animate(
      [
        { transform: outward ? "translate(0, 0) rotate(0deg)" : `translate(${distance}px, -60px) rotate(180deg)` },
        { transform: `translate(${distance * 0.5}px, -115px) rotate(100deg)`, offset: 0.5 },
        { transform: outward ? `translate(${distance}px, -60px) rotate(180deg)` : "translate(0, 0) rotate(360deg)" },
      ],
      { duration: outward ? 850 : 750, easing: "ease-in-out", fill: "forwards" },
    );
    return () => animation.current?.cancel();
  }, [session.kind, session.phase, quietNow, reduced, floating]);

  const bounds = useCallback(() => {
    const width = stage.current?.clientWidth ?? 0;
    const height = stage.current?.clientHeight ?? 0;
    const size = floating ? 128 : 160;
    return { width: Math.max(0, width - size), height: Math.max(0, height - size - 20) };
  }, [floating]);
  const place = useCallback(
    (next: Point) => {
      position.current = inside(next, bounds());
      if (pet.current) pet.current.style.transform = `translate(${position.current.x}px, ${position.current.y}px)`;
    },
    [bounds],
  );
  useEffect(() => {
    const element = stage.current;
    if (!element) return;
    const reset = () => {
      stop();
      const space = bounds();
      place({ x: space.width / 2, y: space.height - 15 });
    };
    reset();
    const observer = typeof ResizeObserver === "undefined" ? undefined : new ResizeObserver(reset);
    observer?.observe(element);
    return () => observer?.disconnect();
  }, [bounds, place, stop]);

  const down = (event: ReactPointerEvent<HTMLButtonElement>) => {
    if (event.button !== 0 || press.current) return;
    rememberVisit();
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
  const play = () => {
    if (!interactive || quiet || !active) {
      setReaction("先安静待一会儿");
      return;
    }
    rememberVisit();
    setReaction("");
    if (session.kind === "ball" && session.phase !== "done") dispatch({ type: "catch" });
    else dispatch({ type: "start", kind: "ball" });
  };

  let action: PetAction = feeling.action;
  if (session.kind === "ball")
    action = session.phase === "return" || session.phase === "caught" || session.phase === "done" ? "happy" : "waiting";
  if (session.kind === "memory")
    action =
      session.phase === "show"
        ? session.pattern[session.step]
        : session.phase === "round" || (session.phase === "done" && session.score === 3)
          ? "happy"
          : "waiting";
  if (session.kind === "breathe") action = session.phase === "done" ? "wave" : "breathe";
  if (reaction && !playing) action = "wave";
  if (dragging) action = "waiting";
  const words =
    reaction ||
    (quiet
      ? "我在，安静陪你"
      : playing
        ? playWords(session)
        : invited
          ? "无聊吗？要不要一起玩三回合？"
          : feeling.words);

  return (
    <>
      <div
        ref={stage}
        className={`${styles.stage} ${floating ? styles.smallStage : ""} ${quietNow ? styles.paused : ""}`}
        data-emotion={quiet ? "安静" : feeling.label}
        data-play-phase={session.phase}
      >
        {!floating && (
          <div className={styles.nook} aria-hidden="true">
            <span />
            <span />
            <span />
          </div>
        )}
        <button
          ref={pet}
          type="button"
          data-pet-interactive="true"
          className={`${styles.pet} ${dragging ? styles.dragging : ""} ${reaction ? styles.greeting : ""}`}
          aria-label="和伙伴打招呼，按住可拖动"
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
            rememberVisit();
            const now = performance.now();
            greeting.current = { at: now, count: now - greeting.current.at < 1400 ? greeting.current.count + 1 : 1 };
            setReaction(
              quiet
                ? "我在，安静陪你"
                : taskPhase === "error"
                  ? "谢谢你摸摸我。我们慢慢来。"
                  : taskPhase === "success"
                    ? "击掌！一起看看这次的结果。"
                    : greeting.current.count >= 3
                      ? "哈哈，有点痒！再摸摸头就好。"
                      : "嗨，我在这儿",
            );
          }}
        >
          <Character kind={character} asleep={quietNow} action={action} inhale={session.phase === "inhale"} />
        </button>
        <button
          ref={ball}
          type="button"
          data-pet-interactive="true"
          className={`${styles.ball} ${session.phase === "catch" ? styles.catchBall : ""}`}
          aria-label={session.phase === "catch" ? "接住奶蛙传回的小球" : "把小球传给伙伴"}
          disabled={!interactive}
          onClick={play}
        >
          ✦
        </button>
        <p className={styles.reaction} role="status" aria-live="polite">
          {words}
        </p>
        {!floating && (
          <span className={styles.playCount}>
            {playing
              ? `${session.kind === "ball" ? "来回抛接" : session.kind === "memory" ? "默契跟拍" : "一起放松"} · 第 ${session.round} 轮`
              : (playCount ?? returns)
                ? `一起玩了 ${playCount ?? returns} 次`
                : `${feeling.label} · 轻点打招呼，或者选一个小玩法`}
          </span>
        )}
      </div>
      <div className={styles.playDock} data-pet-interactive="true" aria-label="陪伴小玩法">
        {playing && session.phase !== "done" ? (
          <>
            {session.kind === "memory" &&
              (["left", "wave", "right"] as Beat[]).map((beat) => (
                <button
                  type="button"
                  key={beat}
                  disabled={session.phase !== "input" || quietNow || !interactive}
                  onClick={() => {
                    rememberVisit();
                    dispatch({ type: "beat", beat });
                  }}
                >
                  {beatWords[beat]}
                </button>
              ))}
            <button
              type="button"
              onClick={() => {
                dispatch({ type: "stop" });
                rememberVisit();
              }}
            >
              先玩到这里
            </button>
          </>
        ) : (
          <>
            <button type="button" disabled={quietNow || !interactive} onClick={play}>
              抛接球
            </button>
            <button
              type="button"
              disabled={quietNow || !interactive}
              onClick={() => {
                rememberVisit();
                setReaction("");
                const beats: Beat[] = ["left", "wave", "right"];
                dispatch({
                  type: "start",
                  kind: "memory",
                  pattern: Array.from({ length: 4 }, () => beats[Math.floor(Math.random() * beats.length)]),
                });
              }}
            >
              跟我做
            </button>
            <button
              type="button"
              disabled={quietNow || !interactive}
              onClick={() => {
                rememberVisit();
                setReaction("");
                dispatch({ type: "start", kind: "breathe" });
              }}
            >
              一起放松
            </button>
            {session.phase === "done" && (
              <button type="button" onClick={() => dispatch({ type: "stop" })}>
                收好
              </button>
            )}
            {invited && (
              <button
                type="button"
                onClick={() => {
                  setDeclined(true);
                  setInvited(false);
                }}
              >
                下次再玩
              </button>
            )}
          </>
        )}
      </div>
    </>
  );
}
