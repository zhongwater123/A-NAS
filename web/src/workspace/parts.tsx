import { RotateCw, TriangleAlert } from "lucide-react";
import type { Ref } from "react";

import { errorText } from "../api/errorMessages";
import type { Kit } from "./kit";
import { isHTML } from "./words";
import styles from "./workspace.module.css";

// Failed is a read that failed and the way to try it again.
export function Failed({ error, onRetry }: { error: unknown; onRetry: () => void }) {
  return (
    <div className={styles.wsCentered}>
      <TriangleAlert className={styles.wsCenteredIcon} aria-hidden="true" />
      <p>{errorText(error, "workspace")}</p>
      <button type="button" className={styles.wsButton} onClick={onRetry}>
        <RotateCw size={15} aria-hidden="true" />
        重试
      </button>
    </div>
  );
}

// acceptHTML tells whether a file can be uploaded as a page, and says so when
// it cannot.
export function acceptHTML(file: File, toast: Kit["toast"]): boolean {
  if (isHTML(file)) return true;
  toast({ text: "只能上传 .html 文件", tone: "error" });
  return false;
}

// HTMLPicker is the hidden input behind an upload button: one HTML file.
export function HTMLPicker({
  ref,
  disabled,
  toast,
  onFile,
}: {
  ref: Ref<HTMLInputElement>;
  disabled?: boolean;
  toast: Kit["toast"];
  onFile: (file: File) => void;
}) {
  return (
    <input
      ref={ref}
      type="file"
      accept=".html,.htm,text/html"
      hidden
      disabled={disabled}
      onChange={(event) => {
        const file = event.target.files?.[0];
        event.target.value = "";
        if (file && acceptHTML(file, toast)) onFile(file);
      }}
    />
  );
}
