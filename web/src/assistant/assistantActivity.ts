// What VNAS is doing, for the companion and anything else on the desktop that
// shows it: the turns of the conversations the VNAS window has on screen, and
// the member's cards waiting for a decision. Only the window reports; nothing
// connects to the member's instance to ask, so watching VNAS never starts one
// (an instance stays up as long as a session of the member does).
import { useSyncExternalStore } from "react";

/** The stages of a turn, in dsh-pet's words (web/src/companion/dsh-pet/shared/work-status.ts). */
export type AssistantActivity = "thinking" | "working" | "result" | "waiting" | "success" | "error";

export interface AssistantActivitySnapshot {
  /** The stage to show, or null when VNAS is idle. */
  state: AssistantActivity | null;
  /** Grows with every change, so the same stage reached again plays again. */
  seq: number;
}

// The stage shown when several conversations differ, as dsh-pet ranks them: a
// card waiting for the member first, and a turn still going over one that ended.
const priority: Record<AssistantActivity, number> = {
  waiting: 60,
  error: 50,
  working: 40,
  thinking: 30,
  result: 25,
  success: 20,
};

const turns = new Map<string, { state: AssistantActivity; seq: number }>();
let awaiting = 0;
let seq = 0;
let snapshot: AssistantActivitySnapshot = { state: null, seq: 0 };
const listeners = new Set<() => void>();

function publish() {
  seq += 1;
  let best: { state: AssistantActivity; seq: number } | undefined;
  for (const turn of turns.values()) {
    if (
      !best ||
      priority[turn.state] > priority[best.state] ||
      (priority[turn.state] === priority[best.state] && turn.seq > best.seq)
    )
      best = turn;
  }
  const state = awaiting > 0 ? "waiting" : (best?.state ?? null);
  snapshot = { state, seq };
  listeners.forEach((listener) => listener());
}

/**
 * The stage a gateway event of a turn leads to: undefined leaves the stage as
 * it is, null ends the turn without a result (it was stopped).
 */
export function activityOf(
  type: string,
  payload?: { name?: unknown; status?: unknown },
): AssistantActivity | null | undefined {
  switch (type) {
    case "message.start":
    case "reasoning.delta":
      return "thinking";
    case "tool.start":
    case "tool.complete":
      // Hermes reports its own bookkeeping as tools named with a leading underscore.
      if (typeof payload?.name !== "string" || !payload.name || payload.name.startsWith("_")) return undefined;
      return type === "tool.start" ? "working" : "result";
    case "message.complete":
      return payload?.status === "error" ? "error" : payload?.status === "interrupted" ? null : "success";
    default:
      return undefined;
  }
}

/** A conversation's turn reached a new stage, or ended without one (null). */
export function reportTurn(sessionId: string, state: AssistantActivity | null): void {
  const previous = turns.get(sessionId);
  if (state === null) {
    if (!previous) return;
    turns.delete(sessionId);
  } else {
    if (previous?.state === state) return;
    turns.set(sessionId, { state, seq: seq + 1 });
  }
  publish();
}

/** The window no longer shows this conversation, or closed: its stage goes. */
export function forgetTurn(sessionId: string): void {
  if (turns.delete(sessionId)) publish();
}

/** How many of the member's cards wait for a decision. */
export function reportAwaiting(count: number): void {
  const next = Math.max(0, Math.floor(count));
  if (next === awaiting) return;
  awaiting = next;
  publish();
}

export function readAssistantActivity(): AssistantActivitySnapshot {
  return snapshot;
}

export function subscribeAssistantActivity(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function useAssistantActivity(): AssistantActivitySnapshot {
  return useSyncExternalStore(subscribeAssistantActivity, readAssistantActivity);
}

/** For tests: back to idle with nothing reported. */
export function resetAssistantActivity(): void {
  turns.clear();
  awaiting = 0;
  seq = 0;
  snapshot = { state: null, seq: 0 };
}
