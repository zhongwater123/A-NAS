import { ChevronRight, CopyPlus, PencilLine, Replace } from "lucide-react";
import { type FormEvent, type ReactNode, useEffect, useRef, useState } from "react";

import { APIError } from "../api/client";
import type { RestoredEntry, TrashItem } from "../api.generated";
import { formatBytes } from "../lib/bytes";
import { Dialog } from "../ui/workspace";
import { type RestoreConflict, restoreTrash } from "./api";
import { FileGlyph } from "./FileGlyph";
import { messageOf, restoredName } from "./model";
import { Name } from "./TrashRow";
import styles from "./trash.module.css";

function Spinner() {
  return <span className={styles.trSpinner} aria-hidden="true" />;
}

// Subjects lists the first few items a dialog is about, and how many more.
function Subjects({ items, detail }: { items: TrashItem[]; detail: (item: TrashItem) => string }) {
  const shown = items.slice(0, 4);
  return (
    <>
      <ul className={styles.trSubjects}>
        {shown.map((item) => (
          <li key={item.id}>
            <FileGlyph name={item.name} kind={item.kind} />
            <span>
              <Name item={item} />
              <small title={detail(item)}>{detail(item)}</small>
            </span>
          </li>
        ))}
      </ul>
      {items.length > shown.length && <p className={styles.trMore}>以及另外 {items.length - shown.length} 项</p>}
    </>
  );
}

function Option({
  icon,
  title,
  detail,
  busy,
  disabled,
  onClick,
}: {
  icon: ReactNode;
  title: string;
  detail: string;
  busy?: boolean;
  disabled: boolean;
  onClick: () => void;
}) {
  return (
    <button type="button" className={styles.trOption} disabled={disabled} onClick={onClick}>
      <span className={styles.trOptionIcon}>{busy ? <Spinner /> : icon}</span>
      <span className={styles.trOptionText}>
        <strong>{title}</strong>
        <small>{detail}</small>
      </span>
      <ChevronRight className={styles.trOptionGo} />
    </button>
  );
}

// TakenDialog opens when one item's name is taken where it goes back: keep
// both, replace the file of that name, or restore under another name.
export function TakenDialog({
  item,
  location,
  onClose,
  onRestored,
  onGone,
}: {
  item: TrashItem;
  location: string;
  onClose: () => void;
  onRestored: (restored: RestoredEntry, asked: string, choice: RestoreConflict) => void;
  onGone: (message: string) => void;
}) {
  // taken is the name found taken; without one the user is typing another.
  const [taken, setTaken] = useState(item.name);
  const [name, setName] = useState(item.name);
  const [busy, setBusy] = useState<RestoreConflict>();
  const [error, setError] = useState("");
  const choices = useRef<HTMLDivElement>(null);
  const field = useRef<HTMLInputElement>(null);
  // The name field and the choices take each other's place; focus follows.
  useEffect(() => {
    if (taken) choices.current?.querySelector<HTMLElement>("button")?.focus();
    else field.current?.select();
  }, [taken]);

  const restore = async (choice: RestoreConflict, asked: string, event?: FormEvent) => {
    event?.preventDefault();
    if (!asked || busy) return;
    setBusy(choice);
    setError("");
    try {
      onRestored(await restoreTrash(item.id, asked, choice), asked, choice);
    } catch (caught) {
      if (caught instanceof APIError && caught.code === "not_found") {
        onGone(messageOf(caught));
        return;
      }
      if (caught instanceof APIError && caught.code === "name_conflict") setTaken(asked);
      else setError(messageOf(caught));
    } finally {
      setBusy(undefined);
    }
  };

  const file = item.kind === "file";
  return (
    <Dialog
      className={styles.trLayer}
      title={`原位置已有同名${file ? "文件" : "文件夹"}`}
      label="同名项目"
      busy={Boolean(busy)}
      onClose={onClose}
      footer={
        <>
          <button
            type="button"
            data-autofocus
            className={`${styles.trBtn} ${styles.quiet}`}
            disabled={Boolean(busy)}
            onClick={onClose}
          >
            取消
          </button>
          {!taken && (
            <button
              type="submit"
              form="tr-rename-form"
              className={`${styles.trBtn} ${styles.solid}`}
              disabled={Boolean(busy) || !name.trim()}
            >
              {busy ? <Spinner /> : null}
              恢复
            </button>
          )}
        </>
      }
    >
      <Subjects items={[item]} detail={() => location} />
      {taken ? (
        <section className={styles.trTaken} role="group" aria-label="同名项目">
          {/* The title says it of the item's own name; another name tried and taken is named. */}
          {taken !== item.name && <p>“{taken}”也已存在</p>}
          <div ref={choices} className={styles.trOptions}>
            <Option
              icon={<CopyPlus />}
              title="保留两者"
              detail={`恢复为“${restoredName(taken, item.kind)}”`}
              busy={busy === "rename"}
              disabled={Boolean(busy)}
              onClick={() => void restore("rename", taken)}
            />
            {file && (
              <Option
                icon={<Replace />}
                title="替换"
                detail={`现有的“${taken}”移到回收站`}
                busy={busy === "replace"}
                disabled={Boolean(busy)}
                onClick={() => void restore("replace", taken)}
              />
            )}
            <Option
              icon={<PencilLine />}
              title="改用其他名称"
              detail="自己输入一个名称"
              disabled={Boolean(busy)}
              onClick={() => {
                setName(restoredName(taken, item.kind));
                setTaken("");
              }}
            />
          </div>
          {!file && <small>文件夹不会被替换或合并。</small>}
        </section>
      ) : (
        <form
          id="tr-rename-form"
          className={styles.trRename}
          onSubmit={(event) => void restore("ask", name.trim(), event)}
        >
          <label>
            <span>名称</span>
            <input
              ref={field}
              value={name}
              disabled={Boolean(busy)}
              aria-label="恢复后的名称"
              onChange={(event) => {
                setName(event.target.value);
                setError("");
              }}
            />
          </label>
        </form>
      )}
      {error && (
        <p className={styles.trError} role="alert">
          {error}
        </p>
      )}
    </Dialog>
  );
}

// ConflictDialog asks once what to do with every item of a selection whose
// name is taken where it goes back.
export function ConflictDialog({
  items,
  locate,
  onChoose,
}: {
  items: TrashItem[];
  locate: (item: TrashItem) => string;
  onChoose: (choice: RestoreConflict | "skip") => void;
}) {
  const files = items.filter((item) => item.kind === "file").length;
  return (
    <Dialog
      className={styles.trLayer}
      title={
        items.length === 1 ? `“${items[0].name}”的原位置已有同名项目` : `${items.length} 个项目的原位置已有同名项目`
      }
      label="同名项目"
      onClose={() => onChoose("skip")}
      footer={
        <>
          <button
            type="button"
            data-autofocus
            className={`${styles.trBtn} ${styles.quiet}`}
            onClick={() => onChoose("skip")}
          >
            跳过
          </button>
          {files > 0 && (
            <button type="button" className={`${styles.trBtn} ${styles.outline}`} onClick={() => onChoose("replace")}>
              替换
            </button>
          )}
          <button type="button" className={`${styles.trBtn} ${styles.solid}`} onClick={() => onChoose("rename")}>
            保留两者
          </button>
        </>
      }
    >
      <Subjects items={items} detail={locate} />
      <p className={styles.trDialogText}>
        保留两者时，恢复的项目改用“名称 (恢复)”这样的新名称。
        {files > 0
          ? `替换时，现有的同名文件移到回收站${files < items.length ? "；文件夹不会被替换，仍然保留两者" : ""}。`
          : "文件夹不会被替换或合并。"}
        跳过的项目留在回收站中。
      </p>
    </Dialog>
  );
}

export function PurgeDialog({
  items,
  everything,
  busy,
  onClose,
  onConfirm,
}: {
  items: TrashItem[];
  everything: boolean;
  busy: boolean;
  onClose: () => void;
  onConfirm: () => void;
}) {
  const size = formatBytes(items.reduce((sum, item) => sum + item.sizeBytes, 0));
  const single = items.length === 1 && !everything ? items[0] : undefined;
  return (
    <Dialog
      className={styles.trLayer}
      title={everything ? "清空回收站？" : single ? "永久删除？" : `永久删除 ${items.length} 项？`}
      busy={busy}
      onClose={onClose}
      footer={
        <>
          <button
            type="button"
            data-autofocus
            className={`${styles.trBtn} ${styles.quiet}`}
            disabled={busy}
            onClick={onClose}
          >
            取消
          </button>
          <button type="button" className={`${styles.trBtn} ${styles.danger}`} disabled={busy} onClick={onConfirm}>
            {busy ? <Spinner /> : null}
            {everything ? "清空回收站" : "永久删除"}
          </button>
        </>
      }
    >
      {!everything && <Subjects items={items} detail={(item) => sizeOf(item)} />}
      <p className={styles.trDialogText}>
        {everything
          ? `全部 ${items.length} 个项目（共 ${size}）将被立即删除，无法再恢复。`
          : single
            ? `${single.kind === "directory" ? "这个文件夹及其中的全部内容" : "这个文件"}将被立即删除，无法再恢复。`
            : `所选的 ${items.length} 个项目（共 ${size}）将被立即删除，无法再恢复。`}
      </p>
    </Dialog>
  );
}

// sizeOf is an item's size; a folder of empty files or one not yet indexed
// has none worth showing.
function sizeOf(item: TrashItem): string {
  if (item.kind === "directory") return item.sizeBytes > 0 ? `文件夹 · ${formatBytes(item.sizeBytes)}` : "文件夹";
  return formatBytes(item.sizeBytes);
}
