// VNAS: the member's stored conversations and their transcripts, the
// confirmation cards, the scheduled tasks with their runs, and the task notices
// among the account's notifications. Turns run over the gateway WebSocket,
// which the tests replace with a stand-in client.
import type {
  AssistantApproval,
  AssistantMessage,
  AssistantSession,
  AssistantTask,
  AssistantTaskRun,
  Notification,
} from "../../api.generated";
import { apiError, http } from "../server";

// assistantSession is a stored conversation with nothing in it unless given.
export function assistantSession(id: string, overrides: Partial<AssistantSession> = {}): AssistantSession {
  return { id, title: "", preview: "", startedAt: 1_760_000_000, messageCount: 0, ...overrides };
}

export interface AssistantOptions {
  sessions?: AssistantSession[];
  /** Every conversation's transcript. */
  messages?: AssistantMessage[];
  approvals?: AssistantApproval[];
}

// assistantHandlers answers the VNAS window's reads: the conversations, their
// transcript and the cards.
export function assistantHandlers({ sessions = [], messages = [], approvals = [] }: AssistantOptions = {}) {
  return [
    http.get("/api/v1/assistant/sessions", ({ response }) => response(200).json({ sessions })),
    http.get("/api/v1/assistant/sessions/{sessionId}/messages", ({ response }) => response(200).json({ messages })),
    http.get("/api/v1/assistant/approvals", ({ response }) => response(200).json({ approvals })),
  ];
}

// assistantTask is a task that runs every morning at 8 and has not run yet.
export function assistantTask(id: string, overrides: Partial<AssistantTask> = {}): AssistantTask {
  return {
    id,
    name: id,
    prompt: "",
    schedule: { kind: "cron", expr: "0 8 * * *", display: "0 8 * * *" },
    state: "scheduled",
    completed: 0,
    ...overrides,
  };
}

// tasksHandlers answers the scheduled tasks, and the runs of the tasks that
// runs names; any other task has none to read.
export function tasksHandlers(tasks: AssistantTask[], runs: Record<string, AssistantTaskRun[]> = {}) {
  return [
    http.get("/api/v1/assistant/tasks", ({ response }) => response(200).json({ tasks })),
    http.get("/api/v1/assistant/tasks/{taskId}/runs", ({ params, response }) => {
      const list = runs[params.taskId];
      return list ? response(200).json({ runs: list }) : response(404).json(apiError("task_not_found"));
    }),
  ];
}

// taskNotice tells that a run of a scheduled task finished.
export function taskNotice(id: string, task: NonNullable<Notification["task"]>): Notification {
  return { id, kind: "assistant_task", actorUsername: "", createdAt: new Date().toISOString(), task };
}

// noticesHandlers answers the account's notifications with items and records
// the ones acknowledged.
export function noticesHandlers(items: Notification[] = []) {
  const acknowledged: string[] = [];
  const handlers = [
    http.get("/api/v1/notifications", ({ response }) => response(200).json({ items })),
    http.post("/api/v1/notifications/{notificationId}/acknowledge", ({ params, response }) => {
      acknowledged.push(params.notificationId);
      return response(204).empty();
    }),
  ];
  return { acknowledged, handlers };
}
