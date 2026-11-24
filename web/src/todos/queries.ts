// 待办's data through TanStack Query (#271). The 待办 window and the desktop's
// notice share this one cache, so 知道了 in one takes the reminder away in
// both. Every key carries the account: a page signed in as someone else
// never shows what the last account read, and what another account read is
// dropped as soon as this one reads.
import { QueryClient, useMutation, useQuery } from "@tanstack/react-query";
import { useEffect } from "react";

import { APIError } from "../api/client";
import { untilNextDay } from "../lib/time";
import {
  acknowledgeTodoReminders,
  listTodoOwners,
  listTodoReminders,
  listTodos,
  type Todo,
  type TodoOwner,
  type TodoReminder,
} from "./api";

// A request the server refused (4xx) is not tried again; one it could not
// answer is, once.
const retry = (failures: number, error: unknown) => failures < 1 && !(error instanceof APIError && error.status < 500);

// Reads happen when a window opens, when the page comes back into view
// (refetchOnWindowFocus), just after local midnight, when what is due moves a
// day closer (dueRefetch), and after a change (changedTodo); nothing polls.
export const todoClient = new QueryClient({
  defaultOptions: { queries: { retry, refetchOnReconnect: false } },
});
// Without a QueryClientProvider nothing mounts the client, and an unmounted
// client does not hear the page come back into view. It lives as long as
// the page.
todoClient.mount();

// What is due changes at local midnight: a page that stays open, such as the
// local console, reads again just after it. A hidden page waits until it is
// seen again, which reads anyway.
const dueRefetch = { refetchInterval: () => untilNextDay(), refetchIntervalInBackground: false } as const;

const keys = {
  account: (account: string) => ["todos", account] as const,
  list: (account: string) => ["todos", account, "list"] as const,
  owners: (account: string) => ["todos", account, "owners"] as const,
  reminders: (account: string) => ["todos", account, "reminders"] as const,
};

const noReminders: TodoReminder[] = [];
const noOwners: TodoOwner[] = [];

// useOnlyAccount drops what another account read, such as the one signed in
// before on the same page.
function useOnlyAccount(account: string) {
  useEffect(() => {
    todoClient.removeQueries({ predicate: (query) => query.queryKey[0] === "todos" && query.queryKey[1] !== account });
  }, [account]);
}

/** The to-dos the account sees, in the server's order: the most urgent first. */
export function useTodos(account: string) {
  useOnlyAccount(account);
  return useQuery(
    { queryKey: keys.list(account), queryFn: ({ signal }) => listTodos(signal), ...dueRefetch },
    todoClient,
  );
}

/** Reads the to-dos now, as when another window asks for one that may be new. */
export function readTodos(account: string): Promise<Todo[]> {
  return todoClient.fetchQuery({ queryKey: keys.list(account), queryFn: ({ signal }) => listTodos(signal) });
}

/** The accounts a to-do can be given to; none while they are read or when they cannot be. */
export function useTodoOwners(account: string): TodoOwner[] {
  const query = useQuery(
    { queryKey: keys.owners(account), queryFn: ({ signal }) => listTodoOwners(signal), staleTime: 60_000 },
    todoClient,
  );
  return query.data ?? noOwners;
}

/** What to remind the account of now. Once the session is gone (401), nothing: not what was read before. */
export function useTodoReminders(account: string): TodoReminder[] {
  useOnlyAccount(account);
  const query = useQuery(
    { queryKey: keys.reminders(account), queryFn: ({ signal }) => listTodoReminders(signal), ...dueRefetch },
    todoClient,
  );
  if (query.error instanceof APIError && query.error.status === 401) return noReminders;
  return query.data ?? noReminders;
}

/** 知道了: takes the reminders away at once, and reads them again once the server answered. */
export function useAcknowledgeReminders(account: string) {
  return useMutation(
    {
      mutationFn: (acknowledged: string[]) => acknowledgeTodoReminders(acknowledged),
      onMutate: async (acknowledged) => {
        const key = keys.reminders(account);
        // A read already on its way may have left before this; it is dropped,
        // or it would bring back what was just acknowledged.
        await todoClient.cancelQueries({ queryKey: key });
        todoClient.setQueryData<TodoReminder[]>(key, (current) =>
          current?.filter((reminder) => !acknowledged.includes(reminder.key)),
        );
      },
      onSettled: () => todoClient.invalidateQueries({ queryKey: keys.reminders(account) }),
    },
    todoClient,
  );
}

/**
 * Shows a to-do the server just returned at once, then reads the list and
 * the reminders again: the server orders the list, and a change can start
 * the reminders over or end them. A read already on its way left before the
 * change, so it is replaced.
 */
export function changedTodo(account: string, todo: Todo): void {
  todoClient.setQueryData<Todo[]>(
    keys.list(account),
    (current) =>
      current &&
      (current.some((item) => item.id === todo.id)
        ? current.map((item) => (item.id === todo.id ? todo : item))
        : [...current, todo]),
  );
  void todoClient.invalidateQueries({ queryKey: keys.list(account) });
  void todoClient.invalidateQueries({ queryKey: keys.reminders(account) });
}

/** Reads the list again, as when someone else changed a to-do first. */
export function readListAgain(account: string): Promise<void> {
  return todoClient.invalidateQueries({ queryKey: keys.list(account) });
}

/** Reads the account's to-dos and reminders again now, as when someone wakes the local console. */
export function readAgain(account: string): Promise<void> {
  return todoClient.invalidateQueries({ queryKey: keys.account(account) }, { cancelRefetch: false });
}

/** For tests: forget everything read. */
export function forgetTodos(): void {
  todoClient.clear();
}
