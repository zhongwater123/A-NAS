// What VNAS is doing, for the companion, summed up the way dsh-pet's host does it: its
// WorkStatusStore (one entry per conversation, the most urgent shown), the bridge that
// publishes it into the page's GET /state store (a working->result flip within 5 s
// merged, nothing published while what is shown stays the same), both vendored from
// dsh-pet/host. This file is the A-NAS adapter dsh-pet's own session watcher would be:
// it maps the VNAS window's gateway events onto the store, and keeps a finished
// conversation's success or error for 60 s before clearing it, as dsh-pet's
// session-notify.ts does (its timer code is ported here: the rest of that file serves
// the Harness's notifications and is not vendored).
//
// Only conversations whose turn this window started count: a window that just shows a
// conversation driven from another device does not move this device's companion. A
// card waiting for the member in such a turn is its own waiting entry. Nothing here
// connects to the member's instance, so watching VNAS never starts one.
import { useSyncExternalStore } from "react";

import { createWorkStatusBridge } from "./dsh-pet/host/plugin/work-status-bridge";
import { PollStateStore, type StateLeaf } from "./dsh-pet/host/state";
import { type HostWorkStatusState, WorkStatusStore } from "./dsh-pet/host/work-status";
import type { WorkStatusSnapshot } from "./dsh-pet/shared/work-status";

/** dsh-pet session-notify.ts TERMINAL_KEEP_MS: a success or error shows this long, then goes. */
const TERMINAL_KEEP_MS = 60 * 1000;
/** dsh-pet's sentinel for status written from outside a session; A-NAS writes none. */
const EXTERNAL_WORK_SESSION = "api";

/** The page's GET /state store (dsh-pet host/state.ts): the companion reads sections.workStatus. */
export const companionState = new PollStateStore();
const store = new WorkStatusStore();
const bridge = createWorkStatusBridge({ state: companionState, workStatus: store, EXTERNAL_WORK_SESSION });

/** Live conversations whose turn this window started and has not seen end. */
const prompting = new Set<string>();
/** Waiting entries for the member's cards, by card: card id -> entry id. */
const cards = new Map<string, string>();
const terminalTimers = new Map<string, ReturnType<typeof setTimeout>>();
let seq = 0;

function cancelTerminalCleanup(id: string) {
  const timer = terminalTimers.get(id);
  if (timer === undefined) return;
  clearTimeout(timer);
  terminalTimers.delete(id);
}

function scheduleTerminalCleanup(id: string) {
  if (terminalTimers.has(id)) return;
  terminalTimers.set(
    id,
    setTimeout(() => {
      terminalTimers.delete(id);
      const state = store.stateOf(id);
      if (state === "success" || state === "error") {
        store.clear(id);
        bridge.publishWorkStatus();
      }
    }, TERMINAL_KEEP_MS),
  );
}

function set(id: string, state: HostWorkStatusState) {
  seq += 1;
  const changed = store.setState(id, state, seq);
  bridge.publishWorkStatus();
  if (!changed) return;
  if (state === "success" || state === "error") scheduleTerminalCleanup(id);
  else cancelTerminalCleanup(id);
}

function clear(id: string) {
  cancelTerminalCleanup(id);
  if (store.clear(id)) bridge.publishWorkStatus();
}

/**
 * The stage a gateway event of a turn leads to: undefined leaves the stage as it is,
 * null ends the turn without a result (it was stopped).
 */
export function stageOf(
  type: string,
  payload?: { name?: unknown; status?: unknown },
): HostWorkStatusState | null | undefined {
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

/** This window started a turn of the conversation (it registered as its browser and prompts). */
export function startTurn(sessionId: string): void {
  prompting.add(sessionId);
}

/** A gateway event of a conversation: counts only for a turn this window started. */
export function reportEvent(sessionId: string, type: string, payload?: { name?: unknown; status?: unknown }): void {
  if (!prompting.has(sessionId)) return;
  const stage = stageOf(type, payload);
  if (type === "message.complete") prompting.delete(sessionId);
  if (stage === undefined) return;
  if (stage === null) clear(sessionId);
  else set(sessionId, stage);
}

/** The member's cards: each one awaiting a decision in a turn this window started is a waiting entry. */
export function reportCards(list: Iterable<{ requestId: string; sessionId: string; status: string }>): void {
  const waiting = new Set<string>();
  for (const card of list) if (card.status === "awaiting" && prompting.has(card.sessionId)) waiting.add(card.requestId);
  for (const [requestId, id] of cards)
    if (!waiting.has(requestId)) {
      cards.delete(requestId);
      clear(id);
    }
  for (const requestId of waiting)
    if (!cards.has(requestId)) {
      const id = `card:${requestId}`;
      cards.set(requestId, id);
      set(id, "waiting");
    }
}

/**
 * The window lost its connection or closed: it can no longer see those turns or
 * cards, so the companion goes back to idle instead of keeping their last stage.
 */
export function forgetAll(): void {
  prompting.clear();
  cards.clear();
  for (const timer of terminalTimers.values()) clearTimeout(timer);
  terminalTimers.clear();
  const ids = new Set<string>();
  const snapshot = store.snapshot();
  if (snapshot.sessionId) ids.add(snapshot.sessionId);
  // The store keeps no list of its entries; clearing the shown one until none is left clears them all.
  while (store.snapshot().sessionId) {
    const id = store.snapshot().sessionId;
    if (!id || !store.clear(id)) break;
    ids.add(id);
  }
  if (ids.size > 0) bridge.publishWorkStatus();
}

/** The status the companion shows, and the counter of its last change (a change replays the animation). */
export interface CompanionWorkStatus {
  workStatus: WorkStatusSnapshot;
  tick: number;
}

function readLeaf(): StateLeaf {
  return companionState.read().sections.workStatus;
}

function subscribe(listener: () => void): () => void {
  let cancel = () => {};
  const arm = () => {
    cancel = companionState.wait({
      since: companionState.read().counter,
      timeoutMs: 2 ** 31 - 1,
      onWake: () => {
        arm();
        listener();
      },
    });
  };
  arm();
  return () => cancel();
}

export function useCompanionWorkStatus(): CompanionWorkStatus {
  const leaf = useSyncExternalStore(subscribe, readLeaf);
  const data = leaf.data as { state?: HostWorkStatusState | null; task?: string | null } | null;
  return { workStatus: { state: data?.state ?? null, task: data?.task ?? null }, tick: leaf.counter };
}

/** For tests: back to idle with nothing reported. */
export function resetWorkStatus(): void {
  forgetAll();
  bridge.stopWorkStatusFlush();
  seq = 0;
}
