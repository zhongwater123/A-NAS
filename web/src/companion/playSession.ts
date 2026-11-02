import { useEffect, useEffectEvent, useReducer, useRef } from "react";

export type PlayKind = "none" | "ball" | "memory" | "breathe";
export type Beat = "left" | "wave" | "right";
export type PlayPhase =
  | "idle"
  | "throw"
  | "return"
  | "catch"
  | "caught"
  | "missed"
  | "show"
  | "gap"
  | "input"
  | "round"
  | "inhale"
  | "exhale"
  | "done";
export interface PlaySession {
  kind: PlayKind;
  phase: PlayPhase;
  round: number;
  score: number;
  step: number;
  pattern: Beat[];
}
export const resting: PlaySession = { kind: "none", phase: "idle", round: 1, score: 0, step: 0, pattern: [] };
type PlayEvent =
  | { type: "start"; kind: Exclude<PlayKind, "none">; pattern?: Beat[] }
  | { type: "next" }
  | { type: "catch" }
  | { type: "beat"; beat: Beat }
  | { type: "stop" };

export function playSession(state: PlaySession, event: PlayEvent): PlaySession {
  if (event.type === "stop") return resting;
  if (event.type === "start")
    return {
      ...resting,
      kind: event.kind,
      pattern: event.pattern ?? [],
      phase: event.kind === "ball" ? "throw" : event.kind === "memory" ? "show" : "inhale",
    };
  if (event.type === "catch" && state.phase === "catch") return { ...state, score: state.score + 1, phase: "caught" };
  if (event.type === "beat" && state.phase === "input") {
    if (state.pattern[state.step] !== event.beat) return { ...state, phase: "done" };
    return state.step + 1 === state.round + 1
      ? { ...state, score: state.score + 1, step: 0, phase: state.round === 3 ? "done" : "round" }
      : { ...state, step: state.step + 1 };
  }
  if (event.type !== "next") return state;
  if (state.kind === "ball") {
    const phase =
      state.phase === "throw"
        ? "return"
        : state.phase === "return"
          ? "catch"
          : state.phase === "catch"
            ? "missed"
            : state.phase === "caught" || state.phase === "missed"
              ? state.round === 3
                ? "done"
                : "throw"
              : state.phase;
    return { ...state, phase, round: phase === "throw" ? state.round + 1 : state.round };
  }
  if (state.kind === "memory") {
    if (state.phase === "round") return { ...state, round: state.round + 1, step: 0, phase: "show" };
    if (state.phase === "show") return { ...state, phase: "gap" };
    if (state.phase === "gap")
      return state.step + 1 === state.round + 1
        ? { ...state, phase: "input", step: 0 }
        : { ...state, phase: "show", step: state.step + 1 };
  }
  if (state.kind === "breathe") {
    if (state.phase === "inhale") return { ...state, phase: "exhale" };
    if (state.phase === "exhale")
      return state.round === 5 ? { ...state, phase: "done" } : { ...state, phase: "inhale", round: state.round + 1 };
  }
  return state;
}

export function playDelay(state: PlaySession, reduced: boolean): number | undefined {
  switch (state.phase) {
    case "throw":
      return 850;
    case "return":
      return 750;
    case "catch":
      return reduced ? undefined : 2800;
    case "caught":
    case "missed":
    case "round":
      return 1000;
    case "show":
      return reduced ? 1100 : 750;
    case "gap":
      return 300;
    case "inhale":
    case "exhale":
      return 3000;
    default:
      return undefined;
  }
}

export function usePlaySession(blocked: boolean, reduced: boolean, onCatch?: () => void) {
  const [session, dispatch] = useReducer(playSession, resting);
  const credited = useRef(0);
  const credit = useEffectEvent(() => onCatch?.());
  useEffect(() => {
    if (blocked) dispatch({ type: "stop" });
  }, [blocked]);
  useEffect(() => {
    if (session.kind === "ball" && session.score > credited.current) credit();
    credited.current = session.score;
  }, [session.kind, session.score]);
  useEffect(() => {
    if (blocked) return;
    const delay = playDelay(session, reduced);
    if (delay === undefined) return;
    const timer = window.setTimeout(() => dispatch({ type: "next" }), delay);
    return () => window.clearTimeout(timer);
  }, [blocked, reduced, session]);
  return { session, dispatch };
}

export const beatWords: Record<Beat, string> = { left: "左边", wave: "招手", right: "右边" };
export function playWords(s: PlaySession): string {
  if (s.kind === "none") return "";
  if (s.phase === "done")
    return s.kind === "ball"
      ? `三回合结束，接住了 ${s.score} 次！谢谢你陪我玩。`
      : s.kind === "memory"
        ? s.score === 3
          ? "三轮都跟上了，我们好有默契！"
          : "差一点点！没关系，再一起试一次？"
        : "一起慢下来了。现在感觉怎么样？";
  if (s.kind === "ball")
    return (
      (
        {
          throw: "你传过来了，我来接！",
          return: "接住啦，准备传回给你…",
          catch: "到你啦！点亮起来的小球接住",
          caught: "配合得真好！",
          missed: "差一点，我们慢慢来。",
        } as Partial<Record<PlayPhase, string>>
      )[s.phase] ?? ""
    );
  if (s.kind === "memory")
    return s.phase === "show"
      ? `看我的：${beatWords[s.pattern[s.step]]}`
      : s.phase === "gap"
        ? "…"
        : s.phase === "round"
          ? "对啦！下一轮多一个动作。"
          : `轮到你：${s.step + 1} / ${s.round + 1}`;
  return `${s.phase === "inhale" ? "和我一起吸气" : "慢慢呼气"} · ${s.round} / 5`;
}
