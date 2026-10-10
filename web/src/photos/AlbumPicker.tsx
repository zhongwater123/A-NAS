import { FolderPlus, Images, X } from "lucide-react";
import { FormEvent, useEffect, useState } from "react";

import { type Caller, canEditAlbum, libraryName, ownLibrary, sharedLibrary } from "./model";
import { listAlbums, thumbnailURL, type PhotoAlbum, type PhotoAsset, type PhotoLibrary } from "./photosApi";

interface Props {
  caller: Caller;
  assets: PhotoAsset[];
  onPick: (album: PhotoAlbum) => void;
  onCreate: (library: PhotoLibrary, name: string) => void;
  onClose: () => void;
}

// AlbumPicker chooses the album photos join: one of their own library, or
// one of the caller's other library, which copies them there first.
export function AlbumPicker({ caller, assets, onPick, onCreate, onClose }: Props) {
  const home = new Set(assets.map((asset) => asset.libraryId));
  const libraries = [ownLibrary(caller), sharedLibrary(caller)].filter((library): library is PhotoLibrary => Boolean(library))
    .sort((a, b) => Number(home.has(b.id)) - Number(home.has(a.id)));
  const [albums, setAlbums] = useState<Map<string, PhotoAlbum[]>>();
  const [name, setName] = useState("");
  const [creating, setCreating] = useState(false);
  useEffect(() => {
    let live = true;
    void Promise.all(libraries.map(async (library) => [library.id, await listAlbums(library.id).catch(() => [])] as const))
      .then((lists) => { if (live) setAlbums(new Map(lists)); });
    return () => { live = false; };
  }, []);
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (name.trim() && libraries[0]) onCreate(libraries[0], name.trim());
  };
  const copies = (library: PhotoLibrary) => assets.some((asset) => asset.libraryId !== library.id);

  return (
    <div className="ph-dialog-backdrop" onPointerDown={(event) => { if (event.target === event.currentTarget) onClose(); }}>
      <div className="ph-dialog ph-picker" role="dialog" aria-modal="true" aria-label="加入相册" onKeyDown={(event) => { if (event.key === "Escape") { event.stopPropagation(); onClose(); } }}>
        <header>
          <h3>加入相册</h3>
          <small>{assets.length} 张照片</small>
          <button type="button" className="ph-dialog-close" aria-label="关闭" onClick={onClose}><X /></button>
        </header>
        {albums === undefined ? <div className="ph-loading"><span className="ph-spinner" /></div> : (
          <div className="ph-picker-list">
            {libraries.map((library) => {
              const editable = (albums.get(library.id) ?? []).filter((album) => canEditAlbum(caller, album));
              return (
                <section key={library.id}>
                  <h4><span>{libraryName(caller, library)}</span>{copies(library) && <small>照片会先复制到{libraryName(caller, library)}</small>}</h4>
                  {editable.map((album) => (
                    <button type="button" key={album.id} className="ph-picker-item" onClick={() => onPick(album)}>
                      <span className="ph-album-thumb">{album.coverId ? <img src={thumbnailURL(album.coverId)} alt="" /> : <Images />}</span>
                      <span>{album.name}</span>
                      <small>{album.photos} 张</small>
                    </button>
                  ))}
                  {!editable.length && <p className="ph-muted">还没有你可以编辑的相册</p>}
                </section>
              );
            })}
          </div>
        )}
        {libraries[0] && (creating ? (
          <form className="ph-picker-new" onSubmit={submit}>
            <input autoFocus aria-label="新相册名称" placeholder={`在${libraryName(caller, libraries[0])}新建相册`} maxLength={100} value={name} onChange={(event) => setName(event.target.value)} />
            <button type="submit" className="primary" disabled={!name.trim()}>创建并加入</button>
          </form>
        ) : (
          <button type="button" className="ph-picker-create" onClick={() => setCreating(true)}><FolderPlus />新建相册</button>
        ))}
      </div>
    </div>
  );
}
