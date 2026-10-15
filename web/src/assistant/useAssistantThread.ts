// The connection layer: keeps one conversation as Hermes Desktop chat messages.
// History comes from the member's Hermes session and is folded by Desktop's own
// transcript model (toChatMessages); a running turn is built from the stream
// with the same helpers Desktop uses, then replaced by the stored transcript.
import { useCallback, useEffect, useRef, useState } from "react";

import { failureText, sessionMessages, streamChat } from "./assistantApi";
import { toChatMessages } from "./hermes/lib/chat-messages/hydration";
import { appendAssistantTextPart, textPart } from "./hermes/lib/chat-messages/parts";
import { upsertToolPart } from "./hermes/lib/chat-messages/tool-parts";
import type { ChatMessage, ChatMessagePart, GatewayEventPayload } from "./hermes/lib/chat-messages/types";
import type { SessionMessage } from "./hermes/types/hermes";

export interface AssistantThread {
  messages: ChatMessage[];
  loading: boolean;
  running: boolean;
  error: string;
  send(text: string): Promise<void>;
  cancel(): void;
}

const now = () => Date.now() / 1000;
const text = (value: unknown) => (typeof value === "string" ? value : "");

// Hermes's API Server leaves a stopped turn without a closing answer: it ends in
// tool calls, an empty message, or the "Operation interrupted: …" sentinel that
// Hermes's own TUI treats as cancellation metadata rather than prose. Such a turn
// gets the interrupted flag Hermes Desktop's model uses for a stopped reply.
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

export function useAssistantThread(sessionId: string | undefined, onTurnFinished: () => void): AssistantThread {
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  // The conversation whose stored history has arrived; until it matches,
  // the thread is loading and a message waits rather than racing the load.
  const [loadedFor, setLoadedFor] = useState<string>();
  const [running, setRunning] = useState(false);
  const [error, setError] = useState("");
  const abort = useRef<AbortController | undefined>(undefined);
  // The conversation on screen; a turn that was left behind must not load its
  // transcript into the one opened since.
  const shown = useRef(sessionId);
  const finished = useRef(onTurnFinished);
  finished.current = onTurnFinished;

  const reload = useCallback(async (id: string) => {
    const stored = await sessionMessages(id);
    if (shown.current === id) setMessages(fromTranscript(stored));
  }, []);

  useEffect(() => {
    shown.current = sessionId;
    // Leaving a conversation stops its turn: Hermes interrupts a run whose stream closes.
    abort.current?.abort();
    setMessages([]);
    setError("");
    setRunning(false);
    if (!sessionId) return;
    let current = true;
    reload(sessionId)
      .catch((reason) => current && setError(failureText(reason)))
      .finally(() => current && setLoadedFor(sessionId));
    return () => { current = false; };
  }, [sessionId, reload]);
  const loading = Boolean(sessionId) && loadedFor !== sessionId;

  const send = useCallback(async (input: string) => {
    if (!sessionId || !input.trim()) return;
    const controller = new AbortController();
    abort.current = controller;
    const liveId = `live-${Date.now()}`;
    const live = (update: (message: ChatMessage) => ChatMessage) =>
      setMessages((list) => list.map((message) => (message.id === liveId ? update(message) : message)));
    setError("");
    setRunning(true);
    setMessages((list) => [
      ...list,
      { id: `local-${Date.now()}`, role: "user", parts: [textPart(input)], timestamp: now() },
      { id: liveId, role: "assistant", parts: [], pending: true, timestamp: now() },
    ]);
    try {
      for await (const { event, data } of streamChat(sessionId, input, controller.signal)) {
        const at = typeof data.ts === "number" ? data.ts : now();
        switch (event) {
          case "assistant.delta":
            live((message) => ({ ...message, parts: appendAssistantTextPart(message.parts, text(data.delta), at) }));
            break;
          case "tool.started":
          case "tool.completed":
          case "tool.failed": {
            const name = text(data.tool_name);
            // Hermes reports its own bookkeeping (a thinking preview) as tools named with a leading underscore.
            if (!name || name.startsWith("_")) break;
            const payload: GatewayEventPayload = { name, preview: text(data.preview) || undefined, args: data.args ?? undefined };
            if (event === "tool.failed") payload.error = text(data.error) || true;
            live((message) => ({ ...message, parts: upsertToolPart(message.parts, payload, event === "tool.started" ? "running" : "complete", at) }));
            break;
          }
          case "run.failed":
            // Hermes's error is provider detail in English; it stays in the instance's log.
            setError("助手没能完成这次回答，请重试");
            break;
        }
      }
      await reload(sessionId);
    } catch (reason) {
      const stopped = controller.signal.aborted;
      if (!stopped) setError(failureText(reason));
      live((message) => ({ ...message, pending: false, interrupted: stopped }));
      await reload(sessionId).catch(() => undefined);
      // Hermes finishes the tool calls already under way before a stop lands
      // and stores them afterwards; look again once it has.
      if (stopped) window.setTimeout(() => { if (!abort.current) void reload(sessionId).catch(() => undefined); }, 4000);
    } finally {
      if (abort.current === controller) {
        abort.current = undefined;
        setRunning(false);
      }
      finished.current();
    }
  }, [sessionId, reload]);

  const cancel = useCallback(() => abort.current?.abort(), []);

  return { messages, loading, running, error, send, cancel };
}
