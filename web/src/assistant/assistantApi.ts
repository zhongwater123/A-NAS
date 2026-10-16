// Client for /api/v1/assistant (internal/assistantapi). Conversations live in
// the member's own Hermes instance: turns run over the gateway WebSocket
// (useAssistantGateway), stored conversations are listed and read over REST,
// and confirmation cards are answered here, never by answering Hermes.
import { APIError, request } from "../api";
import type { SessionMessage } from "./hermes/types/hermes";

export interface AssistantStatus {
  available: boolean;
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
  op: "write" | "copy" | "delete" | "move" | "mkdir";
  path: string;
  to?: string;
  /** What a copy copies, its size in bytes, and whether it is a folder. */
  from?: string;
  size?: number;
  folder?: boolean;
  exists?: boolean;
  before?: string;
  after?: string;
}

/** The change as the executor prepared it when the card was made. */
export interface AssistantChange {
  id: string;
  summary: string;
  status: string;
  error?: string;
  operations: ChangeOperation[];
}

export type ApprovalStatus = "awaiting" | "deciding" | "approved" | "answered" | "applied" | "failed" | "rejected" | "expired" | "stopped";

/** What the assistant asks: to change files, to run a command or script in its sandbox, or questions. */
export type ApprovalKind = "change" | "command" | "question";

/** A dangerous command, or a script as a whole, the assistant asks to run in its sandbox. */
export interface CommandRequest {
  text: string;
  description?: string;
  script?: boolean;
  /** It may be allowed for the rest of the conversation. */
  forSession?: boolean;
}

/** One question of the assistant's clarify tool. */
export interface AssistantQuestion {
  id: string;
  text: string;
  choices?: string[];
  multiSelect?: boolean;
}

/** Something the assistant asked the member in a running turn, waiting for or given an answer. */
export interface Approval {
  requestId: string;
  kind: ApprovalKind;
  /** The live conversation (Hermes's runtime id) it was asked in. */
  sessionId: string;
  storedSessionId?: string;
  status: ApprovalStatus;
  createdAt: number;
  change?: AssistantChange;
  command?: CommandRequest;
  questions?: AssistantQuestion[];
  answers?: Record<string, string | null>;
}

const base = "/api/v1/assistant";
const json = (method: string, value: unknown): RequestInit => ({ method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(value) });

export const assistantStatus = () => request<AssistantStatus>(`${base}/status`);
export const listSessions = () => request<{ sessions: AssistantSession[] }>(`${base}/sessions`).then((body) => body.sessions);
export const renameSession = (id: string, title: string) => request<{ id: string; title: string }>(`${base}/sessions/${encodeURIComponent(id)}`, json("PATCH", { title }), true);
export const deleteSession = (id: string) => request<void>(`${base}/sessions/${encodeURIComponent(id)}`, { method: "DELETE" }, true);
export const sessionMessages = (id: string) => request<{ messages: SessionMessage[] }>(`${base}/sessions/${encodeURIComponent(id)}/messages`).then((body) => body.messages);
export const listApprovals = () => request<{ approvals: Approval[] }>(`${base}/approvals`).then((body) => body.approvals);
export const decideApproval = (requestId: string, confirm: boolean, forSession = false) =>
  request<{ approval: Approval }>(`${base}/approvals/${encodeURIComponent(requestId)}/${confirm ? "confirm" : "reject"}`,
    forSession ? json("POST", { forSession: true }) : { method: "POST" }, true).then((body) => body.approval);
export const answerQuestions = (requestId: string, answers: Record<string, string | null>) =>
  request<{ approval: Approval }>(`${base}/approvals/${encodeURIComponent(requestId)}/answer`, json("POST", { answers }), true).then((body) => body.approval);

/** One entry of the assistant's memory: its notes, or what it noted about the member. */
export interface MemoryItem {
  id: string;
  store: "memory" | "profile";
  text: string;
  updatedAt?: number;
}

/** A skill the assistant made or used; A-NAS's own are not listed. */
export interface LearnedSkill {
  id: string;
  category?: string;
  createdBy?: string;
  useCount: number;
  updatedAt?: number;
}

export interface AssistantMemory {
  memories: MemoryItem[];
  skills: LearnedSkill[];
}

export interface MemoryCleared {
  memories: number;
  skills: number;
  conversations: number;
  busy: number;
}

export const getMemory = () => request<AssistantMemory>(`${base}/memory`);
export const memoryItem = (id: string) => request<{ id: string; kind: string; content: string }>(`${base}/memory/items/${encodeURIComponent(id)}`);
export const forgetMemoryItem = (id: string) => request<void>(`${base}/memory/items/${encodeURIComponent(id)}`, { method: "DELETE" }, true);
export const clearMemory = (conversations: boolean) => request<MemoryCleared>(`${base}/memory/clear`, json("POST", { conversations }), true);

/** The gateway WebSocket on this origin; the session cookie authenticates it. */
export function gatewayUrl(): string {
  const { protocol, host } = window.location;
  return `${protocol === "https:" ? "wss:" : "ws:"}//${host}${base}/ws`;
}

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
    case "changes_unavailable": return "暂时无法处理这项更改，请稍后再试";
    case "approval_not_found": return "这项更改已经不存在";
    case "approval_decided": return "这项更改已经处理过了";
    case "approval_expired": return "助手已经不再等这项确认，什么都没有变";
    case "invalid_answer": return "回答没能交给助手，请重试";
    case "authentication_required": return "登录已过期，请重新登录";
    case "memory_changed": return "助手的记忆刚刚有变化，请再试一次";
    case "memory_not_found": return "这一条已经不在了";
  }
  return reason instanceof TypeError ? "网络连接中断" : "操作没有完成，请重试";
}
