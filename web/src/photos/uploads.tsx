import {
  ChevronDown,
  ChevronUp,
  CircleAlert,
  CircleCheck,
  Film,
  ImageUp,
  RotateCcw,
  ScanSearch,
  X,
} from "lucide-react";
import { useCallback, useEffect, useEffectEvent, useRef, useState } from "react";

import { groupPhotoUploads } from "./uploadGroups";
import { transferStore } from "../transfers/store";
import type { PendingPhoto } from "./layout";
import { messageOf } from "./model";
import { addToAlbum, uploadPhoto } from "./api";

export interface UploadTarget {
  libraryId: string;
  albumId?: string;
  label: string;
}
// rejected marks files refused before sending, which retrying cannot help.
// A video item is a video on its own: it becomes a video, or, a Live
// Photo's MOV, completes the stills already in the library that wait for
// it: completed counts them. liveStill marks a Live Photo still that
// arrived without its MOV.
export interface UploadItem extends PendingPhoto {
  file: File;
  motion?: File;
  target: UploadTarget;
  rejected?: boolean;
  video?: boolean;
  completed?: number;
  liveStill?: boolean;
}
// What a finished batch brought: the targets photos and videos went to, how
// many of each, how many photos a MOV completed, and how many Live Photo
// stills still lack theirs.
export interface UploadSummary {
  targets: UploadTarget[];
  photos: number;
  videos: number;
  completed: number;
  liveStills: number;
}

const accepted = new Set(["image/jpeg", "image/png", "image/heic", "image/heif"]);
// Web uploads stop at 256 MiB for a photo and 4 GiB for a video
// (photo-library and photo-video specs).
const maxBytes = 256 * 1024 ** 2;
const maxVideoBytes = 4 * 1024 ** 3;
const parallel = 2;

function refusal(file: File, motion: File | undefined, video: boolean | undefined) {
  if (!video && !accepted.has(file.type) && !/\.(jpe?g|png|heic|heif)$/i.test(file.name))
    return "支持 JPEG、PNG、HEIC 照片与 MP4、MOV 等常见视频";
  if (video) return file.size > maxVideoBytes ? "视频不能超过 4 GiB" : undefined;
  return file.size > maxBytes || (motion?.size ?? 0) > maxBytes ? "每张照片不能超过 256 MiB" : undefined;
}

// useUploads sends photos two at a time. Each shows at once from the file
// itself; onSettled runs once nothing is left to send, so views can load
// the uploaded photos, after which prune drops the finished previews.
export function useUploads(onSettled: (summary: UploadSummary) => void) {
  const [items, setItems] = useState<UploadItem[]>([]);
  const started = useRef(new Set<string>());
  const counter = useRef(0);
  const settled = useRef(new Set<string>());
  // The transfer center lists every photo as soon as it is added, as one
  // batch, not only the two being sent.
  const tracked = useRef(new Map<string, ReturnType<typeof transferStore.trackExternal>>());
  const update = useCallback(
    (key: string, change: Partial<UploadItem>) =>
      setItems((current) => current.map((item) => (item.key === key ? { ...item, ...change } : item))),
    [],
  );

  const add = useCallback(
    (files: File[], target: UploadTarget) => {
      const batch = `photos-${Date.now().toString(36)}-${counter.current + 1}`;
      const added = groupPhotoUploads(files).map(({ file, motion, video, error: pairError }): UploadItem => {
        const key = `upload-${++counter.current}`;
        const error = pairError ?? refusal(file, motion, video);
        const handle = transferStore.trackExternal(
          {
            key: `photos-${key}`,
            app: "photos",
            name: file.name,
            size: file.size + (motion?.size ?? 0),
            label: `相册 · ${target.label}`,
            batch,
          },
          { queued: !error },
        );
        if (error) handle.fail(error);
        tracked.current.set(key, handle);
        return {
          key,
          name: file.name,
          file,
          motion,
          video,
          target,
          url: URL.createObjectURL(file),
          aspect: video ? 16 / 9 : 4 / 3,
          progress: 0,
          state: error ? "failed" : "waiting",
          error,
          rejected: Boolean(error),
        };
      });
      setItems((current) => [...added, ...current]);
      for (const item of added) {
        // The grid lays a preview out by its shape once the browser knows it;
        // a video it cannot decode keeps a landscape frame.
        if (item.video) {
          const probe = document.createElement("video");
          probe.preload = "metadata";
          probe.onloadedmetadata = () => {
            if (probe.videoWidth && probe.videoHeight)
              update(item.key, { aspect: probe.videoWidth / probe.videoHeight });
            probe.removeAttribute("src");
          };
          probe.src = item.url;
          continue;
        }
        const image = new Image();
        image.onload = () => {
          if (image.naturalWidth && image.naturalHeight)
            update(item.key, { aspect: image.naturalWidth / image.naturalHeight });
        };
        image.src = item.url;
      }
    },
    [update],
  );

  useEffect(() => {
    const active = items.filter((item) => item.state === "uploading").length;
    // Newest first in the list, so the oldest waiting photos go first.
    const ready = items
      .filter((item) => item.state === "waiting" && !started.current.has(item.key))
      .reverse()
      .slice(0, Math.max(0, parallel - active));
    for (const item of ready) {
      started.current.add(item.key);
      update(item.key, { state: "uploading", progress: 0 });
      const handle = tracked.current.get(item.key);
      handle?.start();
      void (async () => {
        try {
          const onProgress = (progress: number) => {
            update(item.key, { progress });
            handle?.progress(progress);
          };
          const result = await uploadPhoto(item.target.libraryId, item.file, onProgress, undefined, item.motion);
          if ("completed" in result) {
            update(item.key, { state: "done", progress: 1, completed: result.completed.length });
          } else {
            if (item.target.albumId) await addToAlbum(item.target.albumId, result.id);
            update(item.key, { state: "done", progress: 1, liveStill: result.liveVideoMissing });
          }
          handle?.done();
        } catch (caught) {
          started.current.delete(item.key);
          update(item.key, { state: "failed", error: messageOf(caught) });
          handle?.fail(messageOf(caught));
        }
      })();
    }
    const busy = items.some((item) => item.state === "waiting" || item.state === "uploading");
    const finished = items.filter((item) => item.state === "done" && !settled.current.has(item.key));
    if (!busy && finished.length) {
      for (const item of finished) settled.current.add(item.key);
      const kept = finished.filter((item) => item.completed === undefined);
      const targets = new Map(
        kept.map((item) => [`${item.target.libraryId}/${item.target.albumId ?? ""}`, item.target]),
      );
      const videos = kept.filter((item) => item.video).length;
      onSettled({
        targets: [...targets.values()],
        photos: kept.length - videos,
        videos,
        completed: finished.reduce((sum, item) => sum + (item.completed ?? 0), 0),
        liveStills: kept.filter((item) => item.liveStill).length,
      });
    }
  }, [items, update, onSettled]);

  const remove = useCallback(
    (keep: (item: UploadItem) => boolean) =>
      setItems((current) => {
        for (const item of current) if (!keep(item) && item.url) URL.revokeObjectURL(item.url);
        return current.filter(keep);
      }),
    [],
  );
  const prune = useCallback(() => remove((item) => item.state !== "done"), [remove]);
  const clear = useCallback(() => remove((item) => item.state === "waiting" || item.state === "uploading"), [remove]);
  const retry = useCallback(
    (key: string) => update(key, { state: "waiting", error: undefined, progress: 0 }),
    [update],
  );
  const release = useEffectEvent(() => {
    for (const item of items) {
      if (item.url) URL.revokeObjectURL(item.url);
      // The queue lives in the photo window: photos not started stay unsent.
      if (item.state === "waiting") tracked.current.get(item.key)?.cancel("相册窗口已关闭，未上传");
    }
  });
  useEffect(() => () => release(), []);
  return { items, add, prune, clear, retry };
}

// UploadTray summarises the queue in the corner and lists it on demand.
export function UploadTray({
  items,
  onRetry,
  onClear,
}: {
  items: UploadItem[];
  onRetry: (key: string) => void;
  onClear: () => void;
}) {
  const [open, setOpen] = useState(false);
  if (!items.length) return null;
  const done = items.filter((item) => item.state === "done").length;
  const failed = items.filter((item) => item.state === "failed").length;
  const busy = items.length - done - failed;
  const progress =
    items.reduce((sum, item) => sum + (item.state === "done" ? 1 : item.state === "uploading" ? item.progress : 0), 0) /
    items.length;
  // Photos count in 张; with videos among them, items count in 项.
  const unit = items.some((item) => item.video) ? "项" : "张";
  const title = busy
    ? `正在上传，还剩 ${busy} ${unit}`
    : failed
      ? `${failed} ${unit}未能上传`
      : `已上传 ${done} ${unit}`;
  return (
    <section className={`ph-upload-tray${open ? " open" : ""}`} aria-label="上传队列">
      <header>
        {busy ? <ImageUp /> : failed ? <CircleAlert className="failed" /> : <CircleCheck className="done" />}
        <strong>{title}</strong>
        <button type="button" aria-label={open ? "收起上传队列" : "展开上传队列"} onClick={() => setOpen(!open)}>
          {open ? <ChevronDown /> : <ChevronUp />}
        </button>
        {!busy && (
          <button type="button" aria-label="关闭上传队列" onClick={onClear}>
            <X />
          </button>
        )}
      </header>
      <div className="ph-upload-bar">
        <span style={{ width: `${Math.round(progress * 100)}%` }} />
      </div>
      {open && (
        <ul>
          {items.map((item) => (
            <li key={item.key} className={item.state}>
              {item.video ? (
                <span className="ph-upload-video">
                  <Film />
                </span>
              ) : (
                <img src={item.url} alt="" />
              )}
              <span>
                <strong>{item.name}</strong>
                <small>
                  {item.state === "failed"
                    ? item.error
                    : item.state === "uploading"
                      ? `${Math.round(item.progress * 100)}%`
                      : item.state === "waiting"
                        ? "等待中"
                        : item.completed !== undefined
                          ? `已为 ${item.completed} 张照片补上实况视频`
                          : item.liveStill
                            ? `已加入${item.target.label} · 实况视频未上传`
                            : `已加入${item.target.label}`}
                </small>
              </span>
              {item.state === "failed" && !item.rejected && (
                <button type="button" aria-label={`重试上传 ${item.name}`} onClick={() => onRetry(item.key)}>
                  <RotateCcw />
                </button>
              )}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

// DropOverlay tells where files dropped on the window will go: a search
// box searches by an image dropped onto it instead.
export function DropOverlay({ target, search }: { target?: UploadTarget; search: boolean }) {
  if (search) {
    return (
      <div className="ph-drop search">
        <div>
          <ScanSearch />
          <strong>松开以图搜图</strong>
          <span>在图库中找出与这张图片相似的照片，图片不会被保存</span>
        </div>
      </div>
    );
  }
  return (
    <div className={`ph-drop${target ? "" : " refused"}`}>
      <div>
        <ImageUp />
        <strong>{target ? "松开即可上传" : "这里不能上传照片"}</strong>
        <span>{target ? `照片和视频会保存到${target.label}` : "只读查看时不能添加照片"}</span>
      </div>
    </div>
  );
}
