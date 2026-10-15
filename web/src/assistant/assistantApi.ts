// Client for /api/v1/assistant (internal/assistantapi). Conversations live in
// the member's own Hermes instance; messages come back in Hermes's message form
// and are turned into chat messages by the vendored Hermes Desktop model.
import { APIError, csrfHeaders, request } from "../api";
import type { SessionMessage } from "./hermes/types/hermes";

export interface AssistantStatus {
  available: boolean;
  changes: boolean;
}

export interface AssistantSession {
  id: string;
  title: string;
  preview: string;
  startedAt: number;
  lastActive?: number;
  messageCount: number;
}

export interface ChangeOperation {
  op: "write" | "delete" | "move" | "mkdir";
  path: string;
  to?: string;
  exists?: boolean;
  before?: string;
  after?: string;
  problem?: string;
}

export interface PendingChange {
  id: number;
  /** The conversation the assistant submitted it in, when known. */
  sessionId?: string;
  summary: string;
  status: "pending" | "applied" | "rejected" | "failed";
  decidedAt?: number;
  error?: string;
  operations: ChangeOperation[];
}

const base = "/api/v1/assistant";
const json = (method: string, value: unknown): RequestInit => ({ method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(value) });

export const assistantStatus = () => request<AssistantStatus>(`${base}/status`);
export const listSessions = () => request<{ sessions: AssistantSession[] }>(`${base}/sessions`).then((body) => body.sessions);
export const createSession = (title = "") => request<{ session: AssistantSession }>(`${base}/sessions`, json("POST", { title }), true).then((body) => body.session);
export const renameSession = (id: string, title: string) => request<{ session: AssistantSession }>(`${base}/sessions/${encodeURIComponent(id)}`, json("PATCH", { title }), true).then((body) => body.session);
export const deleteSession = (id: string) => request<void>(`${base}/sessions/${encodeURIComponent(id)}`, { method: "DELETE" }, true);
export const sessionMessages = (id: string) => request<{ messages: SessionMessage[] }>(`${base}/sessions/${encodeURIComponent(id)}/messages`).then((body) => body.messages);
export const listChanges = () => request<{ changes: PendingChange[] }>(`${base}/changes`).then((body) => body.changes);
export const decideChange = (id: number, confirm: boolean) => request<PendingChange>(`${base}/changes/${id}/${confirm ? "confirm" : "reject"}`, { method: "POST" }, true);

// failureText puts an assistant API failure in the member's words; the
// server's messages are for logs.
export function failureText(reason: unknown): string {
  switch (reason instanceof APIError ? reason.code : "") {
    case "assistant_unavailable": return "助手暂时没有响应，请稍后再试";
    case "assistant_busy": return "助手正忙，请稍后再试";
    case "assistant_failed": return "助手没能完成这次操作，请重试";
    case "session_not_found": return "这个对话已经不存在";
    case "title_in_use": return "已经有对话用了这个名字";
    case "invalid_title": return "名字最多 100 个字";
    case "changes_unavailable": return "暂时无法处理待确认的更改，请稍后再试";
    case "change_not_found": return "这项更改已经不存在";
    case "change_decided": return "这项更改已经处理过了";
  }
  return reason instanceof TypeError ? "网络连接中断" : "操作没有完成，请重试";
}

export interface StreamEvent {
  event: string;
  data: Record<string, unknown>;
}

// streamChat runs one turn and yields Hermes's server-sent events as they arrive.
export async function* streamChat(sessionId: string, input: string, signal: AbortSignal): AsyncGenerator<StreamEvent> {
  const response = await fetch(`${base}/sessions/${encodeURIComponent(sessionId)}/chat`, {
    method: "POST",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json", Accept: "text/event-stream", ...csrfHeaders() },
    body: JSON.stringify({ input }),
    signal,
  });
  if (!response.ok || !response.body) {
    const body = await response.json().catch(() => undefined);
    throw new APIError(response.status, body?.error?.code ?? "request_failed", body?.error?.message ?? `请求失败 (${response.status})`);
  }
  const reader = response.body.pipeThrough(new TextDecoderStream()).getReader();
  let buffer = "";
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buffer += value;
    let boundary;
    while ((boundary = buffer.indexOf("\n\n")) >= 0) {
      const block = buffer.slice(0, boundary);
      buffer = buffer.slice(boundary + 2);
      let event = "message";
      const data: string[] = [];
      for (const line of block.split("\n")) {
        if (line.startsWith(":")) continue;
        if (line.startsWith("event:")) event = line.slice(6).trim();
        else if (line.startsWith("data:")) data.push(line.slice(5).trimStart());
      }
      if (!data.length) continue;
      try {
        yield { event, data: JSON.parse(data.join("\n")) };
      } catch {
        // A keepalive or a malformed frame carries nothing to show.
      }
    }
  }
}
