import { ChevronRight, Folder as FolderIcon, FolderOpen, FolderX, HardDrive } from "lucide-react";
import { useEffect, useState } from "react";

import { Empty, Loading, messageOf, PageHeader, TitleGrid } from "./Browse";
import { artworkURL, getFolder, listFolderRoots, type FolderListing, type FolderRoot } from "./mediaApi";

export interface FolderPlace { entryId: string; path: string }

function Cover({ id }: { id?: string }) {
  const [failed, setFailed] = useState(false);
  if (!id || failed) return <span className="mc-folder-cover empty"><FolderIcon /></span>;
  return <span className="mc-folder-cover"><img src={artworkURL(id, "thumb")} alt="" loading="lazy" onError={() => setFailed(true)} /><FolderIcon /></span>;
}

// FoldersPage browses the media libraries as their folders.
export function FoldersPage({ place, onPlace, refreshKey, onError }: {
  place?: FolderPlace; onPlace: (place?: FolderPlace) => void; refreshKey: number; onError: (message: string) => void;
}) {
  const [roots, setRoots] = useState<FolderRoot[]>();
  const [listing, setListing] = useState<FolderListing>();
  const [missing, setMissing] = useState(false);
  useEffect(() => {
    if (place) return;
    listFolderRoots().then(setRoots, (caught) => { setRoots([]); onError(messageOf(caught)); });
  }, [place, refreshKey, onError]);
  useEffect(() => {
    if (!place) { setListing(undefined); return; }
    let live = true;
    setMissing(false);
    getFolder(place.entryId, place.path).then((value) => { if (live) setListing(value); }, (caught) => {
      if (!live) return;
      setListing(undefined);
      setMissing(true);
      onError(messageOf(caught));
    });
    return () => { live = false; };
  }, [place, refreshKey, onError]);

  if (!place) {
    return (
      <div className="mc-page">
        <PageHeader title="文件夹" count={roots?.length} />
        {!roots ? <Loading /> : !roots.length ? (
          <Empty icon={<FolderOpen />} title="还没有媒体库文件夹" text="新建媒体库后，可以在这里按文件夹浏览视频。" />
        ) : (
          <div className="mc-folder-grid">
            {roots.map((root) => (
              <button key={root.entryId} type="button" className={`mc-folder ${root.missing ? "missing" : ""}`} onClick={() => onPlace({ entryId: root.entryId, path: "" })} disabled={root.missing}>
                <Cover id={root.cover} />
                <span className="mc-folder-text">
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
    <div className="mc-page">
      <nav className="mc-crumbs" aria-label="文件夹路径">
        <button type="button" onClick={() => onPlace(undefined)}><HardDrive />全部文件夹</button>
        <ChevronRight />
        <button type="button" onClick={() => onPlace({ entryId: place.entryId, path: "" })} aria-current={!crumbs.length ? "page" : undefined}>{listing?.root.name ?? "…"}</button>
        {crumbs.map((crumb, index) => (
          <span key={index} className="mc-crumb">
            <ChevronRight />
            <button type="button" aria-current={index === crumbs.length - 1 ? "page" : undefined} onClick={() => onPlace({ entryId: place.entryId, path: crumbs.slice(0, index + 1).join("/") })}>{crumb}</button>
          </span>
        ))}
      </nav>
      {missing ? <Empty icon={<FolderX />} title="这个文件夹已经没有视频" text="它可能已被移动或删除。" /> : !listing ? <Loading /> : (
        <>
          {listing.folders.length > 0 && (
            <div className="mc-folder-grid">
              {listing.folders.map((folder) => (
                <button key={folder.path} type="button" className="mc-folder" onClick={() => onPlace({ entryId: place.entryId, path: folder.path })}>
                  <Cover id={folder.cover} />
                  <span className="mc-folder-text"><strong>{folder.name}</strong><small>{folder.videos} 个视频</small></span>
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
