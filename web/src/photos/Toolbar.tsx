import {
  ArrowLeft,
  Eye,
  EyeOff,
  FolderPlus,
  ImagePlus,
  Merge,
  PanelLeft,
  Pencil,
  Search,
  ShieldCheck,
  Trash2,
  X,
  ZoomIn,
  ZoomOut,
} from "lucide-react";
import type { DragEvent, FormEvent, ReactNode, RefObject } from "react";

import type { PhotoAlbum, PhotoLibrary } from "./api";
import { type ImageDropProps, ImageSearchButton } from "./imageSearch";
import { maxRowHeight, minRowHeight } from "./layout";
import { type Caller, canEditAlbum, libraryName, type Place } from "./model";
import { Avatar } from "./People";
import styles from "./photos.module.css";
import { SelectionActions } from "./SelectionActions";
import type { useSelection } from "./selection";
import type { PlaceTitle } from "./title";
import type { UploadTarget } from "./uploads";
import type { PhotoActions } from "./usePhotoActions";

// SearchBox is what a search box needs: the words typed into it, and an image
// chosen with its button, dropped onto it or pasted into it.
export interface SearchBox {
  query: string;
  setQuery: (query: string) => void;
  submitSearch: (event: FormEvent) => void;
  searchInput: RefObject<HTMLInputElement | null>;
  dragging: boolean;
  imageOver: string;
  imageDrop: Omit<ImageDropProps, "onDragOver">;
  imageDragOver: (name: string) => (event: DragEvent) => void;
  pickSearchImage: () => void;
}

interface Props extends SearchBox {
  toolbar: RefObject<HTMLElement | null>;
  place: Place;
  title: PlaceTitle;
  caller: Caller;
  selection: ReturnType<typeof useSelection>;
  actions: PhotoActions;
  sidebarOpen: boolean;
  onToggleSidebar: () => void;
  setPlace: (place: Place) => void;
  rowHeight: number;
  setRowHeight: (height: number) => void;
  zoomBy: (factor: number) => void;
  setPhotoPicker: (album: PhotoAlbum) => void;
  uploadTarget?: UploadTarget;
  pickFiles: () => void;
  viewingLibrary?: PhotoLibrary;
  // The bar under the toolbar, which some places have.
  children?: ReactNode;
}

// Toolbar is the top of the photos: the place's title, what can be done
// there, the search box and the photo size; under it a member library being
// viewed and the place's own bar. Its height is where the photos start.
export function Toolbar({
  toolbar,
  place,
  title,
  caller,
  selection,
  actions,
  sidebarOpen,
  onToggleSidebar,
  setPlace,
  query,
  setQuery,
  submitSearch,
  searchInput,
  dragging,
  imageOver,
  imageDrop,
  imageDragOver,
  pickSearchImage,
  rowHeight,
  setRowHeight,
  zoomBy,
  setPhotoPicker,
  uploadTarget,
  pickFiles,
  viewingLibrary,
  children,
}: Props) {
  const { namePerson, setMerging, changePerson, renameCurrentAlbum, deleteCurrentAlbum, emptyTrash, stopViewing } =
    actions;
  const photoGrid =
    place.kind === "timeline" ||
    place.kind === "album" ||
    place.kind === "trash" ||
    place.kind === "cluster" ||
    place.kind === "tag" ||
    place.kind === "person" ||
    place.kind === "similar" ||
    place.kind === "image" ||
    (place.kind === "search" && Boolean(place.query));
  // While photos are selected the toolbar turns into their actions. The two
  // rows take turns in one place, so the first button stays the same element
  // and keeps the focus when the selection is cleared with it.
  const selectionBar = selection.selected.size > 0 && (
    <div className={`${styles.phToolbarRow} ${styles.phSelectionRow}`} role="toolbar" aria-label="所选照片的操作">
      <button
        type="button"
        className={styles.phIconButton}
        aria-label="取消选择"
        title="取消选择 (Esc)"
        onClick={selection.clear}
      >
        <X />
      </button>
      <strong>已选 {selection.selected.size} 张</strong>
      <span className={styles.phSelectionHint}>按住 Shift 可连选，拖动勾选圈可批量选择</span>
      <SelectionActions place={place} caller={caller} selection={selection} actions={actions} />
    </div>
  );

  return (
    <header ref={toolbar} className={`${styles.phToolbar}${selection.selected.size ? ` ${styles.selecting}` : ""}`}>
      {selectionBar || (
        <div className={styles.phToolbarRow}>
          <button
            type="button"
            className={styles.phIconButton}
            aria-label={sidebarOpen ? "收起侧栏" : "展开侧栏"}
            aria-expanded={sidebarOpen}
            onClick={onToggleSidebar}
          >
            <PanelLeft />
          </button>
          {title.back && (
            <button
              type="button"
              className={styles.phIconButton}
              aria-label="返回"
              onClick={() => setPlace(title.back!)}
            >
              <ArrowLeft />
            </button>
          )}
          {place.kind === "person" && (
            <Avatar
              occurrenceId={place.person.coverId}
              kind={place.person.kind}
              size={34}
              className={styles.phTitleAvatar}
            />
          )}
          <div className={styles.phTitle}>
            <h2>{title.text}</h2>
            {title.detail && <small>{title.detail}</small>}
          </div>
          {place.kind === "person" && (
            <div className={`${styles.phTitleActions} ${styles.phPersonActions}`}>
              {place.person.canChange && (
                <>
                  <button
                    type="button"
                    className={place.person.name ? styles.phIconButton : `${styles.phButton} primary ${styles.small}`}
                    aria-label={place.person.name ? "改名" : "添加名字"}
                    title={place.person.name ? "改名" : "添加名字"}
                    onClick={() => void namePerson()}
                  >
                    {place.person.name ? (
                      <Pencil />
                    ) : (
                      <>
                        <Pencil />
                        添加名字
                      </>
                    )}
                  </button>
                  <button
                    type="button"
                    className={styles.phIconButton}
                    aria-label="合并到另一个人"
                    title="和另一个人是同一个人？合并"
                    onClick={() => setMerging(place.person)}
                  >
                    <Merge />
                  </button>
                  <button
                    type="button"
                    className={styles.phIconButton}
                    aria-label={place.person.hidden ? "不再隐藏" : "隐藏"}
                    title={place.person.hidden ? "重新显示在人物与宠物中" : "不在人物与宠物中显示"}
                    onClick={() => void changePerson({ hidden: !place.person.hidden })}
                  >
                    {place.person.hidden ? <Eye /> : <EyeOff />}
                  </button>
                </>
              )}
            </div>
          )}
          {place.kind === "album" && canEditAlbum(caller, place.album) && (
            <div className={styles.phTitleActions}>
              <button
                type="button"
                className={styles.phIconButton}
                aria-label="重命名相册"
                title="重命名相册"
                onClick={() => void renameCurrentAlbum()}
              >
                <Pencil />
              </button>
              <button
                type="button"
                className={styles.phIconButton}
                aria-label="删除相册"
                title="删除相册"
                onClick={() => void deleteCurrentAlbum()}
              >
                <Trash2 />
              </button>
            </div>
          )}
          {place.kind !== "search" && (
            <form
              className={`${styles.phSearch}${imageOver === "toolbar" ? ` ${styles.imageDrop}` : ""}`}
              role="search"
              onSubmit={submitSearch}
              {...{ ...imageDrop, onDragOver: imageDragOver("toolbar") }}
            >
              <Search />
              <input
                ref={searchInput}
                type="search"
                aria-label="搜索照片"
                placeholder={dragging ? "拖到这里，以图搜图" : "用一句话找照片，如：海边的猫"}
                maxLength={200}
                value={query}
                onChange={(event) => setQuery(event.target.value)}
              />
              <ImageSearchButton onPick={pickSearchImage} />
            </form>
          )}
          {photoGrid && (
            <div className={styles.phZoom} role="group" aria-label="照片大小">
              <button
                type="button"
                aria-label="缩小照片"
                title="缩小 (Ctrl+滚轮)"
                disabled={rowHeight <= minRowHeight}
                onClick={() => zoomBy(1 / 1.2)}
              >
                <ZoomOut />
              </button>
              <input
                type="range"
                aria-label="照片大小"
                min={minRowHeight}
                max={maxRowHeight}
                value={rowHeight}
                onChange={(event) => setRowHeight(Number(event.target.value))}
              />
              <button
                type="button"
                aria-label="放大照片"
                title="放大 (Ctrl+滚轮)"
                disabled={rowHeight >= maxRowHeight}
                onClick={() => zoomBy(1.2)}
              >
                <ZoomIn />
              </button>
            </div>
          )}
          {place.kind === "album" && canEditAlbum(caller, place.album) && (
            <button type="button" className={styles.phButton} onClick={() => setPhotoPicker(place.album)}>
              <FolderPlus />
              添加照片
            </button>
          )}
          {place.kind === "trash" && (
            <button type="button" className={`${styles.phButton} ${styles.danger}`} onClick={() => void emptyTrash()}>
              <Trash2 />
              清空
            </button>
          )}
          {uploadTarget && place.kind !== "trash" && (
            <button
              type="button"
              className={`${styles.phButton} primary`}
              onClick={pickFiles}
              title={`上传到${uploadTarget.label}`}
            >
              <ImagePlus />
              <span>上传</span>
            </button>
          )}
        </div>
      )}
      {viewingLibrary?.viewing && (
        <div className={styles.phViewing} role="status">
          <ShieldCheck />
          <span>
            只读查看 {libraryName(caller, viewingLibrary)}，
            {new Date(viewingLibrary.viewing.expiresAt).toLocaleString("zh-CN")}{" "}
            自动结束；本次访问已写入审计并通知所有者。
          </span>
          <button type="button" onClick={() => void stopViewing()}>
            结束查看
          </button>
        </div>
      )}
      {children}
    </header>
  );
}
