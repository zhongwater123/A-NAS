import {
  ArrowLeft, CircleAlert, Copy, Download, FolderMinus, FolderPlus, ImagePlus, Images, PanelLeft, Pencil, RefreshCw, RotateCcw, ScanEye, Search, ShieldCheck,
  Sparkles, Trash2, X, ZoomIn, ZoomOut,
} from "lucide-react";
import { DragEvent, FormEvent, KeyboardEvent, ReactNode, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";

import { endViewing } from "../api";
import { AlbumPicker } from "./AlbumPicker";
import { AssetGrid } from "./AssetGrid";
import { AlbumsView, Empty, ThingsView } from "./Collections";
import { useDialogs, useToasts } from "./feedback";
import type { GridProps } from "./grid";
import { clampRowHeight, defaultRowHeight, maxRowHeight, minRowHeight } from "./layout";
import {
  type Caller, canChange, canEditAlbum, daysLeft, isUnavailable, libraryName, libraryOf, messageOf, ownLibrary, type Place, placeLibrary, placeViewing,
  runEach, sharedLibrary,
} from "./model";
import { PhotoPicker } from "./PhotoPicker";
import {
  addToAlbum, copyPhoto, createAlbum, deleteAlbum, emptyPhotoTrash, getAIStatus, listAlbumPhotos, listAlbums, listPhotoLabels, listPhotoLibraries,
  listPhotoTrash, originalURL, purgePhoto, removeFromAlbum, renameAlbum, renamePhoto, restorePhoto, searchPhotos, trashPhoto,
  type PhotoAIStatus, type PhotoAlbum, type PhotoAsset, type PhotoLabelCount, type PhotoLibrary,
} from "./photosApi";
import type { ScrollerHandle } from "./PhotoScroller";
import { useSelection } from "./selection";
import { Sidebar } from "./Sidebar";
import { Timeline } from "./Timeline";
import { DropOverlay, type UploadTarget, UploadTray, useUploads } from "./uploads";
import { Viewer, type ViewerActions } from "./Viewer";
import "./photos.css";

interface Props { userId: string; isAdmin: boolean }

const aiPollInterval = 15_000;
const narrowWidth = 720;

function useStored<T>(key: string, initial: T): [T, (value: T | ((current: T) => T)) => void] {
  const [value, setValue] = useState<T>(() => {
    try { const stored = window.localStorage.getItem(key); return stored === null ? initial : JSON.parse(stored) as T; } catch { return initial; }
  });
  const set = useCallback((next: T | ((current: T) => T)) => setValue((current) => {
    const resolved = typeof next === "function" ? (next as (current: T) => T)(current) : next;
    try { window.localStorage.setItem(key, JSON.stringify(resolved)); } catch { /* private windows keep it for the session only */ }
    return resolved;
  }), [key]);
  return [value, set];
}

const sleep = (ms: number) => new Promise((resolve) => window.setTimeout(resolve, ms));

const placeKey = (place: Place) => {
  switch (place.kind) {
    case "album": return `album:${place.album.id}`;
    case "label": return `label:${place.viewing}:${place.label.id}`;
    case "search": return `search:${place.viewing}:${place.query}`;
    case "things": return `things:${place.viewing}`;
    default: return `${place.kind}:${place.libraryId}`;
  }
};

// PhotosPanel is the photos window: libraries and albums on the side, photos
// filling the rest, a viewer over everything. The photo service decides
// every permission; the panel only leaves out what it would refuse.
export function PhotosPanel({ userId, isAdmin }: Props) {
  const [libraries, setLibraries] = useState<PhotoLibrary[]>();
  const [failure, setFailure] = useState("");
  const [place, setPlaceState] = useState<Place>();
  const caller: Caller = useMemo(() => ({ userId, isAdmin, libraries: libraries ?? [] }), [userId, isAdmin, libraries]);
  const [rowHeight, setRowHeight] = useStored("a-nas.photos.rowHeight", defaultRowHeight);
  const [info, setInfo] = useStored("a-nas.photos.info", true);
  const [docked, setDocked] = useStored("a-nas.photos.sidebar", true);
  const [drawer, setDrawer] = useState(false);
  const [viewAssets, setViewAssets] = useState<PhotoAsset[]>([]);
  const selection = useSelection(viewAssets);
  const [count, setCount] = useState<number>();
  const [viewer, setViewer] = useState<{ id: string; origin?: DOMRect }>();
  const [refreshKey, setRefreshKey] = useState(0);
  const [albumsKey, setAlbumsKey] = useState(0);
  const [hidden, setHidden] = useState<ReadonlySet<string>>(new Set());
  const [ai, setAI] = useState<PhotoAIStatus>();
  const [labels, setLabels] = useState<{ viewing: string; items: PhotoLabelCount[] }>();
  const [semantic, setSemantic] = useState(true);
  const [query, setQuery] = useState("");
  const [picker, setPicker] = useState<PhotoAsset[]>();
  const [photoPicker, setPhotoPicker] = useState<PhotoAlbum>();
  const [busy, setBusy] = useState<{ label: string; done: number; total: number }>();
  const [dragging, setDragging] = useState(false);
  const [width, setWidth] = useState(1000);
  const [topInset, setTopInset] = useState(64);
  const dragDepth = useRef(0);
  const renamed = useRef(false);
  const root = useRef<HTMLDivElement>(null);
  const toolbar = useRef<HTMLElement>(null);
  const searchInput = useRef<HTMLInputElement>(null);
  const scroller = useRef<ScrollerHandle>(null);
  const dialogs = useDialogs();
  const { toast, element: toastElement } = useToasts();
  const reportError = useCallback((message: string) => toast({ text: message, tone: "error" }), [toast]);

  const mine = ownLibrary(caller);
  const shared = sharedLibrary(caller);
  const narrow = width < narrowWidth;
  const sidebarOpen = narrow ? drawer : docked;

  const loadLibraries = useCallback(async () => {
    try {
      const items = await listPhotoLibraries();
      setLibraries(items);
      setFailure("");
      setPlaceState((current) => {
        const library = current && placeLibrary(current);
        if (current && (!library || items.some((item) => item.id === library))) return current;
        const home = items.find((item) => item.kind === "private" && item.ownerUserId === userId && !item.viewing) ?? items[0];
        return home ? { kind: "timeline", libraryId: home.id } : undefined;
      });
    } catch (caught) {
      setFailure(isUnavailable(caught) ? "相册暂时不可用：数据卷未就绪，或此设备尚未启用相册。恢复后点击重试。" : messageOf(caught));
    }
  }, [userId]);
  useEffect(() => { void loadLibraries(); }, [loadLibraries]);

  // Local AI progress; polling it does not count as using the library.
  useEffect(() => {
    let live = true;
    const poll = () => getAIStatus().then((value) => { if (live) setAI(value); }, () => { if (live) setAI(undefined); });
    void poll();
    const timer = window.setInterval(poll, aiPollInterval);
    return () => { live = false; window.clearInterval(timer); };
  }, []);

  // The window's width decides whether the sidebar docks; the toolbar's
  // height is where the photos start.
  useLayoutEffect(() => {
    const element = root.current;
    if (!element) return;
    const measure = () => {
      setWidth(element.clientWidth || 1000);
      setTopInset((toolbar.current?.offsetHeight || 56) + 8);
    };
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    if (toolbar.current) observer.observe(toolbar.current);
    return () => observer.disconnect();
  }, [libraries !== undefined]);

  // Each view reports its own photos when it mounts, so going to the place
  // already shown changes nothing.
  const placeRef = useRef(place);
  placeRef.current = place;
  const setPlace = useCallback((next: Place) => {
    setDrawer(false);
    if (placeKey(next) === (placeRef.current && placeKey(placeRef.current))) return;
    setPlaceState(next);
    selection.clear();
    setHidden(new Set());
    setCount(undefined);
    setSemantic(true);
    if (next.kind !== "search") setQuery("");
  }, [selection.clear]);

  const libraryId = place ? placeLibrary(place) : "";
  // Searches and the things page keep the sidebar on the library they came from.
  const lastLibrary = useRef("");
  if (libraryId) lastLibrary.current = libraryId;
  const sideLibrary = libraryOf(caller, libraryId) ?? (place && "viewing" in place && place.viewing ? libraryOf(caller, place.viewing) : libraryOf(caller, lastLibrary.current)) ?? mine;
  const viewingLibrary = sideLibrary?.viewing ? sideLibrary : undefined;
  const readOnlyPlace = Boolean(viewingLibrary);

  // Search labels match the query by name, as shortcuts to the label view.
  useEffect(() => {
    if (place?.kind !== "search" || labels?.viewing === place.viewing) return;
    let live = true;
    listPhotoLabels(place.viewing).then((value) => { if (live) setLabels({ viewing: place.viewing, items: value.items }); }, () => undefined);
    return () => { live = false; };
  }, [place]);

  // Uploads go to the album or library shown, else to the caller's library.
  const uploadTarget = useMemo((): UploadTarget | undefined => {
    if (!place) return undefined;
    if (place.kind === "album") {
      const library = libraryOf(caller, place.libraryId);
      if (!library || library.viewing) return undefined;
      if (canEditAlbum(caller, place.album)) return { libraryId: place.libraryId, albumId: place.album.id, label: `相册“${place.album.name}”` };
      return { libraryId: place.libraryId, label: libraryName(caller, library) };
    }
    const library = libraryOf(caller, placeLibrary(place)) ?? (viewingLibrary ? undefined : mine);
    if (!library || library.viewing) return undefined;
    return { libraryId: library.id, label: libraryName(caller, library) };
  }, [place, caller, mine, viewingLibrary]);

  const uploads = useUploads(useCallback((targets: UploadTarget[]) => {
    setRefreshKey((value) => value + 1);
    setAlbumsKey((value) => value + 1);
    toast({ text: `照片已保存到${targets.map((target) => target.label).join("、")}` });
  }, [toast]));
  const pending = useMemo(() => place?.kind === "timeline" ? uploads.items.filter((item) => item.target.libraryId === place.libraryId && !item.target.albumId && !item.rejected) : [], [uploads.items, place]);
  // Previews of finished uploads stay until the timeline shows the photos.
  useEffect(() => { if (place?.kind !== "timeline" && uploads.items.some((item) => item.state === "done")) uploads.prune(); }, [uploads.items, place]);
  const pickFiles = () => {
    const input = document.createElement("input");
    input.type = "file";
    input.accept = "image/jpeg,image/png";
    input.multiple = true;
    input.onchange = () => { if (uploadTarget && input.files?.length) uploads.add(Array.from(input.files), uploadTarget); };
    input.click();
  };

  // ---- Actions on photos ----
  const byId = useMemo(() => new Map(viewAssets.map((asset) => [asset.id, asset])), [viewAssets]);
  const chosen = useMemo(() => [...selection.selected].map((id) => byId.get(id)).filter((asset): asset is PhotoAsset => Boolean(asset)), [selection.selected, byId]);
  const hide = (ids: string[]) => setHidden((current) => new Set([...current, ...ids]));
  const unhide = (ids: string[]) => setHidden((current) => new Set([...current].filter((id) => !ids.includes(id))));

  const batch = async <R,>(label: string, assets: PhotoAsset[], action: (asset: PhotoAsset) => Promise<R>) => {
    setBusy({ label, done: 0, total: assets.length });
    const result = await runEach(assets, action, (done) => setBusy({ label, done, total: assets.length }));
    setBusy(undefined);
    return { ...result, details: result.failed.map((failure) => `${failure.item.name}：${messageOf(failure.error)}`) };
  };
  const report = (text: string, failed: string[], extra?: { action?: { label: string; run: () => void } }) => {
    if (failed.length) toast({ text: `${text}；${failed.length} 张未成功`, tone: "error", details: failed, ...extra });
    else toast({ text, ...extra });
  };

  const restore = async (assets: PhotoAsset[], fromTrash = false) => {
    const result = await batch("正在恢复", assets, (asset) => restorePhoto(asset.id));
    const ids = result.succeeded.map((entry) => entry.item.id);
    if (fromTrash) hide(ids); else unhide(ids);
    report(`已恢复 ${ids.length} 张照片`, result.details);
  };
  const trash = async (assets: PhotoAsset[]) => {
    const result = await batch("正在移到回收站", assets, (asset) => trashPhoto(asset.id));
    const moved = result.succeeded.map((entry) => entry.item);
    hide(moved.map((asset) => asset.id));
    selection.clear();
    setAlbumsKey((value) => value + 1);
    report(`已移到回收站：${moved.length} 张`, result.details, moved.length ? { action: { label: "撤销", run: () => void restore(moved) } } : undefined);
  };
  const addTo = async (album: PhotoAlbum, assets: PhotoAsset[]) => {
    setPicker(undefined);
    const result = await batch(`正在加入“${album.name}”`, assets, (asset) => addToAlbum(album.id, asset.id));
    const copied = result.succeeded.filter((entry) => entry.item.libraryId !== album.libraryId).length;
    selection.clear();
    setAlbumsKey((value) => value + 1);
    if (place?.kind === "album" && place.album.id === album.id) setRefreshKey((value) => value + 1);
    report(`已加入相册“${album.name}”${copied ? `，其中 ${copied} 张已复制到${libraryName(caller, libraryOf(caller, album.libraryId))}` : ""}`, result.details,
      place?.kind === "album" && place.album.id === album.id ? undefined : { action: { label: "查看", run: () => setPlace({ kind: "album", libraryId: album.libraryId, album }) } });
  };
  const createAndAdd = async (library: PhotoLibrary, name: string, assets: PhotoAsset[]) => {
    try { await addTo(await createAlbum(library.id, name), assets); }
    catch (caught) { setPicker(undefined); reportError(messageOf(caught)); }
  };
  const copyTo = async (library: PhotoLibrary, assets: PhotoAsset[]) => {
    const result = await batch(`正在复制到${libraryName(caller, library)}`, assets, (asset) => copyPhoto(asset.id, library.id));
    selection.clear();
    report(`已复制 ${result.succeeded.length} 张到${libraryName(caller, library)}`, result.details, { action: { label: "查看", run: () => setPlace({ kind: "timeline", libraryId: library.id }) } });
  };
  const removeFromCurrentAlbum = async (assets: PhotoAsset[]) => {
    if (place?.kind !== "album") return;
    const album = place.album;
    const result = await batch("正在移出相册", assets, (asset) => removeFromAlbum(album.id, asset.id));
    const removed = result.succeeded.map((entry) => entry.item);
    hide(removed.map((asset) => asset.id));
    selection.clear();
    setAlbumsKey((value) => value + 1);
    report(`已从“${album.name}”移出 ${removed.length} 张，照片仍在图库中`, result.details, {
      action: { label: "撤销", run: () => void runEach(removed, (asset) => addToAlbum(album.id, asset.id)).then(() => { unhide(removed.map((asset) => asset.id)); setAlbumsKey((value) => value + 1); }) },
    });
  };
  const purge = async (assets: PhotoAsset[]) => {
    if (!await dialogs.confirm({ title: `永久删除 ${assets.length} 张照片？`, message: "永久删除后无法恢复，照片会从所有相册中移除。", confirm: "永久删除", danger: true })) return;
    const result = await batch("正在永久删除", assets, (asset) => purgePhoto(asset.id));
    hide(result.succeeded.map((entry) => entry.item.id));
    selection.clear();
    report(`已永久删除 ${result.succeeded.length} 张`, result.details);
  };
  const download = async (assets: PhotoAsset[]) => {
    if (assets.length > 10 && !await dialogs.confirm({ title: `下载 ${assets.length} 张原图？`, message: "浏览器会逐张下载，并可能询问是否允许下载多个文件。", confirm: "下载" })) return;
    for (const [position, asset] of assets.entries()) {
      const link = document.createElement("a");
      link.href = originalURL(asset.id, true);
      link.download = asset.name;
      document.body.appendChild(link);
      link.click();
      link.remove();
      if (position < assets.length - 1) await sleep(350);
    }
  };
  const renameOne = async (asset: PhotoAsset) => {
    const name = await dialogs.prompt({ title: "重命名照片", label: "名称", value: asset.name, confirm: "重命名", maxLength: 255 });
    if (!name || name === asset.name) return undefined;
    try { const value = await renamePhoto(asset.id, name); renamed.current = true; return value; }
    catch (caught) { reportError(messageOf(caught)); return undefined; }
  };
  const newAlbum = async (library = sideLibrary) => {
    if (!library || library.viewing) return;
    const name = await dialogs.prompt({ title: `在${libraryName(caller, library)}新建相册`, label: "相册名称", confirm: "创建", maxLength: 100 });
    if (!name) return;
    try {
      const album = await createAlbum(library.id, name);
      setAlbumsKey((value) => value + 1);
      setPlace({ kind: "album", libraryId: library.id, album });
    } catch (caught) { reportError(messageOf(caught)); }
  };
  const renameCurrentAlbum = async () => {
    if (place?.kind !== "album") return;
    const name = await dialogs.prompt({ title: "重命名相册", label: "相册名称", value: place.album.name, confirm: "重命名", maxLength: 100 });
    if (!name || name === place.album.name) return;
    try {
      const album = await renameAlbum(place.album.id, name);
      setPlaceState({ ...place, album });
      setAlbumsKey((value) => value + 1);
    } catch (caught) { reportError(messageOf(caught)); }
  };
  const deleteCurrentAlbum = async () => {
    if (place?.kind !== "album") return;
    if (!await dialogs.confirm({ title: `删除相册“${place.album.name}”？`, message: "只删除这个分组，其中的照片会留在图库和其他相册里。", confirm: "删除相册", danger: true })) return;
    try {
      await deleteAlbum(place.album.id);
      setAlbumsKey((value) => value + 1);
      setPlace({ kind: "albums", libraryId: place.libraryId });
      toast({ text: `已删除相册“${place.album.name}”` });
    } catch (caught) { reportError(messageOf(caught)); }
  };
  const emptyTrash = async () => {
    if (place?.kind !== "trash") return;
    if (!await dialogs.confirm({ title: "清空回收站？", message: "回收站中你可以清除的照片都会被永久删除，无法恢复。", confirm: "清空回收站", danger: true })) return;
    try {
      const purged = await emptyPhotoTrash(place.libraryId);
      setRefreshKey((value) => value + 1);
      toast({ text: `已永久删除 ${purged} 张照片` });
    } catch (caught) { reportError(messageOf(caught)); }
  };
  const openAlbumById = async (album: Pick<PhotoAlbum, "id" | "name" | "libraryId">) => {
    const found = (await listAlbums(album.libraryId).catch(() => [])).find((item) => item.id === album.id);
    setViewer(undefined);
    setPlace({ kind: "album", libraryId: album.libraryId, album: found ?? { ...album, createdBy: "", createdAt: "", photos: 0 } });
  };
  const stopViewing = async () => {
    if (!viewingLibrary?.viewing) return;
    try { await endViewing(viewingLibrary.viewing.grantId); await loadLibraries(); if (mine) setPlace({ kind: "timeline", libraryId: mine.id }); }
    catch (caught) { reportError(messageOf(caught)); }
  };

  // Photos dragged onto the sidebar.
  const assetsFor = (ids: string[]) => ids.map((id) => byId.get(id)).filter((asset): asset is PhotoAsset => Boolean(asset));
  const dropOnAlbum = (album: PhotoAlbum, ids: string[]) => { const assets = assetsFor(ids); if (assets.length) void addTo(album, assets); };
  const dropOnLibrary = (library: PhotoLibrary, ids: string[]) => {
    const assets = assetsFor(ids).filter((asset) => asset.libraryId !== library.id);
    if (assets.length) void copyTo(library, assets);
  };

  // ---- Grid wiring ----
  const openViewer = useCallback((asset: PhotoAsset, image: HTMLImageElement | null) => setViewer({ id: asset.id, origin: image?.getBoundingClientRect() }), []);
  const zoomBy = useCallback((factor: number) => setRowHeight((value) => clampRowHeight(value * factor)), [setRowHeight]);
  const grid: GridProps = {
    rowHeight, onZoom: zoomBy, selected: selection.selected, onOpen: openViewer, onToggle: selection.toggle, onBeginSwipe: selection.beginSwipe,
    onMarquee: selection.marquee, onGestureEnd: selection.endGesture, onBackgroundClick: selection.clear, onSelectGroup: selection.setGroup,
    onDragIds: readOnlyPlace ? undefined : (asset) => selection.selected.has(asset.id) ? [...selection.selected] : [asset.id],
    onAssets: setViewAssets, hidden, refreshKey, scroller, topInset,
  };

  const viewerIndex = viewer ? viewAssets.findIndex((asset) => asset.id === viewer.id) : -1;
  const viewerActions: ViewerActions = {
    trash: (asset) => {
      const position = viewAssets.findIndex((item) => item.id === asset.id);
      const next = viewAssets[position + 1] ?? viewAssets[position - 1];
      setViewer(next ? { id: next.id } : undefined);
      void trash([asset]);
    },
    addToAlbum: (assets) => setPicker(assets),
    download: (assets) => void download(assets),
    rename: renameOne,
    copy: (asset, library) => void copyTo(library, [asset]),
    openAlbum: (album) => void openAlbumById(album),
    openLabel: (label) => { setViewer(undefined); setPlace({ kind: "label", viewing: place ? placeViewing(caller, place) : "", label }); },
    searchTag: (tag) => { setViewer(undefined); startSearch(tag); },
    error: reportError,
  };
  const closeViewer = useCallback(() => {
    setViewer(undefined);
    if (renamed.current) { renamed.current = false; setRefreshKey((value) => value + 1); }
    root.current?.focus();
  }, []);

  const startSearch = (text: string) => {
    if (!place) return;
    const back = place.kind === "search" ? place.back : place;
    setPlace({ kind: "search", viewing: placeViewing(caller, place), query: text, back });
    setQuery(text);
  };
  const submitSearch = (event: FormEvent) => {
    event.preventDefault();
    const text = query.trim();
    if (text) startSearch(text);
    else if (place?.kind === "search") setPlace(place.back);
  };

  // ---- Keyboard and dropped files ----
  const keyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (viewer || dialogs.open || picker || photoPicker) return;
    const typing = (event.target as HTMLElement).closest("input, textarea");
    if (event.key === "Escape" && selection.selected.size && !typing) { event.preventDefault(); selection.clear(); }
    else if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "a" && !typing && place?.kind !== "albums" && place?.kind !== "things") {
      event.preventDefault();
      selection.replace(viewAssets.map((asset) => asset.id));
    } else if (event.key === "/" && !typing) { event.preventDefault(); searchInput.current?.focus(); }
  };
  const carriesFiles = (event: DragEvent) => Array.from(event.dataTransfer.types).includes("Files");
  const dropHandlers = {
    onDragEnter: (event: DragEvent) => { if (carriesFiles(event)) { dragDepth.current++; setDragging(true); } },
    onDragLeave: (event: DragEvent) => { if (carriesFiles(event) && --dragDepth.current <= 0) { dragDepth.current = 0; setDragging(false); } },
    onDragOver: (event: DragEvent) => { if (carriesFiles(event)) { event.preventDefault(); event.dataTransfer.dropEffect = uploadTarget ? "copy" : "none"; } },
    onDrop: (event: DragEvent) => {
      if (!carriesFiles(event)) return;
      event.preventDefault();
      dragDepth.current = 0;
      setDragging(false);
      if (uploadTarget) uploads.add(Array.from(event.dataTransfer.files), uploadTarget);
    },
  };

  if (!libraries || !place) {
    return (
      <div className="ph-app ph-app-state">
        {failure ? <Empty icon={<CircleAlert />} title="相册暂时不可用" text={failure} action={<button type="button" className="ph-button" onClick={() => void loadLibraries()}><RefreshCw />重试</button>} />
          : <div className="ph-loading"><span className="ph-spinner" /></div>}
      </div>
    );
  }

  // ---- Toolbar ----
  const title = (() => {
    switch (place.kind) {
      case "timeline": return { text: libraryName(caller, sideLibrary), detail: count === undefined ? "" : `${count} 张照片` };
      case "albums": return { text: "相册", detail: libraryName(caller, sideLibrary) };
      case "album": return { text: place.album.name, detail: `${count ?? place.album.photos} 张 · ${libraryName(caller, sideLibrary)}`, back: { kind: "albums", libraryId: place.libraryId } as Place };
      case "trash": return { text: "回收站", detail: "照片保留 15 天后自动永久删除" };
      case "things": return { text: "识别的事物", detail: viewingLibrary ? `我的图库、共享图库和${libraryName(caller, viewingLibrary)}` : "我的图库和共享图库" };
      case "label": return { text: place.label.name, detail: count === undefined ? "本地 AI 识别" : `本地 AI 识别出 ${count} 张，可能有误`, back: { kind: "things", viewing: place.viewing } as Place };
      case "search": return { text: `“${place.query}”`, detail: count === undefined ? "搜索结果" : `找到 ${count} 张`, back: place.back };
    }
  })();
  const photoGrid = place.kind === "timeline" || place.kind === "album" || place.kind === "trash" || place.kind === "label" || place.kind === "search";
  const suggestions = place.kind === "search" && labels?.viewing === place.viewing
    ? labels.items.filter((label) => label.name.includes(place.query) || place.query.includes(label.name)).slice(0, 6) : [];
  const subbar: ReactNode = place.kind === "search" && (suggestions.length || !semantic) ? (
    <div className="ph-subbar">
      {!semantic && <span className="ph-subbar-note"><CircleAlert />本地 AI 暂不可用，只按名称和标签匹配</span>}
      {suggestions.length > 0 && <span className="ph-subbar-label"><Sparkles />识别为</span>}
      {suggestions.map((label) => <button type="button" key={label.id} className="ph-chip-button" onClick={() => setPlace({ kind: "label", viewing: place.viewing, label })}>{label.name}<small>{label.photos}</small></button>)}
    </div>
  ) : null;

  // ---- Selection bar ----
  const selectedAll = chosen.length > 0 && chosen.length === selection.selected.size;
  const allChangeable = selectedAll && chosen.every((asset) => canChange(caller, asset));
  const allWritable = selectedAll && chosen.every((asset) => !libraryOf(caller, asset.libraryId)?.viewing);
  const copyTarget = !allWritable ? undefined
    : chosen.every((asset) => asset.libraryId === mine?.id) ? shared
      : chosen.every((asset) => asset.libraryId === shared?.id) ? mine : undefined;
  // While photos are selected the toolbar turns into their actions.
  const selectionBar = selection.selected.size > 0 && (
    <div className="ph-toolbar-row ph-selection-row" role="toolbar" aria-label="所选照片的操作">
      <button type="button" className="ph-icon-button" aria-label="取消选择" title="取消选择 (Esc)" onClick={selection.clear}><X /></button>
      <strong>已选 {selection.selected.size} 张</strong>
      <span className="ph-selection-hint">按住 Shift 可连选，拖动勾选圈可批量选择</span>
      {busy ? <span className="ph-selection-busy"><span className="ph-spinner" />{busy.label} {busy.done}/{busy.total}</span> : place.kind === "trash" ? (
        <>
          <button type="button" onClick={() => void restore(chosen, true)}><RotateCcw /><span>恢复</span></button>
          <button type="button" className="danger" onClick={() => void purge(chosen)}><Trash2 /><span>永久删除</span></button>
        </>
      ) : (
        <>
          {allWritable && <button type="button" onClick={() => setPicker(chosen)}><FolderPlus /><span>加入相册</span></button>}
          {copyTarget && <button type="button" onClick={() => void copyTo(copyTarget, chosen)}><Copy /><span>复制到{copyTarget.kind === "shared" ? "共享" : "我的图库"}</span></button>}
          <button type="button" onClick={() => void download(chosen)}><Download /><span>下载</span></button>
          {place.kind === "album" && canEditAlbum(caller, place.album) && <button type="button" onClick={() => void removeFromCurrentAlbum(chosen)}><FolderMinus /><span>移出相册</span></button>}
          {allChangeable && <button type="button" className="danger" onClick={() => void trash(chosen)}><Trash2 /><span>删除</span></button>}
        </>
      )}
    </div>
  );

  // ---- Main area ----
  const emptyTimeline = readOnlyPlace
    ? <Empty icon={<Images />} title="还没有照片" text="这个图库里还没有照片。" />
    : <Empty icon={<ImagePlus />} title="把照片拖到这里" text="支持 JPEG 和 PNG。照片保存在数据卷上的受管图库，本地 AI 会在设备空闲时整理它们。" action={uploadTarget && <button type="button" className="ph-button primary" onClick={pickFiles}><ImagePlus />选择照片</button>} />;
  const main = (() => {
    switch (place.kind) {
      case "timeline":
        return <Timeline key={place.libraryId} {...grid} libraryId={place.libraryId} pending={pending} onRetryUpload={uploads.retry} onReloaded={uploads.prune} onCount={setCount} onError={reportError} empty={emptyTimeline} />;
      case "album":
        return <AssetGrid key={place.album.id} {...grid} listKey={place.album.id} load={(cursor) => listAlbumPhotos(place.album.id, cursor)} onCount={setCount} onError={reportError}
          empty={<Empty icon={<Images />} title="相册是空的" text="把照片从时间线拖到侧栏的这个相册，或者点“添加照片”。" action={canEditAlbum(caller, place.album) && <button type="button" className="ph-button primary" onClick={() => setPhotoPicker(place.album)}><ImagePlus />添加照片</button>} />} />;
      case "trash":
        return <AssetGrid key={`trash:${place.libraryId}`} {...grid} onOpen={(asset) => selection.toggle(asset, false)} onDragIds={undefined} listKey={`trash:${place.libraryId}`}
          load={async () => ({ items: await listPhotoTrash(place.libraryId) })} onCount={setCount} onError={reportError}
          badge={(asset) => asset.trash && <span className="ph-tile-days">{daysLeft(asset.trash.purgeAfter)} 天后删除</span>}
          empty={<Empty icon={<Trash2 />} title="回收站是空的" text="删除的照片会在这里保留 15 天，期间可以恢复。" />} />;
      case "label":
        return <AssetGrid key={`label:${place.label.id}`} {...grid} listKey={`label:${place.viewing}:${place.label.id}`} load={(cursor) => searchPhotos({ label: place.label.id, name: place.label.name }, place.viewing, cursor)}
          onCount={setCount} onError={reportError} empty={<Empty icon={<ScanEye />} title={`没有${place.label.name}`} text="AI 标签暂不可用，或这些照片已被隐藏此标签。" />} />;
      case "search":
        return <AssetGrid key={`search:${place.query}`} {...grid} listKey={`search:${place.viewing}:${place.query}`} load={(cursor) => searchPhotos({ query: place.query }, place.viewing, cursor)}
          onPage={(page, first) => { if (first && "semantic" in page) setSemantic(Boolean(page.semantic)); }} onCount={setCount} onError={reportError}
          end="以上是相关度较高的照片" empty={<Empty icon={<Search />} title="没有找到相关照片" text="换个说法试试，比如“海边的日落”“桌上的蛋糕”。" />} />;
      case "albums":
        return <AlbumsView libraryId={place.libraryId} readOnly={readOnlyPlace} refreshKey={albumsKey} topInset={topInset} onOpen={(album) => setPlace({ kind: "album", libraryId: place.libraryId, album })} onCreate={() => void newAlbum()} onError={reportError} />;
      case "things":
        return <ThingsView viewing={place.viewing} refreshKey={refreshKey} topInset={topInset} ai={ai} onOpen={(label) => setPlace({ kind: "label", viewing: place.viewing, label })} onError={reportError} />;
    }
  })();

  return (
    <div
      ref={root}
      className={`ph-app${sidebarOpen ? " nav-open" : ""}${narrow ? " narrow" : ""}${selection.selected.size ? " selecting" : ""}`}
      tabIndex={-1}
      onKeyDown={keyDown}
      {...dropHandlers}
    >
      {sidebarOpen && (
        <Sidebar
          caller={caller}
          library={sideLibrary}
          place={place}
          albumsKey={albumsKey}
          count={place.kind === "timeline" ? count : undefined}
          ai={ai}
          onPlace={setPlace}
          onLibrary={(library) => setPlace(place.kind === "albums" ? { kind: "albums", libraryId: library.id }
            : place.kind === "trash" && !library.viewing ? { kind: "trash", libraryId: library.id } : { kind: "timeline", libraryId: library.id })}
          onCreateAlbum={() => void newAlbum()}
          onDropOnAlbum={dropOnAlbum}
          onDropOnLibrary={dropOnLibrary}
        />
      )}
      {narrow && drawer && <div className="ph-scrim" onClick={() => setDrawer(false)} />}

      <main className="ph-main" aria-label={title.text}>
        <header ref={toolbar} className={`ph-toolbar${selection.selected.size ? " selecting" : ""}`}>
          {selectionBar || <div className="ph-toolbar-row">
            <button type="button" className="ph-icon-button" aria-label={sidebarOpen ? "收起侧栏" : "展开侧栏"} aria-expanded={sidebarOpen} onClick={() => narrow ? setDrawer(!drawer) : setDocked(!docked)}><PanelLeft /></button>
            {title.back && <button type="button" className="ph-icon-button" aria-label="返回" onClick={() => setPlace(title.back!)}><ArrowLeft /></button>}
            <div className="ph-title">
              <h2>{title.text}</h2>
              {title.detail && <small>{title.detail}</small>}
            </div>
            {place.kind === "album" && canEditAlbum(caller, place.album) && (
              <div className="ph-title-actions">
                <button type="button" className="ph-icon-button" aria-label="重命名相册" title="重命名相册" onClick={() => void renameCurrentAlbum()}><Pencil /></button>
                <button type="button" className="ph-icon-button" aria-label="删除相册" title="删除相册" onClick={() => void deleteCurrentAlbum()}><Trash2 /></button>
              </div>
            )}
            <form className="ph-search" role="search" onSubmit={submitSearch}>
              <Search />
              <input ref={searchInput} type="search" aria-label="搜索照片" placeholder="搜索照片，如：海边的猫" maxLength={200} value={query}
                onChange={(event) => setQuery(event.target.value)}
                onKeyDown={(event) => { if (event.key === "Escape" && place.kind === "search") { event.preventDefault(); setPlace(place.back); } }} />
            </form>
            {photoGrid && (
              <div className="ph-zoom" role="group" aria-label="照片大小">
                <button type="button" aria-label="缩小照片" title="缩小 (Ctrl+滚轮)" disabled={rowHeight <= minRowHeight} onClick={() => zoomBy(1 / 1.2)}><ZoomOut /></button>
                <input type="range" aria-label="照片大小" min={minRowHeight} max={maxRowHeight} value={rowHeight} onChange={(event) => setRowHeight(Number(event.target.value))} />
                <button type="button" aria-label="放大照片" title="放大 (Ctrl+滚轮)" disabled={rowHeight >= maxRowHeight} onClick={() => zoomBy(1.2)}><ZoomIn /></button>
              </div>
            )}
            {place.kind === "album" && canEditAlbum(caller, place.album) && <button type="button" className="ph-button" onClick={() => setPhotoPicker(place.album)}><FolderPlus />添加照片</button>}
            {place.kind === "trash" && <button type="button" className="ph-button danger" onClick={() => void emptyTrash()}><Trash2 />清空</button>}
            {uploadTarget && place.kind !== "trash" && <button type="button" className="ph-button primary" onClick={pickFiles} title={`上传到${uploadTarget.label}`}><ImagePlus /><span>上传</span></button>}
          </div>}
          {viewingLibrary?.viewing && (
            <div className="ph-viewing" role="status">
              <ShieldCheck />
              <span>只读查看 {libraryName(caller, viewingLibrary)}，{new Date(viewingLibrary.viewing.expiresAt).toLocaleString("zh-CN")} 自动结束；本次访问已写入审计并通知所有者。</span>
              <button type="button" onClick={() => void stopViewing()}>结束查看</button>
            </div>
          )}
          {subbar}
        </header>
        {main}
        {toastElement}
        <UploadTray items={uploads.items} onRetry={uploads.retry} onClear={uploads.clear} />
      </main>

      {dragging && <DropOverlay target={uploadTarget} />}
      {viewer && viewerIndex >= 0 && (
        <Viewer
          assets={viewAssets}
          index={viewerIndex}
          onIndex={(position) => { const asset = viewAssets[position]; if (asset) setViewer({ id: asset.id }); }}
          onClose={closeViewer}
          origin={viewer.origin}
          returnRect={(id) => scroller.current?.revealAsset(id) ?? Promise.resolve(undefined)}
          caller={caller}
          info={info}
          onInfo={setInfo}
          onChanged={() => undefined}
          actions={viewerActions}
        />
      )}
      {picker && <AlbumPicker caller={caller} assets={picker} onPick={(album) => void addTo(album, picker)} onCreate={(library, name) => void createAndAdd(library, name, picker)} onClose={() => setPicker(undefined)} />}
      {photoPicker && <PhotoPicker album={photoPicker} onAdd={(assets) => { setPhotoPicker(undefined); void addTo(photoPicker, assets); }} onClose={() => setPhotoPicker(undefined)} />}
      {dialogs.dialog}
      {busy && !selection.selected.size && <div className="ph-busy" role="status"><span className="ph-spinner" />{busy.label} {busy.done}/{busy.total}</div>}
    </div>
  );
}
