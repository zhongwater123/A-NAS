import { CircleAlert, CircleCheck, FileText, Folder, HardDrive, Minus, Plus, UsersRound, X } from "lucide-react";
import { ReactNode, useCallback, useEffect, useRef, useState } from "react";

import type { Space } from "../api";

import type { SnapshotOrigin } from "./backupApi";
import { spaceLabel } from "./format";

export function Spinner({ small }: { small?: boolean }) {
  return <span className={`bk-spinner${small ? " small" : ""}`} aria-hidden="true" />;
}

export function SpaceIcon({ space }: { space: Pick<Space, "kind"> }) {
  return <span className={`bk-space-icon ${space.kind}`} aria-hidden="true">{space.kind === "shared" ? <UsersRound /> : <HardDrive />}</span>;
}

export function EntryIcon({ kind }: { kind: "file" | "directory" }) {
  return <span className={`bk-entry-icon ${kind}`} aria-hidden="true">{kind === "directory" ? <Folder /> : <FileText />}</span>;
}

export function OriginBadge({ origin }: { origin: SnapshotOrigin }) {
  return <span className={`bk-badge ${origin}`}>{origin === "scheduled" ? "定时" : "手动"}</span>;
}

// SpaceSwitcher picks the space a page works on.
export function SpaceSwitcher({ spaces, value, onChange }: { spaces: Space[]; value: string; onChange: (id: string) => void }) {
  if (spaces.length < 2) return null;
  return (
    <div className="bk-segmented" role="radiogroup" aria-label="空间">
      {spaces.map((space) => (
        <button key={space.id} type="button" role="radio" aria-checked={space.id === value} className={space.id === value ? "active" : ""} onClick={() => onChange(space.id)}>
          {space.kind === "shared" ? <UsersRound /> : <HardDrive />}{spaceLabel(space)}
        </button>
      ))}
    </div>
  );
}

export function Switch({ checked, disabled, label, onChange }: { checked: boolean; disabled?: boolean; label: string; onChange: (checked: boolean) => void }) {
  return (
    <button type="button" role="switch" aria-checked={checked} aria-label={label} disabled={disabled} className={`bk-switch${checked ? " on" : ""}`} onClick={() => onChange(!checked)}>
      <span />
    </button>
  );
}

// Stepper edits a small whole number with buttons or by typing.
export function Stepper({ value, min, max, label, disabled, onChange }: {
  value: number; min: number; max: number; label: string; disabled?: boolean; onChange: (value: number) => void;
}) {
  return (
    <div className="bk-stepper">
      <button type="button" aria-label={`${label}减一`} disabled={disabled || value <= min} onClick={() => onChange(value - 1)}><Minus /></button>
      <input
        type="number" min={min} max={max} value={value} disabled={disabled} aria-label={label}
        onChange={(event) => { const next = Math.round(Number(event.target.value)); if (Number.isFinite(next)) onChange(Math.min(max, Math.max(min, next))); }}
      />
      <button type="button" aria-label={`${label}加一`} disabled={disabled || value >= max} onClick={() => onChange(value + 1)}><Plus /></button>
    </div>
  );
}

export function EmptyState({ icon, title, children, actions }: { icon: ReactNode; title: string; children?: ReactNode; actions?: ReactNode }) {
  return (
    <div className="bk-empty">
      <span className="bk-empty-art" aria-hidden="true">{icon}</span>
      <strong>{title}</strong>
      {children && <p>{children}</p>}
      {actions && <div className="bk-empty-actions">{actions}</div>}
    </div>
  );
}

export function Notice({ tone = "warning", children }: { tone?: "warning" | "danger" | "info"; children: ReactNode }) {
  return <div className={`bk-notice ${tone}`} role={tone === "danger" ? "alert" : "status"}><CircleAlert />{children}</div>;
}

// Dialog is a modal sheet over the backup center. Escape and the close button
// call onClose unless the dialog is busy.
export function Dialog({ title, icon, tone = "accent", busy, wide, onClose, children, footer, label }: {
  title: string; icon: ReactNode; tone?: "accent" | "danger"; busy?: boolean; wide?: boolean; onClose: () => void; children: ReactNode; footer: ReactNode; label?: string;
}) {
  const panel = useRef<HTMLDivElement>(null);
  useEffect(() => {
    // A confirmation starts on its safe choice, never on the destructive one.
    const target = panel.current?.querySelector<HTMLElement>("[data-autofocus], input") ?? panel.current?.querySelector<HTMLElement>("footer .bk-secondary");
    target?.focus();
  }, []);
  useEffect(() => {
    // Only the topmost of stacked dialogs answers Escape.
    const onKey = (event: KeyboardEvent) => {
      const dialogs = document.querySelectorAll(".bk-dialog");
      if (event.key === "Escape" && !busy && dialogs[dialogs.length - 1] === panel.current) onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [busy, onClose]);
  return (
    <div className="bk-backdrop" onPointerDown={(event) => { if (event.target === event.currentTarget && !busy) onClose(); }}>
      <div ref={panel} className={`bk-dialog${wide ? " wide" : ""}`} role="dialog" aria-modal="true" aria-label={label ?? title}>
        <header>
          <span className={`bk-dialog-icon ${tone}`} aria-hidden="true">{icon}</span>
          <h3>{title}</h3>
          <button type="button" className="bk-icon-button" aria-label="关闭" disabled={busy} onClick={onClose}><X /></button>
        </header>
        <div className="bk-dialog-body">{children}</div>
        <footer>{footer}</footer>
      </div>
    </div>
  );
}

export interface Toast { id: number; text: string; tone: "success" | "error"; action?: { label: string; run: () => void } }

export function useToasts() {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const next = useRef(0);
  const dismiss = useCallback((id: number) => setToasts((current) => current.filter((toast) => toast.id !== id)), []);
  const toast = useCallback((text: string, tone: Toast["tone"] = "success", action?: Toast["action"]) => {
    if (!text) return;
    const id = ++next.current;
    setToasts((current) => [...current.slice(-2), { id, text, tone, action }]);
    window.setTimeout(() => dismiss(id), tone === "error" ? 7000 : 4500);
  }, [dismiss]);
  const element = (
    <div className="bk-toasts" aria-live="polite">
      {toasts.map((item) => (
        <div key={item.id} className={`bk-toast ${item.tone}`} role={item.tone === "error" ? "alert" : "status"}>
          {item.tone === "error" ? <CircleAlert /> : <CircleCheck />}
          <span>{item.text}</span>
          {item.action && <button type="button" onClick={() => { item.action?.run(); dismiss(item.id); }}>{item.action.label}</button>}
        </div>
      ))}
    </div>
  );
  return { toast, element };
}
