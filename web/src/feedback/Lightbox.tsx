import { ChevronLeft, ChevronRight, X } from "lucide-react";
import { KeyboardEvent, useEffect, useRef } from "react";

import { FEEDBACK_UI } from "./capture";
import styles from "./feedback.module.css";

// Lightbox shows one image of a set over the whole desktop.
export function Lightbox({
  urls,
  index,
  onIndex,
  onClose,
}: {
  urls: string[];
  index: number;
  onIndex: (index: number) => void;
  onClose: () => void;
}) {
  const layer = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const returnTo = document.activeElement instanceof HTMLElement ? document.activeElement : undefined;
    layer.current?.focus({ preventScroll: true });
    return () => returnTo?.focus({ preventScroll: true });
  }, []);
  const many = urls.length > 1;
  const step = (delta: number) => onIndex((index + delta + urls.length) % urls.length);
  const keyDown = (event: KeyboardEvent) => {
    if (event.key === "Escape") {
      event.stopPropagation();
      onClose();
    }
    if (many && event.key === "ArrowLeft") step(-1);
    if (many && event.key === "ArrowRight") step(1);
  };
  return (
    <div
      ref={layer}
      className={styles.fbLightbox}
      {...{ [FEEDBACK_UI]: "" }}
      role="dialog"
      aria-modal="true"
      aria-label="查看图片"
      tabIndex={-1}
      onKeyDown={keyDown}
      onClick={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <img src={urls[index]} alt={`图片 ${index + 1}/${urls.length}`} />
      <button className={styles.fbLightboxClose} aria-label="关闭图片" onClick={onClose}>
        <X />
      </button>
      {many && (
        <>
          <button className={`${styles.fbLightboxStep} ${styles.prev}`} aria-label="上一张" onClick={() => step(-1)}>
            <ChevronLeft />
          </button>
          <button className={`${styles.fbLightboxStep} ${styles.next}`} aria-label="下一张" onClick={() => step(1)}>
            <ChevronRight />
          </button>
          <span className={styles.fbLightboxCount}>
            {index + 1} / {urls.length}
          </span>
        </>
      )}
    </div>
  );
}
