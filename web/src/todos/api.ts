// 待办's calls to /api/v1/todos. Every account reads and edits the shared
// to-dos; the server keeps each account's private ones, when there are any,
// to that account. Errors are worded by api/errorMessages.ts, domain "todos".
import { call, client } from "../api/client";
import type {
  Todo,
  TodoCategory,
  TodoEdit,
  TodoFields,
  TodoOwner,
  TodoReminder,
  TodoScope,
  TodoStage,
  TodoStatus,
} from "../api.generated";

export type { Todo, TodoCategory, TodoEdit, TodoFields, TodoOwner, TodoReminder, TodoScope, TodoStage, TodoStatus };

const todo = (todoId: string) => ({ params: { path: { todoId } } });

export const listTodos = async (signal?: AbortSignal) => (await call(client.GET("/api/v1/todos", { signal }))).items;
export const addTodo = (fields: TodoFields) => call(client.POST("/api/v1/todos", { body: fields }));
export const updateTodo = (todoId: string, edit: TodoEdit) =>
  call(client.PATCH("/api/v1/todos/{todoId}", { ...todo(todoId), body: edit }));
export const setTodoStatus = (todoId: string, status: TodoStatus) =>
  call(client.PUT("/api/v1/todos/{todoId}/status", { ...todo(todoId), body: { status } }));
export const snoozeTodo = (todoId: string, days: number) =>
  call(client.PUT("/api/v1/todos/{todoId}/snooze", { ...todo(todoId), body: { days } }));
export const listTodoOwners = async (signal?: AbortSignal) =>
  (await call(client.GET("/api/v1/todos/owners", { signal }))).items;
export const listTodoReminders = async (signal?: AbortSignal) =>
  (await call(client.GET("/api/v1/todos/reminders", { signal }))).items;
export const acknowledgeTodoReminders = (keys: string[]) =>
  call(client.POST("/api/v1/todos/reminders/seen", { body: { keys } }));

export { requestKey } from "../api/requestKey";

// The longest title, which a shared screen can show whole.
export const TITLE_MAX = 10;

export const CATEGORIES: TodoCategory[] = ["bill", "medical", "insurance", "id_document", "contract", "other"];

export const CATEGORY_LABEL: Record<TodoCategory, string> = {
  bill: "账单",
  medical: "医疗",
  insurance: "保险",
  id_document: "证件",
  contract: "合同",
  other: "其他",
};

// What a to-do of each kind asks someone to do.
export const CATEGORY_ACTION: Record<TodoCategory, string> = {
  bill: "缴费",
  medical: "复查",
  insurance: "续保",
  id_document: "换证",
  contract: "续约",
  other: "办理",
};
