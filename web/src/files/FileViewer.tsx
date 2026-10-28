import { ChevronLeft, ChevronRight, Download, ExternalLink, ImageOff, X, ZoomIn, ZoomOut } from "lucide-react";
import { KeyboardEvent, useEffect, useRef, useState } from "react";

import type { FileEntry } from "../api.generated";
import { formatBytes } from "../lib/bytes";
import { isPDFPreviewable } from "../lib/fileTypes";
import viewerStyles from "../ui/viewer.module.css";
import { fileVisualURL } from "./preview";

// FileViewer shows the images and PDFs of the open folder over the file
// manager, one at a time, straight from the file endpoint; nothing is saved
// until the user asks for a download (docs/specs/basic-storage-and-sharing.md).
export function FileViewer({
  entries,
  index,
  onIndex,
  onClose,
  onDownload,
  vnasPrivate,
}: {
  entries: FileEntry[];
  index: number;
  onIndex: (index: number) => void;
  onClose: () => void;
  onDownload: (entry: FileEntry) => void;
  /** Set when VNAS may not read what is shown (assistant/webosControl.ts). */
  vnasPrivate?: string;
}) {
  const layer = useRef<HTMLDivElement>(null);
  const entry = entries[index];
  const pdf = isPDFPreviewable(entry.name);
  const [state, setState] = useState<"loading" | "ready" | "failed">("loading");
  const [size, setSize] = useState<{ width: number; height: number }>();
  const [zoomed, setZoomed] = useState(false);
  const many = entries.length > 1;

  useEffect(() => {
    const returnTo = document.activeElement instanceof HTMLElement ? document.activeElement : undefined;
    layer.current?.focus({ preventScroll: true });
    return () => returnTo?.focus({ preventScroll: true });
  }, []);
  // Another file starts afresh.
  const shown = JSON.stringify([entry.id, pdf]);
  const [shownBefore, setShownBefore] = useState(shown);
  if (shownBefore !== shown) {
    setShownBefore(shown);
    setState(pdf ? "ready" : "loading");
    setSize(undefined);
    setZoomed(false);
  }

  const step = (delta: number) => onIndex((index + delta + entries.length) % entries.length);
  const keyDown = (event: KeyboardEvent) => {
    if (event.key === "Escape") {
      event.stopPropagation();
      onClose();
    }
    if (pdf) return;
    if (many && event.key === "ArrowLeft") {
      event.preventDefault();
      step(-1);
    }
    if (many && event.key === "ArrowRight") {
      event.preventDefault();
      step(1);
    }
  };

  const details = [
    formatBytes(entry.sizeBytes),
    size && `${size.width} × ${size.height}`,
    many && `${index + 1} / ${entries.length}`,
  ]
    .filter(Boolean)
    .join(" · ");
  return (
    <div
      ref={layer}
      className={viewerStyles.fvLayer}
      role="dialog"
      aria-modal="true"
      aria-label={`预览 ${entry.name}`}
      tabIndex={-1}
      onKeyDown={keyDown}
      data-vnas-private={vnasPrivate}
    >
      <header className={viewerStyles.fvBar}>
        <div className={viewerStyles.fvTitle}>
          <strong title={entry.name}>{entry.name}</strong>
          <small>{details}</small>
        </div>
        <div className={viewerStyles.fvActions}>
          {!pdf && state === "ready" && (
            <button
              type="button"
              className={viewerStyles.fvIcon}
              aria-label={zoomed ? "适合窗口" : "实际大小"}
              title={zoomed ? "适合窗口" : "实际大小"}
              onClick={() => setZoomed(!zoomed)}
            >
              {zoomed ? <ZoomOut /> : <ZoomIn />}
            </button>
          )}
          <a
            className={viewerStyles.fvIcon}
            href={fileVisualURL(entry)}
            target="_blank"
            rel="noopener noreferrer"
            aria-label="在新标签页打开"
            title="在新标签页打开"
          >
            <ExternalLink />
          </a>
          <button type="button" className={viewerStyles.fvDownload} onClick={() => onDownload(entry)}>
            <Download />
            下载
          </button>
          <button
            type="button"
            className={viewerStyles.fvIcon}
            aria-label="关闭预览"
            title="关闭 (Esc)"
            onClick={onClose}
          >
            <X />
          </button>
        </div>
      </header>

      <div
        className={`${viewerStyles.fvStage}${zoomed ? ` ${viewerStyles.zoomed}` : ""}`}
        onClick={(event) => {
          if (event.target === event.currentTarget && !zoomed) onClose();
        }}
      >
        {pdf ? (
          <iframe key={entry.id} className={viewerStyles.fvPdf} src={fileVisualURL(entry)} title={entry.name} />
        ) : state === "failed" ? (
          <div className={viewerStyles.fvFailed} role="alert">
            <ImageOff />
            <strong>无法预览这张图片</strong>
            <span>暂时无法生成或显示预览，下载后用其他应用打开。</span>
            <button type="button" className={viewerStyles.fvDownload} onClick={() => onDownload(entry)}>
              <Download />
              下载
            </button>
          </div>
        ) : (
          <>
            {state === "loading" && (
              <div className={viewerStyles.fvLoading} role="status">
                <span className={viewerStyles.fvSpinner} />
                正在载入 · {formatBytes(entry.sizeBytes)}
              </div>
            )}
            <img
              key={entry.id}
              className={state === "ready" ? viewerStyles.ready : ""}
              src={fileVisualURL(entry)}
              alt={entry.name}
              draggable={false}
              title={zoomed ? "点击适合窗口" : "点击查看实际大小"}
              onLoad={(event) => {
                setState("ready");
                setSize({ width: event.currentTarget.naturalWidth, height: event.currentTarget.naturalHeight });
              }}
              onError={() => setState("failed")}
              onClick={() => setZoomed(!zoomed)}
            />
          </>
        )}
      </div>

      {many && !pdf && (
        <>
          <button
            type="button"
            className={`${viewerStyles.fvStep} ${viewerStyles.prev}`}
            aria-label="上一个"
            title="上一个 (←)"
            onClick={() => step(-1)}
          >
            <ChevronLeft />
          </button>
          <button
            type="button"
            className={`${viewerStyles.fvStep} ${viewerStyles.next}`}
            aria-label="下一个"
            title="下一个 (→)"
            onClick={() => step(1)}
          >
            <ChevronRight />
          </button>
        </>
      )}
      {many && pdf && (
        <nav className={viewerStyles.fvPager} aria-label="切换文件">
          <button type="button" aria-label="上一个" onClick={() => step(-1)}>
            <ChevronLeft />
          </button>
          <span>
            {index + 1} / {entries.length}
          </span>
          <button type="button" aria-label="下一个" onClick={() => step(1)}>
            <ChevronRight />
          </button>
        </nav>
      )}
    </div>
  );
}
