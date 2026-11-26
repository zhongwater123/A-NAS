import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronLeft, Images, Upload, Video } from "lucide-react";
import { useCallback, useRef, useState } from "react";

import { errorText } from "../../api/errorMessages";
import { listPhotoLibraries, type PhotoAsset, type PhotoLibrary } from "../../photos/api";
import { type Caller, libraryName, ownLibrary } from "../../photos/model";
import { PhotoPicker } from "../../photos/PhotoPicker";
import { isVideoFile } from "../../photos/uploadGroups";
import { type UploadItem, useUploads } from "../../photos/uploads";
import { Spinner } from "../../ui/workspace";
import { addAssets, createProject } from "../api/client";
import styles from "./videocut.module.css";

const states: Record<UploadItem["state"], string> = {
  waiting: "排队中",
  uploading: "上传中",
  done: "已上传",
  failed: "失败",
};

// NewProject makes a project of videos the member uploads now, through the
// photo library's upload queue into their private library, or picks from a
// library they can see, their own or the shared one
// (docs/specs/videocut-center.md, 上传一批视频新建项目、从相册选视频). With
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
  // The libraries videos may come from: the member's own and the shared one,
  // never another member's that an administrator is only viewing. They are
  // named as the photo library names them.
  const caller: Caller = { userId, isAdmin: false, libraries: libraries.data ?? [] };
  const own = ownLibrary(caller);
  const visible = caller.libraries.filter(
    (library) => !library.viewing && (library.kind === "shared" || library === own),
  );
  const nameOf = (library: PhotoLibrary) => libraryName(caller, library);
  const uploads = useUploads(useCallback(() => undefined, []));
  const [picking, setPicking] = useState<PhotoLibrary>();
  const input = useRef<HTMLInputElement>(null);
  const make = useMutation({
    mutationFn: async (assetIds: string[]) =>
      into ? (await addAssets(into, assetIds)).id : (await createProject(assetIds)).id,
    onSuccess: (id) => {
      void client.invalidateQueries({ queryKey: ["videocut"] });
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
    // What the photo library's own upload takes as a video, by type or by name.
    uploads.add([...files].filter(isVideoFile), { libraryId: own.id, label: nameOf(own) });
  };

  return (
    <main className={styles.mtSub}>
      <button type="button" className={`${styles.mtBtn} ${styles.small} ${styles.mtBack}`} onClick={onCancel}>
        <ChevronLeft aria-hidden="true" />
        {into ? "返回项目" : "全部项目"}
      </button>
      <section
        className={`${styles.mtPanel} ${styles.mtStep} ${styles.media}`}
        aria-label={into ? "添加视频" : "新建剪辑项目"}
      >
        <div className={styles.mtStepH}>
          <span className={styles.mtBadge} aria-hidden="true">
            <Video />
          </span>
          <div>
            <h2>{into ? "添加视频" : "新建项目"}</h2>
          </div>
          {uploads.items.length > 0 && (
            <small>
              已上传 <b>{uploads.items.filter((item) => item.state === "done").length}</b> / {uploads.items.length}
            </small>
          )}
        </div>
        {libraries.error && <p className={styles.mtError}>{errorText(libraries.error, "photos")}</p>}
        {!libraries.data && !libraries.error ? (
          <Spinner />
        ) : !own ? (
          <p className={styles.mtNote}>找不到你的个人图库</p>
        ) : (
          <>
            <div className={styles.mtDrop}>
              <div className={styles.mtDropIcon} aria-hidden="true">
                <Upload />
              </div>
              <b>上传视频</b>
              <p>视频存进你的个人图库</p>
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
                accept="video/*,.mov,.mp4,.m4v,.mkv,.webm,.3gp,.mts,.m2ts,.avi"
                aria-label="选择视频"
                onChange={(event) => {
                  send(event.target.files);
                  event.target.value = "";
                }}
              />
            </div>
            <div className={styles.mtOr}>或者</div>
            {visible.map((library) => (
              <button
                key={library.id}
                type="button"
                className={styles.mtSample}
                onClick={() => setPicking(library)}
                disabled={busy}
              >
                <span className={styles.mtRowIcon} aria-hidden="true">
                  <Images />
                </span>
                <span>
                  <b>从{nameOf(library)}选</b>
                  <span>原片留在原来的图库</span>
                </span>
              </button>
            ))}
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
                {completedLive.length} 个 MOV 补进了图库里的实况照片，没有加入项目
              </p>
            )}
            {make.error && <p className={`${styles.mtError} ${styles.mtHint}`}>{errorText(make.error, "videocut")}</p>}
          </>
        )}
      </section>
      {/* Whatever became of the batches after it, what uploaded can make the project. */}
      {!busy && videos.length > 0 && (
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
      {picking && (
        <PhotoPicker
          album={{ libraryId: picking.id, name: nameOf(picking) }}
          title={`从${nameOf(picking)}选视频`}
          videos
          onClose={() => setPicking(undefined)}
          onAdd={(assets: PhotoAsset[]) => {
            setPicking(undefined);
            if (assets.length) make.mutate(assets.map((asset) => asset.id));
          }}
        />
      )}
    </main>
  );
}
