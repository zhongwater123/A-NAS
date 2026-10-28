import { CircleMinus, Copy, Download, FolderMinus, FolderPlus, RotateCcw, Trash2 } from "lucide-react";

import { type Caller, canChange, canEditAlbum, libraryOf, ownLibrary, type Place, sharedLibrary } from "./model";
import styles from "./photos.module.css";
import type { useSelection } from "./selection";
import type { PhotoActions } from "./usePhotoActions";

// SelectionActions are the toolbar's buttons while photos are selected: what
// the place allows with all of them, or how far the action under way is.
export function SelectionActions({
  place,
  caller,
  selection,
  actions,
}: {
  place: Place;
  caller: Caller;
  selection: ReturnType<typeof useSelection>;
  actions: PhotoActions;
}) {
  const {
    chosen,
    busy,
    setPicker,
    restore,
    purge,
    copyTo,
    download,
    removeFromCurrentAlbum,
    takeOutOfCluster,
    untag,
    takeOutOfPerson,
    trash,
  } = actions;
  const mine = ownLibrary(caller);
  const shared = sharedLibrary(caller);
  const selectedAll = chosen.length > 0 && chosen.length === selection.selected.size;
  const allChangeable = selectedAll && chosen.every((asset) => canChange(caller, asset));
  const allWritable = selectedAll && chosen.every((asset) => !libraryOf(caller, asset.libraryId)?.viewing);
  const copyTarget = !allWritable
    ? undefined
    : chosen.every((asset) => asset.libraryId === mine?.id)
      ? shared
      : chosen.every((asset) => asset.libraryId === shared?.id)
        ? mine
        : undefined;

  return busy ? (
    <span className={styles.phSelectionBusy}>
      <span className={styles.phSpinner} />
      {busy.label} {busy.done}/{busy.total}
    </span>
  ) : place.kind === "trash" ? (
    <>
      <button type="button" onClick={() => void restore(chosen, true)}>
        <RotateCcw />
        <span>恢复</span>
      </button>
      <button type="button" className={styles.danger} onClick={() => void purge(chosen)}>
        <Trash2 />
        <span>永久删除</span>
      </button>
    </>
  ) : (
    <>
      {allWritable && (
        <button type="button" onClick={() => setPicker(chosen)}>
          <FolderPlus />
          <span>加入相册</span>
        </button>
      )}
      {copyTarget && (
        <button type="button" onClick={() => void copyTo(copyTarget, chosen)}>
          <Copy />
          <span>复制到{copyTarget.kind === "shared" ? "共享" : "我的图库"}</span>
        </button>
      )}
      <button type="button" onClick={() => void download(chosen)}>
        <Download />
        <span>下载</span>
      </button>
      {place.kind === "album" && canEditAlbum(caller, place.album) && (
        <button type="button" onClick={() => void removeFromCurrentAlbum(chosen)}>
          <FolderMinus />
          <span>移出相册</span>
        </button>
      )}
      {place.kind === "cluster" && allChangeable && (
        <button type="button" title="AI 归错了？只把这些照片移出这一类" onClick={() => void takeOutOfCluster(chosen)}>
          <CircleMinus />
          <span>不是{place.label.name}</span>
        </button>
      )}
      {place.kind === "tag" && allChangeable && (
        <button type="button" onClick={() => void untag(chosen)}>
          <CircleMinus />
          <span>去掉标签</span>
        </button>
      )}
      {place.kind === "person" && place.person.canChange && (
        <button
          type="button"
          title="认错了？把这些照片移出，本地 AI 不会再放回来"
          onClick={() => void takeOutOfPerson(chosen)}
        >
          <CircleMinus />
          <span>不是{place.person.name || "TA"}</span>
        </button>
      )}
      {allChangeable && (
        <button type="button" className={styles.danger} onClick={() => void trash(chosen)}>
          <Trash2 />
          <span>删除</span>
        </button>
      )}
    </>
  );
}
