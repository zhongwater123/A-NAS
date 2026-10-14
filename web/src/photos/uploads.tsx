import { ChevronDown, ChevronUp, CircleAlert, CircleCheck, ImageUp, RotateCcw, X } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";

import { APIError } from "../api";
import { transferStore } from "../transfers/store";
import type { PendingPhoto } from "./layout";
import { messageOf } from "./model";
import { addToAlbum, uploadPhoto } from "./photosApi";

export interface UploadTarget { libraryId: string; albumId?: string; label: string }
// rejected marks files refused before sending, which retrying cannot help.
// uploadKey names the photo's upload for every try, so the NAS keeps one
// copy; assetId is the photo once made, so a retry only adds it to its album.
export interface UploadItem extends PendingPhoto { file: File; target: UploadTarget; rejected?: boolean; uploadKey: string; assetId?: string }

const accepted = new Set(["image/jpeg", "image/png"]);
// Web uploads of JPEG and PNG originals stop at 256 MiB (photo-library spec).
const maxBytes = 256 * 1024 ** 2;
const parallel = 2;
// Waits between tries while the NAS cannot be reached.
const retryDelays = [1000, 3000, 6000, 10000, 15000];
const waitingNote = "与 NAS 的连接中断，恢复后自动继续";

// newUploadKey is random; plain HTTP on the home network has no randomUUID.
function newUploadKey() {
  return Array.from(crypto.getRandomValues(new Uint8Array(16)), (byte) => byte.toString(16).padStart(2, "0")).join("");
}

// passing tells failures that waiting may cure: no connection or a stalled
// one, the NAS restarting behind its web entry, or its volume offline.
function passing(caught: unknown) {
  if (caught instanceof TypeError) return true;
  return caught instanceof APIError && [0, 423, 502, 503, 504].includes(caught.status);
}

function pause(ms: number, signal: AbortSignal) {
  return new Promise<void>((resolve, reject) => {
    const timer = setTimeout(resolve, ms);
    signal.addEventListener("abort", () => { clearTimeout(timer); reject(new DOMException("canceled", "AbortError")); }, { once: true });
  });
}

// useUploads sends photos two at a time. Each shows at once from the file
// itself; onSettled runs once nothing is left to send, so views can load
// the uploaded photos, after which prune drops the finished previews.
export function useUploads(onSettled: (targets: UploadTarget[]) => void) {
  const [items, setItems] = useState<UploadItem[]>([]);
  const started = useRef(new Set<string>());
  const counter = useRef(0);
  const settled = useRef(new Set<string>());
  // The transfer center lists every photo as soon as it is added, as one
  // batch, not only the two being sent.
  const tracked = useRef(new Map<string, ReturnType<typeof transferStore.trackExternal>>());
  const controllers = useRef(new Map<string, AbortController>());
  const update = useCallback((key: string, change: Partial<UploadItem>) => setItems((current) => current.map((item) => item.key === key ? { ...item, ...change } : item)), []);
  // cancel stops a photo waiting or being sent and drops it from the queue.
  const cancel = useCallback((key: string) => {
    controllers.current.get(key)?.abort();
    controllers.current.delete(key);
    started.current.delete(key);
    tracked.current.get(key)?.cancel("已取消");
    setItems((current) => current.filter((item) => {
      if (item.key !== key) return true;
      URL.revokeObjectURL(item.url);
      return false;
    }));
  }, []);
  const retry = useCallback((key: string) => update(key, { state: "waiting", error: undefined, progress: 0 }), [update]);

  const add = useCallback((files: File[], target: UploadTarget) => {
    const batch = `photos-${Date.now().toString(36)}-${counter.current + 1}`;
    const added = files.map((file): UploadItem => {
      const key = `upload-${++counter.current}`;
      const error = !accepted.has(file.type) ? "只支持 JPEG 和 PNG 照片" : file.size > maxBytes ? "照片超过 256 MB" : undefined;
      const controls = error ? undefined : { cancel: () => cancel(key), retry: () => retry(key) };
      const handle = transferStore.trackExternal({ key: `photos-${key}`, app: "photos", name: file.name, size: file.size, label: `相册 · ${target.label}`, batch, controls }, { queued: !error });
      if (error) handle.fail(error);
      tracked.current.set(key, handle);
      return { key, name: file.name, file, target, url: URL.createObjectURL(file), aspect: 4 / 3, progress: 0, state: error ? "failed" : "waiting", error, rejected: Boolean(error), uploadKey: newUploadKey() };
    });
    setItems((current) => [...added, ...current]);
    for (const item of added) {
      // The grid lays a preview out by its shape once the browser knows it.
      const image = new Image();
      image.onload = () => { if (image.naturalWidth && image.naturalHeight) update(item.key, { aspect: image.naturalWidth / image.naturalHeight }); };
      image.src = item.url;
    }
  }, [update, cancel, retry]);

  useEffect(() => {
    const active = items.filter((item) => item.state === "uploading").length;
    // Newest first in the list, so the oldest waiting photos go first.
    const ready = items.filter((item) => item.state === "waiting" && !started.current.has(item.key)).reverse().slice(0, Math.max(0, parallel - active));
    for (const item of ready) {
      started.current.add(item.key);
      update(item.key, { state: "uploading", progress: 0, note: undefined });
      const handle = tracked.current.get(item.key);
      handle?.start();
      const controller = new AbortController();
      controllers.current.set(item.key, controller);
      void (async () => {
        let assetId = item.assetId;
        // A lost or stalled connection is tried again with the same upload
        // key until it goes through or the photo is canceled.
        for (let attempt = 0; ; attempt += 1) {
          try {
            if (!assetId) {
              assetId = (await uploadPhoto(item.target.libraryId, item.file, (progress) => { update(item.key, { progress, note: undefined }); handle?.progress(progress); }, controller.signal, item.uploadKey)).id;
              update(item.key, { assetId });
            }
            if (item.target.albumId) await addToAlbum(item.target.albumId, assetId);
            controllers.current.delete(item.key);
            update(item.key, { state: "done", progress: 1, note: undefined });
            handle?.done();
            return;
          } catch (caught) {
            if (controller.signal.aborted) return;
            if (!passing(caught)) {
              controllers.current.delete(item.key);
              started.current.delete(item.key);
              update(item.key, { state: "failed", error: messageOf(caught), note: undefined });
              handle?.fail(messageOf(caught));
              return;
            }
            update(item.key, { progress: 0, note: waitingNote });
            handle?.note(waitingNote);
            try { await pause(retryDelays[Math.min(attempt, retryDelays.length - 1)], controller.signal); } catch { return; }
          }
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
  const itemsRef = useRef(items);
  itemsRef.current = items;
  useEffect(() => () => {
    for (const item of itemsRef.current) {
      URL.revokeObjectURL(item.url);
      // The queue lives in the photo window: photos not started stay unsent.
      if (item.state === "waiting") tracked.current.get(item.key)?.cancel("相册窗口已关闭，未上传");
    }
  }, []);
  return { items, add, prune, clear, retry, cancel };
}

// UploadTray summarises the queue in the corner and lists it on demand.
export function UploadTray({ items, onRetry, onCancel, onClear }: { items: UploadItem[]; onRetry: (key: string) => void; onCancel: (key: string) => void; onClear: () => void }) {
  const [open, setOpen] = useState(false);
  if (!items.length) return null;
  const done = items.filter((item) => item.state === "done").length;
  const failed = items.filter((item) => item.state === "failed").length;
  const busy = items.length - done - failed;
  const progress = items.reduce((sum, item) => sum + (item.state === "done" ? 1 : item.state === "uploading" ? item.progress : 0), 0) / items.length;
  const waiting = items.some((item) => item.state === "uploading" && item.note);
  const title = waiting ? `${waitingNote}，还剩 ${busy} 张` : busy ? `正在上传，还剩 ${busy} 张` : failed ? `${failed} 张未能上传` : `已上传 ${done} 张`;
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
              <span><strong>{item.name}</strong><small>{item.state === "failed" ? item.error : item.state === "done" ? `已加入${item.target.label}` : item.state === "uploading" ? item.note ?? `${Math.round(item.progress * 100)}%` : "等待中"}</small></span>
              {item.state === "failed" && !item.rejected && <button type="button" aria-label={`重试上传 ${item.name}`} onClick={() => onRetry(item.key)}><RotateCcw /></button>}
              {(item.state === "waiting" || item.state === "uploading") && <button type="button" aria-label={`取消上传 ${item.name}`} onClick={() => onCancel(item.key)}><X /></button>}
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
