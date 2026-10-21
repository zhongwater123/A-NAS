// The connection layer for one conversation, on the gateway (useAssistantGateway)
// as Hermes Desktop drives `hermes serve`: session.create or session.resume give the
// live session, prompt.submit runs a turn whose events build the reply with
// Desktop's own helpers, and session.interrupt stops it. Stored history comes over
// REST and is folded by Desktop's transcript model (toChatMessages). Leaving a
// conversation does not stop its turn: Hermes keeps it running and the product
// service keeps the instance up until it ends.
import { useCallback, useEffect, useRef, useState } from "react";

import { failureText, sessionMessages } from "./assistantApi";
import { toChatMessages } from "./hermes/lib/chat-messages/hydration";
import { appendAssistantTextPart, appendReasoningPart, textPart } from "./hermes/lib/chat-messages/parts";
import { upsertToolPart } from "./hermes/lib/chat-messages/tool-parts";
import type { ChatMessage, ChatMessagePart, GatewayEventPayload } from "./hermes/lib/chat-messages/types";
import type { SessionCreateResult, SessionResumeResult } from "./hermes/shared/gateway-contract.generated";
import type { SessionMessage } from "./hermes/types/hermes";
import type { AssistantGateway } from "./useAssistantGateway";

export interface AssistantThread {
  messages: ChatMessage[];
  loading: boolean;
  running: boolean;
  error: string;
  /** The live session (Hermes's runtime id) of the conversation on screen. */
  liveId?: string;
  send(text: string): Promise<void>;
  cancel(): void;
}

interface Callbacks {
  /** A conversation started here got its stored id. */
  onCreated(storedId: string): void;
  /** A turn ended, or the assistant renamed the conversation. */
  onChanged(): void;
  /**
   * A prompt is about to go in: the window makes itself the session's browser
   * (Hermes's browser control) first, so the turn has the browser tools and
   * its steps come to the window the member sends from.
   */
  beforePrompt?(liveId: string): Promise<void>;
}

const COLS = 100;
// How long a message waits for the instance to start (a cold start takes seconds,
// a first start with Hermes setting itself up longer).
const START_WAIT_MS = 180_000;
const now = () => Date.now() / 1000;
const text = (value: unknown) => (typeof value === "string" ? value : "");

// A stopped turn can end without a closing answer: in tool calls, an empty
// message, or the "Operation interrupted: …" sentinel that Hermes's own TUI treats
// as cancellation metadata rather than prose. Such a turn gets the interrupted
// flag Hermes Desktop's model uses for a stopped reply.
const isStopNotice = (part: ChatMessagePart) => part.type === "text" && part.text.trim().startsWith("Operation interrupted");
const answered = (message: ChatMessage) => {
  const lastTool = message.parts.findLastIndex((part) => part.type === "tool-call");
  return message.parts.slice(lastTool + 1).some((part) => part.type === "text" && part.text.trim() !== "");
};

function fromTranscript(stored: SessionMessage[]): ChatMessage[] {
  const result: ChatMessage[] = [];
  const closeTurn = () => {
    const last = result.at(-1);
    if (!last || last.error) return;
    if (last.role === "user") result.push({ id: `${last.id}-stopped`, role: "assistant", parts: [], interrupted: true, timestamp: last.timestamp });
    else if (last.role === "assistant" && !answered(last)) result[result.length - 1] = { ...last, interrupted: true };
  };
  for (const message of toChatMessages(stored)) {
    if (message.role === "user") closeTurn();
    result.push(message.parts.some(isStopNotice) ? { ...message, parts: message.parts.filter((part) => !isStopNotice(part)) } : message);
  }
  closeTurn();
  return result;
}

export function useAssistantThread(gateway: AssistantGateway, storedId: string | undefined, callbacks: Callbacks): AssistantThread {
  const { client, generation, ready } = gateway;
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  // The conversation whose stored history has arrived; until it matches, the
  // thread is loading.
  const [loadedFor, setLoadedFor] = useState<string>();
  const [running, setRunning] = useState(false);
  const [error, setError] = useState("");
  const [liveId, setLiveId] = useState<string>();
  // The conversation on screen and its live session; events and late replies of
  // any other conversation are ignored.
  const shown = useRef(storedId);
  const live = useRef<string | undefined>(undefined);
  // A conversation this hook started: it already holds its live session and messages.
  const created = useRef<string | undefined>(undefined);
  const resumed = useRef<{ stored?: string; generation: number }>({ generation: -1 });
  const replyId = useRef<string | undefined>(undefined);
  const notify = useRef(callbacks);
  notify.current = callbacks;

  const adopt = (id: string | undefined) => {
    live.current = id;
    setLiveId(id);
  };

  const reload = useCallback(async (id: string) => {
    const stored = await sessionMessages(id);
    if (shown.current === id) setMessages(fromTranscript(stored));
  }, []);

  // The reply being built from the turn's events, added when the first one arrives.
  const updateReply = useCallback((update: (message: ChatMessage) => ChatMessage) => {
    setMessages((list) => {
      const id = replyId.current;
      if (id && list.some((message) => message.id === id)) return list.map((message) => (message.id === id ? update(message) : message));
      const fresh = `live-${Date.now()}`;
      replyId.current = fresh;
      return [...list, update({ id: fresh, role: "assistant", parts: [], pending: true, timestamp: now() })];
    });
  }, []);

  // A conversation opened from the list: its history now, its live session when connected.
  useEffect(() => {
    shown.current = storedId;
    if (storedId && storedId === created.current) {
      created.current = undefined;
      resumed.current = { stored: storedId, generation };
      setLoadedFor(storedId);
      return;
    }
    adopt(undefined);
    replyId.current = undefined;
    setMessages([]);
    setError("");
    setRunning(false);
    resumed.current = { generation: -1 };
    if (!storedId) return;
    let current = true;
    reload(storedId)
      .catch((reason) => current && setError(failureText(reason)))
      .finally(() => current && setLoadedFor(storedId));
    return () => { current = false; };
    // generation only records which connection the new conversation began on;
    // a reconnection resumes it below, without reloading.
  }, [storedId, reload]);

  // Resume the conversation on screen on each (re)connection, which also brings
  // its events to this connection; a turn still running shows as running.
  useEffect(() => {
    if (!ready || !storedId) return;
    if (resumed.current.stored === storedId && resumed.current.generation === generation) return;
    resumed.current = { stored: storedId, generation };
    client.request<SessionResumeResult>("session.resume", { session_id: storedId, cols: COLS, omit_messages: true })
      .then((result) => {
        if (shown.current !== storedId) return;
        adopt(result.session_id);
        if (result.running) {
          setRunning(true);
          updateReply((message) => message);
        }
      })
      .catch(() => {
        // Not resumable now; the history stays readable and sending tries again.
        resumed.current = { generation: -1 };
      });
  }, [client, ready, generation, storedId, updateReply]);

  const finishTurn = useCallback((status: string, failure: boolean) => {
    updateReply((message) => ({ ...message, pending: false, interrupted: status === "interrupted" }));
    replyId.current = undefined;
    setRunning(false);
    if (failure) setError("VNAS 没能完成这次回答，请重试");
    const id = shown.current;
    if (id) void reload(id).catch(() => undefined);
    notify.current.onChanged();
  }, [reload, updateReply]);

  // The turn's events, for the live session on screen only.
  useEffect(() => client.onEvent((event) => {
    if (!event.session_id || event.session_id !== live.current) return;
    const payload = (event.payload ?? {}) as GatewayEventPayload;
    const at = typeof payload.timestamp === "number" ? payload.timestamp : now();
    switch (event.type) {
      case "message.start":
        setRunning(true);
        updateReply((message) => message);
        break;
      case "message.delta":
        updateReply((message) => ({ ...message, parts: appendAssistantTextPart(message.parts, text(payload.text), at) }));
        break;
      case "reasoning.delta":
        updateReply((message) => ({ ...message, parts: appendReasoningPart(message.parts, text(payload.text), at) }));
        break;
      case "tool.start":
      case "tool.complete": {
        const name = text(payload.name);
        // Hermes reports its own bookkeeping as tools named with a leading underscore.
        if (!name || name.startsWith("_")) break;
        updateReply((message) => ({ ...message, parts: upsertToolPart(message.parts, payload, event.type === "tool.start" ? "running" : "complete", at) }));
        break;
      }
      case "message.complete":
        // Hermes's error text is provider detail in English; it stays in the instance's log.
        finishTurn(text(payload.status), payload.status === "error");
        break;
      case "session.title":
        notify.current.onChanged();
        break;
    }
  }), [client, finishTurn, updateReply]);

  const connected = useRef(ready);
  connected.current = ready;

  const send = useCallback(async (input: string) => {
    if (!input.trim()) return;
    setError("");
    setMessages((list) => [...list, { id: `local-${Date.now()}`, role: "user", parts: [textPart(input)], timestamp: now() }]);
    replyId.current = undefined;
    updateReply((message) => message);
    setRunning(true);
    try {
      // A message sent while the instance still starts waits for it.
      if (!connected.current) {
        await new Promise<void>((resolve) => {
          const off = client.on("gateway.ready", () => { off(); window.clearTimeout(timer); resolve(); });
          const timer = window.setTimeout(() => { off(); resolve(); }, START_WAIT_MS);
        });
      }
      let id = live.current;
      if (!id && shown.current) {
        id = (await client.request<SessionResumeResult>("session.resume", { session_id: shown.current, cols: COLS, omit_messages: true })).session_id;
        adopt(id);
      }
      if (!id) {
        const session = await client.request<SessionCreateResult>("session.create", { cols: COLS });
        id = session.session_id;
        adopt(id);
        created.current = session.stored_session_id;
        shown.current = session.stored_session_id;
        notify.current.onCreated(session.stored_session_id);
      }
      await notify.current.beforePrompt?.(id).catch(() => undefined);
      await client.request("prompt.submit", { session_id: id, text: input });
    } catch {
      setError("VNAS 暂时没有响应，请稍后再试");
      updateReply((message) => ({ ...message, pending: false }));
      replyId.current = undefined;
      setRunning(false);
    }
  }, [client, updateReply]);

  const cancel = useCallback(() => {
    if (live.current) void client.request("session.interrupt", { session_id: live.current }).catch(() => undefined);
  }, [client]);

  const loading = Boolean(storedId) && loadedFor !== storedId;
  return { messages, loading, running, error, liveId, send, cancel };
}
