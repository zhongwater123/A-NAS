import { Check, CircleAlert, CircleCheck, Layers, Plus, X } from "lucide-react";
import { FormEvent, ReactNode, useCallback, useEffect, useRef, useState } from "react";

import { messageOf } from "./Browse";
import { addToCollection, createCollection, listCollections, type Collection, type Title } from "./mediaApi";

type DialogRequest =
  | { kind: "prompt"; title: string; label: string; value: string; confirm: string; maxLength: number; resolve: (value?: string) => void }
  | { kind: "confirm"; title: string; message: ReactNode; confirm: string; danger: boolean; resolve: (ok: boolean) => void };

// useDialogs shows prompts and confirmations inside the media center.
export function useDialogs() {
  const [request, setRequest] = useState<DialogRequest>();
  const prompt = useCallback((options: { title: string; label: string; value?: string; confirm?: string; maxLength?: number }) =>
    new Promise<string | undefined>((resolve) => setRequest({ kind: "prompt", value: "", confirm: "确定", maxLength: 40, ...options, resolve })), []);
  const confirm = useCallback((options: { title: string; message: ReactNode; confirm: string; danger?: boolean }) =>
    new Promise<boolean>((resolve) => setRequest({ kind: "confirm", danger: false, ...options, resolve })), []);
  const close = (value?: string | boolean) => {
    if (!request) return;
    setRequest(undefined);
    if (request.kind === "prompt") request.resolve(typeof value === "string" ? value : undefined);
    else request.resolve(value === true);
  };
  return { prompt, confirm, dialog: request ? <Dialog request={request} onClose={close} /> : null };
}

function Dialog({ request, onClose }: { request: DialogRequest; onClose: (value?: string | boolean) => void }) {
  const [value, setValue] = useState(request.kind === "prompt" ? request.value : "");
  const input = useRef<HTMLInputElement>(null);
  const button = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (input.current) { input.current.focus(); input.current.select(); } else button.current?.focus();
  }, []);
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (request.kind === "prompt") { if (value.trim()) onClose(value.trim()); } else onClose(true);
  };
  return (
    <div className="mc-dialog-backdrop" onPointerDown={(event) => { if (event.target === event.currentTarget) onClose(); }}>
      <form className="mc-dialog" role="dialog" aria-modal="true" aria-label={request.title} onSubmit={submit}
        onKeyDown={(event) => { if (event.key === "Escape") { event.stopPropagation(); onClose(); } }}>
        <h3>{request.title}</h3>
        {request.kind === "prompt" ? (
          <label className="mc-field"><span>{request.label}</span><input ref={input} value={value} maxLength={request.maxLength} onChange={(event) => setValue(event.target.value)} /></label>
        ) : <div className="mc-dialog-message">{request.message}</div>}
        <footer className="mc-dialog-actions">
          <span />
          <button type="button" className="mc-glass" onClick={() => onClose()}>取消</button>
          <button ref={button} type="submit" className={request.kind === "confirm" && request.danger ? "mc-danger" : "mc-primary"} disabled={request.kind === "prompt" && !value.trim()}>{request.confirm}</button>
        </footer>
      </form>
    </div>
  );
}

export interface Toast { text: string; tone?: "done" | "error"; action?: { label: string; run: () => void } }

export function useToasts() {
  const [toasts, setToasts] = useState<Array<Toast & { id: number }>>([]);
  const next = useRef(0);
  const dismiss = useCallback((id: number) => setToasts((current) => current.filter((toast) => toast.id !== id)), []);
  const toast = useCallback((value: Toast) => {
    const id = ++next.current;
    setToasts((current) => [...current.slice(-2), { ...value, id }]);
    window.setTimeout(() => dismiss(id), value.action || value.tone === "error" ? 8000 : 3500);
  }, [dismiss]);
  const element = (
    <div className="mc-toasts" role="status" aria-live="polite">
      {toasts.map((item) => (
        <div key={item.id} className={`mc-toast ${item.tone ?? ""}`}>
          {item.tone === "error" ? <CircleAlert /> : <CircleCheck />}
          <span>{item.text}</span>
          {item.action && <button type="button" className="mc-toast-action" onClick={() => { item.action?.run(); dismiss(item.id); }}>{item.action.label}</button>}
          <button type="button" className="mc-toast-close" aria-label="关闭提示" onClick={() => dismiss(item.id)}><X /></button>
        </div>
      ))}
    </div>
  );
  return { toast, element };
}

// CollectionPicker adds a title to one of the user's collections, or to a
// new one.
export function CollectionPicker({ title, onClose, onDone }: { title: Title; onClose: () => void; onDone: (message: string) => void }) {
  const [collections, setCollections] = useState<Collection[]>();
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => { listCollections().then((items) => setCollections(items.filter((item) => !item.automatic)), (caught) => setError(messageOf(caught))); }, []);
  const target = title.type === "episode" && title.showId ? title.showId : title.id;
  const add = async (collection: Collection) => {
    setBusy(true);
    try { await addToCollection(collection.id, [target]); onDone(`已加入“${collection.name}”`); }
    catch (caught) { setError(messageOf(caught)); setBusy(false); }
  };
  const create = async (event: FormEvent) => {
    event.preventDefault();
    if (!name.trim()) return;
    setBusy(true);
    try { const collection = await createCollection(name.trim(), [target]); onDone(`已新建合集“${collection.name}”`); }
    catch (caught) { setError(messageOf(caught)); setBusy(false); }
  };
  return (
    <div className="mc-dialog-backdrop" onPointerDown={(event) => { if (event.target === event.currentTarget) onClose(); }}>
      <div className="mc-dialog" role="dialog" aria-modal="true" aria-label="加入合集" onKeyDown={(event) => { if (event.key === "Escape") { event.stopPropagation(); onClose(); } }}>
        <h3>加入合集</h3>
        <p className="mc-dialog-sub">{title.type === "episode" ? title.showTitle : title.title}</p>
        <form className="mc-new-collection" onSubmit={(event) => void create(event)}>
          <Plus />
          <input autoFocus value={name} maxLength={40} placeholder="新建合集，例如：周末片单" onChange={(event) => setName(event.target.value)} aria-label="新合集名称" />
          <button type="submit" className="mc-primary small" disabled={busy || !name.trim()}>新建并加入</button>
        </form>
        <div className="mc-collection-list">
          {!collections ? <span className="mc-spinner small" /> : collections.map((collection) => (
            <button key={collection.id} type="button" disabled={busy} onClick={() => void add(collection)}>
              <Layers /><span>{collection.name}</span><small>{collection.count} 部</small><Check className="mc-collection-add" />
            </button>
          ))}
          {collections && !collections.length && <p className="mc-picker-empty">还没有合集，在上面新建一个</p>}
        </div>
        {error && <p className="mc-form-error" role="alert">{error}</p>}
        <footer className="mc-dialog-actions"><span /><button type="button" className="mc-glass" onClick={onClose}>完成</button></footer>
      </div>
    </div>
  );
}
