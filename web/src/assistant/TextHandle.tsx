import { Check, Copy, Image as ImageIcon } from "lucide-react";
import { useEffect, useState } from "react";

import { copyText } from "../lib/clipboard";
import { getTextHandle, type TextHandleValue } from "./api";
import styles from "./assistant.module.css";

// What a handle stands for is asked once and kept for a minute: an answer
// renders again with every word it streams, and a field corrected in the
// photo app shows here soon after. Nothing is kept beyond the page.
const KEEP_MS = 60_000;
const asked = new Map<string, { at: number; value: Promise<TextHandleValue | undefined> }>();

function valueOf(handle: string): Promise<TextHandleValue | undefined> {
  const known = asked.get(handle);
  if (known && Date.now() - known.at < KEEP_MS) return known.value;
  // Not found, signed out or offline all show the same: no number.
  const value = getTextHandle(handle).catch(() => undefined);
  asked.set(handle, { at: Date.now(), value });
  return value;
}

/** Forget what was asked, for tests. */
export function forgetTextHandles() {
  asked.clear();
}

// A sensitive number the assistant named by its handle
// (docs/architecture/photo-ocr.md#ai-助手能用看不到具体的值): the number
// itself, which only this window asked the photo service for, with a copy and
// the photo it was read from. Members see the photo anyway, so nothing is
// masked (decision 2026-11-06); a value nobody checked yet says so, as the
// photo's own panel does.
export function TextHandle({
  handle,
  label,
  onOpenPhoto,
}: {
  handle: string;
  label: string;
  onOpenPhoto?: (photo: string) => void;
}) {
  // What was asked for a handle; while it is another handle's, this one loads.
  const [answer, setAnswer] = useState<{ handle: string; value: TextHandleValue | undefined }>();
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    let current = true;
    void valueOf(handle).then((value) => {
      if (current) setAnswer({ handle, value });
    });
    return () => {
      current = false;
    };
  }, [handle]);
  useEffect(() => {
    if (!copied) return;
    const timer = window.setTimeout(() => setCopied(false), 1600);
    return () => window.clearTimeout(timer);
  }, [copied]);

  if (answer?.handle !== handle)
    return (
      <span className={`${styles.asHandle} ${styles.pending}`} aria-busy="true">
        {label}
      </span>
    );
  const { value } = answer;
  if (!value)
    return (
      <span
        className={`${styles.asHandle} ${styles.pending}`}
        title="照片已删除、你已看不到它，或它所在的图库关了“照片中的文字”"
      >
        {label}（无法显示）
      </span>
    );
  return (
    <span className={styles.asHandle} title={`${value.label}，本地 AI 从 ${value.assetName} 读出`}>
      <span className={styles.asHandleValue}>{value.value}</span>
      {value.unchecked && <span className={styles.asHandleNote}>待核对</span>}
      <button
        type="button"
        className={copied ? styles.copied : undefined}
        aria-label={copied ? "已复制" : `复制${value.label}`}
        title={copied ? "已复制" : "复制"}
        onClick={() => setCopied(copyText(value.value))}
      >
        {copied ? <Check /> : <Copy />}
      </button>
      {onOpenPhoto && (
        <button
          type="button"
          aria-label={`在相册中打开 ${value.assetName}`}
          title="在相册中打开照片"
          onClick={() => onOpenPhoto(value.assetId)}
        >
          <ImageIcon />
        </button>
      )}
    </span>
  );
}
