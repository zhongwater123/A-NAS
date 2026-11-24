import { Check, Plus, RotateCcw } from "lucide-react";
import { type ReactNode, useEffect, useEffectEvent, useRef, useState } from "react";

import { APIError } from "../api/client";
import { errorCodeText, errorText } from "../api/errorMessages";
import { formatYuan } from "../lib/money";
import { dayLabel, dueText, relativeTime } from "../lib/time";
import { nameOfAccount, type People, usePeople } from "../people/people";
import { useToasts } from "../ui/feedback";
import { RowMenu } from "../ui/RowMenu";
import { CATEGORY_ACTION, setTodoStatus, snoozeTodo, type Todo, type TodoReminder } from "./api";
import { byUrgency, snoozed, urgent } from "./format";
import {
  changedTodo,
  readListAgain,
  readTodos,
  useAcknowledgeReminders,
  useFreshTodos,
  useTodoReminders,
  useTodos,
} from "./queries";
import { TodoDialog } from "./TodoDialog";
import styles from "./todos.module.css";

interface Props {
  /** The signed-in account. */
  userId: string;
  /** A to-do another window asks to show, such as the desktop's reminder; onIntentHandled hands it back. */
  intent?: { todoId: string };
  onIntentHandled?(handled: { todoId: string }): void;
}

// How long a to-do asked for from another window stays marked.
const MARK_MS = 2_400;
const SNOOZE_DAYS = [1, 3, 7];

// TodosPanel is the 待办 window: what to be reminded of, what is open, what
// waits until a later day, and what was closed in the last 30 days.
export function TodosPanel({ userId, intent, onIntentHandled }: Props) {
  const list = useTodos(userId);
  const reminders = useTodoReminders(userId);
  const acknowledgement = useAcknowledgeReminders(userId);
  // A window left open, as on the local console, reads again just after
  // midnight, when what is due moves a day closer.
  useFreshTodos(userId);
  const [editing, setEditing] = useState<Todo | "new">();
  const [marked, setMarked] = useState<string>();
  const root = useRef<HTMLDivElement>(null);
  const scroller = useRef<HTMLDivElement>(null);
  const { toast, element: toasts } = useToasts();
  const people = usePeople();
  const todos = list.data?.toSorted(byUrgency);
  // What is due counts from when the list was last read or changed.
  const now = list.dataUpdatedAt;

  // A to-do asked for from another window shows once the list is read
  // again, as it may be new. An intent replaced before then is left to its
  // successor.
  const intentHandled = useEffectEvent((handled: { todoId: string }) => onIntentHandled?.(handled));
  useEffect(() => {
    if (!intent) return;
    let current = true;
    readTodos(userId)
      .then(
        (items) => {
          if (current && items.some((item) => item.id === intent.todoId)) setMarked(intent.todoId);
        },
        () => undefined,
      )
      .finally(() => {
        if (current) intentHandled(intent);
      });
    return () => {
      current = false;
    };
  }, [intent, userId]);

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

  const act = async (run: () => Promise<Todo>, said: (todo: Todo) => string) => {
    try {
      const next = await run();
      changedTodo(userId, next);
      toast({ text: said(next), tone: "done" });
    } catch (caught) {
      toast({ text: errorText(caught, "todos"), tone: "error" });
      if (caught instanceof APIError && (caught.code === "not_found" || caught.code === "conflict"))
        void readListAgain(userId);
    }
  };
  const acknowledge = (reminder: TodoReminder) =>
    acknowledgement.mutate([reminder.key], {
      onError: (caught) => toast({ text: errorText(caught, "todos"), tone: "error" }),
    });

  const body = () => {
    if (!todos) {
      return list.error ? (
        <div className={styles.tdCentered}>
          <p>{errorText(list.error, "todos")}</p>
          <button type="button" className={styles.tdSecondary} onClick={() => void list.refetch()}>
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
          (next) => `“${next.title}”推迟到${next.snoozedUntil ? dayLabel(next.snoozedUntil) : ` ${days} 天后`}`,
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
            triggerClassName={styles.tdIconButton}
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
                    onClick={() => acknowledge(reminder)}
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
                  <span className={styles.tdDue}>推迟到{dayLabel(item.snoozedUntil ?? "", now)}</span>,
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
                      triggerClassName={styles.tdIconButton}
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
        <h2>待办</h2>
        <button type="button" className={styles.tdPrimary} onClick={() => setEditing("new")} disabled={!todos}>
          <Plus size={15} /> 新建
        </button>
      </header>
      {body()}
      {editing && (
        <TodoDialog
          userId={userId}
          todo={editing === "new" ? undefined : editing}
          container={root}
          onClose={() => setEditing(undefined)}
          onSaved={(saved, created) => {
            setEditing(undefined);
            changedTodo(userId, saved);
            toast({ text: created ? `已新建“${saved.title}”` : "已保存", tone: "done" });
          }}
          onChanged={() => {
            setEditing(undefined);
            toast({ text: errorCodeText("changed", "todos") ?? "", tone: "error" });
            void readListAgain(userId);
          }}
        />
      )}
      {toasts}
    </div>
  );
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
