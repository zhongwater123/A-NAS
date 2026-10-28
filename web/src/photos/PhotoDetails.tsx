import { CircleDot, Copy, Film, FolderPlus, Pencil, Plus, X } from "lucide-react";
import { FormEvent, useState } from "react";

import { formatBytes } from "../lib/bytes";
import { addPhotoTag, type PhotoAlbum, type PhotoAsset, type PhotoLibrary, removePhotoTag } from "./api";
import {
  type Caller,
  canChange,
  canChangePeople,
  durationLabel,
  formatDateTime,
  libraryName,
  libraryOf,
  liveVideoHint,
  messageOf,
  ownLibrary,
  sharedLibrary,
  videoLines,
} from "./model";
import { PhotoFaces } from "./People";
import styles from "./photos.module.css";

export interface DetailsActions {
  rename: (asset: PhotoAsset) => Promise<PhotoAsset | undefined>;
  copy: (asset: PhotoAsset, library: PhotoLibrary) => void;
  addToAlbum: (assets: PhotoAsset[]) => void;
  openAlbum: (album: Pick<PhotoAlbum, "id" | "name" | "libraryId">) => void;
  openTag: (tag: string) => void;
  // Opens a person's photos.
  openPerson: (personId: string) => void;
  // The member library the viewer was opened from, if being viewed.
  viewing: string;
  // Uploads the MOV a Live Photo still came without.
  attachLiveVideo: (asset: PhotoAsset, file: File) => Promise<void>;
  error: (message: string) => void;
}

const mediaNames: Record<string, string> = {
  "image/png": "PNG",
  "image/heic": "HEIC",
  "image/jpeg": "JPEG",
  "video/mp4": "MP4",
  "video/quicktime": "MOV",
  "video/3gpp": "3GP",
  "video/webm": "WebM",
  "video/x-matroska": "MKV",
  "video/x-msvideo": "AVI",
  "video/mp2t": "MTS",
};

interface Props {
  asset: PhotoAsset;
  // The photo's details with tags and albums; undefined while they load.
  details?: PhotoAsset;
  caller: Caller;
  actions: DetailsActions;
  onChanged: (asset: PhotoAsset) => void;
}

// PhotoDetails is the viewer's side panel: when and what the photo or video
// is, who is in a photo, its tags and its albums.
export function PhotoDetails({ asset, details, caller, actions, onChanged }: Props) {
  const [tag, setTag] = useState("");
  const [attaching, setAttaching] = useState(false);
  const current = details ?? asset;
  const editable = canChange(caller, asset);
  const library = libraryOf(caller, asset.libraryId);
  const writable = Boolean(library && !library.viewing);
  const mine = ownLibrary(caller);
  const shared = sharedLibrary(caller);
  const run = async (action: () => Promise<PhotoAsset>) => {
    try {
      onChanged(await action());
    } catch (caught) {
      actions.error(messageOf(caught));
    }
  };
  const submitTag = (event: FormEvent) => {
    event.preventDefault();
    const name = tag.trim();
    if (!name) return;
    setTag("");
    void run(() => addPhotoTag(asset.id, name));
  };
  const pickLiveVideo = () => {
    const input = document.createElement("input");
    input.type = "file";
    input.accept = ".mov,video/quicktime";
    input.onchange = () => {
      const file = input.files?.[0];
      if (!file) return;
      setAttaching(true);
      void actions.attachLiveVideo(current, file).finally(() => setAttaching(false));
    };
    input.click();
  };
  const megapixels = (current.width * current.height) / 1e6;
  const video = current.video;
  const tags = details?.tags ?? [];
  const albums = details?.albums ?? [];

  return (
    <div className={styles.phDetails}>
      <div className={styles.phDetailsName}>
        <h3>{current.name}</h3>
        {editable && (
          <button
            type="button"
            aria-label={video ? "重命名视频" : "重命名照片"}
            title="重命名"
            onClick={() => void actions.rename(current).then((renamed) => renamed && onChanged(renamed))}
          >
            <Pencil />
          </button>
        )}
      </div>
      <p className={styles.phDetailsWhen}>{current.takenAt ? formatDateTime(current.takenAt) : "没有拍摄时间"}</p>
      <dl>
        {video && (
          <>
            <dt>时长</dt>
            <dd>{durationLabel(video.duration)}</dd>
          </>
        )}
        {current.width > 0 && (
          <>
            <dt>尺寸</dt>
            <dd>
              {current.width} × {current.height}
              {video
                ? ` · ${videoLines(current.width, current.height)}`
                : megapixels >= 1
                  ? ` · ${megapixels.toFixed(1)} MP`
                  : ""}
            </dd>
          </>
        )}
        <dt>大小</dt>
        <dd>
          {formatBytes(current.sizeBytes)} · {mediaNames[current.mediaType] ?? (video ? "视频" : "JPEG")}
        </dd>
        {current.motion && (
          <>
            <dt>动态</dt>
            <dd>
              {current.motion.kind === "live-photo" ? "实况照片" : "动态照片"} · {current.motion.duration.toFixed(1)} 秒
            </dd>
          </>
        )}
        <dt>位置</dt>
        <dd>{libraryName(caller, library)}</dd>
        <dt>导入</dt>
        <dd>{formatDateTime(current.importedAt)}</dd>
        {current.duplicate && (
          <>
            <dt>重复</dt>
            <dd>
              {current.duplicate === "first"
                ? `本图库中最早导入的一${video ? "段" : "张"}`
                : `本图库中已有相同${video ? "视频" : "照片"}`}
            </dd>
          </>
        )}
      </dl>
      {current.alsoKeptBy?.length ? (
        <p className={styles.phDetailsHint}>
          {current.alsoKeptBy.join("、")} 也保存了这{video ? "段视频" : "张照片"}。
        </p>
      ) : null}
      {current.liveVideoMissing && (
        <section className={styles.phDetailsLive} aria-label="实况照片">
          <p>
            <CircleDot />
            实况照片只上传了静态画面
          </p>
          <small>{liveVideoHint}</small>
          {editable && (
            <button type="button" disabled={attaching} onClick={pickLiveVideo}>
              {attaching ? <span className={styles.phSpinner} /> : <Film />}
              {attaching ? "正在上传视频" : "上传视频"}
            </button>
          )}
        </section>
      )}

      {/* Local AI does not look at videos, so they have no people. */}
      {!video && (
        <PhotoFaces
          asset={current}
          faces={details?.people ?? []}
          editable={canChangePeople(caller, asset.libraryId)}
          viewing={actions.viewing}
          onChanged={onChanged}
          onOpenPerson={actions.openPerson}
          onError={actions.error}
        />
      )}

      <section aria-label="标签">
        <h4>标签</h4>
        <div className={styles.phChips}>
          {tags.map((name) => (
            <span className={styles.phChip} key={name}>
              <button type="button" title="查看带这个标签的照片" onClick={() => actions.openTag(name)}>
                {name}
              </button>
              {editable && (
                <button
                  type="button"
                  className={styles.phChipRemove}
                  aria-label={`删除标签 ${name}`}
                  onClick={() => void run(() => removePhotoTag(asset.id, name))}
                >
                  <X />
                </button>
              )}
            </span>
          ))}
          {editable && (
            <form className={styles.phTagForm} onSubmit={submitTag}>
              <input
                aria-label="添加标签"
                placeholder="添加标签"
                maxLength={30}
                value={tag}
                onChange={(event) => setTag(event.target.value)}
              />
              <button type="submit" aria-label="保存标签" disabled={!tag.trim()}>
                <Plus />
              </button>
            </form>
          )}
          {!tags.length && !editable && <span className={styles.phMuted}>没有标签</span>}
        </div>
      </section>

      <section aria-label="相册">
        <h4>相册</h4>
        <div className={styles.phChips}>
          {albums.map((album) => (
            <span className={styles.phChip} key={album.id}>
              <button type="button" onClick={() => actions.openAlbum({ ...album, libraryId: asset.libraryId })}>
                {album.name}
              </button>
            </span>
          ))}
          {writable && (
            <button type="button" className={styles.phChipAdd} onClick={() => actions.addToAlbum([current])}>
              <FolderPlus />
              加入相册
            </button>
          )}
          {!albums.length && !writable && <span className={styles.phMuted}>不在任何相册中</span>}
        </div>
      </section>

      {writable && (
        <section className={styles.phDetailsActions}>
          {library?.kind === "private" && shared && (
            <button type="button" onClick={() => actions.copy(current, shared)}>
              <Copy />
              复制到共享图库
            </button>
          )}
          {library?.kind === "shared" && mine && (
            <button type="button" onClick={() => actions.copy(current, mine)}>
              <Copy />
              复制到我的图库
            </button>
          )}
        </section>
      )}
    </div>
  );
}
