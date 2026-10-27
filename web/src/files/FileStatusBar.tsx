import { CSSProperties } from "react";

import { formatBytes } from "../lib/bytes";
import { useTransfers } from "../transfers/store";
import { FileBrowserItem, FileView } from "./model";

// FileStatusBar counts what the folder shows and follows uploads into the
// open space, which open in the transfer center.
export function FileStatusBar({
  shown,
  total,
  filtered,
  spaceID,
  view,
  onOpenTransfers,
}: {
  // The items shown, after the search, of the total the folder holds.
  shown: FileBrowserItem[];
  total: number;
  filtered: boolean;
  spaceID: string;
  view: FileView;
  onOpenTransfers: () => void;
}) {
  const transfers = useTransfers();
  const fileCount = shown.filter((item) => item.kind === "file").length;
  const directoryCount = shown.filter((item) => item.kind === "directory").length;
  const resourceCount = shown.filter((item) => item.kind === "resource").length;
  const totalSize = shown.reduce((sum, item) => sum + (item.kind === "file" ? item.sizeBytes : 0), 0);
  const uploadsHere = transfers.uploads.filter(
    (upload) =>
      upload.target.spaceId === spaceID &&
      ["queued", "running", "paused", "waiting", "conflict"].includes(upload.phase),
  );
  const uploadBytes = uploadsHere.reduce((sum, upload) => sum + upload.size, 0);
  const uploadPercent = uploadBytes
    ? Math.round((uploadsHere.reduce((sum, upload) => sum + upload.transferred, 0) / uploadBytes) * 100)
    : 0;
  const uploadAttention = uploadsHere.some((upload) => upload.phase === "conflict" || upload.phase === "waiting");
  return (
    <footer className="file-status-bar">
      <span>
        {resourceCount ? `${resourceCount} 个应用资源，` : ""}
        {directoryCount ? `${directoryCount} 个文件夹` : ""}
        {directoryCount && fileCount ? "，" : ""}
        {fileCount ? `${fileCount} 个文件` : !directoryCount && !resourceCount ? "0 个项目" : ""}
      </span>
      <span>{filtered && `已筛选 ${shown.length}/${total} 项`}</span>
      <span>{totalSize > 0 && `文件共 ${formatBytes(totalSize)}`}</span>
      {uploadsHere.length > 0 && (
        <button
          className={`file-transfer-chip ${uploadAttention ? "attention" : ""}`}
          title="在传输中心查看上传"
          aria-label={
            uploadAttention
              ? `${uploadsHere.length} 个上传需要处理，在传输中心查看`
              : `正在上传 ${uploadsHere.length} 个文件，已完成 ${uploadPercent}%，在传输中心查看`
          }
          onClick={onOpenTransfers}
        >
          <span className="file-transfer-ring" style={{ "--percent": uploadPercent } as CSSProperties} />
          <span>
            {uploadAttention ? `${uploadsHere.length} 个需处理` : `上传 ${uploadsHere.length} · ${uploadPercent}%`}
          </span>
        </button>
      )}
      <span>{view === "list" ? "列表视图" : "图标视图"}</span>
    </footer>
  );
}
