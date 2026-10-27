import { ImagePlus, LoaderCircle, Scan, X } from "lucide-react";
import {
  ClipboardEvent,
  DragEvent,
  FormEvent,
  KeyboardEvent,
  ReactNode,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
} from "react";

import { IMAGE_TYPES, MAX_IMAGE_BYTES, MAX_IMAGES, MAX_TEXT } from "./api";
import { captureDesktop } from "./capture";
import styles from "./feedback.module.css";

// An image picked, pasted, dropped or captured but not yet sent; url is an
// object URL for its preview.
export interface AddedImage {
  key: string;
  blob: Blob;
  url: string;
  label: string;
}
export interface Draft {
  text: string;
  added: AddedImage[];
}
export const emptyDraft: Draft = { text: "", added: [] };

let sequence = 0;
function addedImage(blob: Blob, label: string): AddedImage {
  sequence += 1;
  return { key: `added-${sequence}`, blob, url: URL.createObjectURL(blob), label };
}

// releaseDraft frees the previews of a draft that is sent or thrown away.
export function releaseDraft(draft: Draft) {
  for (const image of draft.added) URL.revokeObjectURL(image.url);
}

export interface AttachedImage {
  id: string;
  url: string;
}

interface FeedbackEditorProps {
  label: string;
  draft: Draft;
  onChange: (draft: Draft) => void;
  // Images already saved with the item being edited.
  attached?: AttachedImage[];
  onDetach?: (id: string) => void;
  placeholder: string;
  submitLabel: string;
  busy: boolean;
  error?: string;
  notice?: ReactNode;
  autoFocus?: boolean;
  onSubmit: () => void;
  onCancel?: () => void;
  onPreview: (urls: string[], index: number) => void;
  onCaptured?: () => void;
  // A comment holds less than an item and its editor is smaller.
  maxImages?: number;
  maxText?: number;
  compact?: boolean;
}

// FeedbackEditor writes a new item or comment, or edits one: text, plus
// images from the file picker, the clipboard, a drop or a capture of the
// desktop.
export function FeedbackEditor({
  label,
  draft,
  onChange,
  attached = [],
  onDetach,
  placeholder,
  submitLabel,
  busy,
  error,
  notice,
  autoFocus,
  onSubmit,
  onCancel,
  onPreview,
  onCaptured,
  maxImages = MAX_IMAGES,
  maxText = MAX_TEXT,
  compact = false,
}: FeedbackEditorProps) {
  const [problem, setProblem] = useState("");
  const [capturing, setCapturing] = useState(false);
  const [dragging, setDragging] = useState(false);
  const picker = useRef<HTMLInputElement>(null);
  const textArea = useRef<HTMLTextAreaElement>(null);
  const textID = useId();
  // Captures finish after a render, so additions read the latest draft.
  const latest = useRef(draft);
  const attachedCount = useRef(attached.length);
  useLayoutEffect(() => {
    latest.current = draft;
    attachedCount.current = attached.length;
  });

  // Focus without scrolling: a window wider than the screen makes the
  // desktop scrollable, and the panel slides in from past its edge. Only the
  // list scrolls, to bring a comment box below the fold into view. An edit
  // continues where the text ends.
  useEffect(() => {
    const field = textArea.current;
    if (!autoFocus || !field) return;
    field.focus({ preventScroll: true });
    field.scrollIntoView?.({ block: "nearest" });
    field.setSelectionRange(field.value.length, field.value.length);
  }, [autoFocus]);

  const count = attached.length + draft.added.length;
  const full = count >= maxImages;
  const empty = !draft.text.trim() && count === 0;
  const tooLong = draft.text.length > maxText;

  const add = (images: Array<{ blob: Blob; label: string }>) => {
    const current = latest.current;
    const room = maxImages - attachedCount.current - current.added.length;
    const accepted: AddedImage[] = [];
    let rejected = "";
    for (const image of images) {
      if (!IMAGE_TYPES.includes(image.blob.type)) {
        rejected = "只支持 PNG、JPEG、GIF 或 WebP 图片";
        continue;
      }
      if (image.blob.size > MAX_IMAGE_BYTES) {
        rejected = "单张图片不能超过 10 MiB";
        continue;
      }
      if (accepted.length >= room) {
        rejected = `最多添加 ${maxImages} 张图片`;
        break;
      }
      accepted.push(addedImage(image.blob, image.label));
    }
    setProblem(rejected);
    if (accepted.length) onChange({ ...current, added: [...current.added, ...accepted] });
  };

  const remove = (image: AddedImage) => {
    URL.revokeObjectURL(image.url);
    setProblem("");
    onChange({ ...draft, added: draft.added.filter((item) => item.key !== image.key) });
  };

  const capture = async () => {
    setCapturing(true);
    setProblem("");
    try {
      add([{ blob: await captureDesktop(), label: "界面截图" }]);
      onCaptured?.();
    } catch {
      setProblem("截图失败，可以用系统截图后直接粘贴");
    } finally {
      setCapturing(false);
    }
  };

  const paste = (event: ClipboardEvent<HTMLFormElement>) => {
    const images = Array.from(event.clipboardData?.files ?? []).filter((file) => file.type.startsWith("image/"));
    if (!images.length) return;
    // Keep the text of a mixed clipboard, such as a copied document.
    if (!Array.from(event.clipboardData.types ?? []).includes("text/plain")) event.preventDefault();
    add(images.map((blob) => ({ blob, label: "粘贴的图片" })));
  };

  const hasFiles = (event: DragEvent) => Array.from(event.dataTransfer?.types ?? []).includes("Files");
  const drop = (event: DragEvent<HTMLFormElement>) => {
    if (!hasFiles(event)) return;
    event.preventDefault();
    setDragging(false);
    add(Array.from(event.dataTransfer.files).map((blob) => ({ blob, label: blob.name || "图片" })));
  };

  const submit = (event?: FormEvent) => {
    event?.preventDefault();
    if (busy || empty || tooLong) return;
    onSubmit();
  };
  const keyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (event.key === "Enter" && (event.ctrlKey || event.metaKey)) {
      event.preventDefault();
      submit();
    }
    if (event.key === "Escape" && onCancel) {
      event.stopPropagation();
      onCancel();
    }
  };

  const previews = [...attached.map((image) => image.url), ...draft.added.map((image) => image.url)];
  const shown = problem || error || (tooLong ? `文字最多 ${maxText} 字，当前 ${draft.text.length} 字` : "");

  return (
    <form
      className={`${styles.fbEditor} ${dragging ? styles.dragging : ""} ${onCancel ? styles.editing : ""} ${compact ? styles.compact : ""}`}
      aria-label={label}
      onSubmit={submit}
      onPaste={paste}
      onDragEnter={(event) => {
        if (hasFiles(event)) setDragging(true);
      }}
      onDragOver={(event) => {
        if (hasFiles(event)) event.preventDefault();
      }}
      onDragLeave={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setDragging(false);
      }}
      onDrop={drop}
    >
      {notice}
      <label className="sr-only" htmlFor={textID}>
        {label}内容
      </label>
      <textarea
        ref={textArea}
        id={textID}
        className={styles.fbText}
        value={draft.text}
        placeholder={placeholder}
        rows={compact ? 2 : 3}
        onChange={(event) => onChange({ ...draft, text: event.target.value })}
        onKeyDown={keyDown}
      />
      {count > 0 && (
        <ul className={styles.fbTray} aria-label="附图">
          {attached.map((image, index) => (
            <li key={image.id} className={styles.fbTrayItem}>
              <button
                type="button"
                className={styles.fbTrayThumb}
                aria-label={`查看附图 ${index + 1}`}
                onClick={() => onPreview(previews, index)}
              >
                <img src={image.url} alt="" />
              </button>
              <button
                type="button"
                className={styles.fbTrayRemove}
                aria-label={`移除附图 ${index + 1}`}
                onClick={() => onDetach?.(image.id)}
              >
                <X />
              </button>
            </li>
          ))}
          {draft.added.map((image, index) => (
            <li key={image.key} className={`${styles.fbTrayItem} ${styles.fresh}`}>
              <button
                type="button"
                className={styles.fbTrayThumb}
                aria-label={`查看${image.label}`}
                onClick={() => onPreview(previews, attached.length + index)}
              >
                <img src={image.url} alt="" />
              </button>
              <button
                type="button"
                className={styles.fbTrayRemove}
                aria-label={`移除${image.label}`}
                onClick={() => remove(image)}
              >
                <X />
              </button>
            </li>
          ))}
        </ul>
      )}
      {shown && (
        <p className={styles.fbEditorError} role="alert">
          {shown}
        </p>
      )}
      <div className={styles.fbEditorFoot}>
        <div className={styles.fbTools}>
          <button
            type="button"
            className={styles.fbTool}
            disabled={capturing || full}
            onClick={() => void capture()}
            title="截取当前桌面，不含反馈面板"
          >
            {capturing ? <LoaderCircle className={styles.fbSpin} /> : <Scan />}
            <span className={compact ? "sr-only" : undefined}>{capturing ? "截取中…" : "截取界面"}</span>
          </button>
          <button
            type="button"
            className={styles.fbTool}
            disabled={full}
            onClick={() => picker.current?.click()}
            title="也可以直接粘贴或拖入图片"
          >
            <ImagePlus />
            <span className={compact ? "sr-only" : undefined}>添加图片</span>
          </button>
          {count > 0 && (
            <span className={styles.fbTrayCount}>
              {count}/{maxImages}
            </span>
          )}
          <input
            ref={picker}
            type="file"
            accept={IMAGE_TYPES.join(",")}
            multiple
            hidden
            aria-label="选择图片"
            onChange={(event) => {
              add(Array.from(event.target.files ?? []).map((blob) => ({ blob, label: blob.name || "图片" })));
              event.target.value = "";
            }}
          />
        </div>
        <div className={styles.fbSubmit}>
          {onCancel && (
            <button type="button" className={styles.fbGhost} onClick={onCancel}>
              取消
            </button>
          )}
          <button type="submit" className={styles.fbPrimary} disabled={busy || empty || tooLong} title="Ctrl + Enter">
            {busy && <LoaderCircle className={styles.fbSpin} />}
            {submitLabel}
          </button>
        </div>
      </div>
      {dragging && (
        <div className={styles.fbDropHint} aria-hidden="true">
          <ImagePlus />
          <span>松开以添加图片</span>
        </div>
      )}
    </form>
  );
}
