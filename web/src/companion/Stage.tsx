import { type PointerEvent as ReactPointerEvent, useCallback, useEffect, useRef, useState } from "react";

import { Character } from "./Character";
import styles from "./companion.module.css";
import { advanceFlight, type Flight, inside, type Point } from "./motion";
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
  const quietNow = quiet || !active || reduced;

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
    if (quietNow) stop();
  }, [quietNow, stop]);
  useEffect(() => {
    if (!reaction) return;
    const timer = window.setTimeout(() => setReaction(""), 1800);
    return () => window.clearTimeout(timer);
  }, [reaction]);

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
    if (cancelled || quietNow || native) {
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
    const target = ball.current;
    if (!target || animation.current?.playState === "running") return;
    setReaction("接住了，再来一次？");
    setReturns((value) => value + 1);
    onPlay?.();
    if (reduced || !target.animate) return;
    const distance = position.current.x - 12;
    animation.current = target.animate(
      [
        { transform: "translate(0, 0) rotate(0deg)" },
        { transform: `translate(${distance * 0.55}px, -75px) rotate(160deg)`, offset: 0.35 },
        { transform: `translate(${distance}px, -38px) rotate(260deg)`, offset: 0.55 },
        { transform: `translate(${distance * 0.4}px, -90px) rotate(400deg)`, offset: 0.75 },
        { transform: "translate(0, 0) rotate(540deg)" },
      ],
      { duration: 1700, easing: "ease-in-out" },
    );
  };

  return (
    <div
      ref={stage}
      className={`${styles.stage} ${floating ? styles.smallStage : ""} ${quietNow ? styles.paused : ""}`}
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
          setReaction(quiet ? "我在，安静陪你" : "嗨，我在这儿");
        }}
      >
        <Character kind={character} asleep={quietNow} phase={taskPhase} />
      </button>
      <button
        ref={ball}
        type="button"
        data-pet-interactive="true"
        className={styles.ball}
        aria-label="把小球传给伙伴"
        disabled={!interactive}
        onClick={play}
      >
        ✦
      </button>
      <p className={styles.reaction} role="status" aria-live="polite">
        {reaction || (quiet && taskPhase === "idle" ? "安静陪着你" : "")}
      </p>
      {!floating && (
        <span className={styles.playCount}>
          {(playCount ?? returns) ? `一起玩了 ${playCount ?? returns} 次` : "轻点打招呼 · 按住拖动 · 点小球抛接"}
        </span>
      )}
    </div>
  );
}
