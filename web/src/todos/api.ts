// 待办's calls to /api/v1/todos. Every account reads and edits the shared
// to-dos; the server keeps each account's private ones, when there are any,
// to that account.
import { APIError, call, client } from "../api/client";
import { errorText } from "../api/errorMessages";
import type {
  Todo,
  TodoCategory,
  TodoEdit,
  TodoFields,
  TodoReminder,
  TodoScope,
  TodoStage,
  TodoStatus,
} from "../api.generated";

export type { Todo, TodoCategory, TodoEdit, TodoFields, TodoReminder, TodoScope, TodoStage, TodoStatus };

const todo = (todoId: string) => ({ params: { path: { todoId } } });

export const listTodos = async () => (await call(client.GET("/api/v1/todos"))).items;
export const addTodo = (fields: TodoFields) => call(client.POST("/api/v1/todos", { body: fields }));
export const updateTodo = (todoId: string, edit: TodoEdit) =>
  call(client.PATCH("/api/v1/todos/{todoId}", { ...todo(todoId), body: edit }));
export const setTodoStatus = (todoId: string, status: TodoStatus) =>
  call(client.PUT("/api/v1/todos/{todoId}/status", { ...todo(todoId), body: { status } }));
export const snoozeTodo = (todoId: string, days: number) =>
  call(client.PUT("/api/v1/todos/{todoId}/snooze", { ...todo(todoId), body: { days } }));
export const listTodoReminders = async () => (await call(client.GET("/api/v1/todos/reminders"))).items;
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

// 待办's own error codes, in its words. The family service serves the to-dos,
// so its codes name the family; here they name the to-dos.
const todoMessages: Record<string, string> = {
  family_unavailable: "待办暂时不可用（数据盘未就绪），请稍后再试",
  validation_failed: "填写的内容不符合要求，请检查标题、日期、金额和“谁的”",
  changed: "别人刚改过这件事，已刷新，请再改一次",
  conflict: "这件事已经处理或忽略了，不能再推迟",
  not_found: "这件事已不存在",
};

// todoError explains a failed request in 待办's words.
export function todoError(error: unknown): string {
  if (error instanceof APIError && Object.hasOwn(todoMessages, error.code)) return todoMessages[error.code];
  return errorText(error);
}
