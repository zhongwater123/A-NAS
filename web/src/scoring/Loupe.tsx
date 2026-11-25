import { Spinner } from "../ui/workspace";
import type { FullView } from "./api";
import { orientationMatrix, uprightSize } from "./model";
import styles from "./scoring.module.css";

// The overview of the whole photo fits in this box, in CSS pixels.
const overviewWidth = 200;
const overviewHeight = 140;

// Loupe shows the photo at 100% in place of the whole view: one pixel of the
// photo to one pixel of the screen, so that focus, shake and noise show. The
// point of the photo under the pointer stays under it. box is the shown
// photo's size in CSS pixels, at where the pointer is in it, 0 to 1 across
// and down. The photo comes in its stored orientation and is turned upright
// here, as the browser would for an <img> with its EXIF. An overview in the
// corner shows the whole photo, preview, with the part on screen framed.
export function Loupe({
  view,
  failure,
  preview,
  box,
  at,
}: {
  view?: FullView;
  failure?: string;
  preview: string;
  box: { width: number; height: number };
  at: { x: number; y: number };
}) {
  if (failure) {
    return (
      <div className={styles.scLoupe}>
        <span className={styles.scLoupeNote} role="status">
          {failure}
        </span>
      </div>
    );
  }
  if (!view) {
    return (
      <div className={`${styles.scLoupe} ${styles.loading}`} role="status" aria-label="正在载入 100% 原图">
        <Spinner small />
      </div>
    );
  }
  const ratio = window.devicePixelRatio || 1;
  const stored = { width: view.width / ratio, height: view.height / ratio };
  const upright = uprightSize(view.orientation, stored.width, stored.height);
  // Larger than the box, the photo slides so that the point at the pointer
  // stays put; smaller, it sits in the middle.
  const place = (room: number, size: number, fraction: number) =>
    size <= room ? (room - size) / 2 : fraction * (room - size);
  const left = place(box.width, upright.width, at.x);
  const top = place(box.height, upright.height, at.y);
  const [a, b, c, d, e, f] = orientationMatrix(view.orientation, stored.width, stored.height);
  const scale = Math.min(overviewWidth / upright.width, overviewHeight / upright.height);
  return (
    <div className={styles.scLoupe}>
      <div
        className={styles.scLoupeCanvas}
        style={{
          insetInlineStart: left,
          insetBlockStart: top,
          inlineSize: upright.width,
          blockSize: upright.height,
        }}
      >
        <img
          src={view.url}
          alt="这张照片的 100% 局部"
          draggable={false}
          style={{
            inlineSize: stored.width,
            blockSize: stored.height,
            transform: `matrix(${a}, ${b}, ${c}, ${d}, ${e}, ${f})`,
          }}
        />
      </div>
      <span className={styles.scLoupeScale} aria-hidden="true">
        <b>100%</b>单击返回
      </span>
      <div
        className={styles.scOverview}
        style={{ inlineSize: upright.width * scale, blockSize: upright.height * scale }}
        aria-hidden="true"
      >
        <img src={preview} alt="" draggable={false} />
        <i
          style={{
            insetInlineStart: Math.max(0, -left) * scale,
            insetBlockStart: Math.max(0, -top) * scale,
            inlineSize: Math.min(box.width, upright.width) * scale,
            blockSize: Math.min(box.height, upright.height) * scale,
          }}
        />
      </div>
    </div>
  );
}
