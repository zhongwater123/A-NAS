import { FolderPlus, Images } from "lucide-react";
import { ReactNode, useEffect, useEffectEvent, useState } from "react";

import { listAlbums, type PhotoAlbum, thumbnailURL } from "./api";
import { messageOf } from "./model";
import styles from "./photos.module.css";

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
  const reportError = useEffectEvent(onError);
  useEffect(() => {
    let live = true;
    listAlbums(libraryId).then(
      (items) => {
        if (live) setAlbums(items);
      },
      (caught) => {
        if (live) {
          setAlbums([]);
          reportError(messageOf(caught));
        }
      },
    );
    return () => {
      live = false;
    };
  }, [libraryId, refreshKey]);
  return (
    <div className={styles.phCardsScroll} style={{ paddingTop: topInset }}>
      {albums === undefined ? (
        <div className={styles.phLoading}>
          <span className={styles.phSpinner} />
        </div>
      ) : (
        <div className={styles.phCards}>
          {!readOnly && (
            <button type="button" className={`${styles.phCard} ${styles.phCardNew}`} onClick={onCreate}>
              <span className={styles.phCardCover}>
                <FolderPlus />
              </span>
              <strong>新建相册</strong>
              <small>相册只是分组，照片不会被复制</small>
            </button>
          )}
          {albums.map((album) => (
            <button
              type="button"
              key={album.id}
              className={styles.phCard}
              aria-label={`打开相册 ${album.name}`}
              onClick={() => onOpen(album)}
            >
              <span className={styles.phCardCover}>
                {album.coverId ? <img src={thumbnailURL(album.coverId)} alt="" loading="lazy" /> : <Images />}
              </span>
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

export function Empty({
  icon,
  title,
  text,
  action,
}: {
  icon: ReactNode;
  title: string;
  text: string;
  action?: ReactNode;
}) {
  return (
    <div className={styles.phEmpty}>
      <span className={styles.phEmptyIcon}>{icon}</span>
      <strong>{title}</strong>
      <p>{text}</p>
      {action}
    </div>
  );
}
