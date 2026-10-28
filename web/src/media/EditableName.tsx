import { PenLine } from "lucide-react";
import { KeyboardEvent, ReactNode, useEffect, useRef, useState } from "react";

import { messageOf } from "./Browse";
import styles from "./media.module.css";

// splitName parts a file name into what is edited and its extension, which
// stays so the file remains a video the library reads.
export function splitName(name: string): { stem: string; extension: string } {
  const dot = name.lastIndexOf(".");
  return dot > 0 ? { stem: name.slice(0, dot), extension: name.slice(dot) } : { stem: name, extension: "" };
}

// EditableName shows a name that a double-click turns into a field in its
// place: Enter or leaving the field saves, Escape cancels. The field keeps
// the name's type size, so renaming reads as editing the name itself.
export function EditableName({
  as: Tag = "span",
  text,
  value,
  suffix = "",
  hint,
  label,
  className = "",
  onSave,
}: {
  as?: "h1" | "p" | "strong" | "span";
  text: ReactNode;
  value: string;
  suffix?: string;
  hint?: string;
  label: string;
  className?: string;
  onSave: (value: string) => Promise<void>;
}) {
  const [draft, setDraft] = useState<string>();
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const input = useRef<HTMLInputElement>(null);
  const editing = draft !== undefined;
  useEffect(() => {
    if (editing) {
      input.current?.focus();
      input.current?.select();
    }
  }, [editing]);
  const start = () => {
    if (!saving) {
      setDraft(value);
      setError("");
    }
  };
  const cancel = () => {
    setDraft(undefined);
    setError("");
  };
  const commit = async () => {
    if (draft === undefined || saving) return;
    const next = draft.trim();
    if (!next || next === value) {
      cancel();
      return;
    }
    if (/[\\/]/.test(next) || next.startsWith(".")) {
      setError("名称不能包含 / 或 \\，也不能以 . 开头");
      return;
    }
    setSaving(true);
    try {
      await onSave(next);
      setDraft(undefined);
      setError("");
    } catch (caught) {
      setError(messageOf(caught));
      input.current?.focus();
    } finally {
      setSaving(false);
    }
  };
  // Names sit in headings and paragraphs, where a form may not, so the keys
  // do what a form would.
  const keys = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      cancel();
    }
    if (event.key === "Enter" && !event.nativeEvent.isComposing) {
      event.preventDefault();
      void commit();
    }
  };
  if (!editing) {
    return (
      <Tag className={`${styles.mcEditable} ${className}`} onDoubleClick={start} title={`双击${label}`}>
        {text}
        <button
          type="button"
          className={styles.mcEditablePen}
          aria-label={label}
          onClick={start}
          onDoubleClick={(event) => event.stopPropagation()}
        >
          <PenLine />
        </button>
      </Tag>
    );
  }
  return (
    <Tag className={`${styles.mcEditable} ${styles.editing} ${className}`}>
      <span className={styles.mcEditableForm}>
        <span className={styles.mcEditableField}>
          <span className={styles.mcEditableSizer} aria-hidden="true">
            {draft || " "}
          </span>
          {/* size 1 lets the sizer alone set the field's width. */}
          <input
            ref={input}
            value={draft}
            aria-label={label}
            maxLength={240}
            size={1}
            disabled={saving}
            spellCheck={false}
            onChange={(event) => {
              setDraft(event.target.value);
              setError("");
            }}
            onKeyDown={keys}
            onBlur={() => (error ? cancel() : void commit())}
          />
        </span>
        {suffix && <span className={styles.mcEditableSuffix}>{suffix}</span>}
        {saving && <span className={`${styles.mcSpinner} ${styles.tiny}`} aria-label="正在重命名" />}
      </span>
      {(error || hint) && (
        <small className={`${styles.mcEditableNote} ${error ? styles.error : ""}`} role={error ? "alert" : undefined}>
          {error || hint}
        </small>
      )}
    </Tag>
  );
}
