import { FolderPlus, Images } from "lucide-react";
import { ReactNode, useEffect, useState } from "react";

import { messageOf } from "./model";
import { listAlbums, thumbnailURL, type PhotoAlbum } from "./photosApi";

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
