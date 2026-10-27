import { Pencil, Trash2 } from "lucide-react";
import { FormEvent } from "react";

import type { FileEntry } from "../api.generated";
import styles from "./files.module.css";

// DeleteDialog confirms moving items to the trash.
export function DeleteDialog({
  entries,
  busy,
  onCancel,
  onConfirm,
}: {
  entries: FileEntry[];
  busy: boolean;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  return (
    <div className={styles.fileDialogBackdrop}>
      <section
        className={styles.fileConfirmDialog}
        role="alertdialog"
        aria-modal="true"
        aria-labelledby="file-delete-title"
      >
        <span className={styles.fileConfirmIcon}>
          <Trash2 />
        </span>
        <div>
          <h3 id="file-delete-title">移到回收站？</h3>
          <p>
            {entries.length === 1 ? `“${entries[0].name}”` : `选中的 ${entries.length} 个项目`}
            将移到回收站，可在 30 天内恢复。
          </p>
        </div>
        <div className={styles.fileConfirmActions}>
          <button disabled={busy} onClick={onCancel}>
            取消
          </button>
          <button className={styles.dangerButton} disabled={busy} onClick={onConfirm}>
            {busy ? "正在移动…" : "移到回收站"}
          </button>
        </div>
      </section>
    </div>
  );
}

// RenameDialog asks for an item's new name.
export function RenameDialog({
  entry,
  busy,
  onCancel,
  onSubmit,
}: {
  entry: FileEntry;
  busy: boolean;
  onCancel: () => void;
  onSubmit: (event: FormEvent<HTMLFormElement>) => void;
}) {
  return (
    <div className={styles.fileDialogBackdrop}>
      <form className={styles.fileRenameDialog} aria-label={`重命名 ${entry.name}`} onSubmit={onSubmit}>
        <span>
          <Pencil />
        </span>
        <div>
          <h3>重命名</h3>
          <p>为“{entry.name}”输入新名称。</p>
          <input autoFocus name="name" aria-label="新名称" defaultValue={entry.name} required />
        </div>
        <footer>
          <button type="button" disabled={busy} onClick={onCancel}>
            取消
          </button>
          <button className="primary" disabled={busy}>
            保存
          </button>
        </footer>
      </form>
    </div>
  );
}
