import { Copy, FileText, FolderOpen, History } from "lucide-react";
import { ReactNode } from "react";

import type { FileEntry } from "../api.generated";
import { formatBytes } from "../lib/bytes";
import {
  isOfficeDocument,
  isPDFPreviewable,
  isPreviewableImage,
  isTextPreviewable,
  isVideoFile,
} from "../lib/fileTypes";
import { filePreviewURL } from "./api";
import { FileIcon } from "./FileIcon";
import styles from "./files.module.css";
import { FileBrowserItem, formatFileDate, othersSpace } from "./model";
import { fileVisualURL } from "./preview";

// FileInspector is the details pane: a preview of the selected item and
// what it is, or how many items are selected.
export function FileInspector({
  item,
  count,
  location,
  viewing,
  textPreview,
  previewFailed,
  onPreviewFailed,
  onView,
  onVersions,
}: {
  item?: FileBrowserItem;
  count: number;
  // The folder the item is in.
  location: string;
  viewing: boolean;
  textPreview?: { content?: string; error?: string };
  // The item's picture could not be loaded; it shows its icon instead.
  previewFailed: boolean;
  onPreviewFailed: () => void;
  onView: (entry: FileEntry) => void;
  onVersions: (entry: FileEntry) => void;
}) {
  return (
    <aside className={styles.fileInspector} aria-label="详细信息" data-vnas-private={viewing ? othersSpace : undefined}>
      <header>详细信息</header>
      {count > 1 ? (
        <div className={styles.fileInspectorEmpty}>
          <Copy />
          <strong>已选择 {count} 个项目</strong>
          <span>可通过工具栏或右键菜单批量剪切、复制和删除</span>
        </div>
      ) : item ? (
        <>
          <div
            className={`${styles.fileInspectorPreview} ${styles[item.kind] ?? ""} ${styles[item.resource ?? ""] ?? ""} ${isPreviewableImage(item.name) || isVideoFile(item.name) ? styles.image : isPDFPreviewable(item.name) ? styles.pdf : isTextPreviewable(item.name) ? styles.text : isOfficeDocument(item.name) ? styles.office : ""}`}
          >
            <InspectorPreview
              item={item}
              textPreview={textPreview}
              failed={previewFailed}
              onFailed={onPreviewFailed}
              onView={onView}
            />
          </div>
          <div className={styles.fileInspectorCopy}>
            <h3>{item.name}</h3>
            <p>{item.type}</p>
            <dl>
              <dt>资源类型</dt>
              <dd>{item.kind === "resource" ? "应用资源" : item.type}</dd>
              <dt>内容</dt>
              <dd>{item.kind === "file" ? formatBytes(item.sizeBytes) : item.detail}</dd>
              <dt>所在位置</dt>
              <dd>{location}</dd>
              {item.modifiedAt && (
                <>
                  <dt>修改时间</dt>
                  <dd>{formatFileDate(item.modifiedAt)}</dd>
                </>
              )}
            </dl>
            {item.entry?.kind === "file" && !viewing && (
              <button className={styles.fileInspectorAction} onClick={() => onVersions(item.entry!)}>
                <History />
                历史版本
              </button>
            )}
            {item.resource === "photos" && (
              <p className={styles.fileInspectorHint}>
                这是相册中个人图库的图库投影。打开后交由相册处理，不暴露受管存储路径。
              </p>
            )}
            {item.resource === "movies" && (
              <p className={styles.fileInspectorHint}>
                打开影视中心，把个人空间或共享空间中的视频文件夹整理成媒体库。视频仍是这里的普通文件。
              </p>
            )}
            {item.resource === "music" && (
              <p className={styles.fileInspectorHint}>音乐会自动整理个人空间和共享空间里的音乐文件，文件留在原处。</p>
            )}
            {item.planned && <p className={styles.fileInspectorHint}>该资源入口已预留，功能尚未开发。</p>}
          </div>
        </>
      ) : (
        <div className={styles.fileInspectorEmpty}>
          <FolderOpen />
          <strong>选择一个项目</strong>
          <span>详细信息会显示在这里</span>
        </div>
      )}
    </aside>
  );
}

function InspectorPreview({
  item,
  textPreview,
  failed,
  onFailed,
  onView,
}: {
  item: FileBrowserItem;
  textPreview?: { content?: string; error?: string };
  failed: boolean;
  onFailed: () => void;
  onView: (entry: FileEntry) => void;
}): ReactNode {
  const entry = item.entry?.kind === "file" ? item.entry : undefined;
  if (entry && isPreviewableImage(item.name) && !failed)
    return (
      <button
        type="button"
        className={styles.fileInspectorZoom}
        title="点击预览"
        aria-label={`预览 ${item.name}`}
        onClick={() => onView(entry)}
      >
        <img src={fileVisualURL(entry)} alt={`${item.name} 的预览`} onError={onFailed} />
      </button>
    );
  if (entry && isVideoFile(item.name) && !failed)
    return <img src={fileVisualURL(entry)} alt={`${item.name} 的预览`} onError={onFailed} />;
  if (entry && isPDFPreviewable(item.name))
    return <iframe src={filePreviewURL(entry.id)} title={`${item.name} 的预览`} />;
  if (entry && isTextPreviewable(item.name))
    return textPreview?.content !== undefined ? (
      <pre>{textPreview.content}</pre>
    ) : textPreview?.error ? (
      <span className={styles.filePreviewMessage}>{textPreview.error}</span>
    ) : (
      <span className="loader" />
    );
  if (entry && isOfficeDocument(item.name))
    return (
      <div className={styles.filePreviewUnavailable}>
        <FileText />
        <strong>需要文档转换服务</strong>
        <span>Word 与 PowerPoint 无法由浏览器原生可靠预览。</span>
      </div>
    );
  return <FileIcon item={item} />;
}
