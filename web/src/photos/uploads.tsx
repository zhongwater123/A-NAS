import { ChevronDown, ChevronUp, CircleAlert, CircleCheck, ImageUp, RotateCcw, X } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";

import type { PendingPhoto } from "./layout";
import { messageOf } from "./model";
import { addToAlbum, uploadPhoto } from "./photosApi";

export interface UploadTarget { libraryId: string; albumId?: string; label: string }
// rejected marks files refused before sending, which retrying cannot help.
export interface UploadItem extends PendingPhoto { file: File; target: UploadTarget; rejected?: boolean }

const accepted = new Set(["image/jpeg", "image/png"]);
// Web uploads of JPEG and PNG originals stop at 256 MiB (photo-library spec).
const maxBytes = 256 * 1024 ** 2;
const parallel = 2;

// useUploads sends photos two at a time. Each shows at once from the file
// itself; onSettled runs once nothing is left to send, so views can load
// the uploaded photos, after which prune drops the finished previews.
export function useUploads(onSettled: (targets: UploadTarget[]) => void) {
  const [items, setItems] = useState<UploadItem[]>([]);
  const started = useRef(new Set<string>());
  const counter = useRef(0);
  const settled = useRef(new Set<string>());
  const update = useCallback((key: string, change: Partial<UploadItem>) => setItems((current) => current.map((item) => item.key === key ? { ...item, ...change } : item)), []);

  const add = useCallback((files: File[], target: UploadTarget) => {
    const added = files.map((file): UploadItem => {
      const key = `upload-${++counter.current}`;
      const error = !accepted.has(file.type) ? "只支持 JPEG 和 PNG 照片" : file.size > maxBytes ? "照片超过 256 MB" : undefined;
      return { key, name: file.name, file, target, url: URL.createObjectURL(file), aspect: 4 / 3, progress: 0, state: error ? "failed" : "waiting", error, rejected: Boolean(error) };
    });
    setItems((current) => [...added, ...current]);
    for (const item of added) {
      // The grid lays a preview out by its shape once the browser knows it.
      const image = new Image();
      image.onload = () => { if (image.naturalWidth && image.naturalHeight) update(item.key, { aspect: image.naturalWidth / image.naturalHeight }); };
      image.src = item.url;
    }
  }, [update]);

  useEffect(() => {
    const active = items.filter((item) => item.state === "uploading").length;
    // Newest first in the list, so the oldest waiting photos go first.
    const ready = items.filter((item) => item.state === "waiting" && !started.current.has(item.key)).reverse().slice(0, Math.max(0, parallel - active));
    for (const item of ready) {
      started.current.add(item.key);
      update(item.key, { state: "uploading", progress: 0 });
      void (async () => {
        try {
          const asset = await uploadPhoto(item.target.libraryId, item.file, (progress) => update(item.key, { progress }));
          if (item.target.albumId) await addToAlbum(item.target.albumId, asset.id);
          update(item.key, { state: "done", progress: 1 });
        } catch (caught) {
          started.current.delete(item.key);
          update(item.key, { state: "failed", error: messageOf(caught) });
        }
      })();
    }
    const busy = items.some((item) => item.state === "waiting" || item.state === "uploading");
    const finished = items.filter((item) => item.state === "done" && !settled.current.has(item.key));
    if (!busy && finished.length) {
      for (const item of finished) settled.current.add(item.key);
      const targets = new Map(finished.map((item) => [`${item.target.libraryId}/${item.target.albumId ?? ""}`, item.target]));
      onSettled([...targets.values()]);
    }
  }, [items, update, onSettled]);

  const remove = useCallback((keep: (item: UploadItem) => boolean) => setItems((current) => {
    for (const item of current) if (!keep(item)) URL.revokeObjectURL(item.url);
    return current.filter(keep);
  }), []);
  const prune = useCallback(() => remove((item) => item.state !== "done"), [remove]);
  const clear = useCallback(() => remove((item) => item.state === "waiting" || item.state === "uploading"), [remove]);
  const retry = useCallback((key: string) => update(key, { state: "waiting", error: undefined, progress: 0 }), [update]);
  const itemsRef = useRef(items);
  itemsRef.current = items;
  useEffect(() => () => { for (const item of itemsRef.current) URL.revokeObjectURL(item.url); }, []);
  return { items, add, prune, clear, retry };
}

// UploadTray summarises the queue in the corner and lists it on demand.
export function UploadTray({ items, onRetry, onClear }: { items: UploadItem[]; onRetry: (key: string) => void; onClear: () => void }) {
  const [open, setOpen] = useState(false);
  if (!items.length) return null;
  const done = items.filter((item) => item.state === "done").length;
  const failed = items.filter((item) => item.state === "failed").length;
  const busy = items.length - done - failed;
  const progress = items.reduce((sum, item) => sum + (item.state === "done" ? 1 : item.state === "uploading" ? item.progress : 0), 0) / items.length;
  const title = busy ? `正在上传，还剩 ${busy} 张` : failed ? `${failed} 张未能上传` : `已上传 ${done} 张`;
  return (
    <section className={`ph-upload-tray${open ? " open" : ""}`} aria-label="上传队列">
      <header>
        {busy ? <ImageUp /> : failed ? <CircleAlert className="failed" /> : <CircleCheck className="done" />}
        <strong>{title}</strong>
        <button type="button" aria-label={open ? "收起上传队列" : "展开上传队列"} onClick={() => setOpen(!open)}>{open ? <ChevronDown /> : <ChevronUp />}</button>
        {!busy && <button type="button" aria-label="关闭上传队列" onClick={onClear}><X /></button>}
      </header>
      <div className="ph-upload-bar"><span style={{ width: `${Math.round(progress * 100)}%` }} /></div>
      {open && (
        <ul>
          {items.map((item) => (
            <li key={item.key} className={item.state}>
              <img src={item.url} alt="" />
              <span><strong>{item.name}</strong><small>{item.state === "failed" ? item.error : item.state === "done" ? `已加入${item.target.label}` : item.state === "uploading" ? `${Math.round(item.progress * 100)}%` : "等待中"}</small></span>
              {item.state === "failed" && !item.rejected && <button type="button" aria-label={`重试上传 ${item.name}`} onClick={() => onRetry(item.key)}><RotateCcw /></button>}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

// DropOverlay tells where files dropped on the window will go.
export function DropOverlay({ target }: { target?: UploadTarget }) {
  return (
    <div className={`ph-drop${target ? "" : " refused"}`}>
      <div>
        <ImageUp />
        <strong>{target ? "松开即可上传" : "这里不能上传照片"}</strong>
        <span>{target ? `照片会保存到${target.label}` : "只读查看时不能添加照片"}</span>
      </div>
    </div>
  );
}
