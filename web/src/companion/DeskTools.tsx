import { useState } from "react";

import type { PersonalDeskState } from "./deskStore";
import styles from "./personalDesk.module.css";

export function FocusDesk({ desk }: { desk: PersonalDeskState }) {
  const [minutes, setMinutes] = useState(25);
  const { value, store, clock, remaining } = desk;
  const f = value.focus;
  const active = f.status === "running" || f.status === "paused";
  const linked = value.todos.find((todo) => todo.id === f.todoId && !todo.done);
  return (
    <section className={styles.focus} aria-label="番茄钟">
      <div className={styles.focusMeta}>
        <span>
          {f.status === "done"
            ? "这一轮完成了"
            : active
              ? f.mode === "break"
                ? "休息时间"
                : f.status === "paused"
                  ? "已暂停"
                  : "正在专注"
              : "一次，只做一件事"}
        </span>
        <small>累计 {value.rounds} 轮</small>
      </div>
      <div className={styles.clock} role="timer" aria-label="剩余时间">
        {active || f.status === "done" ? clock : `${String(minutes).padStart(2, "0")}:00`}
      </div>
      <p className={styles.focusTitle}>
        {f.title && f.status !== "idle"
          ? f.title
          : f.status === "done"
            ? "做得不错，给自己一点休息时间。"
            : "把这一小段时间，留给眼前的事。"}
      </p>
      <div className={styles.progress} aria-hidden="true">
        <span style={{ width: `${active || f.status === "done" ? (1 - remaining / f.duration) * 100 : 0}%` }} />
      </div>
      {!active && (
        <div className={styles.durations}>
          {[25, 45].map((n) => (
            <button type="button" key={n} aria-pressed={minutes === n} onClick={() => setMinutes(n)}>
              {n} 分钟
            </button>
          ))}
          <label>
            <input
              aria-label="专注时长（分钟）"
              type="number"
              min={1}
              max={90}
              value={minutes}
              onChange={(e) => setMinutes(Math.min(90, Math.max(1, Math.round(Number(e.target.value)) || 1)))}
            />
            分钟
          </label>
        </div>
      )}
      <div className={styles.controls}>
        {f.status === "running" ? (
          <button type="button" className={styles.primary} onClick={() => store.pause()}>
            暂停
          </button>
        ) : f.status === "paused" ? (
          <button type="button" className={styles.primary} onClick={() => store.resume()}>
            继续专注
          </button>
        ) : (
          <button type="button" className={styles.primary} onClick={() => store.start(minutes)}>
            {" "}
            {f.status === "done" ? "再专注一轮" : "开始专注"}
          </button>
        )}
        {active ? (
          <button type="button" onClick={store.reset}>
            结束本轮
          </button>
        ) : (
          <button type="button" onClick={() => store.start(5, "break")}>
            休息 5 分钟
          </button>
        )}
        {f.status === "done" && linked && (
          <button type="button" onClick={() => store.toggle(linked.id)}>
            完成这条待办
          </button>
        )}
      </div>
      <p className={styles.hint}>此设备计时，刷新可继续；关闭页面后不会弹出提醒。</p>
    </section>
  );
}

export function TodoDesk({ desk, onFocus }: { desk: PersonalDeskState; onFocus(): void }) {
  const [text, setText] = useState("");
  const { value, store } = desk;
  const remaining = value.todos.filter((todo) => !todo.done).length;
  const busy = value.focus.status === "running" || value.focus.status === "paused";
  return (
    <section className={styles.todos} aria-label="简单待办">
      <h2>
        慢慢来，一件件完成 <span>{remaining} 件待办</span>
      </h2>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          store.add(text);
          setText("");
        }}
      >
        <input
          aria-label="添加待办"
          placeholder="写下一件想完成的小事…"
          maxLength={160}
          value={text}
          disabled={value.todos.length >= 100}
          onChange={(e) => setText(e.target.value)}
        />
        <button type="submit" disabled={!text.trim() || value.todos.length >= 100}>
          添加
        </button>
      </form>
      {!value.todos.length && <p className={styles.empty}>还没有待办。先写下一件小事，大肥鱼陪你开始。</p>}
      <ul>
        {[...value.todos.filter((t) => !t.done), ...value.todos.filter((t) => t.done)].map((todo) => (
          <li key={todo.id}>
            <label>
              <input type="checkbox" checked={todo.done} onChange={() => store.toggle(todo.id)} />
              <span className={todo.done ? styles.done : undefined}>{todo.text}</span>
            </label>
            {!todo.done && (
              <button
                type="button"
                disabled={busy}
                aria-label={`专注：${todo.text}`}
                onClick={() => {
                  store.start(25, "focus", todo.id);
                  onFocus();
                }}
              >
                专注 25 分钟
              </button>
            )}
            <button
              type="button"
              className={styles.remove}
              aria-label={`删除待办：${todo.text}`}
              onClick={() => store.remove(todo.id)}
            >
              ×
            </button>
          </li>
        ))}
      </ul>
      {value.todos.length >= 100 && <p className={styles.hint}>已保存 100 条，删除一些后再添加。</p>}
      {busy && <p className={styles.hint}>先完成或结束当前计时，再开始下一件事。</p>}
      <p className={styles.hint}>仅保存在此设备，勾选完成由你决定。</p>
    </section>
  );
}

export function FocusBadge({ desk, onOpen }: { desk: PersonalDeskState; onOpen(): void }) {
  if (desk.value.focus.status === "idle") return null;
  return (
    <button
      type="button"
      className={styles.focusBadge}
      data-focus-badge="true"
      data-pet-interactive="true"
      onClick={onOpen}
    >
      {desk.value.focus.status === "done"
        ? "这一轮完成啦 · 回小窝"
        : `${desk.value.focus.status === "paused" ? "已暂停" : desk.value.focus.mode === "break" ? "休息" : "专注"} ${desk.clock} · 回小窝`}
    </button>
  );
}
