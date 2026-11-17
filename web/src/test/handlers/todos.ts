// 待办: the to-dos and reminders one account sees, and what the window and
// the desktop's reminder send back.
import type { Todo, TodoReminder } from "../../api.generated";
import { isoDate } from "../../lib/time";
import { http } from "../server";

export function todo(overrides: Partial<Todo> = {}): Todo {
  return {
    id: "todo_1",
    title: "交物业费",
    category: "bill",
    scope: "shared",
    status: "open",
    source: "manual",
    remindRound: 0,
    version: 1,
    createdAt: "2026-11-10T08:00:00Z",
    createdById: "user:mom",
    createdByName: "mom",
    ...overrides,
  };
}

export function reminder(overrides: Partial<TodoReminder> = {}): TodoReminder {
  const todoId = overrides.todoId ?? "todo_1";
  const stage = overrides.stage ?? "soon";
  return {
    key: `todo:${todoId}:0:${stage}`,
    todoId,
    stage,
    title: "交物业费",
    category: "bill",
    due: "2026-11-20",
    scope: "shared",
    ...overrides,
  };
}

export interface TodosOptions {
  todos?: Todo[];
  reminders?: TodoReminder[];
}

// todosHandlers answers the reads and records the writes: the bodies sent,
// by operation, in order.
export function todosHandlers(options: TodosOptions = {}) {
  const sent = {
    added: [] as unknown[],
    updated: [] as Array<{ todoId: string; body: unknown }>,
    status: [] as Array<{ todoId: string; status: string }>,
    snoozed: [] as Array<{ todoId: string; days: number }>,
    acknowledged: [] as string[][],
  };
  const items = () => options.todos ?? [todo()];
  const find = (todoId: string) => items().find((item) => item.id === todoId) ?? todo({ id: todoId });
  const handlers = [
    http.get("/api/v1/todos", ({ response }) => response(200).json({ items: items() })),
    http.get("/api/v1/todos/reminders", ({ response }) => response(200).json({ items: options.reminders ?? [] })),
    http.post("/api/v1/todos/reminders/seen", async ({ request, response }) => {
      sent.acknowledged.push((await request.json()).keys);
      return response(204).empty();
    }),
    http.post("/api/v1/todos", async ({ request, response }) => {
      const body = await request.json();
      sent.added.push(body);
      const { requestKey: _key, ...fields } = body;
      return response(201).json(todo({ id: "todo_new", ...fields }));
    }),
    http.patch("/api/v1/todos/{todoId}", async ({ params, request, response }) => {
      const body = await request.json();
      sent.updated.push({ todoId: params.todoId, body });
      const { version, ...fields } = body;
      return response(200).json({ ...find(params.todoId), ...fields, version: version + 1 });
    }),
    http.put("/api/v1/todos/{todoId}/status", async ({ params, request, response }) => {
      const { status } = await request.json();
      sent.status.push({ todoId: params.todoId, status });
      const closed =
        status === "open" ? {} : { closedAt: new Date().toISOString(), closedById: "user:mom", closedByName: "mom" };
      const {
        closedAt: _at,
        closedById: _by,
        closedByName: _name,
        snoozedUntil: _until,
        ...rest
      } = find(params.todoId);
      return response(200).json({ ...rest, status, ...closed });
    }),
    http.put("/api/v1/todos/{todoId}/snooze", async ({ params, request, response }) => {
      const { days } = await request.json();
      sent.snoozed.push({ todoId: params.todoId, days });
      const until = new Date();
      until.setDate(until.getDate() + days);
      return response(200).json({ ...find(params.todoId), snoozedUntil: isoDate(until) });
    }),
  ];
  return { handlers, sent };
}
