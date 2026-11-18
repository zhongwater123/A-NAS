import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronLeft, Images, Upload, Video } from "lucide-react";
import { useCallback, useRef, useState } from "react";

import { errorText } from "../api/errorMessages";
import { listPhotoLibraries, type PhotoAlbum, type PhotoAsset } from "../photos/api";
import { PhotoPicker } from "../photos/PhotoPicker";
import { type UploadItem, useUploads } from "../photos/uploads";
import { Spinner } from "../ui/workspace";
import { addAssets, createProject } from "./api";
import styles from "./montage.module.css";

const states: Record<UploadItem["state"], string> = {
  waiting: "排队中",
  uploading: "上传中",
  done: "已上传",
  failed: "失败",
};

// NewProject makes a project of videos the member uploads now, through the
// photo library's upload queue into their private library, or picks from it
// (docs/specs/montage-center.md, 上传一批视频新建项目、从相册选视频). With
// into, the videos join that project instead. Only videos that uploaded go
// in: the failed may be retried or left out. It is laid out as the
// prototype's footage panel: a drop zone, or the photo library.
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
  const title = into ? "追加视频" : "选这次要剪的视频";

  const send = (files: FileList | null) => {
    if (!own || !files?.length) return;
    setSettled(false);
    uploads.add(
      [...files].filter((file) => file.type.startsWith("video/") || /\.(mov|mp4|m4v)$/i.test(file.name)),
      { libraryId: own.id, label: "我的图库" },
    );
  };

  return (
    <main className={styles.mtSub}>
      <button type="button" className={`${styles.mtBtn} ${styles.small} ${styles.mtBack}`} onClick={onCancel}>
        <ChevronLeft aria-hidden="true" />
        {into ? "返回项目" : "全部项目"}
      </button>
      <section
        className={`${styles.mtPanel} ${styles.mtStep} ${styles.media}`}
        aria-label={into ? "追加视频" : "新建剪辑项目"}
      >
        <div className={styles.mtStepH}>
          <span className={styles.mtBadge} aria-hidden="true">
            <Video />
          </span>
          <div>
            <div className={styles.mtStepN}>素材</div>
            <h2>{title}</h2>
          </div>
          {uploads.items.length > 0 && (
            <small>
              已上传 <b>{uploads.items.filter((item) => item.state === "done").length}</b> / {uploads.items.length}
            </small>
          )}
        </div>
        {libraries.error && <p className={styles.mtError}>{errorText(libraries.error)}</p>}
        {!libraries.data && !libraries.error ? (
          <Spinner />
        ) : !own ? (
          <p className={styles.mtNote}>找不到你的个人图库。</p>
        ) : (
          <>
            <div className={styles.mtDrop}>
              <div className={styles.mtDropIcon} aria-hidden="true">
                <Upload />
              </div>
              <b>上传视频</b>
              <p>从这台电脑选一批视频，存进你的个人图库，再交给剪辑中心</p>
              <button
                type="button"
                className={`${styles.mtBtn} ${styles.primary}`}
                onClick={() => input.current?.click()}
                disabled={busy}
              >
                <Upload aria-hidden="true" />
                选择视频
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
            <div className={styles.mtOr}>或者</div>
            <button type="button" className={styles.mtSample} onClick={() => setPicking(true)} disabled={busy}>
              <span className={styles.mtRowIcon} aria-hidden="true">
                <Images />
              </span>
              <span>
                <b>从相册选</b>
                <span>选个人图库里已有的视频，原片留在原处</span>
              </span>
            </button>
            {uploads.items.length > 0 && (
              <ul className={`${styles.mtRows} ${styles.mtHint}`} aria-label="上传" role="status">
                {uploads.items.map((item) => (
                  <li key={item.key} className={styles.mtRow} data-state={item.state}>
                    <span className={styles.mtRowName}>
                      <b>{item.name}</b>
                      {item.state === "failed" && item.error && <small>{item.error}</small>}
                      {item.state === "uploading" && (
                        <span className={styles.mtBar} aria-hidden="true">
                          <span style={{ width: `${Math.round(item.progress * 100)}%` }} />
                        </span>
                      )}
                    </span>
                    {item.state === "failed" && !item.rejected ? (
                      <button type="button" className={styles.mtLink} onClick={() => uploads.retry(item.key)}>
                        重试
                      </button>
                    ) : (
                      <span className={styles.mtRowEnd}>{states[item.state]}</span>
                    )}
                  </li>
                ))}
              </ul>
            )}
            {completedLive.length > 0 && (
              <p className={`${styles.mtNote} ${styles.mtHint}`}>
                {completedLive.length} 个 MOV 补进了图库里的实况照片，没有加入项目。
              </p>
            )}
            {make.error && <p className={`${styles.mtError} ${styles.mtHint}`}>{errorText(make.error)}</p>}
          </>
        )}
      </section>
      {settled && !busy && videos.length > 0 && (
        <div className={styles.mtFoot}>
          <button type="button" className={styles.mtBtn} onClick={onCancel}>
            取消
          </button>
          <button
            type="button"
            className={`${styles.mtBtn} ${styles.primary}`}
            disabled={make.isPending}
            onClick={() => make.mutate(videos.map((item) => item.assetId as string))}
          >
            {failed.length ? `只用上传成功的 ${videos.length} 段${verb}` : `${verb}（${videos.length} 段）`}
          </button>
        </div>
      )}
      {picking && own && (
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
    </main>
  );
}
