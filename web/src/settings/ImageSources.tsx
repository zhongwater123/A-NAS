import {
  ChevronRight,
  Folder,
  HardDrive,
  ImageOff,
  ImagePlus,
  Images,
  Laptop,
  type LucideIcon,
  UsersRound,
} from "lucide-react";
import { DragEvent, ReactNode, useEffect, useRef, useState } from "react";

import type { FileEntry, Space } from "../api.generated";
import { listEntries, listSpaces } from "../files/api";
import { fileVisualURL } from "../files/preview";
import { isPreviewableImage } from "../lib/fileTypes";
import {
  displayURL,
  listPhotoLibraries,
  listTimeline,
  type PhotoAsset,
  type PhotoLibrary,
  previewURL,
} from "../photos/api";
import { Banner, messageOf } from "./parts";
import styles from "./settings.module.css";

// ImageSources is where the avatar and wallpaper dialogs find a picture: this
// computer, the user's own NAS files or their photo libraries. Spaces and
// libraries seen through Administrative Viewing Mode belong to someone else
// and are left out.

type Source = "device" | "files" | "photos";

const sources: Array<{ id: Source; label: string; icon: LucideIcon }> = [
  { id: "device", label: "本机图片", icon: Laptop },
  { id: "files", label: "NAS 文件", icon: HardDrive },
  { id: "photos", label: "相册", icon: Images },
];

// The page decodes at most this much from the computer.
const maxLocalBytes = 64 << 20;

// ImageSources hands the chosen picture over as a URL the page can load;
// revoke frees a picture read from this computer. onError reports a file
// that cannot be used, and "" when the user moves to another source.
export function ImageSources({
  opening,
  onPick,
  onError,
}: {
  opening: boolean;
  onPick: (url: string, revoke?: () => void) => void;
  onError: (message: string) => void;
}) {
  const [source, setSource] = useState<Source>("device");
  return (
    <>
      <div className={styles.avdTabs} role="tablist" aria-label="图片来源">
        {sources.map(({ id, label, icon: Icon }) => (
          <button
            key={id}
            role="tab"
            aria-selected={source === id}
            className={source === id ? styles.active : ""}
            onClick={() => {
              setSource(id);
              onError("");
            }}
          >
            <Icon />
            {label}
          </button>
        ))}
      </div>
      <div
        className={styles.avdBody}
        role="tabpanel"
        aria-label={sources.find((item) => item.id === source)?.label}
        aria-busy={opening}
      >
        {source === "device" && (
          <DeviceSource
            onPick={(file) => {
              if (!file.type.startsWith("image/")) {
                onError("请选择图片文件。");
                return;
              }
              if (file.size > maxLocalBytes) {
                onError("这张图片太大了，请选择 64 MB 以内的图片。");
                return;
              }
              const url = URL.createObjectURL(file);
              onPick(url, () => URL.revokeObjectURL(url));
            }}
          />
        )}
        {source === "files" && <FileSource onPick={(entry) => onPick(fileVisualURL(entry, "preview"))} />}
        {source === "photos" && <PhotoSource onPick={(asset) => onPick(displayURL(asset))} />}
        {opening && (
          <div className={styles.avdOpening} role="status">
            <span className="loader" />
            正在打开图片…
          </div>
        )}
      </div>
    </>
  );
}

function DeviceSource({ onPick }: { onPick: (file: File) => void }) {
  const input = useRef<HTMLInputElement>(null);
  const [over, setOver] = useState(false);
  // A screenshot on the clipboard can be pasted straight in.
  useEffect(() => {
    const paste = (event: ClipboardEvent) => {
      const file = [...(event.clipboardData?.files ?? [])].find((item) => item.type.startsWith("image/"));
      if (file) {
        event.preventDefault();
        onPick(file);
      }
    };
    window.addEventListener("paste", paste);
    return () => window.removeEventListener("paste", paste);
  }, [onPick]);
  const drop = (event: DragEvent<HTMLDivElement>) => {
    event.preventDefault();
    setOver(false);
    const file = event.dataTransfer.files[0];
    if (file) onPick(file);
  };
  return (
    <div
      className={`${styles.avdDrop} ${over ? styles.over : ""}`}
      onDragOver={(event) => {
        event.preventDefault();
        setOver(true);
      }}
      onDragLeave={() => setOver(false)}
      onDrop={drop}
    >
      <span className={styles.avdDropArt} aria-hidden="true">
        <ImagePlus />
      </span>
      <h4>把照片拖到这里</h4>
      <p>或者从这台电脑上选择，也可以直接粘贴截图</p>
      <button className={`${styles.setBtn} primary`} onClick={() => input.current?.click()}>
        选择图片…
      </button>
      <input
        ref={input}
        type="file"
        accept="image/*"
        hidden
        aria-label="从本机选择图片"
        onChange={(event) => {
          const file = event.target.files?.[0];
          event.target.value = "";
          if (file) onPick(file);
        }}
      />
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
    void listSpaces()
      .then((items) => {
        const own = items
          .filter((space) => !space.viewing)
          .sort((left, right) => (left.kind === "private" ? -1 : 1) - (right.kind === "private" ? -1 : 1));
        setSpaces(own);
        setSpaceID(own[0]?.id ?? "");
      })
      .catch((caught) => setError(messageOf(caught)));
  }, []);
  const folder = trail.at(-1)?.id ?? "";
  // Each folder shows nothing until its own entries arrive.
  const browsed = JSON.stringify([spaceID, folder]);
  const [browsedBefore, setBrowsedBefore] = useState(browsed);
  if (browsedBefore !== browsed) {
    setBrowsedBefore(browsed);
    if (spaceID) {
      setEntries(undefined);
      setError("");
    }
  }
  useEffect(() => {
    if (!spaceID) return;
    let current = true;
    void listEntries(spaceID, folder)
      .then((items) => {
        if (current) setEntries(items);
      })
      .catch((caught) => {
        if (current) {
          setEntries([]);
          setError(messageOf(caught));
        }
      });
    return () => {
      current = false;
    };
  }, [spaceID, folder]);
  const folders = (entries ?? [])
    .filter((entry) => entry.kind === "directory" && !entry.name.startsWith("."))
    .sort(byName);
  const images = (entries ?? [])
    .filter((entry) => entry.kind === "file" && isPreviewableImage(entry.name))
    .sort(byName);
  const space = spaces?.find((item) => item.id === spaceID);
  return (
    <div className={styles.avdBrowser}>
      <div className={styles.avdToolbar}>
        {spaces && spaces.length > 1 && (
          <div className={styles.avdSegment} role="group" aria-label="空间">
            {spaces.map((item) => (
              <button
                key={item.id}
                aria-pressed={item.id === spaceID}
                onClick={() => {
                  setSpaceID(item.id);
                  setTrail([]);
                }}
              >
                {item.kind === "shared" ? <UsersRound /> : <HardDrive />}
                {spaceName(item)}
              </button>
            ))}
          </div>
        )}
        {trail.length > 0 && (
          <nav className={styles.avdCrumbs} aria-label="位置">
            <button onClick={() => setTrail([])}>{space ? spaceName(space) : "空间"}</button>
            {trail.map((entry, index) => (
              <span key={entry.id}>
                <ChevronRight aria-hidden="true" />
                <button onClick={() => setTrail(trail.slice(0, index + 1))} disabled={index === trail.length - 1}>
                  {entry.name}
                </button>
              </span>
            ))}
          </nav>
        )}
      </div>
      {error && <Banner tone="error">{error}</Banner>}
      {entries === undefined && !error ? (
        <Loading text="正在读取文件夹…" />
      ) : !folders.length && !images.length ? (
        !error && (
          <Empty
            icon={ImageOff}
            title="这里没有图片"
            text={trail.length ? "换个文件夹看看。" : "可以先把照片上传到 NAS，或者选择本机图片。"}
          />
        )
      ) : (
        <div className={styles.avdGrid} aria-label="文件夹和图片">
          {folders.map((entry) => (
            <button
              key={entry.id}
              className={`${styles.avdTile} ${styles.folder}`}
              onClick={() => setTrail([...trail, entry])}
              title={entry.name}
            >
              <span className={styles.avdThumb}>
                <Folder />
              </span>
              <span className={styles.avdName}>{entry.name}</span>
            </button>
          ))}
          {images.map((entry) => (
            <button
              key={entry.id}
              className={styles.avdTile}
              onClick={() => onPick(entry)}
              title={entry.name}
              aria-label={`选择 ${entry.name}`}
            >
              <span className={styles.avdThumb}>
                <img
                  src={fileVisualURL(entry, "thumbnail")}
                  alt=""
                  loading="lazy"
                  decoding="async"
                  onError={(event) => {
                    event.currentTarget.hidden = true;
                  }}
                />
              </span>
              <span className={styles.avdName}>{entry.name}</span>
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
    void listPhotoLibraries()
      .then((items) => {
        const own = items
          .filter((library) => !library.viewing)
          .sort((left, right) => (left.kind === "private" ? -1 : 1) - (right.kind === "private" ? -1 : 1));
        setLibraries(own);
        setLibraryID(own[0]?.id ?? "");
        if (!own.length) setAssets([]);
      })
      .catch((caught) => {
        setLibraries([]);
        setAssets([]);
        setError(messageOf(caught));
      });
  }, []);
  // Each library shows nothing until its own photos arrive.
  const [libraryBefore, setLibraryBefore] = useState(libraryID);
  if (libraryBefore !== libraryID) {
    setLibraryBefore(libraryID);
    if (libraryID) {
      setAssets(undefined);
      setNext(undefined);
      setError("");
    }
  }
  useEffect(() => {
    if (!libraryID) return;
    let current = true;
    void listTimeline(libraryID, "", 120)
      .then((page) => {
        if (current) {
          setAssets(page.items);
          setNext(page.next);
        }
      })
      .catch((caught) => {
        if (current) {
          setAssets([]);
          setError(messageOf(caught));
        }
      });
    return () => {
      current = false;
    };
  }, [libraryID]);
  const loadMore = async () => {
    if (!next) return;
    setMore(true);
    try {
      const page = await listTimeline(libraryID, next, 120);
      setAssets([...(assets ?? []), ...page.items]);
      setNext(page.next);
    } catch (caught) {
      setError(messageOf(caught));
    } finally {
      setMore(false);
    }
  };
  return (
    <div className={styles.avdBrowser}>
      {libraries && libraries.length > 1 && (
        <div className={styles.avdToolbar}>
          <div className={styles.avdSegment} role="group" aria-label="图库">
            {libraries.map((library) => (
              <button key={library.id} aria-pressed={library.id === libraryID} onClick={() => setLibraryID(library.id)}>
                {library.kind === "shared" ? <UsersRound /> : <Images />}
                {library.kind === "shared" ? "共享图库" : "我的图库"}
              </button>
            ))}
          </div>
        </div>
      )}
      {error && <Banner tone="error">{error}</Banner>}
      {assets === undefined ? (
        <Loading text="正在读取照片…" />
      ) : !assets.length ? (
        !error && <Empty icon={Images} title="还没有照片" text="导入相册的照片会出现在这里。" />
      ) : (
        <>
          <div className={`${styles.avdGrid} ${styles.photos}`} aria-label="照片">
            {assets.map((asset) => (
              <button
                key={asset.id}
                className={`${styles.avdTile} ${styles.photo}`}
                onClick={() => onPick(asset)}
                title={asset.name}
                aria-label={`选择 ${asset.name}`}
              >
                <span className={styles.avdThumb}>
                  <img
                    src={previewURL(asset)}
                    alt=""
                    loading="lazy"
                    decoding="async"
                    onError={(event) => {
                      event.currentTarget.hidden = true;
                    }}
                  />
                </span>
              </button>
            ))}
          </div>
          {next && (
            <button className={`${styles.setBtn} ${styles.avdMore}`} onClick={() => void loadMore()} disabled={more}>
              {more ? "正在加载…" : "加载更多照片"}
            </button>
          )}
        </>
      )}
    </div>
  );
}

function Loading({ text }: { text: string }) {
  return (
    <div className={styles.avdState} role="status">
      <span className="loader" />
      <p>{text}</p>
    </div>
  );
}

function Empty({ icon: Icon, title, text }: { icon: LucideIcon; title: string; text: ReactNode }) {
  return (
    <div className={styles.avdState}>
      <span className={styles.avdStateArt}>
        <Icon />
      </span>
      <h4>{title}</h4>
      <p>{text}</p>
    </div>
  );
}

function spaceName(space: Space): string {
  return space.kind === "shared" ? "共享空间" : "个人空间";
}

function byName(left: FileEntry, right: FileEntry): number {
  return left.name.localeCompare(right.name, "zh-CN", { numeric: true });
}

export function loadImage(url: string): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const image = new Image();
    image.decoding = "async";
    image.onload = () => (image.naturalWidth > 0 ? resolve(image) : reject(new Error("empty image")));
    image.onerror = () => reject(new Error("unreadable image"));
    image.src = url;
  });
}
