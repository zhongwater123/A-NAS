import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, Images, Upload } from "lucide-react";
import { useCallback, useRef, useState } from "react";

import { errorText } from "../api/errorMessages";
import { listPhotoLibraries, type PhotoAlbum, type PhotoAsset } from "../photos/api";
import { PhotoPicker } from "../photos/PhotoPicker";
import { type UploadItem, useUploads } from "../photos/uploads";
import { Notice, Spinner } from "../ui/workspace";
import workspaceStyles from "../ui/workspace.module.css";
import { addAssets, createProject } from "./api";
import styles from "./montage.module.css";

// NewProject makes a project of videos the member uploads now, through the
// photo library's upload queue into their private library, or picks from it
// (docs/specs/montage-center.md, 上传一批视频新建项目、从相册选视频). With
// into, the videos join that project instead. Only videos that uploaded go
// in: the failed may be retried or left out.
export function NewProject({
  userId,
  into,
  onDone,
  onCancel,
}: {
  userId: string;
  into?: string;
  onDone: (projectId: string) => void;
  onCancel: () => void;
}) {
  const client = useQueryClient();
  const libraries = useQuery({ queryKey: ["photos", "libraries"], queryFn: listPhotoLibraries });
  const own = libraries.data?.find(
    (library) => library.kind === "private" && library.ownerUserId === userId && !library.viewing,
  );
  const [settled, setSettled] = useState(false);
  const uploads = useUploads(useCallback(() => setSettled(true), []));
  const [picking, setPicking] = useState(false);
  const input = useRef<HTMLInputElement>(null);
  const make = useMutation({
    mutationFn: async (assetIds: string[]) =>
      into ? (await addAssets(into, assetIds)).id : (await createProject(assetIds)).id,
    onSuccess: (id) => {
      void client.invalidateQueries({ queryKey: ["montage"] });
      onDone(id);
    },
  });

  const videos = uploads.items.filter((item) => item.state === "done" && item.video && item.assetId);
  const completedLive = uploads.items.filter((item) => item.state === "done" && item.video && !item.assetId);
  const failed = uploads.items.filter((item) => item.state === "failed");
  const busy = uploads.items.some((item) => item.state === "waiting" || item.state === "uploading");
  const verb = into ? "加入项目" : "新建项目";

  const send = (files: FileList | null) => {
    if (!own || !files?.length) return;
    setSettled(false);
    uploads.add(
      [...files].filter((file) => file.type.startsWith("video/") || /\.(mov|mp4|m4v)$/i.test(file.name)),
      { libraryId: own.id, label: "我的图库" },
    );
  };

  return (
    <section className={styles.mtPage} aria-label={into ? "追加视频" : "新建剪辑项目"}>
      <header className={styles.mtHeader}>
        <button type="button" className={styles.mtBack} onClick={onCancel} aria-label="返回">
          <ArrowLeft />
        </button>
        <h2>{into ? "追加视频" : "新建剪辑项目"}</h2>
      </header>
      {libraries.error && <Notice tone="danger">{errorText(libraries.error)}</Notice>}
      {!libraries.data ? (
        <Spinner />
      ) : !own ? (
        <Notice tone="warning">找不到你的个人图库。</Notice>
      ) : (
        <>
          <div className={styles.mtChoices}>
            <button type="button" className={styles.mtChoice} onClick={() => input.current?.click()} disabled={busy}>
              <Upload />
              <strong>上传视频</strong>
              <small>存进你的个人图库，再交给剪辑中心</small>
            </button>
            <button type="button" className={styles.mtChoice} onClick={() => setPicking(true)} disabled={busy}>
              <Images />
              <strong>从相册选</strong>
              <small>选你能看到的视频，原片留在原来的图库</small>
            </button>
            <input
              ref={input}
              hidden
              type="file"
              multiple
              accept="video/*,.mov,.mp4,.m4v"
              aria-label="选择视频"
              onChange={(event) => {
                send(event.target.files);
                event.target.value = "";
              }}
            />
          </div>
          {uploads.items.length > 0 && (
            <UploadResults
              items={uploads.items}
              busy={busy}
              settled={settled}
              videos={videos.length}
              completedLive={completedLive.length}
              failed={failed}
              onRetry={uploads.retry}
            />
          )}
          {make.error && <Notice tone="danger">{errorText(make.error)}</Notice>}
          {settled && !busy && videos.length > 0 && (
            <footer className={styles.mtActions}>
              <button
                type="button"
                className={workspaceStyles.bkPrimary}
                disabled={make.isPending}
                onClick={() => make.mutate(videos.map((item) => item.assetId as string))}
              >
                {failed.length ? `只用上传成功的 ${videos.length} 段${verb}` : `${verb}（${videos.length} 段）`}
              </button>
            </footer>
          )}
          {picking && (
            <PhotoPicker
              // The picker browses one library's timeline: the member's own.
              album={{ id: "", libraryId: own.id, name: "我的图库" } as unknown as PhotoAlbum}
              onClose={() => setPicking(false)}
              onAdd={(assets: PhotoAsset[]) => {
                setPicking(false);
                const chosen = assets.filter((asset) => asset.mediaType.startsWith("video/")).map((asset) => asset.id);
                if (chosen.length) make.mutate(chosen);
              }}
            />
          )}
        </>
      )}
    </section>
  );
}

function UploadResults({
  items,
  busy,
  settled,
  videos,
  completedLive,
  failed,
  onRetry,
}: {
  items: UploadItem[];
  busy: boolean;
  settled: boolean;
  videos: number;
  completedLive: number;
  failed: UploadItem[];
  onRetry: (key: string) => void;
}) {
  if (busy || !settled) {
    const done = items.filter((item) => item.state === "done").length;
    return (
      <p className={styles.mtStatus} role="status">
        正在上传 {done} / {items.length}，进度也在传输中心里
      </p>
    );
  }
  return (
    <div className={styles.mtResults} role="status">
      <p>
        上传成功 {videos} 段视频{failed.length ? `，失败 ${failed.length} 个` : ""}
      </p>
      {completedLive > 0 && <p>{completedLive} 个 MOV 补进了图库里的实况照片，没有加入项目。</p>}
      {failed.length > 0 && (
        <ul>
          {failed.map((item) => (
            <li key={item.key}>
              {item.name}：{item.error}
              {!item.rejected && (
                <button type="button" className={styles.mtLink} onClick={() => onRetry(item.key)}>
                  重试
                </button>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
