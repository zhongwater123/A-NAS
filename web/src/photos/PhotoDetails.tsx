import { Copy, FolderPlus, Pencil, Plus, Sparkles, X } from "lucide-react";
import { FormEvent, useState } from "react";

import { type Caller, canChange, formatDateTime, formatSize, libraryName, libraryOf, messageOf, ownLibrary, sharedLibrary } from "./model";
import { addPhotoTag, hideAILabel, removePhotoTag, type PhotoAILabel, type PhotoAlbum, type PhotoAsset, type PhotoLibrary } from "./photosApi";

export interface DetailsActions {
  rename: (asset: PhotoAsset) => Promise<PhotoAsset | undefined>;
  copy: (asset: PhotoAsset, library: PhotoLibrary) => void;
  addToAlbum: (assets: PhotoAsset[]) => void;
  openAlbum: (album: Pick<PhotoAlbum, "id" | "name" | "libraryId">) => void;
  openLabel: (label: PhotoAILabel) => void;
  searchTag: (tag: string) => void;
  error: (message: string) => void;
}

interface Props {
  asset: PhotoAsset;
  // The photo's details with tags, labels and albums; undefined while they load.
  details?: PhotoAsset;
  caller: Caller;
  actions: DetailsActions;
  onChanged: (asset: PhotoAsset) => void;
}

// PhotoDetails is the viewer's side panel: when and what the photo is, its
// tags (the user's first, then what local AI sees) and its albums.
export function PhotoDetails({ asset, details, caller, actions, onChanged }: Props) {
  const [tag, setTag] = useState("");
  const current = details ?? asset;
  const editable = canChange(caller, asset);
  const library = libraryOf(caller, asset.libraryId);
  const writable = Boolean(library && !library.viewing);
  const mine = ownLibrary(caller);
  const shared = sharedLibrary(caller);
  const run = async (action: () => Promise<PhotoAsset>) => {
    try { onChanged(await action()); } catch (caught) { actions.error(messageOf(caught)); }
  };
  const submitTag = (event: FormEvent) => {
    event.preventDefault();
    const name = tag.trim();
    if (!name) return;
    setTag("");
    void run(() => addPhotoTag(asset.id, name));
  };
  const megapixels = current.width * current.height / 1e6;
  const tags = details?.tags ?? [];
  const labels = details?.aiLabels ?? [];
  const albums = details?.albums ?? [];

  return (
    <div className="ph-details">
      <div className="ph-details-name">
        <h3>{current.name}</h3>
        {editable && <button type="button" aria-label="重命名照片" title="重命名" onClick={() => void actions.rename(current).then((renamed) => renamed && onChanged(renamed))}><Pencil /></button>}
      </div>
      <p className="ph-details-when">{current.takenAt ? formatDateTime(current.takenAt) : "没有拍摄时间"}</p>
      <dl>
        {current.width > 0 && <><dt>尺寸</dt><dd>{current.width} × {current.height}{megapixels >= 1 ? ` · ${megapixels.toFixed(1)} MP` : ""}</dd></>}
        <dt>大小</dt><dd>{formatSize(current.sizeBytes)} · {current.mediaType === "image/png" ? "PNG" : "JPEG"}</dd>
        <dt>位置</dt><dd>{libraryName(caller, library)}</dd>
        <dt>导入</dt><dd>{formatDateTime(current.importedAt)}</dd>
        {current.duplicate && <><dt>重复</dt><dd>{current.duplicate === "first" ? "本图库中最早导入的一张" : "本图库中已有相同照片"}</dd></>}
      </dl>
      {current.alsoKeptBy?.length ? <p className="ph-details-hint">{current.alsoKeptBy.join("、")} 也保存了这张照片。</p> : null}

      <section aria-label="标签">
        <h4>标签</h4>
        <div className="ph-chips">
          {tags.map((name) => (
            <span className="ph-chip" key={name}>
              <button type="button" onClick={() => actions.searchTag(name)}>{name}</button>
              {editable && <button type="button" className="ph-chip-remove" aria-label={`删除标签 ${name}`} onClick={() => void run(() => removePhotoTag(asset.id, name))}><X /></button>}
            </span>
          ))}
          {editable && (
            <form className="ph-tag-form" onSubmit={submitTag}>
              <input aria-label="添加标签" placeholder="添加标签" maxLength={30} value={tag} onChange={(event) => setTag(event.target.value)} />
              <button type="submit" aria-label="保存标签" disabled={!tag.trim()}><Plus /></button>
            </form>
          )}
          {!tags.length && !editable && <span className="ph-muted">没有标签</span>}
        </div>
      </section>

      {labels.length > 0 && (
        <section aria-label="AI 标签">
          <h4><Sparkles />本地 AI 识别</h4>
          <div className="ph-chips">
            {labels.map((label) => (
              <span className="ph-chip ai" key={label.id}>
                <button type="button" title="查看同类照片" onClick={() => actions.openLabel(label)}>{label.name}</button>
                {editable && <button type="button" className="ph-chip-remove" aria-label={`隐藏 AI 标签 ${label.name}`} title="识别错了？只对这张照片隐藏" onClick={() => void run(() => hideAILabel(asset.id, label.id))}><X /></button>}
              </span>
            ))}
          </div>
        </section>
      )}

      <section aria-label="相册">
        <h4>相册</h4>
        <div className="ph-chips">
          {albums.map((album) => <span className="ph-chip" key={album.id}><button type="button" onClick={() => actions.openAlbum({ ...album, libraryId: asset.libraryId })}>{album.name}</button></span>)}
          {writable && <button type="button" className="ph-chip-add" onClick={() => actions.addToAlbum([current])}><FolderPlus />加入相册</button>}
          {!albums.length && !writable && <span className="ph-muted">不在任何相册中</span>}
        </div>
      </section>

      {writable && (
        <section className="ph-details-actions">
          {library?.kind === "private" && shared && <button type="button" onClick={() => actions.copy(current, shared)}><Copy />复制到共享图库</button>}
          {library?.kind === "shared" && mine && <button type="button" onClick={() => actions.copy(current, mine)}><Copy />复制到我的图库</button>}
        </section>
      )}
    </div>
  );
}
