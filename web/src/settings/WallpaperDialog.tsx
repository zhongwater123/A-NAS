import { ArrowLeft, Wallpaper as WallpaperIcon, X } from "lucide-react";
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";

import type { Wallpaper, WallpaperStyle } from "../api.generated";
import { uploadWallpaper } from "../desktop/api";
import { wallpaperBackdrop } from "../desktop/wallpaper";
import { DesktopPreview } from "./DesktopPreview";
import { ImageSources, loadImage } from "./ImageSources";
import { Banner, messageOf } from "./parts";
import styles from "./settings.module.css";

// WallpaperDialog lets an administrator pick the picture every desktop shows,
// from this computer, their NAS files or their photo libraries. The page
// scales it to at most 3840 px on the long side and sends a JPEG, which leaves
// the photo's location and other metadata behind; the server makes the stored
// wallpaper from it all the same.

interface Picked {
  url: string;
  image: HTMLImageElement;
  revoke?: () => void;
}

export const wallpaperMaxEdge = 3840;

export function WallpaperDialog({
  style,
  onClose,
  onSaved,
}: {
  style: WallpaperStyle;
  onClose: () => void;
  onSaved: (wallpaper: Wallpaper) => void;
}) {
  const [picked, setPicked] = useState<Picked>();
  const [opening, setOpening] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  // Opening another image and closing let the last one go.
  const pickedRef = useRef<Picked>(undefined);
  useLayoutEffect(() => {
    pickedRef.current = picked;
  });
  useEffect(() => () => pickedRef.current?.revoke?.(), []);

  const close = useCallback(() => {
    if (!saving) onClose();
  }, [saving, onClose]);
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") close();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [close]);

  const open = useCallback(async (url: string, revoke?: () => void) => {
    setOpening(true);
    setError("");
    try {
      const image = await loadImage(url);
      pickedRef.current?.revoke?.();
      setPicked({ url, image, revoke });
    } catch {
      revoke?.();
      setError("浏览器打不开这张图片。HEIC 等格式可以先放到 NAS 上，再从“NAS 文件”或“相册”里选择。");
    } finally {
      setOpening(false);
    }
  }, []);

  const repick = () => {
    picked?.revoke?.();
    setPicked(undefined);
    setError("");
  };
  const save = async () => {
    if (!picked) return;
    setSaving(true);
    setError("");
    try {
      onSaved(await uploadWallpaper(await scaleToJPEG(picked.image)));
    } catch (caught) {
      setError(messageOf(caught));
      setSaving(false);
    }
  };

  const size = picked && fitted(picked.image.naturalWidth, picked.image.naturalHeight);
  return (
    <div
      className={styles.setDialogLayer}
      onPointerDown={(event) => {
        if (event.target === event.currentTarget) close();
      }}
    >
      <div
        className={`${styles.setDialog} ${styles.avd} ${picked ? styles.wpd : ""}`}
        role="dialog"
        aria-modal="true"
        aria-label="更换壁纸"
      >
        <header className={styles.avdHead}>
          {picked ? (
            <button className={styles.setIconBtn} aria-label="重新选择图片" onClick={repick} disabled={saving}>
              <ArrowLeft />
            </button>
          ) : (
            <span className={`${styles.setTile} ${styles.personalize} ${styles.lg}`}>
              <WallpaperIcon />
            </span>
          )}
          <div>
            <h3>{picked ? "设为壁纸" : "更换壁纸"}</h3>
            <p>
              {picked
                ? "这是它在桌面上的样子。设为壁纸后，所有账号的桌面都会换成这张图片。"
                : "选择一张图片作为所有账号的桌面壁纸。"}
            </p>
          </div>
          <button className={styles.setIconBtn} aria-label="关闭" onClick={close} disabled={saving}>
            <X />
          </button>
        </header>
        {error && (
          <div className={styles.avdBanner}>
            <Banner tone="error">{error}</Banner>
          </div>
        )}
        {picked && size ? (
          <>
            <div className={styles.wpdStage}>
              <DesktopPreview picture={{ url: picked.url, ...size }} style={style} label="新壁纸在桌面上的预览" />
              <p>
                {picked.image.naturalWidth} × {picked.image.naturalHeight} 像素
                {size.width < picked.image.naturalWidth ? `，保存时缩小到 ${size.width} × ${size.height}` : ""}
              </p>
            </div>
            <footer className={styles.avdFoot}>
              <button className={styles.setBtn} onClick={close} disabled={saving}>
                取消
              </button>
              <button className={`${styles.setBtn} primary`} onClick={() => void save()} disabled={saving}>
                {saving ? "正在保存…" : "设为壁纸"}
              </button>
            </footer>
          </>
        ) : (
          <ImageSources opening={opening} onPick={(url, revoke) => void open(url, revoke)} onError={setError} />
        )}
      </div>
    </div>
  );
}

function fitted(width: number, height: number): { width: number; height: number } {
  const ratio = Math.min(1, wallpaperMaxEdge / Math.max(width, height, 1));
  return { width: Math.max(1, Math.round(width * ratio)), height: Math.max(1, Math.round(height * ratio)) };
}

// scaleToJPEG draws the picture at no more than wallpaperMaxEdge on the long
// side over the desktop's navy, so transparency matches what the server keeps.
export function scaleToJPEG(image: HTMLImageElement): Promise<Blob> {
  const { width, height } = fitted(image.naturalWidth, image.naturalHeight);
  const canvas = document.createElement("canvas");
  canvas.width = width;
  canvas.height = height;
  const context = canvas.getContext("2d");
  if (!context) return Promise.reject(new Error("无法处理图片"));
  context.fillStyle = wallpaperBackdrop;
  context.fillRect(0, 0, width, height);
  context.imageSmoothingQuality = "high";
  context.drawImage(image, 0, 0, width, height);
  return new Promise((resolve, reject) =>
    canvas.toBlob((blob) => (blob ? resolve(blob) : reject(new Error("无法处理图片"))), "image/jpeg", 0.9),
  );
}
