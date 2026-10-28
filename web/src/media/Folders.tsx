import { ChevronRight, Folder as FolderIcon, FolderOpen, FolderX, HardDrive } from "lucide-react";
import { useEffect, useState } from "react";

import { artworkURL, type FolderListing, type FolderRoot, getFolder, listFolderRoots } from "./api";
import { Empty, Loading, messageOf, PageHeader, TitleGrid } from "./Browse";
import styles from "./media.module.css";

export interface FolderPlace {
  entryId: string;
  path: string;
}

function Cover({ id }: { id?: string }) {
  const [failed, setFailed] = useState(false);
  if (!id || failed)
    return (
      <span className={`${styles.mcFolderCover} ${styles.empty}`}>
        <FolderIcon />
      </span>
    );
  return (
    <span className={styles.mcFolderCover}>
      <img src={artworkURL(id, "thumb")} alt="" loading="lazy" onError={() => setFailed(true)} />
      <FolderIcon />
    </span>
  );
}

// FoldersPage browses the media libraries as their folders.
export function FoldersPage({
  place,
  onPlace,
  refreshKey,
  onError,
}: {
  place?: FolderPlace;
  onPlace: (place?: FolderPlace) => void;
  refreshKey: number;
  onError: (message: string) => void;
}) {
  const [roots, setRoots] = useState<FolderRoot[]>();
  const [listing, setListing] = useState<FolderListing>();
  const [missing, setMissing] = useState(false);
  // Each load of a folder starts without the last failure; the root list
  // shows no folder.
  const [loadedFor, setLoadedFor] = useState({ place, refreshKey });
  if (loadedFor.place !== place || loadedFor.refreshKey !== refreshKey) {
    setLoadedFor({ place, refreshKey });
    if (place) setMissing(false);
    else setListing(undefined);
  }
  useEffect(() => {
    if (place) return;
    listFolderRoots().then(setRoots, (caught) => {
      setRoots([]);
      onError(messageOf(caught));
    });
  }, [place, refreshKey, onError]);
  useEffect(() => {
    if (!place) return;
    let live = true;
    getFolder(place.entryId, place.path).then(
      (value) => {
        if (live) setListing(value);
      },
      (caught) => {
        if (!live) return;
        setListing(undefined);
        setMissing(true);
        onError(messageOf(caught));
      },
    );
    return () => {
      live = false;
    };
  }, [place, refreshKey, onError]);

  if (!place) {
    return (
      <div className={styles.mcPage}>
        <PageHeader title="文件夹" count={roots?.length} />
        {!roots ? (
          <Loading />
        ) : !roots.length ? (
          <Empty icon={<FolderOpen />} title="还没有媒体库文件夹" text="新建媒体库后，可以在这里按文件夹浏览视频。" />
        ) : (
          <div className={styles.mcFolderGrid}>
            {roots.map((root) => (
              <button
                key={root.entryId}
                type="button"
                className={`${styles.mcFolder} ${root.missing ? styles.missing : ""}`}
                onClick={() => onPlace({ entryId: root.entryId, path: "" })}
                disabled={root.missing}
              >
                <Cover id={root.cover} />
                <span className={styles.mcFolderText}>
                  <strong>{root.name}</strong>
                  <small>{root.missing ? "文件夹不存在" : `${root.libraryName} · ${root.videos} 个视频`}</small>
                </span>
              </button>
            ))}
          </div>
        )}
      </div>
    );
  }
  const crumbs = place.path ? place.path.split("/") : [];
  return (
    <div className={styles.mcPage}>
      <nav className={styles.mcCrumbs} aria-label="文件夹路径">
        <button type="button" onClick={() => onPlace(undefined)}>
          <HardDrive />
          全部文件夹
        </button>
        <ChevronRight />
        <button
          type="button"
          onClick={() => onPlace({ entryId: place.entryId, path: "" })}
          aria-current={!crumbs.length ? "page" : undefined}
        >
          {listing?.root.name ?? "…"}
        </button>
        {crumbs.map((crumb, index) => (
          <span key={index} className={styles.mcCrumb}>
            <ChevronRight />
            <button
              type="button"
              aria-current={index === crumbs.length - 1 ? "page" : undefined}
              onClick={() => onPlace({ entryId: place.entryId, path: crumbs.slice(0, index + 1).join("/") })}
            >
              {crumb}
            </button>
          </span>
        ))}
      </nav>
      {missing ? (
        <Empty icon={<FolderX />} title="这个文件夹已经没有视频" text="它可能已被移动或删除。" />
      ) : !listing ? (
        <Loading />
      ) : (
        <>
          {listing.folders.length > 0 && (
            <div className={styles.mcFolderGrid}>
              {listing.folders.map((folder) => (
                <button
                  key={folder.path}
                  type="button"
                  className={styles.mcFolder}
                  onClick={() => onPlace({ entryId: place.entryId, path: folder.path })}
                >
                  <Cover id={folder.cover} />
                  <span className={styles.mcFolderText}>
                    <strong>{folder.name}</strong>
                    <small>{folder.videos} 个视频</small>
                  </span>
                </button>
              ))}
            </div>
          )}
          {listing.videos.length > 0 && <TitleGrid items={listing.videos} still />}
        </>
      )}
    </div>
  );
}
