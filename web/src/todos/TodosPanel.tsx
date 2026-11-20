import { Check, Ellipsis, Plus, RotateCcw } from "lucide-react";
import { DropdownMenu } from "radix-ui";
import { type ReactNode, useCallback, useEffect, useEffectEvent, useRef, useState } from "react";

import { APIError } from "../api/client";
import { formatYuan } from "../lib/money";
import { calendarDate, dateOnly, relativeTime, untilNextDay } from "../lib/time";
import { nameOfAccount, type People, usePeople } from "../people/people";
import { useToasts } from "../ui/feedback";
import { CATEGORY_ACTION, listTodos, setTodoStatus, snoozeTodo, type Todo, type TodoReminder, todoError } from "./api";
import { byUrgency, dueText, snoozed, urgent } from "./format";
import { acknowledgeTodoReminder, loadTodoReminders, useTodoReminders } from "./reminders";
import { TodoDialog } from "./TodoDialog";
import styles from "./todos.module.css";

interface Props {
  /** A to-do another window asks to show, such as the desktop's reminder; onIntentHandled hands it back. */
  intent?: { todoId: string };
  onIntentHandled?(handled: { todoId: string }): void;
}

// How long a to-do asked for from another window stays marked.
const MARK_MS = 2_400;
const SNOOZE_DAYS = [1, 3, 7];

// TodosPanel is the 待办 window: what to be reminded of, what is open, what
// waits until a later day, and what was closed in the last 30 days.
export function TodosPanel({ intent, onIntentHandled }: Props) {
  const [todos, setTodos] = useState<Todo[]>();
  const reminders = useTodoReminders();
  const [failure, setFailure] = useState("");
  const [editing, setEditing] = useState<Todo | "new">();
  const [marked, setMarked] = useState<string>();
  // When the list was last read or changed: what is due counts from that day.
  const [now, setNow] = useState(0);
  const root = useRef<HTMLDivElement>(null);
  const scroller = useRef<HTMLDivElement>(null);
  const { toast, element: toasts } = useToasts();
  const people = usePeople();

  const show = useCallback((items: Todo[]) => {
    setTodos(items.toSorted(byUrgency));
    setNow(Date.now());
  }, []);
  const reload = useCallback(async () => {
    try {
      show(await listTodos());
      setFailure("");
    } catch (caught) {
      setFailure(todoError(caught));
    }
  }, [show]);

  useEffect(() => {
    let live = true;
    listTodos().then(
      (items) => {
        if (live) show(items);
      },
      (caught: unknown) => {
        if (live) setFailure(todoError(caught));
      },
    );
    void loadTodoReminders();
    return () => {
      live = false;
    };
  }, [show]);

  // A window left open, as on the local console, reads again just after
  // midnight, when what is due moves a day closer.
  useEffect(() => {
    let timer = 0;
    const nextDay = () => {
      timer = window.setTimeout(() => {
        void reload();
        void loadTodoReminders();
        nextDay();
      }, untilNextDay());
    };
    nextDay();
    return () => window.clearTimeout(timer);
  }, [reload]);

  // A to-do asked for from another window shows once the list is read
  // again, as it may be new. An intent replaced before then is left to its
  // successor.
  const intentHandled = useEffectEvent((handled: { todoId: string }) => onIntentHandled?.(handled));
  useEffect(() => {
    if (!intent) return;
    let current = true;
    listTodos()
      .then(
        (items) => {
          if (!current) return;
          show(items);
          setFailure("");
          if (items.some((item) => item.id === intent.todoId)) setMarked(intent.todoId);
        },
        () => undefined,
      )
      .finally(() => {
        if (current) intentHandled(intent);
      });
    return () => {
      current = false;
    };
  }, [intent, show]);

  // The marked to-do scrolls into view inside the window, not the desktop.
  useEffect(() => {
    if (!marked) return;
    const view = scroller.current;
    const row = [...(view?.querySelectorAll<HTMLElement>("[data-todo-id]") ?? [])].find(
      (element) => element.dataset.todoId === marked,
    );
    if (view && row) {
      const offset = row.getBoundingClientRect().top - view.getBoundingClientRect().top;
      view.scrollTo?.({ top: view.scrollTop + offset - 16, behavior: "smooth" });
    }
    const timer = window.setTimeout(() => setMarked(undefined), MARK_MS);
    return () => window.clearTimeout(timer);
  }, [marked]);

  const replace = useCallback((next: Todo) => {
    setTodos((current) => current?.map((item) => (item.id === next.id ? next : item)).toSorted(byUrgency));
    setNow(Date.now());
  }, []);
  const act = async (run: () => Promise<Todo>, said: (todo: Todo) => string) => {
    try {
      const next = await run();
      replace(next);
      toast({ text: said(next), tone: "done" });
      // A change to a to-do can start its reminders over or end them.
      void loadTodoReminders();
    } catch (caught) {
      toast({ text: todoError(caught), tone: "error" });
      if (caught instanceof APIError && (caught.code === "not_found" || caught.code === "conflict")) void reload();
    }
  };
  const acknowledge = async (reminder: TodoReminder) => {
    try {
      await acknowledgeTodoReminder([reminder.key]);
    } catch (caught) {
      toast({ text: todoError(caught), tone: "error" });
    }
  };

  const body = () => {
    if (!todos) {
      return failure ? (
        <div className={styles.tdCentered}>
          <p>{failure}</p>
          <button type="button" className={styles.tdSecondary} onClick={() => void reload()}>
            重试
          </button>
        </div>
      ) : (
        <div className={styles.tdCentered}>
          <span className="loader" aria-label="正在打开待办" />
        </div>
      );
    }
    const open = todos.filter((item) => item.status === "open" && !snoozed(item, now));
    const waiting = todos.filter((item) => snoozed(item, now));
    const closed = todos.filter((item) => item.status !== "open");
    const rowOf = (item: Todo, aside: ReactNode, actions: ReactNode) => (
      <li
        key={item.id}
        data-todo-id={item.id}
        className={`${styles.tdRow} ${marked === item.id ? styles.marked : ""}`}
        aria-current={marked === item.id ? "true" : undefined}
      >
        <div className={styles.tdRowMain}>
          <strong>{item.title}</strong>
          <span className={styles.tdMeta}>{details(item, people).join(" · ")}</span>
        </div>
        {aside}
        <div className={styles.tdRowActions}>{actions}</div>
      </li>
    );
    const status = (item: Todo) => ({
      done: () =>
        act(
          () => setTodoStatus(item.id, "done"),
          (next) => `已处理“${next.title}”`,
        ),
      ignore: () =>
        act(
          () => setTodoStatus(item.id, "ignored"),
          (next) => `已忽略“${next.title}”`,
        ),
      reopen: () =>
        act(
          () => setTodoStatus(item.id, "open"),
          (next) => `已重新打开“${next.title}”`,
        ),
      snooze: (days: number) =>
        act(
          () => snoozeTodo(item.id, days),
          (next) => `“${next.title}”推迟到${next.snoozedUntil ? dayText(next.snoozedUntil) : ` ${days} 天后`}`,
        ),
    });
    const openActions = (item: Todo) => {
      const run = status(item);
      return (
        <>
          <button
            type="button"
            className={styles.tdIconButton}
            aria-label={`已处理：${item.title}`}
            title="已处理"
            onClick={() => void run.done()}
          >
            <Check size={16} />
          </button>
          <RowMenu
            label={`更多操作：${item.title}`}
            items={[
              { label: "已处理", run: () => void run.done() },
              { label: "忽略", run: () => void run.ignore() },
              ...SNOOZE_DAYS.map((days) => ({ label: `推迟 ${days} 天`, run: () => void run.snooze(days) })),
              { label: "编辑", run: () => setEditing(item) },
            ]}
          />
        </>
      );
    };
    return (
      <div className={styles.tdScroll} ref={scroller}>
        {reminders.length > 0 && (
          <section className={styles.tdSection} aria-labelledby="td-reminders">
            <h3 id="td-reminders">要提醒的</h3>
            <ul className={styles.tdReminders}>
              {reminders.map((reminder) => (
                <li key={reminder.key}>
                  <strong>{reminder.title}</strong>
                  <span className={reminder.stage === "soon" ? "" : styles.urgent}>{dueText(reminder.due, now)}</span>
                  <button
                    type="button"
                    className={styles.tdSecondary}
                    aria-label={`知道了：${reminder.title}`}
                    onClick={() => void acknowledge(reminder)}
                  >
                    知道了
                  </button>
                </li>
              ))}
            </ul>
          </section>
        )}
        <section className={styles.tdSection} aria-labelledby="td-open">
          <h3 id="td-open">待处理</h3>
          {open.length === 0 ? (
            <p className={styles.tdEmpty}>{waiting.length ? "没有要马上处理的事" : "都处理完了"}</p>
          ) : (
            <ul className={styles.tdRows}>
              {open.map((item) =>
                rowOf(
                  item,
                  <span className={`${styles.tdDue} ${urgent(item, now) ? styles.urgent : ""}`}>
                    {item.due ? dueText(item.due, now) : "不限日期"}
                  </span>,
                  openActions(item),
                ),
              )}
            </ul>
          )}
        </section>
        {waiting.length > 0 && (
          <section className={styles.tdSection} aria-labelledby="td-waiting">
            <h3 id="td-waiting">推迟中</h3>
            <ul className={styles.tdRows}>
              {waiting.map((item) =>
                rowOf(
                  item,
                  <span className={styles.tdDue}>推迟到{dayText(item.snoozedUntil ?? "")}</span>,
                  openActions(item),
                ),
              )}
            </ul>
          </section>
        )}
        {closed.length > 0 && (
          <section className={styles.tdSection} aria-labelledby="td-closed">
            <h3 id="td-closed">最近处理</h3>
            <ul className={`${styles.tdRows} ${styles.closed}`}>
              {closed.map((item) =>
                rowOf(
                  item,
                  <span className={styles.tdDue}>
                    {item.status === "done" ? "已处理" : "已忽略"}
                    {item.closedAt ? ` · ${relativeTime(item.closedAt, now)}` : ""}
                  </span>,
                  <>
                    <button
                      type="button"
                      className={styles.tdSecondary}
                      aria-label={`重新打开：${item.title}`}
                      onClick={() => void status(item).reopen()}
                    >
                      <RotateCcw size={13} /> 重新打开
                    </button>
                    <RowMenu
                      label={`更多操作：${item.title}`}
                      items={[{ label: "编辑", run: () => setEditing(item) }]}
                    />
                  </>,
                ),
              )}
            </ul>
          </section>
        )}
      </div>
    );
  };

  return (
    <div className={styles.tdApp} ref={root}>
      <header className={styles.tdHead}>
        <div>
          <h2>待办</h2>
          <p>全家都能看到、都能处理。快到期时会提醒每个人。</p>
        </div>
        <button type="button" className={styles.tdPrimary} onClick={() => setEditing("new")} disabled={!todos}>
          <Plus size={15} /> 新建
        </button>
      </header>
      {body()}
      {editing && (
        <TodoDialog
          todo={editing === "new" ? undefined : editing}
          container={root}
          onClose={() => setEditing(undefined)}
          onSaved={(saved, created) => {
            setEditing(undefined);
            if (created) show([...(todos ?? []).filter((item) => item.id !== saved.id), saved]);
            else replace(saved);
            toast({ text: created ? `已新建“${saved.title}”` : "已保存", tone: "done" });
            void loadTodoReminders();
          }}
          onChanged={() => {
            setEditing(undefined);
            toast({ text: "别人刚改过这件事，已刷新，请再改一次", tone: "error" });
            void reload();
          }}
        />
      )}
      {toasts}
    </div>
  );
}

// dayText is a date-only value as “11月20日”, with the year when it is not this year's.
function dayText(value: string): string {
  return calendarDate(dateOnly(value));
}

// details says what kind of matter a to-do is, whose and how much.
function details(item: Todo, people: People): string[] {
  const owner = item.ownerId ? nameOfAccount(people, item.ownerId, item.ownerName ?? "已删除的账号") : "全家";
  const parts = [CATEGORY_ACTION[item.category], owner];
  if (item.amountCents !== undefined) parts.push(formatYuan(item.amountCents));
  if (item.status !== "open" && item.closedById)
    parts.push(
      `${nameOfAccount(people, item.closedById, item.closedByName ?? "已删除的账号")}${item.status === "done" ? "处理" : "忽略"}`,
    );
  return parts;
}

interface MenuItem {
  label: string;
  run: () => void;
}

// RowMenu is the … menu of a to-do.
function RowMenu({ label, items }: { label: string; items: MenuItem[] }) {
  return (
    <DropdownMenu.Root modal={false}>
      <DropdownMenu.Trigger className={styles.tdIconButton} aria-label={label}>
        <Ellipsis size={16} />
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content className={styles.tdMenu} align="end" sideOffset={4}>
          {items.map((item) => (
            <DropdownMenu.Item key={item.label} className={styles.tdMenuItem} onSelect={item.run}>
              {item.label}
            </DropdownMenu.Item>
          ))}
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );
}
