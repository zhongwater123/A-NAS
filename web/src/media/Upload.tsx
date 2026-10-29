import {
  Captions,
  Check,
  ChevronDown,
  ChevronRight,
  CircleAlert,
  CloudUpload,
  FileText,
  Film,
  FolderOpen,
  FolderPlus,
  HardDrive,
  Image,
  LibraryBig,
  Plus,
  RotateCcw,
  Users,
  X,
} from "lucide-react";
import { CSSProperties, DragEvent, FormEvent, ReactNode, useEffect, useMemo, useRef, useState } from "react";

import type { FileEntry } from "../api.generated";
import { createDirectory, listEntries } from "../files/api";
import { formatBytes } from "../lib/bytes";
import type { Category, LibraryKind, MediaLibrary } from "./api";
import { messageOf } from "./Browse";
import { Dropdown } from "./Dropdown";
import { kindInfo } from "./Libraries";
import styles from "./media.module.css";
import {
  acceptedTypes,
  type Destination,
  type FileRole,
  pickedFromDrop,
  pickedFromList,
  type UploadItem,
  type Uploads,
} from "./uploads";

// Which libraries a page uploads into: a category page offers the libraries
// whose titles it lists.
const scopeKinds: Record<Category, LibraryKind[]> = {
  all: ["movies", "shows", "mixed", "other"],
  movie: ["movies", "mixed"],
  show: ["shows", "mixed"],
  other: ["other", "mixed"],
};
const scopeNames: Record<Category, string> = {
  all: "媒体库",
  movie: "电影媒体库",
  show: "电视剧媒体库",
  other: "其他视频媒体库",
};

export function uploadTargets(libraries: MediaLibrary[], scope: Category): MediaLibrary[] {
  return libraries.filter(
    (library) => scopeKinds[scope].includes(library.kind) && library.folders.some((folder) => !folder.missing),
  );
}

export const hasFiles = (event: DragEvent) => Array.from(event.dataTransfer?.types ?? []).includes("Files");

const hints: Record<LibraryKind, ReactNode> = {
  movies: (
    <>
      电影按 <code>片名 (年份).mkv</code> 命名识别最准；同名的字幕、NFO 和 <code>poster.jpg</code> 可以一起上传。
    </>
  ),
  shows: (
    <>
      每部剧放在自己的文件夹里，例如 <code>繁花/Season 1/繁花 S01E01.mkv</code>；可以直接上传整个剧集文件夹。
    </>
  ),
  mixed: (
    <>
      电影按 <code>片名 (年份)</code>、剧集按 <code>剧名/Season 1/S01E01</code> 命名效果最好；可以直接上传整个文件夹。
    </>
  ),
  other: <>家庭录像、短片等按文件名展示，可以按事件分别放进文件夹。</>,
};

// A mixed library tells titles apart by their names, so uploading from a
// category page says what makes a file land in that category.
const mixedHints: Partial<Record<Category, ReactNode>> = {
  movie: (
    <>
      混合媒体库按文件名区分类型：<code>片名 (年份).mkv</code> 会识别为电影；同名的字幕、NFO 和海报可以一起上传。
    </>
  ),
  show: (
    <>
      混合媒体库按文件名区分类型：放进 <code>剧名/Season 1/</code> 或命名为 <code>剧名 S01E01</code>{" "}
      才会识别为剧集；可以直接上传整个剧集文件夹。
    </>
  ),
  other: (
    <>
      混合媒体库按文件名区分类型：带年份的文件名会当作电影，手机和相机拍的视频归入“其他”。想全部放进“其他”，可以新建一个“其他”类型的媒体库。
    </>
  ),
};

const roleIcons: Record<FileRole, ReactNode> = {
  video: <Film />,
  subtitle: <Captions />,
  image: <Image />,
  nfo: <FileText />,
  other: <FileText />,
};

function statusOf(item: UploadItem): ReactNode {
  switch (item.state) {
    case "ready":
      return "待上传";
    case "waiting":
      return "排队中";
    case "uploading":
      return `${Math.floor(item.progress * 100)}%`;
    case "done":
      return (
        <>
          <Check />
          已上传
        </>
      );
    case "exists":
      return "同名文件已存在，未覆盖";
    case "skipped":
      return "不是视频、字幕、NFO 或海报，已跳过";
    case "failed":
      return item.error ?? "上传失败";
    case "canceled":
      return "已取消";
  }
}

const rememberKey = "anas.media.upload.library";
function remembered(): string {
  try {
    return window.localStorage.getItem(rememberKey) ?? "";
  } catch {
    return "";
  }
}
function remember(libraryId: string) {
  try {
    window.localStorage.setItem(rememberKey, libraryId);
  } catch {
    /* the choice is only a convenience */
  }
}

// progressOf sums what this run of uploads has sent, by bytes.
export function progressOf(items: UploadItem[]) {
  const started = items.filter((item) => item.to && item.state !== "canceled" && item.state !== "ready");
  const total = started.reduce((sum, item) => sum + item.file.size, 0);
  const sent = started.reduce(
    (sum, item) =>
      sum + item.file.size * (item.state === "uploading" ? item.progress : item.state === "waiting" ? 0 : 1),
    0,
  );
  const finished = started.filter((item) => item.state !== "waiting" && item.state !== "uploading").length;
  return {
    count: started.length,
    finished,
    share: total ? sent / total : 0,
    failed: items.filter((item) => item.state === "failed").length,
  };
}

// UploadDialog chooses a library folder and sends files into it. Uploads
// keep going when it closes; the top bar then shows their progress.
export function UploadDialog({
  uploads,
  libraries,
  scope,
  preferred,
  onClose,
  onCreateLibrary,
}: {
  uploads: Uploads;
  libraries: MediaLibrary[];
  scope: Category;
  preferred?: string;
  onClose: () => void;
  onCreateLibrary?: () => void;
}) {
  const targets = useMemo(() => uploadTargets(libraries, scope), [libraries, scope]);
  const initial = () => {
    const ids = [preferred, remembered()].filter(Boolean);
    return (
      ids.map((id) => targets.find((library) => library.id === id)).find(Boolean) ??
      targets.find((library) => library.spaceKind === "shared") ??
      targets[0]
    );
  };
  const [libraryId, setLibraryId] = useState(() => initial()?.id ?? "");
  const library = targets.find((candidate) => candidate.id === libraryId) ?? targets[0];
  const roots = library?.folders.filter((folder) => !folder.missing) ?? [];
  const [rootId, setRootId] = useState(roots[0]?.entryId ?? "");
  const root = roots.find((folder) => folder.entryId === rootId) ?? roots[0];
  const [trail, setTrail] = useState<{ id: string; name: string }[]>([]);
  const [browsing, setBrowsing] = useState(false);
  const [folders, setFolders] = useState<FileEntry[]>();
  const [naming, setNaming] = useState<string>();
  const [error, setError] = useState("");
  const [over, setOver] = useState(false);
  const fileInput = useRef<HTMLInputElement>(null);
  const folderInput = useRef<HTMLInputElement>(null);
  // The dialog takes focus, so Escape closes it and Tab starts inside it.
  const dialog = useRef<HTMLDivElement>(null);
  useEffect(() => {
    dialog.current?.focus({ preventScroll: true });
  }, []);
  const parentId = trail.at(-1)?.id ?? root?.entryId ?? "";

  // Libraries are polled, so the listing follows the space, not the object.
  const spaceId = library?.spaceId ?? "";
  // Each folder browsed shows nothing until its own folders arrive.
  const browsed = browsing && spaceId && parentId ? JSON.stringify([spaceId, parentId]) : "";
  const [browsedBefore, setBrowsedBefore] = useState(browsed);
  if (browsedBefore !== browsed) {
    setBrowsedBefore(browsed);
    if (browsed) setFolders(undefined);
  }
  useEffect(() => {
    if (!browsing || !spaceId || !parentId) return;
    let live = true;
    listEntries(spaceId, parentId).then(
      (items) => {
        if (live) setFolders(items.filter((item) => item.kind === "directory" && !item.name.startsWith(".")));
      },
      (caught) => {
        if (live) {
          setFolders([]);
          setError(messageOf(caught));
        }
      },
    );
    return () => {
      live = false;
    };
  }, [browsing, spaceId, parentId]);

  const items = uploads.items;
  const ready = items.filter((item) => item.state === "ready");
  const readyBytes = ready.reduce((sum, item) => sum + item.file.size, 0);
  const progress = progressOf(items);
  const places = new Set(items.map((item) => item.to?.place).filter(Boolean));
  const place =
    library && root
      ? [library.name, ...(roots.length > 1 ? [root.name] : []), ...trail.map((crumb) => crumb.name)].join(" / ")
      : "";

  const choose = (next: MediaLibrary) => {
    setLibraryId(next.id);
    setRootId(next.folders.find((folder) => !folder.missing)?.entryId ?? "");
    setTrail([]);
    setNaming(undefined);
    setError("");
  };
  const start = () => {
    if (!library || !parentId || !ready.length) return;
    const to: Destination = {
      libraryId: library.id,
      libraryName: library.name,
      spaceId: library.spaceId,
      spaceKind: library.spaceKind,
      parentId,
      place,
    };
    remember(library.id);
    setError("");
    uploads.start(to);
  };
  const makeFolder = async (event: FormEvent) => {
    event.preventDefault();
    const name = naming?.trim();
    if (!name || !library) return;
    try {
      const made = await createDirectory(library.spaceId, parentId, name);
      setNaming(undefined);
      setTrail([...trail, { id: made.id, name: made.name }]);
    } catch (caught) {
      setError(messageOf(caught));
    }
  };
  const drop = (event: DragEvent) => {
    if (!hasFiles(event)) return;
    event.preventDefault();
    setOver(false);
    if (!library) return;
    void pickedFromDrop(event.dataTransfer).then(uploads.add);
  };

  let summary: ReactNode;
  if (uploads.running)
    summary = (
      <>
        正在上传 {Math.min(progress.finished + 1, progress.count)}/{progress.count} · {Math.floor(progress.share * 100)}
        %
      </>
    );
  else if (ready.length)
    summary = (
      <>
        {ready.length} 个文件 · {formatBytes(readyBytes)}
      </>
    );
  else if (progress.count) {
    const count = (state: UploadItem["state"]) => items.filter((item) => item.state === state).length;
    summary = [
      count("done") && `已上传 ${count("done")} 个`,
      count("exists") && `${count("exists")} 个已存在`,
      count("failed") && `${count("failed")} 个失败`,
      count("canceled") && `${count("canceled")} 个已取消`,
    ]
      .filter(Boolean)
      .join("，");
  }

  return (
    <div
      className={styles.mcDialogBackdrop}
      onDragOver={(event) => {
        if (hasFiles(event)) {
          event.preventDefault();
          setOver(true);
        }
      }}
      onDragLeave={(event) => {
        if (event.target === event.currentTarget) setOver(false);
      }}
      onDrop={drop}
      onPointerDown={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <div
        ref={dialog}
        tabIndex={-1}
        className={`${styles.mcDialog} ${styles.wide} ${styles.mcUpload}`}
        role="dialog"
        aria-modal="true"
        aria-label="上传视频"
        onKeyDown={(event) => {
          if (event.key === "Escape") {
            event.stopPropagation();
            onClose();
          }
        }}
      >
        <header className={styles.mcDialogHead}>
          <span className={styles.mcUploadMark}>
            <CloudUpload />
          </span>
          <div className={styles.mcUploadTitle}>
            <h3>上传视频</h3>
            <small>保存到媒体库的文件夹，上传完成后自动出现在影视中心</small>
          </div>
          <button type="button" className={styles.mcIconButton} aria-label="关闭" onClick={onClose}>
            <X />
          </button>
        </header>
        {!library ? (
          <div className={styles.mcDialogBody}>
            <div className={`${styles.mcState} ${styles.compact}`}>
              <LibraryBig />
              <h2>还没有{scopeNames[scope]}</h2>
              <p>先新建一个媒体库，选好存放视频的文件夹，再把视频上传进去。</p>
              {onCreateLibrary && (
                <button type="button" className={styles.mcPrimary} onClick={onCreateLibrary}>
                  <Plus />
                  新建媒体库
                </button>
              )}
            </div>
          </div>
        ) : (
          <div className={styles.mcDialogBody}>
            <section className={styles.mcUploadTarget} aria-label="上传位置">
              {targets.length > 1 && (
                <div className={styles.mcUploadLibraries} role="radiogroup" aria-label="媒体库">
                  {targets.map((candidate) => (
                    <button
                      key={candidate.id}
                      type="button"
                      role="radio"
                      aria-checked={candidate.id === library.id}
                      className={candidate.id === library.id ? styles.active : ""}
                      onClick={() => choose(candidate)}
                    >
                      <span className={styles.mcKindIcon}>{kindInfo[candidate.kind].icon}</span>
                      <span>
                        <strong>{candidate.name}</strong>
                        <small>
                          {kindInfo[candidate.kind].label} · {candidate.spaceKind === "shared" ? "共享" : "个人"}
                        </small>
                      </span>
                    </button>
                  ))}
                </div>
              )}
              <div className={styles.mcUploadWhere}>
                <span
                  className={styles.mcUploadSpace}
                  title={library.spaceKind === "shared" ? "共享空间：所有成员可见" : "个人空间：只有你能看到"}
                >
                  {library.spaceKind === "shared" ? <Users /> : <HardDrive />}
                </span>
                <nav className={styles.mcUploadCrumbs} aria-label="保存位置">
                  <strong>{library.name}</strong>
                  {roots.length > 1 ? (
                    <span>
                      <ChevronRight />
                      <Dropdown
                        label="媒体库文件夹"
                        value={root?.entryId ?? ""}
                        onChange={(value) => {
                          setRootId(value);
                          setTrail([]);
                        }}
                        choices={roots.map((folder) => ({
                          value: folder.entryId,
                          label: folder.name,
                          hint: folder.path !== folder.name ? folder.path : undefined,
                        }))}
                      />
                    </span>
                  ) : (
                    <span>
                      <ChevronRight />
                      <button type="button" onClick={() => setTrail([])}>
                        {root?.name}
                      </button>
                    </span>
                  )}
                  {trail.map((crumb, index) => (
                    <span key={crumb.id}>
                      <ChevronRight />
                      <button type="button" onClick={() => setTrail(trail.slice(0, index + 1))}>
                        {crumb.name}
                      </button>
                    </span>
                  ))}
                </nav>
                <button
                  type="button"
                  className={styles.mcLink}
                  aria-expanded={browsing}
                  onClick={() => setBrowsing(!browsing)}
                >
                  {browsing ? "收起" : "选择子文件夹"}
                  <ChevronDown className={browsing ? styles.flipped : ""} />
                </button>
              </div>
              {browsing && (
                <div className={styles.mcPicker}>
                  <div className={`${styles.mcPickerList} ${styles.short}`} role="list" aria-label="子文件夹">
                    {!folders ? (
                      <span className={`${styles.mcSpinner} ${styles.small}`} />
                    ) : (
                      folders.map((entry) => (
                        <div key={entry.id} className={styles.mcPickerRow} role="listitem">
                          <button
                            type="button"
                            className={styles.mcPickerOpen}
                            onClick={() => setTrail([...trail, { id: entry.id, name: entry.name }])}
                          >
                            <FolderOpen />
                            <span>{entry.name}</span>
                            <ChevronRight />
                          </button>
                        </div>
                      ))
                    )}
                    {folders && !folders.length && naming === undefined && (
                      <p className={styles.mcPickerEmpty}>这里没有子文件夹，文件会直接保存在这里</p>
                    )}
                  </div>
                  {naming === undefined ? (
                    <button type="button" className={styles.mcPickerNew} onClick={() => setNaming("")}>
                      <FolderPlus />
                      新建文件夹
                    </button>
                  ) : (
                    <form
                      className={`${styles.mcNewCollection} ${styles.mcPickerNaming}`}
                      onSubmit={(event) => void makeFolder(event)}
                    >
                      <FolderPlus />
                      <input
                        autoFocus
                        value={naming}
                        maxLength={120}
                        placeholder="文件夹名称，例如剧名"
                        aria-label="新文件夹名称"
                        onChange={(event) => setNaming(event.target.value)}
                        onKeyDown={(event) => {
                          if (event.key === "Escape") {
                            event.stopPropagation();
                            setNaming(undefined);
                          }
                        }}
                      />
                      <button type="submit" className={`${styles.mcPrimary} ${styles.small}`} disabled={!naming.trim()}>
                        新建
                      </button>
                    </form>
                  )}
                </div>
              )}
            </section>

            <div className={`${styles.mcDropzone} ${over ? styles.over : ""} ${items.length ? styles.compact : ""}`}>
              <span className={styles.mcDropzoneIcon}>
                <CloudUpload />
              </span>
              <div className={styles.mcDropzoneText}>
                <strong>把视频文件或整个文件夹拖到这里</strong>
                <small>支持视频、字幕（SRT、ASS、VTT）、NFO 和海报图片</small>
              </div>
              <div className={styles.mcDropzoneActions}>
                <button
                  type="button"
                  className={`${styles.mcGlass} ${styles.small}`}
                  onClick={() => fileInput.current?.click()}
                >
                  <Film />
                  选择文件
                </button>
                <button
                  type="button"
                  className={`${styles.mcGlass} ${styles.small}`}
                  onClick={() => folderInput.current?.click()}
                >
                  <FolderOpen />
                  选择文件夹
                </button>
              </div>
              <input
                ref={fileInput}
                type="file"
                multiple
                accept={acceptedTypes}
                hidden
                aria-label="选择要上传的文件"
                onChange={(event) => {
                  uploads.add(pickedFromList(event.target.files));
                  event.target.value = "";
                }}
              />
              {/* React has no prop for choosing a whole folder. */}
              <input
                ref={(element) => {
                  folderInput.current = element;
                  element?.setAttribute("webkitdirectory", "");
                }}
                type="file"
                multiple
                hidden
                aria-label="选择要上传的文件夹"
                onChange={(event) => {
                  uploads.add(pickedFromList(event.target.files));
                  event.target.value = "";
                }}
              />
            </div>

            {items.length > 0 && (
              <ul className={styles.mcUploadList} aria-label="上传列表">
                {items.map((item) => (
                  <li key={item.id} className={`${styles.mcUploadRow} ${styles[item.state] ?? ""}`}>
                    <span className={`${styles.mcUploadType} ${styles[item.role] ?? ""}`}>{roleIcons[item.role]}</span>
                    <div className={styles.mcUploadMain}>
                      <div className={styles.mcUploadLine}>
                        <strong title={item.folder ? `${item.folder}/${item.file.name}` : item.file.name}>
                          {item.file.name}
                        </strong>
                        <span className={styles.mcUploadStatus}>
                          {item.state === "failed" && <CircleAlert />}
                          {statusOf(item)}
                        </span>
                      </div>
                      <small>
                        {[
                          item.folder,
                          formatBytes(item.file.size),
                          places.size > 1 && item.to ? `到 ${item.to.place}` : "",
                        ]
                          .filter(Boolean)
                          .join(" · ")}
                      </small>
                      {item.state === "uploading" && (
                        <span
                          className={styles.mcUploadBar}
                          role="progressbar"
                          aria-label={`${item.file.name}上传进度`}
                          aria-valuemin={0}
                          aria-valuemax={100}
                          aria-valuenow={Math.floor(item.progress * 100)}
                        >
                          <span style={{ width: `${Math.max(2, item.progress * 100)}%` }} />
                        </span>
                      )}
                    </div>
                    {(item.state === "failed" || (item.state === "canceled" && item.to)) && (
                      <button
                        type="button"
                        className={`${styles.mcIconButton} ${styles.small}`}
                        aria-label={`重试${item.file.name}`}
                        title="重试"
                        onClick={() => uploads.retry(item.id)}
                      >
                        <RotateCcw />
                      </button>
                    )}
                    {(item.state === "waiting" || item.state === "uploading") && (
                      <button
                        type="button"
                        className={`${styles.mcIconButton} ${styles.small}`}
                        aria-label={`取消上传${item.file.name}`}
                        title="取消"
                        onClick={() => uploads.cancel(item.id)}
                      >
                        <X />
                      </button>
                    )}
                    {(item.state === "ready" || item.state === "skipped") && (
                      <button
                        type="button"
                        className={`${styles.mcIconButton} ${styles.small}`}
                        aria-label={`移除${item.file.name}`}
                        title="移除"
                        onClick={() => uploads.remove(item.id)}
                      >
                        <X />
                      </button>
                    )}
                  </li>
                ))}
              </ul>
            )}
            <p className={styles.mcHint}>
              <Film />
              {(library.kind === "mixed" && mixedHints[scope]) || hints[library.kind]}
            </p>
          </div>
        )}
        {error && (
          <p className={styles.mcFormError} role="alert">
            {error}
          </p>
        )}
        <footer className={styles.mcDialogActions}>
          <span className={styles.mcUploadSummary} aria-live="polite">
            {summary}
          </span>
          {uploads.running ? (
            <>
              <button type="button" className={styles.mcGlass} onClick={uploads.cancelAll}>
                全部取消
              </button>
              <button type="button" className={styles.mcPrimary} onClick={onClose}>
                在后台上传
              </button>
            </>
          ) : ready.length ? (
            <>
              <button type="button" className={styles.mcGlass} onClick={onClose}>
                取消
              </button>
              <button
                type="button"
                className={`${styles.mcPrimary} ${styles.mcUploadStart}`}
                onClick={start}
                disabled={!parentId}
              >
                <CloudUpload />
                上传 {ready.length} 个文件
              </button>
            </>
          ) : (
            <button type="button" className={progress.count ? styles.mcPrimary : styles.mcGlass} onClick={onClose}>
              {progress.count ? "完成" : "关闭"}
            </button>
          )}
        </footer>
      </div>
    </div>
  );
}

// UploadPill shows uploads that run while the dialog is closed.
export function UploadPill({ uploads, onOpen }: { uploads: Uploads; onOpen: () => void }) {
  const progress = progressOf(uploads.items);
  if (!uploads.running && !progress.failed) return null;
  const share = Math.floor(progress.share * 100);
  return (
    <button
      type="button"
      className={`${styles.mcUploadPill} ${uploads.running ? "" : styles.failed}`}
      onClick={onOpen}
      aria-label={
        uploads.running ? `正在上传，已完成 ${share}%，查看上传列表` : `${progress.failed} 个文件上传失败，查看上传列表`
      }
    >
      {uploads.running ? (
        <span className={styles.mcUploadRing} style={{ "--mc-share": `${share * 3.6}deg` } as CSSProperties}>
          <CloudUpload />
        </span>
      ) : (
        <CircleAlert />
      )}
      <span>
        {uploads.running
          ? `上传中 ${Math.min(progress.finished + 1, progress.count)}/${progress.count}`
          : `${progress.failed} 个上传失败`}
      </span>
    </button>
  );
}

// DropOverlay takes files dropped on a page that can upload.
export function DropOverlay({
  label,
  onDrop,
  onLeave,
}: {
  label: string;
  onDrop: (event: DragEvent) => void;
  onLeave: () => void;
}) {
  return (
    <div
      className={styles.mcDrop}
      onDragOver={(event) => event.preventDefault()}
      onDragLeave={(event) => {
        if (event.target === event.currentTarget) onLeave();
      }}
      onDrop={(event) => {
        event.preventDefault();
        onDrop(event);
      }}
    >
      <div className={styles.mcDropCard}>
        <span className={styles.mcDropzoneIcon}>
          <CloudUpload />
        </span>
        <strong>松开即可上传到{label}</strong>
        <small>可以拖入视频、字幕、海报，或整个文件夹</small>
      </div>
    </div>
  );
}
