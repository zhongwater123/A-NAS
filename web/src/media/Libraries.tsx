import {
  AlertTriangle, Check, ChevronRight, Clapperboard, Film, Folder, FolderOpen, FolderPlus, HardDrive, Layers, Pencil, Plus, RefreshCw, Trash2, Tv, Users, Video, X,
} from "lucide-react";
import { FormEvent, ReactNode, useEffect, useState } from "react";

import { listEntries, type FileEntry, type Space } from "../api";
import { Empty, messageOf, PageHeader } from "./Browse";
import { Menu } from "./Cards";
import { ago, bytes } from "./format";
import { artworkURL, createLibrary, updateLibrary, type LibraryKind, type MediaLibrary } from "./mediaApi";

export const kindInfo: Record<LibraryKind, { label: string; icon: ReactNode; text: string }> = {
  movies: { label: "电影", icon: <Film />, text: "每个视频都是一部电影，按片名与年份识别" },
  shows: { label: "电视剧", icon: <Tv />, text: "按剧集文件夹、季与集号整理成剧" },
  mixed: { label: "混合", icon: <Layers />, text: "自动区分电影、剧集与其他视频" },
  other: { label: "其他", icon: <Video />, text: "家庭录像、短片等，按文件名展示" },
};

function counts(library: MediaLibrary): string {
  const parts = [
    library.counts.movies ? `${library.counts.movies} 部电影` : "",
    library.counts.shows ? `${library.counts.shows} 部剧 · ${library.counts.episodes} 集` : "",
    library.counts.others ? `${library.counts.others} 个视频` : "",
  ].filter(Boolean);
  return parts.length ? parts.join(" · ") : "还没有视频";
}

function scanText(library: MediaLibrary, processing: boolean): { text: string; busy: boolean; warn?: boolean } {
  if (library.scan.state === "scanning") return { text: "正在扫描文件夹…", busy: true };
  if (library.scan.state === "processing" && processing) return { text: `正在读取视频信息，还剩 ${library.scan.pending} 个`, busy: true };
  if (library.scan.error) return { text: library.scan.error, busy: false, warn: true };
  if (library.folders.some((folder) => folder.missing)) return { text: "有文件夹不存在", busy: false, warn: true };
  return { text: library.scan.scannedAt ? `${ago(library.scan.scannedAt)}扫描` : "等待扫描", busy: false };
}

function LibraryCovers({ library }: { library: MediaLibrary }) {
  const covers = library.covers.slice(0, 4);
  return (
    <div className={`mc-library-covers n${covers.length}`} aria-hidden="true">
      {covers.length ? covers.map((id) => (
        <img key={id} src={artworkURL(id, "poster")} alt="" loading="lazy"
          onError={(event) => { const image = event.currentTarget; if (!image.dataset.fallback) { image.dataset.fallback = "1"; image.src = artworkURL(id, "thumb"); } else image.style.visibility = "hidden"; }} />
      )) : <span className="mc-library-glyph">{kindInfo[library.kind].icon}</span>}
    </div>
  );
}

export function LibrariesPage({ libraries, processing, canCreate, onOpen, onCreate, onEdit, onScan, onDelete }: {
  libraries?: MediaLibrary[]; processing: boolean; canCreate: boolean;
  onOpen: (library: MediaLibrary) => void; onCreate: () => void; onEdit: (library: MediaLibrary) => void;
  onScan: (library: MediaLibrary) => void; onDelete: (library: MediaLibrary) => void;
}) {
  return (
    <div className="mc-page">
      <PageHeader title="媒体库" count={libraries?.length}>
        {canCreate && <button type="button" className="mc-primary small" onClick={onCreate}><Plus />新建媒体库</button>}
      </PageHeader>
      {libraries && !processing && libraries.length > 0 && (
        <p className="mc-note"><AlertTriangle />设备上没有可用的视频处理组件，暂时只能读取文件名，并直接播放浏览器支持的格式。</p>
      )}
      {!libraries ? null : !libraries.length ? (
        <Empty icon={<FolderPlus />} title="还没有媒体库" text="选择存放视频的文件夹，影视中心会自动整理。">
          {canCreate && <button type="button" className="mc-primary" onClick={onCreate}><Plus />新建媒体库</button>}
        </Empty>
      ) : (
        <div className="mc-library-grid">
          {libraries.map((library) => {
            const status = scanText(library, processing);
            return (
              <article key={library.id} className="mc-library">
                <button type="button" className="mc-library-open" onClick={() => onOpen(library)} aria-label={`打开媒体库${library.name}`}>
                  <LibraryCovers library={library} />
                </button>
                <div className="mc-library-body">
                  <header>
                    <strong>{library.name}</strong>
                    <span className={`mc-tag ${library.kind}`}>{kindInfo[library.kind].label}</span>
                    <span className="mc-tag space">{library.spaceKind === "shared" ? <><Users />共享</> : <><HardDrive />个人</>}</span>
                    {library.canManage && (
                      <Menu label={`${library.name}的操作`} className="mc-library-menu" items={[
                        { label: "重新扫描", icon: <RefreshCw />, run: () => onScan(library) },
                        { label: "编辑", icon: <Pencil />, run: () => onEdit(library) },
                        { label: "删除媒体库", icon: <Trash2 />, danger: true, run: () => onDelete(library) },
                      ]} />
                    )}
                  </header>
                  <p className="mc-library-counts">{counts(library)}{library.counts.sizeBytes ? <span> · {bytes(library.counts.sizeBytes)}</span> : null}</p>
                  <ul className="mc-library-folders">
                    {library.folders.map((folder) => (
                      <li key={folder.entryId} className={folder.missing ? "missing" : ""} title={folder.path}>
                        {folder.missing ? <AlertTriangle /> : <Folder />}<span>{folder.path || folder.name}</span>
                      </li>
                    ))}
                  </ul>
                  <p className={`mc-library-status ${status.warn ? "warn" : ""}`}>
                    {status.busy ? <span className="mc-spinner tiny" /> : status.warn ? <AlertTriangle /> : <Check />}{status.text}
                  </p>
                </div>
              </article>
            );
          })}
          {canCreate && (
            <button type="button" className="mc-library-add" onClick={onCreate}><span><Plus /></span>新建媒体库</button>
          )}
        </div>
      )}
    </div>
  );
}

interface PickedFolder { id: string; name: string; path: string }

// LibraryDialog creates or edits a library in two steps: what it holds, then
// which folders of one space.
export function LibraryDialog({ library, spaces, isAdmin, onClose, onSaved }: {
  library?: MediaLibrary; spaces: Space[]; isAdmin: boolean; onClose: () => void; onSaved: (library: MediaLibrary) => void;
}) {
  const usable = spaces.filter((space) => !space.viewing && (space.kind === "private" || isAdmin));
  const [step, setStep] = useState<1 | 2>(1);
  const [name, setName] = useState(library?.name ?? "");
  const [kind, setKind] = useState<LibraryKind>(library?.kind ?? "movies");
  const [spaceId, setSpaceId] = useState(library?.spaceId ?? usable.find((space) => space.kind === "shared")?.id ?? usable[0]?.id ?? "");
  const [picked, setPicked] = useState<PickedFolder[]>(library?.folders.map((folder) => ({ id: folder.entryId, name: folder.name, path: folder.path })) ?? []);
  const [trail, setTrail] = useState<{ id: string; name: string }[]>([]);
  const [entries, setEntries] = useState<FileEntry[]>();
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const parent = trail.at(-1)?.id ?? "";
  useEffect(() => {
    if (step !== 2 || !spaceId) return;
    let live = true;
    setEntries(undefined);
    listEntries(spaceId, parent).then((items) => { if (live) setEntries(items.filter((item) => item.kind === "directory" && !item.name.startsWith("."))); },
      (caught) => { if (live) { setEntries([]); setError(messageOf(caught)); } });
    return () => { live = false; };
  }, [step, spaceId, parent]);
  const space = usable.find((candidate) => candidate.id === spaceId);
  const pathOf = (entry: FileEntry) => [...trail.map((crumb) => crumb.name), entry.name].join("/");
  const toggle = (entry: FileEntry) => setPicked((current) => current.some((folder) => folder.id === entry.id)
    ? current.filter((folder) => folder.id !== entry.id)
    : [...current, { id: entry.id, name: entry.name, path: pathOf(entry) }]);
  const switchSpace = (id: string) => { if (id === spaceId) return; setSpaceId(id); setTrail([]); setPicked([]); };
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (step === 1) { if (name.trim()) setStep(2); return; }
    if (!picked.length) return;
    setSaving(true);
    setError("");
    try {
      const input = { name: name.trim(), kind, folderIds: picked.map((folder) => folder.id) };
      onSaved(library ? await updateLibrary(library.id, input) : await createLibrary({ ...input, spaceId }));
    } catch (caught) {
      setError(messageOf(caught));
      setSaving(false);
    }
  };
  return (
    <div className="mc-dialog-backdrop" onPointerDown={(event) => { if (event.target === event.currentTarget && !saving) onClose(); }}>
      <form className="mc-dialog wide" role="dialog" aria-modal="true" aria-label={library ? "编辑媒体库" : "新建媒体库"} onSubmit={(event) => void submit(event)}
        onKeyDown={(event) => { if (event.key === "Escape" && !saving) { event.stopPropagation(); onClose(); } }}>
        <header className="mc-dialog-head">
          <h3>{library ? "编辑媒体库" : "新建媒体库"}</h3>
          <ol className="mc-steps" aria-label="步骤">
            <li className={step === 1 ? "active" : "done"}><span>{step === 1 ? 1 : <Check />}</span>类型与名称</li>
            <li className={step === 2 ? "active" : ""}><span>2</span>选择文件夹</li>
          </ol>
          <button type="button" className="mc-icon-button" aria-label="关闭" onClick={onClose} disabled={saving}><X /></button>
        </header>
        {step === 1 ? (
          <div className="mc-dialog-body">
            <label className="mc-field">
              <span>名称</span>
              <input autoFocus value={name} maxLength={40} placeholder="例如：家庭影院、动画片" onChange={(event) => setName(event.target.value)} />
            </label>
            <div className="mc-field">
              <span>内容类型</span>
              <div className="mc-kinds" role="radiogroup" aria-label="内容类型">
                {(Object.keys(kindInfo) as LibraryKind[]).map((key) => (
                  <button key={key} type="button" role="radio" aria-checked={kind === key} className={kind === key ? "active" : ""} onClick={() => setKind(key)}>
                    <span className="mc-kind-icon">{kindInfo[key].icon}</span>
                    <strong>{kindInfo[key].label}</strong>
                    <small>{kindInfo[key].text}</small>
                  </button>
                ))}
              </div>
            </div>
            <p className="mc-hint"><Clapperboard />按 <code>电影名 (年份)</code> 或 <code>剧名/Season 1/S01E01</code> 命名效果最好；同目录的 NFO、poster.jpg、fanart.jpg 和字幕文件会被一并读取。</p>
          </div>
        ) : (
          <div className="mc-dialog-body">
            {!library && usable.length > 1 && (
              <div className="mc-segmented" role="tablist" aria-label="空间">
                {usable.map((candidate) => (
                  <button key={candidate.id} type="button" role="tab" aria-selected={candidate.id === spaceId} className={candidate.id === spaceId ? "active" : ""} onClick={() => switchSpace(candidate.id)}>
                    {candidate.kind === "shared" ? <><Users />共享空间</> : <><HardDrive />我的空间</>}
                  </button>
                ))}
              </div>
            )}
            <p className="mc-hint">{space?.kind === "shared" ? "共享空间的媒体库对所有成员可见，每个人的播放进度和收藏互不影响。" : "个人空间的媒体库只有你自己能看到。"}</p>
            <div className="mc-picker">
              <nav className="mc-picker-crumbs" aria-label="位置">
                <button type="button" onClick={() => setTrail([])}>{space?.kind === "shared" ? "共享空间" : "我的空间"}</button>
                {trail.map((crumb, index) => (
                  <span key={crumb.id}><ChevronRight /><button type="button" onClick={() => setTrail(trail.slice(0, index + 1))}>{crumb.name}</button></span>
                ))}
              </nav>
              <div className="mc-picker-list" role="listbox" aria-label="文件夹" aria-multiselectable="true">
                {!entries ? <span className="mc-spinner small" /> : !entries.length ? <p className="mc-picker-empty">这里没有子文件夹</p> : entries.map((entry) => {
                  const selected = picked.some((folder) => folder.id === entry.id);
                  return (
                    <div key={entry.id} className={`mc-picker-row ${selected ? "selected" : ""}`} role="option" aria-selected={selected}>
                      <button type="button" className="mc-check" aria-label={`${selected ? "取消选择" : "选择"}${entry.name}`} onClick={() => toggle(entry)}>{selected && <Check />}</button>
                      <button type="button" className="mc-picker-open" onClick={() => setTrail([...trail, { id: entry.id, name: entry.name }])}>
                        <FolderOpen /><span>{entry.name}</span><ChevronRight />
                      </button>
                    </div>
                  );
                })}
              </div>
            </div>
            <div className="mc-picked" aria-label="已选择的文件夹">
              {picked.length ? picked.map((folder) => (
                <span key={folder.id} className="mc-picked-chip" title={folder.path}>
                  <Folder />{folder.path || folder.name}
                  <button type="button" aria-label={`移除${folder.name}`} onClick={() => setPicked(picked.filter((item) => item.id !== folder.id))}><X /></button>
                </span>
              )) : <span className="mc-picked-none">勾选一个或多个文件夹</span>}
            </div>
          </div>
        )}
        {error && <p className="mc-form-error" role="alert">{error}</p>}
        <footer className="mc-dialog-actions">
          {step === 2 && <button type="button" className="mc-glass" onClick={() => setStep(1)} disabled={saving}>上一步</button>}
          <span />
          <button type="button" className="mc-glass" onClick={onClose} disabled={saving}>取消</button>
          <button type="submit" className="mc-primary" disabled={saving || (step === 1 ? !name.trim() : !picked.length)}>
            {step === 1 ? "下一步" : saving ? "正在保存…" : library ? "保存并重新扫描" : "创建并扫描"}
          </button>
        </footer>
      </form>
    </div>
  );
}
