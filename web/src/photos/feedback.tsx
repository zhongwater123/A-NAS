import { CircleAlert, CircleCheck, X } from "lucide-react";
import { FormEvent, ReactNode, useCallback, useEffect, useRef, useState } from "react";

type DialogRequest =
  | { kind: "prompt"; title: string; label: string; value: string; confirm: string; maxLength: number; resolve: (value?: string) => void }
  | { kind: "confirm"; title: string; message: ReactNode; confirm: string; danger: boolean; resolve: (ok: boolean) => void };

// useDialogs replaces window.prompt and window.confirm with dialogs inside
// the photos window; each call resolves when the dialog closes.
export function useDialogs() {
  const [request, setRequest] = useState<DialogRequest>();
  const prompt = useCallback((options: { title: string; label: string; value?: string; confirm?: string; maxLength?: number }) =>
    new Promise<string | undefined>((resolve) => setRequest({ kind: "prompt", value: "", confirm: "确定", maxLength: 100, ...options, resolve })), []);
  const confirm = useCallback((options: { title: string; message: ReactNode; confirm: string; danger?: boolean }) =>
    new Promise<boolean>((resolve) => setRequest({ kind: "confirm", danger: false, ...options, resolve })), []);
  const close = (value?: string | boolean) => {
    if (!request) return;
    setRequest(undefined);
    if (request.kind === "prompt") request.resolve(typeof value === "string" ? value : undefined);
    else request.resolve(value === true);
  };
  const dialog = request ? <Dialog request={request} onClose={close} /> : null;
  return { prompt, confirm, dialog, open: Boolean(request) };
}

function Dialog({ request, onClose }: { request: DialogRequest; onClose: (value?: string | boolean) => void }) {
  const [value, setValue] = useState(request.kind === "prompt" ? request.value : "");
  const input = useRef<HTMLInputElement>(null);
  const button = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (input.current) { input.current.focus(); input.current.select(); }
    else button.current?.focus();
  }, []);
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (request.kind === "prompt") {
      const trimmed = value.trim();
      if (trimmed) onClose(trimmed);
    } else onClose(true);
  };
  return (
    <div className="ph-dialog-backdrop" onPointerDown={(event) => { if (event.target === event.currentTarget) onClose(); }}>
      <form className="ph-dialog" role="dialog" aria-modal="true" aria-label={request.title} onSubmit={submit} onKeyDown={(event) => { if (event.key === "Escape") { event.stopPropagation(); onClose(); } }}>
        <h3>{request.title}</h3>
        {request.kind === "prompt" ? (
          <label>
            <span>{request.label}</span>
            <input ref={input} value={value} maxLength={request.maxLength} onChange={(event) => setValue(event.target.value)} />
          </label>
        ) : <div className="ph-dialog-message">{request.message}</div>}
        <div className="ph-dialog-actions">
          <button type="button" onClick={() => onClose()}>取消</button>
          <button ref={button} type="submit" className={request.kind === "confirm" && request.danger ? "danger" : "primary"} disabled={request.kind === "prompt" && !value.trim()}>{request.confirm}</button>
        </div>
      </form>
    </div>
  );
}

export interface Toast { text: string; tone?: "done" | "error"; action?: { label: string; run: () => void }; details?: string[] }

// useToasts shows short outcomes at the bottom of the window; an action such
// as undo stays a little longer.
export function useToasts() {
  const [toasts, setToasts] = useState<Array<Toast & { id: number }>>([]);
  const next = useRef(0);
  const dismiss = useCallback((id: number) => setToasts((current) => current.filter((toast) => toast.id !== id)), []);
  const toast = useCallback((value: Toast) => {
    const id = ++next.current;
    setToasts((current) => [...current.slice(-2), { ...value, id }]);
    window.setTimeout(() => dismiss(id), value.action || value.tone === "error" ? 9000 : 4500);
  }, [dismiss]);
  const element = (
    <div className="ph-toasts" role="status" aria-live="polite">
      {toasts.map((item) => <ToastView key={item.id} toast={item} onClose={() => dismiss(item.id)} />)}
    </div>
  );
  return { toast, element };
}

function ToastView({ toast, onClose }: { toast: Toast; onClose: () => void }) {
  const [expanded, setExpanded] = useState(false);
  return (
    <div className={`ph-toast ${toast.tone ?? ""}`}>
      {toast.tone === "error" ? <CircleAlert /> : <CircleCheck />}
      <span>{toast.text}</span>
      {toast.details?.length ? <button type="button" onClick={() => setExpanded(!expanded)}>{expanded ? "收起" : "原因"}</button> : null}
      {toast.action && <button type="button" className="ph-toast-action" onClick={() => { toast.action?.run(); onClose(); }}>{toast.action.label}</button>}
      <button type="button" className="ph-toast-close" aria-label="关闭提示" onClick={onClose}><X /></button>
      {expanded && <ul>{toast.details?.map((detail, index) => <li key={index}>{detail}</li>)}</ul>}
    </div>
  );
}
