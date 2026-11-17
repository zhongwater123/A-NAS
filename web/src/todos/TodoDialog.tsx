import { type ReactNode, type RefObject, useRef, useState } from "react";

import { APIError } from "../api/client";
import { parseYuan, yuanText } from "../lib/money";
import { displayName, usePeople } from "../people/people";
import { Modal } from "../ui/modal";
import {
  addTodo,
  CATEGORIES,
  CATEGORY_ACTION,
  CATEGORY_LABEL,
  requestKey,
  TITLE_MAX,
  type Todo,
  type TodoCategory,
  todoError,
  updateTodo,
} from "./api";
import styles from "./todos.module.css";

// TodoDialog adds a to-do, or edits one from the version it was read at.
// Only what it is about is required; a to-do added by hand is the whole
// family's.
export function TodoDialog({
  todo,
  container,
  onClose,
  onSaved,
  onChanged,
}: {
  todo?: Todo;
  container: RefObject<HTMLElement | null>;
  onClose: () => void;
  onSaved: (saved: Todo, created: boolean) => void;
  // Someone else saved the to-do since it was read.
  onChanged: () => void;
}) {
  const people = usePeople();
  const [title, setTitle] = useState(todo?.title ?? "");
  const [category, setCategory] = useState<TodoCategory>(todo?.category ?? "other");
  const [due, setDue] = useState(todo?.due ?? "");
  const [ownerId, setOwnerId] = useState(todo?.ownerId ?? "");
  const [amount, setAmount] = useState(todo?.amountCents === undefined ? "" : yuanText(todo.amountCents));
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState("");
  // One key for every attempt from this dialog, so a retry adds it once.
  const key = useRef<string>(undefined);

  const length = [...title.trim()].length;
  const cents = amount.trim() ? parseYuan(amount) : undefined;
  const amountWrong = amount.trim() !== "" && cents === undefined;
  const ready = length > 0 && length <= TITLE_MAX && !amountWrong;
  // An owner whose account is gone stays chosen, under the name it had.
  const owners = people.profiles.map((profile) => ({ id: profile.id, name: displayName(profile) }));
  if (todo?.ownerId && !owners.some((owner) => owner.id === todo.ownerId))
    owners.push({ id: todo.ownerId, name: todo.ownerName ?? "已删除的账号" });

  const save = async () => {
    if (!ready || busy) return;
    setBusy(true);
    setProblem("");
    const fields = {
      title: title.trim(),
      category,
      ...(due ? { due } : {}),
      ...(ownerId ? { ownerId } : {}),
      ...(cents === undefined ? {} : { amountCents: cents }),
    };
    try {
      if (todo) onSaved(await updateTodo(todo.id, { ...fields, version: todo.version }), false);
      else {
        key.current ??= requestKey();
        onSaved(await addTodo({ requestKey: key.current, ...fields }), true);
      }
    } catch (caught) {
      setBusy(false);
      if (todo && caught instanceof APIError && caught.code === "changed") onChanged();
      else setProblem(todoError(caught));
    }
  };

  return (
    <Modal
      title={todo ? "编辑待办" : "新建待办"}
      container={container}
      busy={busy}
      onClose={onClose}
      footer={
        <>
          <span className={styles.tdGrow} />
          <button type="button" className={styles.tdSecondary} onClick={onClose} disabled={busy}>
            取消
          </button>
          <button type="submit" form="td-dialog-form" className={styles.tdPrimary} disabled={!ready || busy}>
            {busy ? "正在保存…" : "保存"}
          </button>
        </>
      }
    >
      <form
        id="td-dialog-form"
        className={styles.tdForm}
        onSubmit={(event) => {
          event.preventDefault();
          void save();
        }}
      >
        <Field
          label="什么事"
          hint={
            <span className={length > TITLE_MAX ? styles.urgent : ""}>
              {length}/{TITLE_MAX} 个字，写短一点，例如“交物业费”
            </span>
          }
        >
          <input value={title} onChange={(event) => setTitle(event.target.value)} disabled={busy} />
        </Field>
        <div className={styles.tdFormRow}>
          <Field label="类别">
            <select
              value={category}
              onChange={(event) => setCategory(event.target.value as TodoCategory)}
              disabled={busy}
            >
              {CATEGORIES.map((value) => (
                <option key={value} value={value}>
                  {CATEGORY_LABEL[value]}（{CATEGORY_ACTION[value]}）
                </option>
              ))}
            </select>
          </Field>
          <Field label="截止日" hint="不填就不提醒">
            <input type="date" value={due} onChange={(event) => setDue(event.target.value)} disabled={busy} />
          </Field>
        </div>
        <div className={styles.tdFormRow}>
          <Field label="谁的">
            <select value={ownerId} onChange={(event) => setOwnerId(event.target.value)} disabled={busy}>
              <option value="">全家</option>
              {owners.map((owner) => (
                <option key={owner.id} value={owner.id}>
                  {owner.name}
                </option>
              ))}
            </select>
          </Field>
          <Field
            label="金额（元）"
            hint={amountWrong ? <span className={styles.urgent}>请填写数字，最多两位小数</span> : "可不填，例如 128.50"}
          >
            <input
              value={amount}
              inputMode="decimal"
              onChange={(event) => setAmount(event.target.value)}
              disabled={busy}
            />
          </Field>
        </div>
        {problem && (
          <p className={styles.tdProblem} role="alert">
            {problem}
          </p>
        )}
      </form>
    </Modal>
  );
}

// Field lays out a labelled form control, with its hint under it, outside
// the label so the control's name stays the label's.
function Field({ label, hint, children }: { label: string; hint?: ReactNode; children: ReactNode }) {
  return (
    <div className={styles.tdField}>
      <label>
        <span>{label}</span>
        {children}
      </label>
      {hint && <span className={styles.tdFieldHint}>{hint}</span>}
    </div>
  );
}
