import { useLayoutEffect, useRef, useState, type CSSProperties } from "react";

import type { WallpaperStyle } from "../api";
import { drawWallpaper, type WallpaperPicture } from "../wallpaper";
import "./personalization.css";

// DesktopPreview draws a small copy of this browser's desktop with picture
// laid out in style, so 居中 and 平铺 show the picture's size against the
// screen it will fill. The faint rail, status bar and dock only place it.
export function DesktopPreview({
  picture,
  style,
  label,
}: {
  picture?: WallpaperPicture;
  style: WallpaperStyle;
  label: string;
}) {
  const frame = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(0);
  useLayoutEffect(() => {
    const element = frame.current;
    if (!element) return;
    setWidth(element.getBoundingClientRect().width);
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(([entry]) => setWidth(entry.contentRect.width));
    observer.observe(element);
    return () => observer.disconnect();
  }, []);
  const screen = { width: Math.max(1, window.innerWidth), height: Math.max(1, window.innerHeight) };
  const scale = width > 0 ? width / screen.width : 0.2;
  return (
    <div
      ref={frame}
      className="pz-preview"
      role="img"
      aria-label={label}
      style={
        {
          aspectRatio: `${screen.width} / ${screen.height}`,
          "--pz-aspect": screen.width / screen.height,
          ...(picture ? drawWallpaper(style, picture, scale) : {}),
        } as CSSProperties
      }
    >
      <span className="pz-preview-rail" aria-hidden="true" />
      <span className="pz-preview-status" aria-hidden="true" />
      <span className="pz-preview-dock" aria-hidden="true" />
    </div>
  );
}
