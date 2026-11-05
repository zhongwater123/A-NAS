import { useEffect, useMemo, useState, useSyncExternalStore } from "react";

import { deviceStorage } from "./deviceStorage";

type Todo = { id: string; text: string; done: boolean };
type Focus = {
  status: "idle" | "running" | "paused" | "done";
  mode: "focus" | "break";
  duration: number;
  remaining: number;
  deadline: number | null;
  todoId: string | null;
  title: string;
  completedAt?: number;
};
export type Desk = { todos: Todo[]; focus: Focus; rounds: number; persistent: boolean; focusAck: number };
const blankFocus = (): Focus => ({
  status: "idle",
  mode: "focus",
  duration: 25 * 60000,
  remaining: 25 * 60000,
  deadline: null,
  todoId: null,
  title: "",
});
const initial = (): Desk => ({ todos: [], focus: blankFocus(), rounds: 0, persistent: true, focusAck: 0 });
const finite = (value: unknown): value is number => typeof value === "number" && Number.isFinite(value);
export const deskKey = (owner: string) => `anas:companion:desk:v1:${encodeURIComponent(owner)}`;

function read(storage: Pick<Storage, "getItem">, key: string): Desk {
  try {
    const raw = storage.getItem(key);
    if (!raw) return initial();
    const data = JSON.parse(raw);
    const f = data.focus;
    if (
      !Array.isArray(data.todos) ||
      data.todos.length > 100 ||
      !data.todos.every(
        (t: Todo) =>
          typeof t?.id === "string" &&
          t.id.length <= 100 &&
          typeof t.text === "string" &&
          t.text.length <= 160 &&
          typeof t.done === "boolean",
      ) ||
      !f ||
      !["idle", "running", "paused", "done"].includes(f.status) ||
      !["focus", "break"].includes(f.mode) ||
      !finite(f.duration) ||
      f.duration < 60000 ||
      f.duration > 90 * 60000 ||
      !finite(f.remaining) ||
      f.remaining < 0 ||
      f.remaining > f.duration ||
      (f.status === "running" ? !finite(f.deadline) || f.deadline <= 0 : f.deadline !== null) ||
      !(f.todoId === null || typeof f.todoId === "string") ||
      typeof f.title !== "string" ||
      f.title.length > 160 ||
      !Number.isSafeInteger(data.rounds) ||
      data.rounds < 0
    )
      return initial();
    return {
      todos: data.todos,
      focus: { ...f, completedAt: finite(f.completedAt) ? f.completedAt : undefined },
      rounds: data.rounds,
      persistent: true,
      focusAck: finite(data.focusAck) ? data.focusAck : 0,
    };
  } catch {
    return { ...initial(), persistent: false };
  }
}

// One device, scoped to a signed-in member. These are not NAS scheduled tasks.
export function createDesk(owner: string, storage: Pick<Storage, "getItem" | "setItem">) {
  const key = deskKey(owner);
  let value = read(storage, key);
  const listeners = new Set<() => void>();
  const notify = () => {
    for (const listener of listeners) listener();
  };
  const save = (next: Desk) => {
    value = next;
    try {
      value = { ...next, persistent: true };
      storage.setItem(key, JSON.stringify(value));
    } catch {
      value = { ...next, persistent: false };
    }
    notify();
  };
  const tick = (now = Date.now()) => {
    if (value.focus.status !== "running" || value.focus.deadline === null || now < value.focus.deadline) return;
    save({
      ...value,
      rounds: value.rounds + (value.focus.mode === "focus" ? 1 : 0),
      focus: { ...value.focus, status: "done", remaining: 0, completedAt: value.focus.deadline, deadline: null },
    });
  };
  const refresh = (event: StorageEvent) => {
    if (event.key !== null && event.key !== key) return;
    value = read(storage, key);
    tick();
    notify();
  };
  return {
    get: () => value,
    tick,
    subscribe: (listener: () => void) => {
      if (!listeners.size) {
        value = read(storage, key);
        window.addEventListener("storage", refresh);
      }
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
        if (!listeners.size) window.removeEventListener("storage", refresh);
      };
    },
    add: (text: string) => {
      const trimmed = text.trim();
      if (!trimmed || trimmed.length > 160 || value.todos.length >= 100) return;
      save({
        ...value,
        todos: [
          ...value.todos,
          { id: crypto.getRandomValues(new Uint32Array(4)).join("-"), text: trimmed, done: false },
        ],
      });
    },
    toggle: (id: string) =>
      save({ ...value, todos: value.todos.map((todo) => (todo.id === id ? { ...todo, done: !todo.done } : todo)) }),
    remove: (id: string) => save({ ...value, todos: value.todos.filter((todo) => todo.id !== id) }),
    start: (minutes: number, mode: "focus" | "break" = "focus", todoId: string | null = null, now = Date.now()) => {
      if (
        value.focus.status === "running" ||
        value.focus.status === "paused" ||
        !Number.isInteger(minutes) ||
        minutes < 1 ||
        minutes > 90
      )
        return;
      const todo = value.todos.find((item) => item.id === todoId && !item.done);
      const duration = minutes * 60000;
      save({
        ...value,
        focus: {
          status: "running",
          mode,
          duration,
          remaining: duration,
          deadline: now + duration,
          todoId: todo?.id ?? null,
          title: todo?.text ?? "",
        },
      });
    },
    pause: (now = Date.now()) => {
      tick(now);
      if (value.focus.status !== "running") return;
      save({
        ...value,
        focus: {
          ...value.focus,
          status: "paused",
          remaining: Math.min(value.focus.remaining, Math.max(0, value.focus.deadline! - now)),
          deadline: null,
        },
      });
    },
    resume: (now = Date.now()) => {
      if (value.focus.status !== "paused") return;
      save({ ...value, focus: { ...value.focus, status: "running", deadline: now + value.focus.remaining } });
    },
    acknowledgeFocus: () => save({ ...value, focusAck: value.focus.completedAt ?? 0 }),
    reset: () => save({ ...value, focus: blankFocus() }),
  };
}
const stores = new Map<string, ReturnType<typeof createDesk>>();
export function usePersonalDesk(owner: string) {
  const store = useMemo(() => {
    let found = stores.get(owner);
    if (!found) {
      found = createDesk(owner, deviceStorage);
      stores.set(owner, found);
    }
    return found;
  }, [owner]);
  const value = useSyncExternalStore(store.subscribe, store.get);
  const [now, setNow] = useState(Date.now);
  const running = value.focus.status === "running";
  useEffect(() => {
    const update = () => {
      const current = Date.now();
      store.tick(current);
      setNow(current);
    };
    update();
    // Only a running clock needs a tick every second. Otherwise checking again when the
    // page comes back is enough, and the pet and home are not redrawn every second.
    const timer = running ? window.setInterval(update, 1000) : undefined;
    document.addEventListener("visibilitychange", update);
    return () => {
      clearInterval(timer);
      document.removeEventListener("visibilitychange", update);
    };
  }, [store, running]);
  const remaining =
    value.focus.status === "running"
      ? Math.min(value.focus.remaining, Math.max(0, value.focus.deadline! - now))
      : value.focus.remaining;
  const seconds = Math.ceil(remaining / 1000);
  const clock = `${String(Math.floor(seconds / 60)).padStart(2, "0")}:${String(seconds % 60).padStart(2, "0")}`;
  const f = value.focus;
  const message =
    f.status === "running"
      ? f.mode === "break"
        ? "休息一下，看看远处。我来帮你看时间。"
        : `这轮我来守时，你只管${f.title ? "做好这一件事" : "专心"}。`
      : f.status === "paused"
        ? "先缓一缓，准备好了再继续。"
        : f.status === "done"
          ? f.mode === "break"
            ? "休息好啦，要不要开始下一件小事？"
            : "开心 · 这一轮专注完成啦！伸个懒腰，歇一会儿。"
          : undefined;
  return { value, store, clock, remaining, message };
}
export type PersonalDeskState = ReturnType<typeof usePersonalDesk>;
