import { ChevronLeft, ChevronRight, Download, X } from "lucide-react";
import { useEffect, useEffectEvent, useRef, useState } from "react";

import { formatBytes } from "../lib/bytes";
import { isHEIFImage, isPDFPreviewable, isPreviewableImage, isTextPreviewable, isVideoFile } from "../lib/fileTypes";
import "../files/viewer.css";
import { contentURL, readText, type ShareEntry } from "./api";
import { errorText, leaf } from "./format";
import { EntryTile, isAudio, kindOf } from "./parts";

// SharePreview looks like the file manager's viewer and steps through the
// files of the open folder; it reads the share's own copy, never the source.
export function SharePreview({
  shareId,
  entries,
  index,
  onIndex,
  onClose,
  onSave,
}: {
  shareId: string;
  entries: ShareEntry[];
  index: number;
  onIndex: (index: number) => void;
  onClose: () => void;
  onSave?: (entry: ShareEntry) => void;
}) {
  const layer = useRef<HTMLDivElement>(null);
  const entry = entries[index];
  const name = leaf(entry.path);
  const url = contentURL(shareId, entry, isHEIFImage(name));
  const [text, setText] = useState<string>();
  const [error, setError] = useState("");
  const many = entries.length > 1;
  useEffect(() => {
    const returnTo = document.activeElement instanceof HTMLElement ? document.activeElement : undefined;
    layer.current?.focus({ preventScroll: true });
    return () => returnTo?.focus({ preventScroll: true });
  }, []);
  // Another file starts without the last one's text or error.
  const shown = JSON.stringify([shareId, entry.id, name]);
  const [shownBefore, setShownBefore] = useState(shown);
  if (shownBefore !== shown) {
    setShownBefore(shown);
    setText(undefined);
    setError("");
  }
  const read = useEffectEvent((signal: AbortSignal) => readText(shareId, entry, signal));
  useEffect(() => {
    if (!isTextPreviewable(name)) return;
    const abort = new AbortController();
    void read(abort.signal)
      .then(setText)
      .catch((error) => {
        if (!abort.signal.aborted) setError(errorText(error));
      });
    return () => abort.abort();
  }, [shown, name]);
  const step = (delta: number) => onIndex((index + delta + entries.length) % entries.length);
  const media = isPreviewableImage(name) || isHEIFImage(name);
  const save = onSave && (
    <button type="button" className="fv-download" onClick={() => onSave(entry)}>
      <Download />
      保存
    </button>
  );
  const unsupported = (
    <div className="fv-failed sh-preview-file">
      <EntryTile name={name} isDir={false} size="lg" />
      <strong>{name}</strong>
      <span>
        {kindOf(name, false).label} · {formatBytes(entry.size)} · 这种格式不能在浏览器中预览，保存后用其他应用打开。
      </span>
      {save}
    </div>
  );
  return (
    <div
      ref={layer}
      className="fv-layer sh-preview"
      role="dialog"
      aria-modal="true"
      aria-label={`预览 ${name}`}
      tabIndex={-1}
      onKeyDown={(event) => {
        if (event.key === "Escape") {
          event.stopPropagation();
          onClose();
        }
        if (!many || (event.target as HTMLElement).closest("video, audio, iframe, pre")) return;
        if (event.key === "ArrowLeft") {
          event.preventDefault();
          step(-1);
        }
        if (event.key === "ArrowRight") {
          event.preventDefault();
          step(1);
        }
      }}
    >
      <header className="fv-bar">
        <div className="fv-title">
          <strong title={name}>{name}</strong>
          <small>
            {[formatBytes(entry.size), many && `${index + 1} / ${entries.length}`].filter(Boolean).join(" · ")}
          </small>
        </div>
        <div className="fv-actions">
          {save}
          <button type="button" className="fv-icon" aria-label="关闭预览" title="关闭 (Esc)" onClick={onClose}>
            <X />
          </button>
        </div>
      </header>
      <div
        className="fv-stage"
        onClick={(event) => {
          if (event.target === event.currentTarget) onClose();
        }}
      >
        {error ? (
          <div className="fv-failed" role="alert">
            <strong>无法预览</strong>
            <span>{error}</span>
            {save}
          </div>
        ) : media ? (
          <img
            key={entry.id}
            className="ready"
            src={url}
            alt={name}
            draggable={false}
            onError={() => setError("无法显示这张图片，保存后用其他应用查看。")}
          />
        ) : isPDFPreviewable(name) ? (
          <iframe key={entry.id} className="fv-pdf" title={name} src={url} />
        ) : isTextPreviewable(name) ? (
          <div className="sh-preview-text">
            <pre>{text === undefined ? "正在读取…" : text}</pre>
            {entry.size > 262144 && <p>只预览前 256 KiB，保存后可查看完整内容。</p>}
          </div>
        ) : isVideoFile(name) ? (
          <video
            key={entry.id}
            src={url}
            controls
            onError={() => setError("浏览器无法播放这个视频，保存后用其他应用查看。")}
          />
        ) : isAudio(name) ? (
          <div className="sh-preview-audio">
            <EntryTile name={name} isDir={false} size="lg" />
            <strong>{name}</strong>
            <audio
              key={entry.id}
              src={url}
              controls
              onError={() => setError("浏览器无法播放这个音频，保存后用其他应用查看。")}
            />
          </div>
        ) : (
          unsupported
        )}
      </div>
      {many && (
        <>
          <button
            type="button"
            className="fv-step prev"
            aria-label="上一个"
            title="上一个 (←)"
            onClick={() => step(-1)}
          >
            <ChevronLeft />
          </button>
          <button type="button" className="fv-step next" aria-label="下一个" title="下一个 (→)" onClick={() => step(1)}>
            <ChevronRight />
          </button>
        </>
      )}
    </div>
  );
}
