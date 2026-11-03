import { useEffect, useReducer } from "react";

export type PlayPhase = "idle" | "hiding" | "peek" | "found" | "gifted";
export interface PlaySession {
  phase: PlayPhase;
  side: "left" | "right";
  offered: boolean;
}
export const resting: PlaySession = { phase: "idle", side: "right", offered: false };
type PlayEvent =
  | { type: "invite"; side: "left" | "right" }
  | { type: "peek" }
  | { type: "find" }
  | { type: "gift" }
  | { type: "stop" };

export function playSession(state: PlaySession, event: PlayEvent): PlaySession {
  switch (event.type) {
    case "invite":
      return { phase: "hiding", side: event.side, offered: true };
    case "peek":
      return state.phase === "hiding" ? { ...state, phase: "peek" } : state;
    case "find":
      return state.phase === "peek" ? { ...state, phase: "found" } : state;
    case "gift":
      return state.phase === "found" ? { ...state, phase: "gifted" } : state;
    case "stop":
      return state.phase === "idle" ? state : { ...state, phase: "idle" };
  }
}

// A single invitation per mounted visit. Ignoring or declining is neutral;
// task failures, not a person's absence, cause disappointment.
export function usePlaySession(blocked: boolean, autoAllowed: boolean) {
  const [session, dispatch] = useReducer(playSession, resting);
  useEffect(() => {
    if (blocked) dispatch({ type: "stop" });
  }, [blocked]);
  useEffect(() => {
    if (blocked) return;
    let delay: number;
    let event: PlayEvent;
    if (session.phase === "idle" && !session.offered && autoAllowed) {
      delay = 12000;
      event = { type: "invite", side: Math.random() < 0.5 ? "left" : "right" };
    } else if (session.phase === "hiding") {
      delay = 650;
      event = { type: "peek" };
    } else if (session.phase === "peek" || session.phase === "found" || session.phase === "gifted") {
      delay = session.phase === "peek" ? 20000 : session.phase === "found" ? 30000 : 4500;
      event = { type: "stop" };
    } else return;
    const timer = window.setTimeout(() => dispatch(event), delay);
    return () => window.clearTimeout(timer);
  }, [blocked, autoAllowed, session]);
  return { session, dispatch };
}

export function playWords(phase: PlayPhase) {
  switch (phase) {
    case "hiding":
      return "我有个小惊喜……先藏好。";
    case "peek":
      return "我躲起来啦！点一下露出来的小脑袋，找找我？";
    case "found":
      return "开心 · 你找到我啦！这朵小花送给你。";
    case "gifted":
      return "开心 · 你收下了！好啦，我在旁边陪你。";
    default:
      return "";
  }
}
