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
//
// Each step also asks for its own clip and bubble line (behaviors.ts), which go into the
// store with the stage (patch 0028): what the step works on, cut short, only from these
// turns and never on the local console's shared screen. Never the reasoning's text, which
// comes in small, often English pieces.
//
// The store lives here, outside the companion's own code, which loads later and only
// when animations are installed: the VNAS window writes to it whether or not the card
// is there, and companionReady() tells it whether one is (petCard.tsx reads the store).
import { isLocalConsole } from "../desktop/LocalConsoleScreenSaver";
import { BEHAVIORS, type Behavior, behaviorLine, shortDetail, toolStep } from "./behaviors";
import { createWorkStatusBridge } from "./dsh-pet/host/plugin/work-status-bridge";
import { PollStateStore } from "./dsh-pet/host/state";
import { currentTaskFromTodo, type HostWorkStatusState, WorkStatusStore } from "./dsh-pet/host/work-status";

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

/**
 * What a stage shows besides its own clip and lines: the clip the step asks for and a line for its bubble.
 * key tells one step from the next: the events of one step keep its line, the next step picks again.
 */
export interface Step {
  key: string;
  anim: string | null;
  line: () => string | null;
}

/** The line each entry shows, with the stage and step it was picked for. */
const lines = new Map<string, { key: string; task: string | null }>();
/** The step each conversation's todo list is at, which its thinking names. */
const todos = new Map<string, string>();
let stoppedListener: (() => void) | null = null;

function behaviorStep(behavior: Behavior, key: string, detail = "", shared = isLocalConsole()): Step {
  return { key, anim: BEHAVIORS[behavior].clip, line: () => behaviorLine(behavior, detail, shared) };
}

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
        lines.delete(id);
        store.clear(id);
        bridge.publishWorkStatus();
      }
    }, TERMINAL_KEEP_MS),
  );
}

/**
 * The entry's stage and what its step shows, published at once. The line and clip are written every time,
 * null when the step has none, so that no step shows the one before's.
 */
function set(id: string, state: HostWorkStatusState, step: Step | null) {
  seq += 1;
  const changed = store.setState(id, state, seq);
  const key = `${state}\u0000${step?.key ?? ""}`;
  const last = lines.get(id);
  const task = last?.key === key ? last.task : (step?.line() ?? null);
  lines.set(id, { key, task });
  store.setDetail(id, { task, anim: step?.anim ?? null });
  bridge.publishWorkStatus();
  if (!changed) return;
  if (state === "success" || state === "error") scheduleTerminalCleanup(id);
  else cancelTerminalCleanup(id);
}

function clear(id: string) {
  cancelTerminalCleanup(id);
  lines.delete(id);
  if (store.clear(id)) bridge.publishWorkStatus();
}

/**
 * What the companion reads of a gateway event's payload (gateway-contract.generated.ts): a tool's name, id
 * and arguments, a turn's status, a todo list. The rest of a payload (result, result_text, summary) is not
 * shown.
 */
export interface GatewayPayload {
  name?: unknown;
  tool_id?: unknown;
  args?: unknown;
  status?: unknown;
  todos?: unknown;
  result?: unknown;
  result_text?: unknown;
  summary?: unknown;
}

/**
 * The stage a gateway event of a turn leads to and what its step shows: undefined leaves the stage as it
 * is, a null stage ends the turn without a result (it was stopped). A tool's failure is not in the
 * gateway's contract (tool.complete carries no error), so a step that failed shows as one done.
 */
export function stepOf(
  type: string,
  payload?: GatewayPayload,
  todo?: string,
  shared = isLocalConsole(),
): { stage: HostWorkStatusState | null; step: Step | null } | undefined {
  switch (type) {
    case "message.start":
    case "reasoning.delta":
      // Thinking names the step the todo list is at, when VNAS keeps one.
      return {
        stage: "thinking",
        step:
          todo && !shared
            ? { key: `todo:${todo}`, anim: BEHAVIORS.thinking.clip, line: () => `下一步：${shortDetail(todo)}` }
            : behaviorStep("thinking", "thinking", "", shared),
      };
    case "tool.start":
    case "tool.complete": {
      const name = payload?.name;
      // Hermes reports its own bookkeeping as tools named with a leading underscore.
      if (typeof name !== "string" || !name || name.startsWith("_")) return undefined;
      const stage = type === "tool.start" ? "working" : "result";
      // VNAS moving the pet: the stage's own clip and lines, so that what it asked for shows.
      if (name.startsWith("mcp__companion__")) return { stage, step: null };
      const call = `${name}\u0000${typeof payload?.tool_id === "string" ? payload.tool_id : ""}`;
      if (type === "tool.complete") return { stage, step: behaviorStep("step-done", call, "", shared) };
      const { behavior, detail } = toolStep(name, payload?.args);
      // clarify waits for the member, as a card does.
      return { stage: behavior === "waiting" ? "waiting" : stage, step: behaviorStep(behavior, call, detail, shared) };
    }
    case "message.complete":
      if (payload?.status === "interrupted") return { stage: null, step: null };
      return payload?.status === "error"
        ? { stage: "error", step: behaviorStep("error", "error", "", shared) }
        : { stage: "success", step: behaviorStep("success", "success", "", shared) };
    default:
      return undefined;
  }
}

/** The stage a gateway event of a turn leads to (stepOf without the step). */
export function stageOf(type: string, payload?: GatewayPayload): HostWorkStatusState | null | undefined {
  return stepOf(type, payload)?.stage;
}

/** This window started a turn of the conversation (it registered as its browser and prompts). */
export function startTurn(sessionId: string): void {
  prompting.add(sessionId);
}

/**
 * The member stopped the conversation's turn from this window. The turn counts as stopped from the
 * moment the window asks Hermes to stop it, as the product service counts it (contract v1.3): the
 * companion leaves its stage now instead of when Hermes answers, and a command Hermes still sends for
 * that turn is not this window's (not-controlling) and does not move the companion.
 */
export function stopTurn(sessionId: string): void {
  if (!prompting.delete(sessionId)) return;
  todos.delete(sessionId);
  clear(sessionId);
  stoppedListener?.();
}

/**
 * The prompt of a turn this window started did not go in (prompt.submit failed): the turn is not this
 * window's, and the next one in the conversation, perhaps from another device, must not move this
 * device's companion. Unlike a stop, nothing happened that the companion answers.
 */
export function withdrawTurn(sessionId: string): void {
  if (!prompting.delete(sessionId)) return;
  todos.delete(sessionId);
  clear(sessionId);
}

/**
 * Calls the listener when the member stops a turn here (stopTurn), for the companion to answer it; the
 * stage is already gone. Returns the call that removes it.
 */
export function onTurnStopped(listener: () => void): () => void {
  stoppedListener = listener;
  return () => {
    if (stoppedListener === listener) stoppedListener = null;
  };
}

/**
 * Whether the conversation's running turn is one this window started: only such a turn's commands
 * move this device's companion (commands.ts answers any other not-controlling).
 */
export function isPrompting(sessionId: string): boolean {
  return prompting.has(sessionId);
}

/** A gateway event of a conversation: counts only for a turn this window started. */
export function reportEvent(sessionId: string, type: string, payload?: GatewayPayload): void {
  if (!prompting.has(sessionId)) return;
  if (type === "todo.updated") {
    const current = Array.isArray(payload?.todos) ? currentTaskFromTodo({ data: { todos: payload.todos } }) : null;
    if (current) todos.set(sessionId, current);
    else todos.delete(sessionId);
    // A thinking turn names the new step now.
    if (store.stateOf(sessionId) !== "thinking") return;
  }
  const next = stepOf(type === "todo.updated" ? "message.start" : type, payload, todos.get(sessionId));
  if (type === "message.complete") {
    prompting.delete(sessionId);
    todos.delete(sessionId);
  }
  if (next === undefined) return;
  if (next.stage === null) clear(sessionId);
  else set(sessionId, next.stage, next.step);
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
      set(id, "waiting", behaviorStep("waiting", "card"));
    }
}

/**
 * The window lost its connection or closed: it can no longer see those turns or
 * cards, so the companion goes back to idle instead of keeping their last stage.
 */
export function forgetAll(): void {
  prompting.clear();
  cards.clear();
  lines.clear();
  todos.clear();
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

/** Calls the listener after every write to companionState, as dsh-pet's long poll of GET /state wakes. */
export function subscribeCompanionState(listener: () => void): () => void {
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

/** The pet companionState speaks of: dsh-pet's id for its main pet, whose animations 大肥鱼 plays. */
export const COMPANION_PET = "main";
/** Its width on the page, in CSS pixels; its clips are 16:9. */
export const COMPANION_SIZE = 240;

let cardReady = false;

/**
 * Whether 大肥鱼's card is on this page and follows companionState. Until it is (no animations
 * installed, its code not loaded yet, the desktop gone), a command written there would wait for a
 * card that never comes, so VNAS answers that there is no companion instead.
 */
export function companionReady(): boolean {
  return cardReady;
}

/** The card is on the page and reads companionState from now on, or has gone. */
export function setCompanionReady(ready: boolean): void {
  cardReady = ready;
}

let cardPaused = false;

/**
 * Whether the card is paused in a way a command does not end, so that it would drop one: the page is
 * hidden. Its pause after a minute without input does not count, as a command ends that one (petCard.tsx).
 */
export function companionPaused(): boolean {
  return cardPaused;
}

/** The card's pause a command does not end has begun or ended. */
export function setCompanionPaused(paused: boolean): void {
  cardPaused = paused;
}

/** For tests: back to idle with nothing reported. */
export function resetWorkStatus(): void {
  forgetAll();
  bridge.stopWorkStatusFlush();
  seq = 0;
}
