import { FolderPlus, Images, ScanEye, Sparkles } from "lucide-react";
import { ReactNode, useEffect, useState } from "react";

import { messageOf } from "./model";
import { listAlbums, listPhotoLabels, thumbnailURL, type PhotoAIStatus, type PhotoAlbum, type PhotoLabelCount } from "./photosApi";

interface AlbumsProps {
  libraryId: string;
  readOnly: boolean;
  refreshKey: number;
  topInset: number;
  onOpen: (album: PhotoAlbum) => void;
  onCreate: () => void;
  onError: (message: string) => void;
}

// AlbumsView shows a library's albums as covers.
export function AlbumsView({ libraryId, readOnly, refreshKey, topInset, onOpen, onCreate, onError }: AlbumsProps) {
  const [albums, setAlbums] = useState<PhotoAlbum[]>();
  useEffect(() => {
    let live = true;
    listAlbums(libraryId).then((items) => { if (live) setAlbums(items); }, (caught) => { if (live) { setAlbums([]); onError(messageOf(caught)); } });
    return () => { live = false; };
  }, [libraryId, refreshKey]);
  return (
    <div className="ph-cards-scroll" style={{ paddingTop: topInset }}>
      {albums === undefined ? <div className="ph-loading"><span className="ph-spinner" /></div> : (
        <div className="ph-cards">
          {!readOnly && (
            <button type="button" className="ph-card ph-card-new" onClick={onCreate}>
              <span className="ph-card-cover"><FolderPlus /></span>
              <strong>新建相册</strong>
              <small>相册只是分组，照片不会被复制</small>
            </button>
          )}
          {albums.map((album) => (
            <button type="button" key={album.id} className="ph-card" aria-label={`打开相册 ${album.name}`} onClick={() => onOpen(album)}>
              <span className="ph-card-cover">{album.coverId ? <img src={thumbnailURL(album.coverId)} alt="" loading="lazy" /> : <Images />}</span>
              <strong>{album.name}</strong>
              <small>{album.photos} 张</small>
            </button>
          ))}
          {readOnly && !albums.length && <Empty icon={<Images />} title="没有相册" text="这个图库还没有相册。" />}
        </div>
      )}
    </div>
  );
}

interface ThingsProps {
  viewing: string;
  refreshKey: number;
  topInset: number;
  ai?: PhotoAIStatus;
  onOpen: (label: PhotoLabelCount) => void;
  onError: (message: string) => void;
}

// ThingsView lists what local AI recognises in the photos, by how often.
export function ThingsView({ viewing, refreshKey, topInset, ai, onOpen, onError }: ThingsProps) {
  const [labels, setLabels] = useState<{ items: PhotoLabelCount[]; ready: boolean }>();
  useEffect(() => {
    let live = true;
    listPhotoLabels(viewing).then((value) => { if (live) setLabels(value); }, (caught) => { if (live) { setLabels({ items: [], ready: false }); onError(messageOf(caught)); } });
    return () => { live = false; };
  }, [viewing, refreshKey]);
  if (!labels) return <div className="ph-cards-scroll" style={{ paddingTop: topInset }}><div className="ph-loading"><span className="ph-spinner" /></div></div>;
  if (!labels.ready || !labels.items.length) {
    const text = ai?.state === "unavailable" || !labels.ready && !ai
      ? "此设备还没有启用本地 AI。启用后，它会在空闲时认出照片里的猫、海滩、蛋糕等事物。"
      : !labels.ready ? "本地 AI 正在准备识别用的词表，稍后再来看看。"
        : "本地 AI 还没在照片里认出熟悉的事物。整理完更多照片后会出现在这里。";
    return <div className="ph-cards-scroll" style={{ paddingTop: topInset }}><Empty icon={<ScanEye />} title="识别的事物" text={text} /></div>;
  }
  return (
    <div className="ph-cards-scroll" style={{ paddingTop: topInset }}>
      <p className="ph-cards-note"><Sparkles />本地 AI 在照片里认出的事物，识别可能有误；在照片信息中可以隐藏错误的标签。</p>
      <div className="ph-cards things">
        {labels.items.map((label) => (
          <button type="button" key={label.id} className="ph-card ph-thing" aria-label={`查看 ${label.name}`} onClick={() => onOpen(label)}>
            <span className="ph-card-cover"><img src={thumbnailURL(label.coverId)} alt="" loading="lazy" /></span>
            <strong>{label.name}</strong>
            <small>{label.photos} 张</small>
          </button>
        ))}
      </div>
    </div>
  );
}

export function Empty({ icon, title, text, action }: { icon: ReactNode; title: string; text: string; action?: ReactNode }) {
  return (
    <div className="ph-empty">
      <span className="ph-empty-icon">{icon}</span>
      <strong>{title}</strong>
      <p>{text}</p>
      {action}
    </div>
  );
}
