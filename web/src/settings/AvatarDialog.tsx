import { ArrowLeft, ImagePlus, X, ZoomIn, ZoomOut } from "lucide-react";
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import Cropper, { type Area } from "react-easy-crop";

import { uploadAvatar, type User } from "../api";
import { ImageSources, loadImage } from "./ImageSources";
import { Banner, messageOf } from "./parts";
import "./avatar.css";

// AvatarDialog lets the user pick a picture from this computer, from their
// NAS files or from the photo library, crop it to a circle and save it. The
// page sends a 512 px square; the server turns it into the 256 px JPEG every
// page loads, so the original never travels again.

interface Picked {
  url: string;
  image: HTMLImageElement;
  revoke?: () => void;
}

// The upload is the small cropped square.
const croppedEdge = 512;

export function AvatarDialog({ onClose, onSaved }: { onClose: () => void; onSaved: (user: User) => void }) {
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

  // open loads the picture first, so a format the browser cannot show is
  // reported here rather than as an empty cropper.
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
  const save = async (pixels: Area) => {
    if (!picked) return;
    setSaving(true);
    setError("");
    try {
      onSaved(await uploadAvatar(await cropToJPEG(picked.image, pixels)));
    } catch (caught) {
      setError(messageOf(caught));
      setSaving(false);
    }
  };

  return (
    <div
      className="set-dialog-layer"
      onPointerDown={(event) => {
        if (event.target === event.currentTarget) close();
      }}
    >
      <div className="set-dialog avd" role="dialog" aria-modal="true" aria-label="更换头像">
        <header className="avd-head">
          {picked ? (
            <button className="set-icon-btn" aria-label="重新选择图片" onClick={repick} disabled={saving}>
              <ArrowLeft />
            </button>
          ) : (
            <span className="set-tile accent lg">
              <ImagePlus />
            </span>
          )}
          <div>
            <h3>{picked ? "调整头像" : "更换头像"}</h3>
            <p>
              {picked
                ? "拖动照片调整位置，滚动或拖动滑块缩放。"
                : "选择一张照片，下一步裁成圆形。保存后会压缩成小图，打开页面时不会加载原图。"}
            </p>
          </div>
          <button className="set-icon-btn" aria-label="关闭" onClick={close} disabled={saving}>
            <X />
          </button>
        </header>
        {error && (
          <div className="avd-banner">
            <Banner tone="error">{error}</Banner>
          </div>
        )}
        {picked ? (
          <CropStage picked={picked} saving={saving} onCancel={close} onSave={(pixels) => void save(pixels)} />
        ) : (
          <ImageSources opening={opening} onPick={(url, revoke) => void open(url, revoke)} onError={setError} />
        )}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------- Crop

function CropStage({
  picked,
  saving,
  onCancel,
  onSave,
}: {
  picked: Picked;
  saving: boolean;
  onCancel: () => void;
  onSave: (pixels: Area) => void;
}) {
  const [crop, setCrop] = useState({ x: 0, y: 0 });
  const [zoom, setZoom] = useState(1);
  const [area, setArea] = useState<Area>();
  const [pixels, setPixels] = useState<Area>();
  return (
    <>
      <div className="avd-crop">
        <div className="avd-crop-area">
          <Cropper
            image={picked.url}
            crop={crop}
            zoom={zoom}
            minZoom={1}
            maxZoom={5}
            zoomSpeed={0.25}
            aspect={1}
            cropShape="round"
            showGrid={false}
            objectFit="contain"
            onCropChange={setCrop}
            onZoomChange={setZoom}
            onCropComplete={(next, nextPixels) => {
              setArea(next);
              setPixels(nextPixels);
            }}
          />
        </div>
        <aside className="avd-side">
          <h4>预览</h4>
          <div className="avd-previews">
            {[80, 40, 28].map((size) => (
              <Preview key={size} picked={picked} area={area} size={size} />
            ))}
          </div>
          <p className="avd-preview-note">从左到右：我的账号、系统快捷栏、问题反馈中的大小</p>
          <label className="avd-zoom">
            <ZoomOut aria-hidden="true" />
            <input
              type="range"
              min={1}
              max={5}
              step={0.01}
              value={zoom}
              aria-label="缩放"
              onChange={(event) => setZoom(Number(event.target.value))}
            />
            <ZoomIn aria-hidden="true" />
          </label>
        </aside>
      </div>
      <footer className="avd-foot">
        <button className="set-btn" onClick={onCancel} disabled={saving}>
          取消
        </button>
        <button className="set-btn primary" onClick={() => pixels && onSave(pixels)} disabled={saving || !pixels}>
          {saving ? "正在保存…" : "保存头像"}
        </button>
      </footer>
    </>
  );
}

// Preview draws the chosen square at one of the sizes avatars appear in.
function Preview({ picked, area, size }: { picked: Picked; area?: Area; size: number }) {
  if (!area || area.width <= 0 || area.height <= 0)
    return <span className="avd-preview" style={{ width: size, height: size }} />;
  const width = (size * 100) / area.width;
  const height = (size * 100) / area.height;
  return (
    <span className="avd-preview" style={{ width: size, height: size }}>
      <img
        src={picked.url}
        alt=""
        style={{ width, height, left: (-area.x / 100) * width, top: (-area.y / 100) * height }}
      />
    </span>
  );
}

// cropToJPEG draws the chosen square at no more than 512 px on white, so a
// transparent PNG does not turn black.
export function cropToJPEG(image: HTMLImageElement, pixels: Area): Promise<Blob> {
  const edge = Math.max(1, Math.min(croppedEdge, Math.round(pixels.width)));
  const canvas = document.createElement("canvas");
  canvas.width = edge;
  canvas.height = edge;
  const context = canvas.getContext("2d");
  if (!context) return Promise.reject(new Error("无法处理图片"));
  context.fillStyle = "#fff";
  context.fillRect(0, 0, edge, edge);
  context.imageSmoothingQuality = "high";
  context.drawImage(image, pixels.x, pixels.y, pixels.width, pixels.height, 0, 0, edge, edge);
  return new Promise((resolve, reject) =>
    canvas.toBlob((blob) => (blob ? resolve(blob) : reject(new Error("无法处理图片"))), "image/jpeg", 0.92),
  );
}
