import { ChevronLeft, ChevronRight, CircleAlert, CircleCheck, Copy, Download, ImagePlus, Pencil, RefreshCw, RotateCcw, Search, ShieldCheck, Trash2, X } from "lucide-react";
import { ChangeEvent, DragEvent, FormEvent, useCallback, useEffect, useMemo, useRef, useState } from "react";

import { APIError, endViewing } from "./api";
import {
  PhotoAsset, PhotoLibrary, copyPhoto, emptyPhotoTrash, getPhoto, listPhotoLibraries, listPhotoTrash, listTimeline, originalURL,
  previewURL, purgePhoto, renamePhoto, restorePhoto, searchPhotos, trashPhoto, uploadPhoto,
} from "./photosApi";

interface Props { userId: string; isAdmin: boolean }

// PhotosPanel is the managed photo library: a timeline per library, search
// over the caller's and the shared library (plus a member library while
// viewing it), a viewer and the library's trash.
// All rules live in the photo service; the panel only hides actions the
// caller could not perform anyway.
export function PhotosPanel({ userId, isAdmin }: Props) {
  const [libraries, setLibraries] = useState<PhotoLibrary[]>([]);
  const [libraryId, setLibraryId] = useState("");
  const [view, setView] = useState<"timeline" | "trash" | "search">("timeline");
  const [query, setQuery] = useState("");
  const [searched, setSearched] = useState("");
  const [semantic, setSemantic] = useState(true);
  const [assets, setAssets] = useState<PhotoAsset[]>([]);
  const [next, setNext] = useState<string>();
  const [trash, setTrash] = useState<PhotoAsset[]>([]);
  const [selected, setSelected] = useState<number>();
  const [uploading, setUploading] = useState<{ done: number; total: number }>();
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [loading, setLoading] = useState(true);

  const library = libraries.find((item) => item.id === libraryId);
  const shared = libraries.find((item) => item.kind === "shared");
  const mine = libraries.find((item) => item.kind === "private" && item.ownerUserId === userId && !item.viewing);
  const readOnly = Boolean(library?.viewing);
  const viewingUntil = library?.viewing ? new Date(library.viewing.expiresAt).toLocaleString("zh-CN") : "";
  // Search results come from several libraries, so actions follow each
  // photo's own library.
  const libraryOf = (asset: PhotoAsset) => libraries.find((item) => item.id === asset.libraryId);
  const canChange = (asset: PhotoAsset) => {
    const owner = libraryOf(asset);
    return Boolean(owner && !owner.viewing && (owner.kind === "private" || asset.uploadedBy === userId || isAdmin));
  };

  const loadLibraries = useCallback(async () => {
    try {
      const items = await listPhotoLibraries();
      setLibraries(items);
      setLibraryId((current) => items.some((item) => item.id === current)
        ? current
        : (items.find((item) => item.kind === "private" && item.ownerUserId === userId) ?? items[0])?.id ?? "");
    } catch (caught) { setError(messageOf(caught)); setLoading(false); }
  }, [userId]);
  useEffect(() => { void loadLibraries(); }, [loadLibraries]);

  const stopViewing = async () => {
    if (!library?.viewing) return;
    try { await endViewing(library.viewing.grantId); setError(""); await loadLibraries(); }
    catch (caught) { setError(messageOf(caught)); }
  };

  const loadTimeline = useCallback(async (cursor = "") => {
    if (!libraryId) return;
    try {
      const page = await listTimeline(libraryId, cursor);
      setAssets((current) => cursor ? [...current, ...page.items] : page.items);
      setNext(page.next);
      setError("");
    } catch (caught) { setError(messageOf(caught)); }
    finally { setLoading(false); }
  }, [libraryId]);

  const loadSearch = useCallback(async (cursor = "") => {
    if (!searched) return;
    try {
      const page = await searchPhotos(searched, readOnly ? libraryId : "", cursor);
      setAssets((current) => cursor ? [...current, ...page.items] : page.items);
      setNext(page.next);
      setSemantic(page.semantic);
      setError("");
    } catch (caught) { setError(messageOf(caught)); }
    finally { setLoading(false); }
  }, [searched, readOnly, libraryId]);

  const loadTrash = useCallback(async () => {
    if (!libraryId) return;
    try { setTrash(await listPhotoTrash(libraryId)); setError(""); }
    catch (caught) { setError(messageOf(caught)); }
  }, [libraryId]);

  useEffect(() => {
    setSelected(undefined);
    if (view === "timeline") void loadTimeline();
    else if (view === "search") void loadSearch();
    else void loadTrash();
  }, [view, loadTimeline, loadSearch, loadTrash]);
  const reload = () => view === "search" ? loadSearch() : loadTimeline();
  // Until the libraries have loaded, as while the photo service is
  // unavailable, refreshing loads them; selecting one then loads its view.
  const refresh = () => !libraryId ? loadLibraries() : view === "trash" ? loadTrash() : reload();
  const clearSearch = () => { setQuery(""); setSearched(""); setView("timeline"); };
  const submitSearch = (event: FormEvent) => {
    event.preventDefault();
    const text = query.trim();
    if (!text) { clearSearch(); return; }
    if (text === searched && view === "search") { void loadSearch(); return; }
    setLoading(true);
    setSearched(text);
    setView("search");
  };
  // Administrative viewing never includes the member's trash.
  useEffect(() => { if (readOnly) setView("timeline"); }, [readOnly]);

  // Thumbnails render in the background after an upload, and the grid shows
  // the original meanwhile; recheck the pending ones for about a minute so
  // the grid does not keep loading full-size originals.
  const pending = useMemo(() => assets.filter((asset) => asset.thumbnail === "pending").map((asset) => asset.id).join(","), [assets]);
  useEffect(() => {
    if (!pending) return;
    let attempts = 0;
    const timer = window.setInterval(() => {
      if (++attempts >= thumbnailChecks) window.clearInterval(timer);
      void Promise.all(pending.split(",").map((assetId) => getPhoto(assetId).catch(() => undefined))).then((checked) => {
        const settled = new Map(checked.filter((asset): asset is PhotoAsset => asset !== undefined && asset.thumbnail !== "pending").map((asset) => [asset.id, asset]));
        if (settled.size) setAssets((current) => current.map((asset) => settled.get(asset.id) ?? asset));
      });
    }, thumbnailCheckInterval);
    return () => window.clearInterval(timer);
  }, [pending]);

  const upload = async (files: File[]) => {
    if (!libraryId || readOnly || !files.length) return;
    setNotice("");
    const failures: string[] = [];
    setUploading({ done: 0, total: files.length });
    for (const [index, file] of files.entries()) {
      try { await uploadPhoto(libraryId, file); }
      catch (caught) { failures.push(`${file.name}：${messageOf(caught)}`); }
      setUploading({ done: index + 1, total: files.length });
    }
    setUploading(undefined);
    await loadTimeline();
    if (failures.length) setError(`${failures.length} 张未能导入。${failures.join("；")}`);
  };

  const act = async (action: () => Promise<unknown>, after: () => Promise<void>) => {
    setNotice("");
    try { await action(); await after(); }
    catch (caught) { setError(messageOf(caught)); }
  };

  const current = selected === undefined ? undefined : assets[selected];
  const currentLibrary = current ? libraryOf(current) : undefined;
  // Copying again would only add another independent copy, so say it worked.
  const copyTo = (target: PhotoLibrary, label: string) => {
    if (current) void act(() => copyPhoto(current.id, target.id), async () => { setError(""); setNotice(`已复制到${label}`); });
  };
  useEffect(() => setNotice(""), [selected]);
  const groups = useMemo(() => groupByDay(assets), [assets]);
  // Focus the viewer when it opens so Escape and the arrow keys work at once.
  const viewer = useRef<HTMLDivElement>(null);
  const viewerOpen = current !== undefined;
  useEffect(() => { if (viewerOpen) viewer.current?.focus(); }, [viewerOpen]);

  return (
    <div
      className="product-page photos-page"
      onDragOver={(event) => { if (!readOnly && view === "timeline") event.preventDefault(); }}
      onDrop={(event: DragEvent<HTMLDivElement>) => { event.preventDefault(); if (view === "timeline") void upload(Array.from(event.dataTransfer.files)); }}
    >
      <div className="page-heading">
        <div><p className="section-label">PHOTOS</p><h2>相册</h2><p>{readOnly ? "只读查看，访问已写入审计" : "照片保存在数据卷上的受管图库，AI 不可用时也能浏览"}</p></div>
        <div className="photos-actions">
          <form className="photos-search" role="search" onSubmit={submitSearch}>
            <input type="search" aria-label="搜索照片" placeholder="搜索，如：海边的猫" maxLength={200} value={query} onChange={(event) => setQuery(event.target.value)} />
            <button type="submit" aria-label="搜索"><Search size={14} /></button>
          </form>
          <select aria-label="图库" value={libraryId} onChange={(event) => { setLoading(true); setLibraryId(event.target.value); if (view === "search") clearSearch(); }}>
            {libraries.map((item) => <option key={item.id} value={item.id}>{libraryLabel(item, userId)}</option>)}
          </select>
          <div className="segmented" role="tablist" aria-label="相册视图">
            <button role="tab" aria-selected={view === "timeline"} className={view === "timeline" ? "active" : ""} onClick={() => setView("timeline")}>照片</button>
            {!readOnly && <button role="tab" aria-selected={view === "trash"} className={view === "trash" ? "active" : ""} onClick={() => setView("trash")}>回收站</button>}
          </div>
          {view === "timeline" && !readOnly && <label className="upload-button"><ImagePlus size={14} />上传照片<input type="file" aria-label="上传照片" accept="image/jpeg,image/png" multiple onChange={(event: ChangeEvent<HTMLInputElement>) => { const files = Array.from(event.target.files ?? []); event.target.value = ""; void upload(files); }} /></label>}
          <button aria-label="刷新相册" onClick={() => void refresh()}><RefreshCw size={14} /></button>
        </div>
      </div>
      {readOnly && <div className="status-banner viewing-banner" role="status"><ShieldCheck />只读查看 {library?.ownerName ?? "成员"} 的私有图库，{viewingUntil} 自动结束；本次访问已写入审计并通知所有者。<button onClick={() => void stopViewing()}>结束查看</button></div>}
      {uploading && <div className="status-banner" role="status"><span className="loader" />正在上传 {uploading.done}/{uploading.total}</div>}
      {error && <div className="status-banner error" role="alert"><CircleAlert />{error}</div>}
      {notice && !current && <div className="status-banner" role="status"><CircleCheck />{notice}</div>}

      {view === "search" ? (
        <>
          <div className="inline-form">
            <span className="photos-note">在我的图库{readOnly ? `、共享图库和 ${library?.ownerName ?? "成员"} 的私有图库` : "和共享图库"}中搜索“{searched}”，最相关的排在前面。</span>
            <button onClick={clearSearch}><X size={13} />清除搜索</button>
          </div>
          {!semantic && <div className="status-banner" role="status"><CircleAlert />智能搜索暂不可用，只按照片名称匹配。</div>}
          {loading ? <div className="empty-compact">正在搜索…</div> : !assets.length && !error && <div className="empty-compact">没有找到相关照片</div>}
          <div className="photo-grid">
            {assets.map((asset, index) => (
              <button key={asset.id} className="photo-tile" aria-label={`查看 ${asset.name}`} onClick={() => setSelected(index)}>
                <img src={previewURL(asset)} alt={asset.name} loading="lazy" onError={(event) => showOriginal(event.currentTarget, asset.id)} />
              </button>
            ))}
          </div>
          {next && <button className="photos-more" onClick={() => void loadSearch(next)}>加载更多</button>}
        </>
      ) : view === "timeline" ? (
        <>
          {loading ? <div className="empty-compact">正在载入照片…</div> : !assets.length && !error && <div className="empty-compact">还没有照片。{readOnly ? "" : "点击“上传照片”或把 JPEG、PNG 拖到这里。"}</div>}
          {groups.map((group) => (
            <section className="photo-day" key={group.label} aria-label={group.label}>
              <h3>{group.label}</h3>
              <div className="photo-grid">
                {group.items.map(({ asset, index }) => (
                  <button key={asset.id} className="photo-tile" aria-label={`查看 ${asset.name}`} onClick={() => setSelected(index)}>
                    <img src={previewURL(asset)} alt={asset.name} loading="lazy" onError={(event) => showOriginal(event.currentTarget, asset.id)} />
                    {asset.duplicate === "duplicate" && <span className="photo-badge">重复</span>}
                  </button>
                ))}
              </div>
            </section>
          ))}
          {next && <button className="photos-more" onClick={() => void loadTimeline(next)}>加载更多</button>}
        </>
      ) : (
        <>
          <div className="inline-form">
            <span className="photos-note">回收站中的照片保留 15 天后自动永久删除。</span>
            {!readOnly && <button className="danger-link" disabled={!trash.length} onClick={() => { if (window.confirm("永久删除回收站中你可以清除的全部照片？")) void act(() => emptyPhotoTrash(libraryId), loadTrash); }}>清空回收站</button>}
          </div>
          <div className="data-list">
            {trash.map((asset) => (
              <div className="data-row" key={asset.id}>
                <Trash2 /><strong>{asset.name}</strong>
                <small>{asset.trash ? `${formatDate(asset.trash.purgeAfter)} 永久删除` : ""}</small>
                <button onClick={() => void act(() => restorePhoto(asset.id), loadTrash)}><RotateCcw size={13} />恢复</button>
                <button className="danger-link" onClick={() => { if (window.confirm(`永久删除“${asset.name}”？此操作无法撤销。`)) void act(() => purgePhoto(asset.id), loadTrash); }}>永久删除</button>
              </div>
            ))}
          </div>
          {!trash.length && <div className="empty-compact">回收站为空</div>}
        </>
      )}

      {current && selected !== undefined && (
        <div ref={viewer} className="photo-viewer" role="dialog" aria-label={`查看 ${current.name}`} onKeyDown={(event) => {
          if (event.key === "Escape") setSelected(undefined);
          if (event.key === "ArrowLeft" && selected > 0) setSelected(selected - 1);
          if (event.key === "ArrowRight" && selected < assets.length - 1) setSelected(selected + 1);
        }} tabIndex={-1}>
          <div className="photo-viewer-stage">
            <button className="photo-nav" aria-label="上一张" disabled={selected === 0} onClick={() => setSelected(selected - 1)}><ChevronLeft /></button>
            <img src={originalURL(current.id)} alt={current.name} />
            <button className="photo-nav" aria-label="下一张" disabled={selected === assets.length - 1} onClick={() => setSelected(selected + 1)}><ChevronRight /></button>
          </div>
          <aside className="photo-viewer-info">
            <button className="photo-close" aria-label="关闭查看" onClick={() => setSelected(undefined)}><X /></button>
            <h3>{current.name}</h3>
            <dl>
              <dt>拍摄时间</dt><dd>{current.takenAt ? formatDate(current.takenAt) : "未记录"}</dd>
              <dt>导入时间</dt><dd>{formatDate(current.importedAt)}</dd>
              <dt>尺寸</dt><dd>{current.width && current.height ? `${current.width} × ${current.height}` : "未知"}</dd>
              <dt>大小</dt><dd>{formatSize(current.sizeBytes)}</dd>
              {current.duplicate && <><dt>重复</dt><dd>{current.duplicate === "first" ? "本图库中最早导入的一张" : "本图库中已有相同照片"}</dd></>}
            </dl>
            {current.alsoKeptBy?.length ? <p className="photo-hint">{current.alsoKeptBy.join("、")} 也保存了相同的照片。</p> : null}
            <div className="photo-viewer-actions">
              <a className="upload-button" href={originalURL(current.id, true)}><Download size={14} />下载原图</a>
              {canChange(current) && <button onClick={() => {
                const name = window.prompt("新的照片名称", current.name);
                if (name && name !== current.name) void act(() => renamePhoto(current.id, name), reload);
              }}><Pencil size={13} />重命名</button>}
              {currentLibrary?.kind === "private" && !currentLibrary.viewing && shared && <button onClick={() => copyTo(shared, "共享图库")}><Copy size={13} />复制到共享图库</button>}
              {currentLibrary?.kind === "shared" && mine && <button onClick={() => copyTo(mine, "我的图库")}><Copy size={13} />复制到我的图库</button>}
              {canChange(current) && <button className="danger-link" onClick={() => void act(() => trashPhoto(current.id), async () => { setSelected(undefined); await reload(); })}><Trash2 size={13} />移到回收站</button>}
            </div>
            {/* The viewer covers the panel's banners, so its outcome shows here. */}
            {notice && <p className="photo-notice" role="status">{notice}</p>}
            {error && <p className="photo-notice failed">{error}</p>}
          </aside>
        </div>
      )}
    </div>
  );
}

function libraryLabel(library: PhotoLibrary, userId: string) {
  if (library.kind === "shared") return "共享图库";
  if (library.ownerUserId === userId) return "我的图库";
  return `只读查看 · ${library.ownerName ?? "成员"}`;
}

// groupByDay keeps the timeline order and starts a new section whenever the
// capture day changes.
function groupByDay(assets: PhotoAsset[]) {
  const groups: Array<{ label: string; items: Array<{ asset: PhotoAsset; index: number }> }> = [];
  assets.forEach((asset, index) => {
    const label = new Date(asset.takenAt ?? asset.importedAt).toLocaleDateString("zh-CN", { year: "numeric", month: "long", day: "numeric" });
    const last = groups.at(-1);
    if (last?.label === label) last.items.push({ asset, index });
    else groups.push({ label, items: [{ asset, index }] });
  });
  return groups;
}

// A thumbnail reported ready can still be missing until reconciliation
// renders it again; the original displays directly in its place.
function showOriginal(image: HTMLImageElement, assetId: string) {
  const original = originalURL(assetId);
  if (image.getAttribute("src") !== original) image.src = original;
}

const thumbnailCheckInterval = 3000;
const thumbnailChecks = 20;

function formatDate(value: string) { return new Date(value).toLocaleString("zh-CN"); }

function formatSize(bytes: number) {
  if (bytes >= 1024 ** 2) return `${(bytes / 1024 ** 2).toFixed(1)} MiB`;
  return `${Math.max(1, Math.round(bytes / 1024))} KiB`;
}

const photoErrors: Record<string, string> = {
  unsupported_media_type: "只支持 JPEG 和 PNG 照片",
  too_large: "照片超过导入大小上限",
  insufficient_storage: "数据卷剩余空间不足",
  forbidden: "你不能修改这张照片",
  conflict: "名称已存在，或照片状态已经变化",
  validation_failed: "名称无效",
  photos_unavailable: "相册暂时不可用：数据卷未就绪，或此设备尚未启用相册。恢复后点击刷新。",
};

function messageOf(error: unknown) {
  if (error instanceof APIError) return photoErrors[error.code] ?? error.message;
  return error instanceof Error ? error.message : "请求失败";
}
