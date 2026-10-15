import {
  ArrowLeft, ChevronRight, Folder, HardDrive, ImageOff, ImagePlus, Images, Laptop, UsersRound, X, ZoomIn, ZoomOut, type LucideIcon,
} from "lucide-react";
import { DragEvent, ReactNode, useCallback, useEffect, useRef, useState } from "react";
import Cropper, { type Area } from "react-easy-crop";

import { listEntries, listSpaces, uploadAvatar, type FileEntry, type Space, type User } from "../api";
import { isPreviewableImage } from "../fileTypes";
import { fileVisualURL } from "../files/preview";
import { displayURL, listPhotoLibraries, listTimeline, previewURL, type PhotoAsset, type PhotoLibrary } from "../photos/photosApi";
import { Banner, messageOf } from "./parts";
import "./avatar.css";

// AvatarDialog lets the user pick a picture from this computer, from their
// NAS files or from the photo library, crop it to a circle and save it. The
// page sends a 512 px square; the server turns it into the 256 px JPEG every
// page loads, so the original never travels again.

type Source = "device" | "files" | "photos";

interface Picked { url: string; image: HTMLImageElement; revoke?: () => void }

const sources: Array<{ id: Source; label: string; icon: LucideIcon }> = [
  { id: "device", label: "本机图片", icon: Laptop },
  { id: "files", label: "NAS 文件", icon: HardDrive },
  { id: "photos", label: "相册", icon: Images },
];

// The page decodes at most this much from the computer; the upload itself is
// the small cropped square.
const maxLocalBytes = 64 << 20;
const croppedEdge = 512;

export function AvatarDialog({ onClose, onSaved }: { onClose: () => void; onSaved: (user: User) => void }) {
  const [source, setSource] = useState<Source>("device");
  const [picked, setPicked] = useState<Picked>();
  const [opening, setOpening] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const pickedRef = useRef<Picked>(undefined);
  pickedRef.current = picked;
  useEffect(() => () => pickedRef.current?.revoke?.(), []);

  const close = useCallback(() => { if (!saving) onClose(); }, [saving, onClose]);
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => { if (event.key === "Escape") close(); };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [close]);

  // open loads the picture first, so a format the browser cannot show is
  // reported here rather than as an empty cropper.
  const open = useCallback(async (url: string, revoke?: () => void) => {
    setOpening(true); setError("");
    try {
      const image = await loadImage(url);
      pickedRef.current?.revoke?.();
      setPicked({ url, image, revoke });
    } catch {
      revoke?.();
      setError("浏览器打不开这张图片。HEIC 等格式可以先放到 NAS 上，再从“NAS 文件”或“相册”里选择。");
    } finally { setOpening(false); }
  }, []);

  const repick = () => { picked?.revoke?.(); setPicked(undefined); setError(""); };
  const save = async (pixels: Area) => {
    if (!picked) return;
    setSaving(true); setError("");
    try { onSaved(await uploadAvatar(await cropToJPEG(picked.image, pixels))); }
    catch (caught) { setError(messageOf(caught)); setSaving(false); }
  };

  return (
    <div className="set-dialog-layer" onPointerDown={(event) => { if (event.target === event.currentTarget) close(); }}>
      <div className="set-dialog avd" role="dialog" aria-modal="true" aria-label="更换头像">
        <header className="avd-head">
          {picked
            ? <button className="set-icon-btn" aria-label="重新选择图片" onClick={repick} disabled={saving}><ArrowLeft /></button>
            : <span className="set-tile accent lg"><ImagePlus /></span>}
          <div>
            <h3>{picked ? "调整头像" : "更换头像"}</h3>
            <p>{picked ? "拖动照片调整位置，滚动或拖动滑块缩放。" : "选择一张照片，下一步裁成圆形。保存后会压缩成小图，打开页面时不会加载原图。"}</p>
          </div>
          <button className="set-icon-btn" aria-label="关闭" onClick={close} disabled={saving}><X /></button>
        </header>
        {error && <div className="avd-banner"><Banner tone="error">{error}</Banner></div>}
        {picked
          ? <CropStage picked={picked} saving={saving} onCancel={close} onSave={(pixels) => void save(pixels)} />
          : <>
            <div className="avd-tabs" role="tablist" aria-label="图片来源">
              {sources.map(({ id, label, icon: Icon }) => (
                <button key={id} role="tab" aria-selected={source === id} className={source === id ? "active" : ""} onClick={() => { setSource(id); setError(""); }}>
                  <Icon />{label}
                </button>
              ))}
            </div>
            <div className="avd-body" role="tabpanel" aria-label={sources.find((item) => item.id === source)?.label} aria-busy={opening}>
              {source === "device" && <DeviceSource onPick={(file) => {
                if (!file.type.startsWith("image/")) { setError("请选择图片文件。"); return; }
                if (file.size > maxLocalBytes) { setError("这张图片太大了，请选择 64 MB 以内的图片。"); return; }
                const url = URL.createObjectURL(file);
                void open(url, () => URL.revokeObjectURL(url));
              }} />}
              {source === "files" && <FileSource onPick={(entry) => void open(fileVisualURL(entry, "preview"))} />}
              {source === "photos" && <PhotoSource onPick={(asset) => void open(displayURL(asset))} />}
              {opening && <div className="avd-opening" role="status"><span className="loader" />正在打开图片…</div>}
            </div>
          </>}
      </div>
    </div>
  );
}

// ------------------------------------------------------------------- Sources

function DeviceSource({ onPick }: { onPick: (file: File) => void }) {
  const input = useRef<HTMLInputElement>(null);
  const [over, setOver] = useState(false);
  // A screenshot on the clipboard can be pasted straight in.
  useEffect(() => {
    const paste = (event: ClipboardEvent) => {
      const file = [...(event.clipboardData?.files ?? [])].find((item) => item.type.startsWith("image/"));
      if (file) { event.preventDefault(); onPick(file); }
    };
    window.addEventListener("paste", paste);
    return () => window.removeEventListener("paste", paste);
  }, [onPick]);
  const drop = (event: DragEvent<HTMLDivElement>) => {
    event.preventDefault(); setOver(false);
    const file = event.dataTransfer.files[0];
    if (file) onPick(file);
  };
  return (
    <div className={`avd-drop ${over ? "over" : ""}`} onDragOver={(event) => { event.preventDefault(); setOver(true); }} onDragLeave={() => setOver(false)} onDrop={drop}>
      <span className="avd-drop-art" aria-hidden="true"><ImagePlus /></span>
      <h4>把照片拖到这里</h4>
      <p>或者从这台电脑上选择，也可以直接粘贴截图</p>
      <button className="set-btn primary" onClick={() => input.current?.click()}>选择图片…</button>
      <input ref={input} type="file" accept="image/*" hidden aria-label="从本机选择图片" onChange={(event) => {
        const file = event.target.files?.[0];
        event.target.value = "";
        if (file) onPick(file);
      }} />
      <small>JPEG、PNG、WebP 等浏览器能打开的图片</small>
    </div>
  );
}

function FileSource({ onPick }: { onPick: (entry: FileEntry) => void }) {
  const [spaces, setSpaces] = useState<Space[]>();
  const [spaceID, setSpaceID] = useState("");
  // The folders opened below the space root, outermost first.
  const [trail, setTrail] = useState<FileEntry[]>([]);
  const [entries, setEntries] = useState<FileEntry[]>();
  const [error, setError] = useState("");
  useEffect(() => {
    // Spaces seen through Administrative Viewing Mode belong to someone else.
    void listSpaces().then((items) => {
      const own = items.filter((space) => !space.viewing).sort((left, right) => (left.kind === "private" ? -1 : 1) - (right.kind === "private" ? -1 : 1));
      setSpaces(own);
      setSpaceID(own[0]?.id ?? "");
    }).catch((caught) => setError(messageOf(caught)));
  }, []);
  const folder = trail.at(-1)?.id ?? "";
  useEffect(() => {
    if (!spaceID) return;
    let current = true;
    setEntries(undefined); setError("");
    void listEntries(spaceID, folder).then((items) => { if (current) setEntries(items); })
      .catch((caught) => { if (current) { setEntries([]); setError(messageOf(caught)); } });
    return () => { current = false; };
  }, [spaceID, folder]);
  const folders = (entries ?? []).filter((entry) => entry.kind === "directory" && !entry.name.startsWith(".")).sort(byName);
  const images = (entries ?? []).filter((entry) => entry.kind === "file" && isPreviewableImage(entry.name)).sort(byName);
  const space = spaces?.find((item) => item.id === spaceID);
  return (
    <div className="avd-browser">
      <div className="avd-toolbar">
        {spaces && spaces.length > 1 && (
          <div className="avd-segment" role="group" aria-label="空间">
            {spaces.map((item) => (
              <button key={item.id} aria-pressed={item.id === spaceID} onClick={() => { setSpaceID(item.id); setTrail([]); }}>
                {item.kind === "shared" ? <UsersRound /> : <HardDrive />}{spaceName(item)}
              </button>
            ))}
          </div>
        )}
        {trail.length > 0 && (
          <nav className="avd-crumbs" aria-label="位置">
            <button onClick={() => setTrail([])}>{space ? spaceName(space) : "空间"}</button>
            {trail.map((entry, index) => (
              <span key={entry.id}><ChevronRight aria-hidden="true" /><button onClick={() => setTrail(trail.slice(0, index + 1))} disabled={index === trail.length - 1}>{entry.name}</button></span>
            ))}
          </nav>
        )}
      </div>
      {error && <Banner tone="error">{error}</Banner>}
      {entries === undefined && !error ? <Loading text="正在读取文件夹…" /> : !folders.length && !images.length ? (
        !error && <Empty icon={ImageOff} title="这里没有图片" text={trail.length ? "换个文件夹看看。" : "可以先把照片上传到 NAS，或者选择本机图片。"} />
      ) : (
        <div className="avd-grid" aria-label="文件夹和图片">
          {folders.map((entry) => (
            <button key={entry.id} className="avd-tile folder" onClick={() => setTrail([...trail, entry])} title={entry.name}>
              <span className="avd-thumb"><Folder /></span><span className="avd-name">{entry.name}</span>
            </button>
          ))}
          {images.map((entry) => (
            <button key={entry.id} className="avd-tile" onClick={() => onPick(entry)} title={entry.name} aria-label={`选择 ${entry.name}`}>
              <span className="avd-thumb"><img src={fileVisualURL(entry, "thumbnail")} alt="" loading="lazy" decoding="async" onError={(event) => { event.currentTarget.hidden = true; }} /></span>
              <span className="avd-name">{entry.name}</span>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

function PhotoSource({ onPick }: { onPick: (asset: PhotoAsset) => void }) {
  const [libraries, setLibraries] = useState<PhotoLibrary[]>();
  const [libraryID, setLibraryID] = useState("");
  const [assets, setAssets] = useState<PhotoAsset[]>();
  const [next, setNext] = useState<string>();
  const [more, setMore] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    void listPhotoLibraries().then((items) => {
      const own = items.filter((library) => !library.viewing).sort((left, right) => (left.kind === "private" ? -1 : 1) - (right.kind === "private" ? -1 : 1));
      setLibraries(own);
      setLibraryID(own[0]?.id ?? "");
      if (!own.length) setAssets([]);
    }).catch((caught) => { setLibraries([]); setAssets([]); setError(messageOf(caught)); });
  }, []);
  useEffect(() => {
    if (!libraryID) return;
    let current = true;
    setAssets(undefined); setNext(undefined); setError("");
    void listTimeline(libraryID, "", 120).then((page) => { if (current) { setAssets(page.items); setNext(page.next); } })
      .catch((caught) => { if (current) { setAssets([]); setError(messageOf(caught)); } });
    return () => { current = false; };
  }, [libraryID]);
  const loadMore = async () => {
    if (!next) return;
    setMore(true);
    try { const page = await listTimeline(libraryID, next, 120); setAssets([...(assets ?? []), ...page.items]); setNext(page.next); }
    catch (caught) { setError(messageOf(caught)); }
    finally { setMore(false); }
  };
  return (
    <div className="avd-browser">
      {libraries && libraries.length > 1 && (
        <div className="avd-toolbar">
          <div className="avd-segment" role="group" aria-label="图库">
            {libraries.map((library) => (
              <button key={library.id} aria-pressed={library.id === libraryID} onClick={() => setLibraryID(library.id)}>
                {library.kind === "shared" ? <UsersRound /> : <Images />}{library.kind === "shared" ? "共享图库" : "我的图库"}
              </button>
            ))}
          </div>
        </div>
      )}
      {error && <Banner tone="error">{error}</Banner>}
      {assets === undefined ? <Loading text="正在读取照片…" /> : !assets.length ? (
        !error && <Empty icon={Images} title="还没有照片" text="导入相册的照片会出现在这里。" />
      ) : (
        <>
          <div className="avd-grid photos" aria-label="照片">
            {assets.map((asset) => (
              <button key={asset.id} className="avd-tile photo" onClick={() => onPick(asset)} title={asset.name} aria-label={`选择 ${asset.name}`}>
                <span className="avd-thumb"><img src={previewURL(asset)} alt="" loading="lazy" decoding="async" onError={(event) => { event.currentTarget.hidden = true; }} /></span>
              </button>
            ))}
          </div>
          {next && <button className="set-btn avd-more" onClick={() => void loadMore()} disabled={more}>{more ? "正在加载…" : "加载更多照片"}</button>}
        </>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------- Crop

function CropStage({ picked, saving, onCancel, onSave }: { picked: Picked; saving: boolean; onCancel: () => void; onSave: (pixels: Area) => void }) {
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
            onCropComplete={(next, nextPixels) => { setArea(next); setPixels(nextPixels); }}
          />
        </div>
        <aside className="avd-side">
          <h4>预览</h4>
          <div className="avd-previews">
            {[80, 40, 28].map((size) => <Preview key={size} picked={picked} area={area} size={size} />)}
          </div>
          <p className="avd-preview-note">从左到右：我的账号、系统快捷栏、问题反馈中的大小</p>
          <label className="avd-zoom">
            <ZoomOut aria-hidden="true" />
            <input type="range" min={1} max={5} step={0.01} value={zoom} aria-label="缩放" onChange={(event) => setZoom(Number(event.target.value))} />
            <ZoomIn aria-hidden="true" />
          </label>
        </aside>
      </div>
      <footer className="avd-foot">
        <button className="set-btn" onClick={onCancel} disabled={saving}>取消</button>
        <button className="set-btn primary" onClick={() => pixels && onSave(pixels)} disabled={saving || !pixels}>{saving ? "正在保存…" : "保存头像"}</button>
      </footer>
    </>
  );
}

// Preview draws the chosen square at one of the sizes avatars appear in.
function Preview({ picked, area, size }: { picked: Picked; area?: Area; size: number }) {
  if (!area || area.width <= 0 || area.height <= 0) return <span className="avd-preview" style={{ width: size, height: size }} />;
  const width = (size * 100) / area.width;
  const height = (size * 100) / area.height;
  return (
    <span className="avd-preview" style={{ width: size, height: size }}>
      <img src={picked.url} alt="" style={{ width, height, left: (-area.x / 100) * width, top: (-area.y / 100) * height }} />
    </span>
  );
}

// ------------------------------------------------------------------- Helpers

function Loading({ text }: { text: string }) {
  return <div className="avd-state" role="status"><span className="loader" /><p>{text}</p></div>;
}

function Empty({ icon: Icon, title, text }: { icon: LucideIcon; title: string; text: ReactNode }) {
  return <div className="avd-state"><span className="avd-state-art"><Icon /></span><h4>{title}</h4><p>{text}</p></div>;
}

function spaceName(space: Space): string { return space.kind === "shared" ? "共享空间" : "个人空间"; }

function byName(left: FileEntry, right: FileEntry): number { return left.name.localeCompare(right.name, "zh-CN", { numeric: true }); }

function loadImage(url: string): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const image = new Image();
    image.decoding = "async";
    image.onload = () => (image.naturalWidth > 0 ? resolve(image) : reject(new Error("empty image")));
    image.onerror = () => reject(new Error("unreadable image"));
    image.src = url;
  });
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
  return new Promise((resolve, reject) => canvas.toBlob((blob) => (blob ? resolve(blob) : reject(new Error("无法处理图片"))), "image/jpeg", 0.92));
}
