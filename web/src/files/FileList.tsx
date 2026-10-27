import { ChevronDown, Clock3, FolderClosed, FolderPlus, Pin, Search } from "lucide-react";
import { MouseEvent as ReactMouseEvent, PointerEvent as ReactPointerEvent, useRef, useState } from "react";

import { formatBytes } from "../lib/bytes";
import { FileArtwork } from "./FileIcon";
import styles from "./files.module.css";
import { FileBrowserItem, FileView, formatFileDate } from "./model";
import { DragProps } from "./useDragReorder";
import { FileOrder, FileSort, isPinned } from "./useFileOrder";

type SelectionRect = { left: number; top: number; width: number; height: number };
type Modifiers = Pick<ReactMouseEvent, "ctrlKey" | "metaKey" | "shiftKey">;

const columns: Array<{ sort: Exclude<FileSort, "manual">; label: string; className?: string }> = [
  { sort: "name", label: "名称" },
  { sort: "modifiedAt", label: "修改时间", className: styles.fileDateColumn },
  { sort: "type", label: "类型", className: styles.fileTypeColumn },
  { sort: "sizeBytes", label: "大小" },
];

// FileList shows the open folder as a list or as icons. A press on empty
// space starts a selection box; the list says why it is empty when it is.
export function FileList({
  view,
  items,
  selectedIDs,
  order,
  loading,
  hasSpace,
  query,
  viewing,
  dragProps,
  onSort,
  onSelect,
  onBoxSelect,
  onOpen,
  onContextMenu,
  onClearQuery,
}: {
  view: FileView;
  items: FileBrowserItem[];
  selectedIDs: string[];
  order: FileOrder;
  loading: boolean;
  hasSpace: boolean;
  query: string;
  viewing: boolean;
  dragProps: (item: FileBrowserItem) => DragProps;
  onSort: (sort: FileSort) => void;
  onSelect: (item: FileBrowserItem, event: Modifiers) => void;
  // onBoxSelect selects what the box covers, from nothing as the press begins.
  onBoxSelect: (ids: string[]) => void;
  onOpen: (item: FileBrowserItem) => void;
  onContextMenu: (event: ReactMouseEvent, item?: FileBrowserItem) => void;
  onClearQuery: () => void;
}) {
  const shellRef = useRef<HTMLDivElement>(null);
  const [selectionRect, setSelectionRect] = useState<SelectionRect>();
  const { sortBy, direction } = order;
  const beginBoxSelection = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (event.button !== 0 || (event.target as HTMLElement).closest("[data-file-item], button, a, input, select"))
      return;
    const shell = shellRef.current;
    if (!shell) return;
    event.preventDefault();
    onBoxSelect([]);
    const bounds = shell.getBoundingClientRect();
    const startX = event.clientX;
    const startY = event.clientY;
    const move = (moveEvent: PointerEvent) => {
      const left = Math.min(startX, moveEvent.clientX);
      const top = Math.min(startY, moveEvent.clientY);
      const right = Math.max(startX, moveEvent.clientX);
      const bottom = Math.max(startY, moveEvent.clientY);
      setSelectionRect({
        left: left - bounds.left + shell.scrollLeft,
        top: top - bounds.top + shell.scrollTop,
        width: right - left,
        height: bottom - top,
      });
      const matches = Array.from(shell.querySelectorAll<HTMLElement>("[data-file-item]"))
        .filter((element) => {
          const rect = element.getBoundingClientRect();
          return rect.left < right && rect.right > left && rect.top < bottom && rect.bottom > top;
        })
        .map((element) => element.dataset.fileItem!)
        .filter(Boolean);
      onBoxSelect(matches);
    };
    const stop = () => {
      setSelectionRect(undefined);
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", stop);
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", stop, { once: true });
  };
  const itemProps = (item: FileBrowserItem) => ({
    "data-file-item": item.id,
    ...dragProps(item),
    onClick: (event: ReactMouseEvent) => onSelect(item, event),
    onDoubleClick: () => onOpen(item),
    onContextMenu: (event: ReactMouseEvent) => onContextMenu(event, item),
  });
  const pinBadge = (item: FileBrowserItem) =>
    isPinned(item, order) && <Pin className={styles.filePinBadge} aria-label="已置顶" />;

  return (
    <div
      className={styles.fileListShell}
      ref={shellRef}
      onPointerDown={beginBoxSelection}
      onContextMenu={onContextMenu}
    >
      {view === "list" ? (
        <div className={styles.fileList} role="table" aria-label="文件列表" aria-busy={loading}>
          <div className={styles.fileListHeader} role="row">
            {columns.map((column) => (
              <div
                key={column.sort}
                className={column.className}
                role="columnheader"
                aria-sort={sortBy === column.sort ? direction : "none"}
              >
                <button onClick={() => onSort(column.sort)}>
                  {column.label}
                  {sortBy === column.sort && (
                    <ChevronDown className={direction === "ascending" ? styles.ascending : ""} />
                  )}
                </button>
              </div>
            ))}
          </div>
          {!loading &&
            items.map((item) => (
              <div
                className={`${styles.fileListRow} ${selectedIDs.includes(item.id) ? styles.selected : ""}`}
                role="row"
                key={item.id}
                aria-selected={selectedIDs.includes(item.id)}
                {...itemProps(item)}
              >
                <div className={styles.fileNameCell} role="cell">
                  <span
                    className={`${styles.fileTypeIcon} ${styles[item.kind] ?? ""} ${styles[item.resource ?? ""] ?? ""}`}
                  >
                    <FileArtwork item={item} />
                  </span>
                  <span className={styles.fileNameCopy}>
                    <span>
                      <strong>
                        {pinBadge(item)}
                        {item.name}
                        {item.kind === "resource" && <em>{item.planned ? "规划中" : "应用资源"}</em>}
                      </strong>
                      <small>{item.detail}</small>
                    </span>
                  </span>
                </div>
                <div className={`${styles.fileDateCell} ${styles.fileDateColumn}`} role="cell">
                  {item.modifiedAt ? (
                    <>
                      <Clock3 />
                      {formatFileDate(item.modifiedAt)}
                    </>
                  ) : (
                    "—"
                  )}
                </div>
                <div className={`${styles.fileTypeCell} ${styles.fileTypeColumn}`} role="cell">
                  <span
                    className={
                      item.kind === "directory" ? styles.directory : item.kind === "resource" ? styles.resource : ""
                    }
                  >
                    {item.type}
                  </span>
                </div>
                <div className={styles.fileSizeCell} role="cell">
                  {item.kind === "file" ? formatBytes(item.sizeBytes) : "—"}
                </div>
              </div>
            ))}
        </div>
      ) : (
        <div className={styles.fileIconView} role="listbox" aria-label="文件图标">
          {!loading &&
            items.map((item) => (
              <button
                className={`${styles.fileIconCard} ${selectedIDs.includes(item.id) ? styles.selected : ""}`}
                role="option"
                key={item.id}
                aria-selected={selectedIDs.includes(item.id)}
                {...itemProps(item)}
              >
                <span
                  className={`${styles.fileTypeIcon} ${styles[item.kind] ?? ""} ${styles[item.resource ?? ""] ?? ""}`}
                >
                  <FileArtwork item={item} />
                </span>
                <strong>
                  {pinBadge(item)}
                  {item.name}
                </strong>
                <small>{item.detail}</small>
              </button>
            ))}
        </div>
      )}
      {selectionRect && <div className={styles.fileSelectionBox} aria-hidden="true" style={selectionRect} />}
      {loading ? (
        <div className={styles.fileEmptyState} role="status">
          <span className="loader" />
          <strong>正在读取文件…</strong>
          <span>正在加载这个目录的内容</span>
        </div>
      ) : !hasSpace ? (
        <div className={styles.fileEmptyState}>
          <FolderClosed />
          <strong>还没有可用空间</strong>
          <span>完成数据卷与账号设置后，空间会显示在这里。</span>
        </div>
      ) : !items.length && query ? (
        <div className={styles.fileEmptyState}>
          <Search />
          <strong>没有匹配的项目</strong>
          <span>请尝试其他名称，或清除搜索条件。</span>
          <button onClick={onClearQuery}>清除搜索</button>
        </div>
      ) : !items.length ? (
        <div className={styles.fileEmptyState}>
          <FolderPlus />
          <strong>这个文件夹是空的</strong>
          <span>{viewing ? "这里暂时没有内容。" : "把文件或文件夹拖到这里上传，或新建文件夹开始整理。"}</span>
        </div>
      ) : null}
    </div>
  );
}
