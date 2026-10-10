import { Plus, X } from "lucide-react";
import { FormEvent, useState } from "react";

import { PhotoAILabel, PhotoAlbum, PhotoAsset, addPhotoTag, addToAlbum, hideAILabel, removePhotoTag } from "./photosApi";

interface Props {
  // The photo's details; undefined while they load.
  asset?: PhotoAsset;
  // Whether the caller may tag the photo and hide its AI labels.
  canEdit: boolean;
  // Albums the caller may add the photo to.
  albums: PhotoAlbum[];
  onChange: (asset: PhotoAsset) => void;
  onTag: (tag: string) => void;
  onLabel: (label: PhotoAILabel) => void;
  onAdded: (album: PhotoAlbum, asset: PhotoAsset) => void;
  onError: (error: unknown) => void;
}

// PhotoMetadata shows a photo's user tags, AI labels and albums in the viewer.
// User tags come first; AI labels are marked as such and can be hidden on
// this photo; a photo from another library joins an album as a copy.
export function PhotoMetadata({ asset, canEdit, albums, onChange, onTag, onLabel, onAdded, onError }: Props) {
  const [tag, setTag] = useState("");
  if (!asset) return null;
  const tags = asset.tags ?? [];
  const labels = asset.aiLabels ?? [];
  const inAlbums = asset.albums ?? [];
  const candidates = albums.filter((album) => !inAlbums.some((member) => member.id === album.id));
  const run = async (action: () => Promise<PhotoAsset>) => {
    try { onChange(await action()); } catch (caught) { onError(caught); }
  };
  const submitTag = (event: FormEvent) => {
    event.preventDefault();
    const name = tag.trim();
    if (!name) return;
    setTag("");
    void run(() => addPhotoTag(asset.id, name));
  };

  return (
    <div className="photo-metadata">
      {(tags.length > 0 || canEdit) && (
        <div className="photo-labels" aria-label="标签">
          <span>标签</span>
          {tags.map((name) => (
            <span className="photo-chip" key={name}>
              <button onClick={() => onTag(name)}>{name}</button>
              {canEdit && <button aria-label={`删除标签 ${name}`} onClick={() => void run(() => removePhotoTag(asset.id, name))}><X size={11} /></button>}
            </span>
          ))}
          {canEdit && (
            <form className="photo-tag-form" onSubmit={submitTag}>
              <input aria-label="添加标签" placeholder="添加标签" maxLength={30} value={tag} onChange={(event) => setTag(event.target.value)} />
              <button type="submit" aria-label="保存标签"><Plus size={12} /></button>
            </form>
          )}
        </div>
      )}
      {labels.length > 0 && (
        <div className="photo-labels" aria-label="AI 标签">
          <span>本地 AI 识别</span>
          {labels.map((label) => (
            <span className="photo-chip ai" key={label.id}>
              <button title={`相似度 ${label.score.toFixed(2)}，点击查看同类照片`} onClick={() => onLabel(label)}>{label.name}</button>
              {canEdit && <button aria-label={`隐藏 AI 标签 ${label.name}`} title="识别错了？只对这张照片隐藏" onClick={() => void run(() => hideAILabel(asset.id, label.id))}><X size={11} /></button>}
            </span>
          ))}
        </div>
      )}
      {(inAlbums.length > 0 || candidates.length > 0) && (
        <div className="photo-labels" aria-label="相册">
          <span>相册</span>
          {inAlbums.map((album) => <span className="photo-chip" key={album.id}><span>{album.name}</span></span>)}
          {candidates.length > 0 && (
            <select aria-label="加入相册" value="" onChange={(event) => {
              const album = candidates.find((item) => item.id === event.target.value);
              if (album) void addToAlbum(album.id, asset.id).then((added) => onAdded(album, added), onError);
            }}>
              <option value="">加入相册…</option>
              {candidates.map((album) => <option key={album.id} value={album.id}>{album.name}</option>)}
            </select>
          )}
        </div>
      )}
    </div>
  );
}
