import { ArrowLeft, CircleAlert, PanelLeft, RefreshCw, Search, X } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { listSpaces, type Space } from "../api";
import { CategoryPage, FavoritesPage, HistoryPage, Loading, messageOf } from "./Browse";
import { ActionsContext, type MediaActions } from "./Cards";
import { CollectionPage, CollectionsPage } from "./Collections";
import { ShowPage, VideoPage } from "./Detail";
import { CollectionPicker, useDialogs, useToasts } from "./feedback";
import { FoldersPage, type FolderPlace } from "./Folders";
import { HomePage } from "./Home";
import { LibrariesPage, LibraryDialog } from "./Libraries";
import {
  clearHistory, createCollection, deleteCollection, deleteLibrary, getHome, getShow, listLibraries, removeFromCollection, removeHistory, renameCollection, scanLibrary,
  setFavorite, setWatched, type Category, type Home, type MediaLibrary, type Title,
} from "./mediaApi";
import { Player, type PlayRequest } from "./Player";
import { type Section, Sidebar } from "./Sidebar";
import "./media.css";

type Place =
  | { kind: "home" }
  | { kind: "history" }
  | { kind: "favorites" }
  | { kind: "folders"; folder?: FolderPlace }
  | { kind: "collections" }
  | { kind: "collection"; id: string }
  | { kind: "libraries" }
  | { kind: "library"; id: string }
  | { kind: "category"; category: Category }
  | { kind: "search"; query: string }
  | { kind: "video"; id: string }
  | { kind: "show"; id: string };

const narrowWidth = 760;
const busyPoll = 4000;
const idlePoll = 60_000;

function sectionOf(place: Place): Section | undefined {
  switch (place.kind) {
    case "home": case "history": case "favorites": case "folders": case "collections": case "libraries": return place.kind;
    case "collection": return "collections";
    case "library": return "libraries";
    case "category": return `category:${place.category}`;
  }
  return undefined;
}

// MediaCenter is the 影视中心 window: navigation on the left, the page on the
// right, the player over both. The server decides every permission.
export function MediaCenter({ userId, isAdmin }: { userId: string; isAdmin: boolean }) {
  const [stack, setStack] = useState<Place[]>([{ kind: "home" }]);
  const place = stack[stack.length - 1];
  const [libraries, setLibraries] = useState<MediaLibrary[]>();
  const [processing, setProcessing] = useState(true);
  const [home, setHome] = useState<Home>();
  const [failure, setFailure] = useState("");
  const [refreshKey, setRefreshKey] = useState(0);
  const [query, setQuery] = useState("");
  const [play, setPlay] = useState<PlayRequest>();
  const [picker, setPicker] = useState<Title>();
  const [editing, setEditing] = useState<{ library?: MediaLibrary; spaces: Space[] }>();
  const [width, setWidth] = useState(1100);
  const [docked, setDocked] = useState(true);
  const [drawer, setDrawer] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const content = useRef<HTMLDivElement>(null);
  const signature = useRef("");
  const dialogs = useDialogs();
  const { toast, element: toasts } = useToasts();
  const refresh = useCallback(() => setRefreshKey((value) => value + 1), []);
  const onError = useCallback((message: string) => toast({ text: message, tone: "error" }), [toast]);
  const narrow = width < narrowWidth;
  const sidebarOpen = narrow ? drawer : docked;

  useEffect(() => {
    const element = root.current;
    if (!element || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(([entry]) => setWidth(entry.contentRect.width));
    observer.observe(element);
    return () => observer.disconnect();
  }, []);

  const navigate = useCallback((next: Place, push = true) => {
    setStack((current) => (push ? [...current, next] : [next]));
    setDrawer(false);
    content.current?.scrollTo?.({ top: 0 });
  }, []);
  const back = useCallback(() => setStack((current) => (current.length > 1 ? current.slice(0, -1) : current)), []);

  // Libraries poll quickly while any of them is being scanned; when what they
  // hold changes, the pages reload.
  const loadLibraries = useCallback(async () => {
    try {
      const result = await listLibraries();
      setLibraries(result.items);
      setProcessing(result.processing);
      setFailure("");
      const next = result.items.map((library) => `${library.id}:${library.counts.movies}:${library.counts.shows}:${library.counts.episodes}:${library.counts.others}:${library.scan.state}`).join("|");
      if (signature.current && next !== signature.current) refresh();
      signature.current = next;
      return result.items.some((library) => library.scan.state !== "idle");
    } catch (caught) {
      setFailure(messageOf(caught));
      return false;
    }
  }, [refresh]);
  useEffect(() => {
    let timer = 0;
    let live = true;
    const tick = async () => {
      const busy = await loadLibraries();
      if (live) timer = window.setTimeout(() => void tick(), busy ? busyPoll : idlePoll);
    };
    void tick();
    return () => { live = false; window.clearTimeout(timer); };
  }, [loadLibraries]);

  useEffect(() => {
    if (place.kind !== "home") return;
    let live = true;
    getHome().then((value) => { if (live) setHome(value); }, (caught) => { if (live) onError(messageOf(caught)); });
    return () => { live = false; };
  }, [place.kind, refreshKey, onError]);

  const playTitle = useCallback(async (title: Title, restart = false) => {
    if (title.type === "show") {
      try {
        const show = await getShow(title.id);
        if (show.next) setPlay({ videoId: show.next.id, restart });
      } catch (caught) { onError(messageOf(caught)); }
      return;
    }
    setPlay({ videoId: title.id, restart });
  }, [onError]);

  const actions: MediaActions = useMemo(() => ({
    open: (title) => {
      if (title.type === "show") navigate({ kind: "show", id: title.id });
      else if (title.type === "episode" && title.showId) navigate({ kind: "show", id: title.showId });
      else navigate({ kind: "video", id: title.id });
    },
    play: (title, restart) => void playTitle(title, restart),
    toggleFavorite: (title) => {
      setFavorite(title.id, !title.favorite).then(() => {
        toast({ text: title.favorite ? "已取消收藏" : "已加入我的收藏" });
        refresh();
      }, (caught) => onError(messageOf(caught)));
    },
    setWatched: (title, watched) => {
      setWatched(title.id, watched).then(() => {
        toast({ text: watched ? "已标记为已看" : "已标记为未看" });
        refresh();
      }, (caught) => onError(messageOf(caught)));
    },
    addToCollection: (title) => setPicker(title),
  }), [navigate, playTitle, toast, refresh, onError]);

  const openLibraryDialog = async (library?: MediaLibrary) => {
    try { setEditing({ library, spaces: await listSpaces() }); } catch (caught) { onError(messageOf(caught)); }
  };
  const canCreate = true;

  const select = (section: Section) => {
    if (section.startsWith("category:")) navigate({ kind: "category", category: section.slice(9) as Category }, false);
    else navigate({ kind: section } as Place, false);
    setQuery("");
  };

  const submitSearch = (value: string) => {
    setQuery(value);
    const trimmed = value.trim();
    if (!trimmed) { if (place.kind === "search") back(); return; }
    if (place.kind === "search") setStack((current) => [...current.slice(0, -1), { kind: "search", query: trimmed }]);
    else navigate({ kind: "search", query: trimmed });
  };

  const removeLibrary = async (library: MediaLibrary) => {
    const ok = await dialogs.confirm({
      title: `删除媒体库“${library.name}”？`, danger: true, confirm: "删除媒体库",
      message: <>只会删除影视中心里的目录、播放进度和收藏。<strong>文件夹中的视频文件不会被删除</strong>，以后可以重新添加。</>,
    });
    if (!ok) return;
    try { await deleteLibrary(library.id); toast({ text: `已删除媒体库“${library.name}”` }); void loadLibraries(); refresh(); }
    catch (caught) { onError(messageOf(caught)); }
  };

  const libraryOf = (id: string) => libraries?.find((library) => library.id === id);
  const detail = place.kind === "video" || place.kind === "show";

  let page;
  switch (place.kind) {
    case "home":
      page = !home ? <Loading /> : (
        <HomePage home={home} canCreate={canCreate} onLibraries={() => navigate({ kind: "libraries" }, false)} onCreateLibrary={() => void openLibraryDialog()}
          onCategory={(category) => {
            if (category === "favorites") navigate({ kind: "favorites" }, false);
            else if (category === "history") navigate({ kind: "history" }, false);
            else navigate({ kind: "category", category }, false);
          }} />
      );
      break;
    case "history":
      page = <HistoryPage refreshKey={refreshKey} onError={onError}
        onRemove={(title) => { removeHistory(title.id).then(refresh, (caught) => onError(messageOf(caught))); }}
        onClear={async () => {
          if (await dialogs.confirm({ title: "清空播放记录？", message: "只清除列表，播放进度与已看标记会保留。", confirm: "清空", danger: true })) {
            clearHistory().then(() => { toast({ text: "已清空播放记录" }); refresh(); }, (caught) => onError(messageOf(caught)));
          }
        }} />;
      break;
    case "favorites":
      page = <FavoritesPage refreshKey={refreshKey} onError={onError} />;
      break;
    case "folders":
      page = <FoldersPage place={place.folder} refreshKey={refreshKey} onError={onError} onPlace={(folder) => setStack((current) => [...current.slice(0, -1), { kind: "folders", folder }])} />;
      break;
    case "collections":
      page = <CollectionsPage refreshKey={refreshKey} onError={onError} onOpen={(collection) => navigate({ kind: "collection", id: collection.id })}
        onCreate={async () => {
          const name = await dialogs.prompt({ title: "新建合集", label: "合集名称", confirm: "新建" });
          if (!name) return;
          createCollection(name).then((collection) => { toast({ text: `已新建合集“${collection.name}”` }); refresh(); }, (caught) => onError(messageOf(caught)));
        }} />;
      break;
    case "collection":
      page = <CollectionPage id={place.id} refreshKey={refreshKey} onError={onError} onBack={back}
        onRename={async (collection) => {
          const name = await dialogs.prompt({ title: "重命名合集", label: "合集名称", value: collection.name, confirm: "保存" });
          if (name && name !== collection.name) renameCollection(collection.id, name).then(refresh, (caught) => onError(messageOf(caught)));
        }}
        onDelete={async (collection) => {
          if (await dialogs.confirm({ title: `删除合集“${collection.name}”？`, message: "合集里的影片不会被删除。", confirm: "删除合集", danger: true })) {
            deleteCollection(collection.id).then(() => { toast({ text: "已删除合集" }); back(); refresh(); }, (caught) => onError(messageOf(caught)));
          }
        }}
        onRemove={(collection, title) => { removeFromCollection(collection.id, title.id).then(() => { toast({ text: `已从“${collection.name}”移除` }); refresh(); }, (caught) => onError(messageOf(caught))); }} />;
      break;
    case "libraries":
      page = <LibrariesPage libraries={libraries} processing={processing} canCreate={canCreate}
        onOpen={(library) => navigate({ kind: "library", id: library.id })}
        onCreate={() => void openLibraryDialog()} onEdit={(library) => void openLibraryDialog(library)} onDelete={(library) => void removeLibrary(library)}
        onScan={(library) => { scanLibrary(library.id).then(() => { toast({ text: `正在重新扫描“${library.name}”` }); void loadLibraries(); }, (caught) => onError(messageOf(caught))); }} />;
      break;
    case "library": {
      const library = libraryOf(place.id);
      page = library ? <CategoryPage category="all" library={library} libraries={libraries ?? []} refreshKey={refreshKey} onError={onError} /> : <Loading />;
      break;
    }
    case "category":
      page = <CategoryPage key={place.category} category={place.category} libraries={libraries ?? []} refreshKey={refreshKey} onError={onError} />;
      break;
    case "search":
      page = <CategoryPage key="search" category="all" search={place.query} libraries={libraries ?? []} refreshKey={refreshKey} onError={onError} />;
      break;
    case "video":
      page = <VideoPage id={place.id} refreshKey={refreshKey} onBack={back} onError={onError} />;
      break;
    case "show":
      page = <ShowPage id={place.id} refreshKey={refreshKey} onBack={back} onError={onError} />;
      break;
  }

  return (
    <ActionsContext.Provider value={actions}>
      <div ref={root} className={`mc-app ${narrow ? "narrow" : ""} ${sidebarOpen ? "with-sidebar" : ""}`} data-user={userId}>
        {(sidebarOpen || narrow) && (
          <div className={`mc-side ${narrow ? "drawer" : ""} ${sidebarOpen ? "open" : ""}`}>
            <Sidebar active={sectionOf(place)} libraries={libraries} processing={processing} onSelect={select} />
          </div>
        )}
        {narrow && drawer && <div className="mc-scrim" onPointerDown={() => setDrawer(false)} />}
        <main className={`mc-main ${detail ? "detail" : ""}`}>
          <div className="mc-topbar">
            <button type="button" className="mc-icon-button" aria-label={sidebarOpen ? "收起侧栏" : "展开侧栏"} onClick={() => (narrow ? setDrawer(!drawer) : setDocked(!docked))}><PanelLeft /></button>
            {stack.length > 1 && <button type="button" className="mc-icon-button" aria-label="返回" onClick={back}><ArrowLeft /></button>}
            <label className="mc-search">
              <Search />
              <input value={query} placeholder="搜索电影、电视剧、视频" aria-label="搜索影视"
                onChange={(event) => submitSearch(event.target.value)} onKeyDown={(event) => { if (event.key === "Escape") submitSearch(""); }} />
              {query && <button type="button" aria-label="清除搜索" onClick={() => submitSearch("")}><X /></button>}
            </label>
            <span className="mc-topbar-gap" />
            {place.kind === "libraries" && libraries?.some((library) => library.canManage) && (
              <button type="button" className="mc-icon-button" aria-label="全部重新扫描" title="全部重新扫描"
                onClick={() => { Promise.all(libraries.filter((library) => library.canManage).map((library) => scanLibrary(library.id))).then(() => { toast({ text: "已开始重新扫描全部媒体库" }); void loadLibraries(); }, (caught) => onError(messageOf(caught))); }}>
                <RefreshCw />
              </button>
            )}
          </div>
          <div ref={content} className="mc-content">
            {failure && !libraries ? (
              <div className="mc-state"><CircleAlert /><h2>影视中心暂时不可用</h2><p>{failure}</p><button type="button" className="mc-glass" onClick={() => void loadLibraries()}>重试</button></div>
            ) : page}
          </div>
        </main>
        {play && <Player request={play} onClose={() => { setPlay(undefined); refresh(); }} onNavigate={(videoId) => setPlay({ videoId })} onSaved={() => undefined} />}
        {picker && <CollectionPicker title={picker} onClose={() => setPicker(undefined)} onDone={(text) => { setPicker(undefined); toast({ text }); refresh(); }} />}
        {editing && <LibraryDialog library={editing.library} spaces={editing.spaces} isAdmin={isAdmin} onClose={() => setEditing(undefined)}
          onSaved={(library) => {
            setEditing(undefined);
            toast({ text: editing.library ? `已保存“${library.name}”，正在重新扫描` : `已创建“${library.name}”，正在扫描` });
            void loadLibraries();
            refresh();
          }} />}
        {dialogs.dialog}
        {toasts}
      </div>
    </ActionsContext.Provider>
  );
}
