// Client for /api/v1/assistant (internal/assistantapi). Conversations live in
// the member's own Hermes instance: turns run over the gateway WebSocket
// (useAssistantGateway), stored conversations are listed and read over REST,
// and confirmation cards are answered here, never by answering Hermes.
import { APIError, call, client } from "../api/client";
import { errorText } from "../api/errorMessages";
import type { AssistantApproval, AssistantMemory } from "../api.generated";
import type { SessionMessage } from "./hermes/types/hermes";

export type {
  AssistantChange,
  AssistantMemory,
  AssistantSession,
  AssistantStatus,
  AssistantTask,
  AssistantTaskRun,
} from "../api.generated";
/** Something the assistant asked the member in a running turn, waiting for or given an answer. */
export type Approval = AssistantApproval;
export type ApprovalStatus = Approval["status"];
/** What the assistant asks: to change files, to run a command or script in its sandbox, or questions. */
export type ApprovalKind = Approval["kind"];
export type ChangeOperation = NonNullable<Approval["change"]>["operations"][number];
/** A dangerous command, or a script as a whole, the assistant asks to run in its sandbox. */
export type CommandRequest = NonNullable<Approval["command"]>;
/** One question of the assistant's clarify tool. */
export type AssistantQuestion = NonNullable<Approval["questions"]>[number];
/** One entry of the assistant's memory: its notes, or what it noted about the member. */
export type MemoryItem = AssistantMemory["memories"][number];
/** A skill the assistant made or used; A-NAS's own are not listed. */
export type LearnedSkill = AssistantMemory["skills"][number];
export type MemoryCleared = Awaited<ReturnType<typeof clearMemory>>;

const session = (sessionId: string) => ({ params: { path: { sessionId } } });
const request = (requestId: string) => ({ params: { path: { requestId } } });
const task = (taskId: string) => ({ params: { path: { taskId } } });

export const assistantStatus = () => call(client.GET("/api/v1/assistant/status"));
export const listSessions = async () => (await call(client.GET("/api/v1/assistant/sessions"))).sessions;
export const renameSession = (sessionId: string, title: string) =>
  call(client.PATCH("/api/v1/assistant/sessions/{sessionId}", { ...session(sessionId), body: { title } }));
export const deleteSession = (sessionId: string) =>
  call(client.DELETE("/api/v1/assistant/sessions/{sessionId}", session(sessionId)));
// The contract passes Hermes's stored messages through; their shape is Hermes's.
export const sessionMessages = async (sessionId: string) =>
  (await call(client.GET("/api/v1/assistant/sessions/{sessionId}/messages", session(sessionId))))
    .messages as unknown as SessionMessage[];
export const listApprovals = async () => (await call(client.GET("/api/v1/assistant/approvals"))).approvals;
export const decideApproval = async (requestId: string, confirm: boolean, forSession = false) =>
  (
    await call(
      confirm
        ? client.POST("/api/v1/assistant/approvals/{requestId}/confirm", {
            ...request(requestId),
            body: forSession ? { forSession: true } : undefined,
          })
        : client.POST("/api/v1/assistant/approvals/{requestId}/reject", request(requestId)),
    )
  ).approval;
export const answerQuestions = async (requestId: string, answers: Record<string, string | null>) =>
  (
    await call(
      client.POST("/api/v1/assistant/approvals/{requestId}/answer", { ...request(requestId), body: { answers } }),
    )
  ).approval;

export const getMemory = () => call(client.GET("/api/v1/assistant/memory"));
export const memoryItem = (memoryId: string) =>
  call(client.GET("/api/v1/assistant/memory/items/{memoryId}", { params: { path: { memoryId } } }));
export const forgetMemoryItem = (memoryId: string) =>
  call(client.DELETE("/api/v1/assistant/memory/items/{memoryId}", { params: { path: { memoryId } } }));
export const clearMemory = (conversations: boolean) =>
  call(client.POST("/api/v1/assistant/memory/clear", { body: { conversations } }));

export const listTasks = async () => (await call(client.GET("/api/v1/assistant/tasks"))).tasks;
export const taskRuns = async (taskId: string) =>
  (await call(client.GET("/api/v1/assistant/tasks/{taskId}/runs", task(taskId)))).runs;
export const pauseTask = async (taskId: string) =>
  (await call(client.POST("/api/v1/assistant/tasks/{taskId}/pause", task(taskId)))).task;
export const resumeTask = async (taskId: string) =>
  (await call(client.POST("/api/v1/assistant/tasks/{taskId}/resume", task(taskId)))).task;
/** Runs a task now; finished is false when it goes on after the answer. */
export const runTask = (taskId: string) => call(client.POST("/api/v1/assistant/tasks/{taskId}/run", task(taskId)));
export const deleteTask = (taskId: string) => call(client.DELETE("/api/v1/assistant/tasks/{taskId}", task(taskId)));

/** The gateway WebSocket on this origin; the session cookie authenticates it. */
export function gatewayUrl(): string {
  const { protocol, host } = window.location;
  return `${protocol === "https:" ? "wss:" : "ws:"}//${host}/api/v1/assistant/ws`;
}

// failureText puts an assistant API failure in the member's words; the
// server's messages are for logs.
export function failureText(reason: unknown): string {
  switch (reason instanceof APIError ? reason.code : "") {
    case "assistant_unavailable":
      return "VNAS 暂时没有响应，请稍后再试";
    case "assistant_busy":
      return "VNAS 正忙，请稍后再试";
    case "assistant_failed":
      return "VNAS 没能完成这次操作，请重试";
    case "session_not_found":
      return "这个对话已经不存在";
    case "title_in_use":
      return "已经有对话用了这个名字";
    case "invalid_title":
      return "名字最多 100 个字";
    case "changes_unavailable":
      return "暂时无法处理这项更改，请稍后再试";
    case "approval_not_found":
      return "这项更改已经不存在";
    case "approval_decided":
      return "这项更改已经处理过了";
    case "approval_expired":
      return "VNAS 已经不再等这项确认，什么都没有变";
    case "invalid_answer":
      return "回答没能交给 VNAS，请重试";
    case "memory_changed":
      return "VNAS 的记忆刚刚有变化，请再试一次";
    case "memory_not_found":
      return "这一条已经不在了";
    case "task_not_found":
      return "这个定时任务已经不在了";
    case "task_running":
      return "这个任务正在运行，等它完成再试";
  }
  return errorText(reason);
}
