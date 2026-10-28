// The calm workspace of 备份中心 and 回收站: spinner, empty state, notice,
// modal dialog and toasts, on the .bk-app root that sets their palette.
// They keep the bk- names they had in 备份中心.
import { CircleAlert, CircleCheck, FileText, Folder, X } from "lucide-react";
import { ReactNode, useCallback, useEffect, useRef, useState } from "react";

import workspaceStyles from "./workspace.module.css";

export function Spinner({ small }: { small?: boolean }) {
  return (
    <span className={`${workspaceStyles.bkSpinner}${small ? ` ${workspaceStyles.small}` : ""}`} aria-hidden="true" />
  );
}

// className lets a window's own stylesheet tell the icon apart.
export function EntryIcon({ kind, className = "" }: { kind: "file" | "directory"; className?: string }) {
  return (
    <span className={`${workspaceStyles.bkEntryIcon} ${workspaceStyles[kind] ?? ""} ${className}`} aria-hidden="true">
      {kind === "directory" ? <Folder /> : <FileText />}
    </span>
  );
}

export function EmptyState({
  icon,
  title,
  children,
  actions,
}: {
  icon: ReactNode;
  title: string;
  children?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <div className={workspaceStyles.bkEmpty}>
      <span className={workspaceStyles.bkEmptyArt} aria-hidden="true">
        {icon}
      </span>
      <strong>{title}</strong>
      {children && <p>{children}</p>}
      {actions && <div className={workspaceStyles.bkEmptyActions}>{actions}</div>}
    </div>
  );
}

export function Notice({ tone = "warning", children }: { tone?: "warning" | "danger" | "info"; children: ReactNode }) {
  return (
    <div
      className={`${workspaceStyles.bkNotice} ${workspaceStyles[tone] ?? ""}`}
      role={tone === "danger" ? "alert" : "status"}
    >
      <CircleAlert />
      {children}
    </div>
  );
}

// Dialog is a modal sheet over the backup center or the trash. Escape and the
// close button call onClose unless the dialog is busy.
export function Dialog({
  title,
  icon,
  tone = "accent",
  busy,
  wide,
  onClose,
  children,
  footer,
  label,
}: {
  title: string;
  icon: ReactNode;
  tone?: "accent" | "danger";
  busy?: boolean;
  wide?: boolean;
  onClose: () => void;
  children: ReactNode;
  footer: ReactNode;
  label?: string;
}) {
  const panel = useRef<HTMLDivElement>(null);
  useEffect(() => {
    // A confirmation starts on its safe choice, never on the destructive one.
    const target =
      panel.current?.querySelector<HTMLElement>("[data-autofocus], input") ??
      panel.current?.querySelector<HTMLElement>(`footer .${workspaceStyles.bkSecondary}`);
    target?.focus();
  }, []);
  return (
    <div
      className={workspaceStyles.bkBackdrop}
      onPointerDown={(event) => {
        if (event.target === event.currentTarget && !busy) onClose();
      }}
    >
      {/* Escape closes the dialog the focus is in, and only that one: a
          dialog stacked inside another stops it there, and a dialog in
          another window never hears it. A click inside keeps the focus here. */}
      {/* oxlint-disable-next-line jsx-a11y/no-noninteractive-element-interactions */}
      <div
        ref={panel}
        className={`${workspaceStyles.bkDialog}${wide ? ` ${workspaceStyles.wide}` : ""}`}
        role="dialog"
        aria-modal="true"
        aria-label={label ?? title}
        tabIndex={-1}
        onKeyDown={(event) => {
          if (event.key !== "Escape") return;
          event.stopPropagation();
          if (!busy) onClose();
        }}
      >
        <header>
          <span className={`${workspaceStyles.bkDialogIcon} ${workspaceStyles[tone] ?? ""}`} aria-hidden="true">
            {icon}
          </span>
          <h3>{title}</h3>
          <button
            type="button"
            className={workspaceStyles.bkIconButton}
            aria-label="关闭"
            disabled={busy}
            onClick={onClose}
          >
            <X />
          </button>
        </header>
        <div className={workspaceStyles.bkDialogBody}>{children}</div>
        <footer>{footer}</footer>
      </div>
    </div>
  );
}

export interface Toast {
  id: number;
  text: string;
  tone: "success" | "error";
  action?: { label: string; run: () => void };
}

export function useToasts() {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const next = useRef(0);
  const dismiss = useCallback((id: number) => setToasts((current) => current.filter((toast) => toast.id !== id)), []);
  const toast = useCallback(
    (text: string, tone: Toast["tone"] = "success", action?: Toast["action"]) => {
      if (!text) return;
      const id = ++next.current;
      setToasts((current) => [...current.slice(-2), { id, text, tone, action }]);
      window.setTimeout(() => dismiss(id), tone === "error" ? 7000 : 4500);
    },
    [dismiss],
  );
  const element = (
    <div className={workspaceStyles.bkToasts} aria-live="polite">
      {toasts.map((item) => (
        <div
          key={item.id}
          className={`${workspaceStyles.bkToast} ${workspaceStyles[item.tone] ?? ""}`}
          role={item.tone === "error" ? "alert" : "status"}
        >
          {item.tone === "error" ? <CircleAlert /> : <CircleCheck />}
          <span>{item.text}</span>
          {item.action && (
            <button
              type="button"
              onClick={() => {
                item.action?.run();
                dismiss(item.id);
              }}
            >
              {item.action.label}
            </button>
          )}
        </div>
      ))}
    </div>
  );
  return { toast, element };
}
