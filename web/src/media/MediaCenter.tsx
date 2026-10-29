import { ArrowLeft, CircleAlert, PanelLeft, RefreshCw, Search, X } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import type { Space } from "../api.generated";
import { listSpaces } from "../files/api";
import {
  type Category,
  clearHistory,
  createCollection,
  deleteCollection,
  deleteLibrary,
  getHome,
  getShow,
  type Home,
  listLibraries,
  type MediaLibrary,
  removeFromCollection,
  removeHistory,
  renameCollection,
  scanLibrary,
  setFavorite,
  setWatched,
  type Title,
} from "./api";
import { CategoryPage, FavoritesPage, HistoryPage, Loading, messageOf } from "./Browse";
import { ActionsContext, type MediaActions } from "./Cards";
import { CollectionPage, CollectionsPage } from "./Collections";
import { ShowPage, VideoPage } from "./Detail";
import { type FolderPlace, FoldersPage } from "./Folders";
import { CollectionPicker, RenameDialog, useDialogs, useToasts } from "./feedback";
import { HomePage } from "./Home";
import { LibrariesPage, LibraryDialog } from "./Libraries";
import styles from "./media.module.css";
import { Player, type PlayRequest } from "./Player";
import { type Section, Sidebar } from "./Sidebar";
import { DropOverlay, hasFiles, UploadDialog, UploadPill, uploadTargets } from "./Upload";
import { pickedFromDrop, useUploads } from "./uploads";

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
const busyPoll = 3000;
const idlePoll = 20_000;
// After the user changes or rescans a library, poll quickly for a while so a
// short scan is not missed between two idle polls.
const followWindow = 30_000;

function sectionOf(place: Place): Section | undefined {
  switch (place.kind) {
    case "home":
    case "history":
    case "favorites":
    case "folders":
    case "collections":
    case "libraries":
      return place.kind;
    case "collection":
      return "collections";
    case "library":
      return "libraries";
    case "category":
      return `category:${place.category}`;
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
  const [renaming, setRenaming] = useState<Title>();
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
      const next = result.items
        .map(
          (library) =>
            `${library.id}:${library.counts.movies}:${library.counts.shows}:${library.counts.episodes}:${library.counts.others}:${library.scan.state}`,
        )
        .join("|");
      if (signature.current && next !== signature.current) refresh();
      signature.current = next;
      return result.items.some((library) => library.scan.state !== "idle");
    } catch (caught) {
      setFailure(messageOf(caught));
      return false;
    }
  }, [refresh]);
  const poll = useRef<{ timer: number; until: number; tick?: () => void }>({ timer: 0, until: 0 });
  useEffect(() => {
    let live = true;
    const state = poll.current;
    // A refresh can start while another load is still running; whichever
    // finishes last clears the other's timer, so one poll stays scheduled.
    const tick = async () => {
      window.clearTimeout(state.timer);
      const busy = await loadLibraries();
      if (!live) return;
      window.clearTimeout(state.timer);
      state.timer = window.setTimeout(() => void tick(), busy || Date.now() < state.until ? busyPoll : idlePoll);
    };
    state.tick = () => void tick();
    void tick();
    return () => {
      live = false;
      window.clearTimeout(state.timer);
      state.tick = undefined;
    };
  }, [loadLibraries]);
  // follow refreshes the libraries now and keeps polling quickly for a while.
  const follow = useCallback(() => {
    poll.current.until = Date.now() + followWindow;
    poll.current.tick?.();
  }, []);
  // Uploads outlive their dialog; a finished batch is announced only when
  // the dialog is not there to show it.
  const [uploading, setUploading] = useState<{ scope: Category; preferred?: string }>();
  const uploadShown = useRef(false);
  useEffect(() => {
    uploadShown.current = Boolean(uploading);
  }, [uploading]);
  const lastUpload = useRef<{ scope: Category; preferred?: string }>({ scope: "all" });
  const uploads = useUploads({
    onUploaded: follow,
    onSettled: (settled) => {
      follow();
      if (uploadShown.current) return;
      const done = settled.filter((item) => item.state === "done").length;
      const failed = settled.filter((item) => item.state === "failed").length;
      if (failed)
        toast({
          text: `${failed} 个文件上传失败`,
          tone: "error",
          action: { label: "查看", run: () => setUploading(lastUpload.current) },
        });
      else if (done) toast({ text: `已上传 ${done} 个文件，新视频稍后出现在影视中心` });
    },
  });
  const openUpload = (scope: Category, preferred?: string) => {
    // A new visit starts clean unless uploads are still going or some failed.
    if (!uploads.running && !uploads.items.some((item) => item.state === "failed")) uploads.clear();
    lastUpload.current = { scope, preferred };
    setUploading({ scope, preferred });
  };
  const closeUpload = () => {
    setUploading(undefined);
    if (!uploads.running) uploads.clear();
  };
  const [dropping, setDropping] = useState(false);

  const rescan = useCallback(
    (targets: MediaLibrary[], message: string) => {
      Promise.all(targets.map((library) => scanLibrary(library.id))).then(
        (scanned) => {
          setLibraries((current) =>
            current?.map((library) => scanned.find((item) => item?.id === library.id) ?? library),
          );
          toast({ text: message });
          follow();
        },
        (caught) => onError(messageOf(caught)),
      );
    },
    [follow, toast, onError],
  );

  useEffect(() => {
    if (place.kind !== "home") return;
    let live = true;
    getHome().then(
      (value) => {
        if (live) setHome(value);
      },
      (caught) => {
        if (live) onError(messageOf(caught));
      },
    );
    return () => {
      live = false;
    };
  }, [place.kind, refreshKey, onError]);

  const closePlayer = () => {
    setPlay(undefined);
    refresh();
  };

  const playTitle = useCallback(
    async (title: Title, restart = false) => {
      if (title.type === "show") {
        try {
          const show = await getShow(title.id);
          if (show.next) setPlay({ videoId: show.next.id, restart });
        } catch (caught) {
          onError(messageOf(caught));
        }
        return;
      }
      setPlay({ videoId: title.id, restart });
    },
    [onError],
  );

  const actions: MediaActions = useMemo(
    () => ({
      open: (title) => {
        if (title.type === "show") navigate({ kind: "show", id: title.id });
        else if (title.type === "episode" && title.showId) navigate({ kind: "show", id: title.showId });
        else navigate({ kind: "video", id: title.id });
      },
      play: (title, restart) => void playTitle(title, restart),
      toggleFavorite: (title) => {
        setFavorite(title.id, !title.favorite).then(
          () => {
            toast({ text: title.favorite ? "已取消收藏" : "已加入我的收藏" });
            refresh();
          },
          (caught) => onError(messageOf(caught)),
        );
      },
      setWatched: (title, watched) => {
        setWatched(title.id, watched).then(
          () => {
            toast({ text: watched ? "已标记为已看" : "已标记为未看" });
            refresh();
          },
          (caught) => onError(messageOf(caught)),
        );
      },
      addToCollection: (title) => setPicker(title),
      rename: (title) => setRenaming(title),
      renamed: (message) => {
        toast({ text: message });
        follow();
        refresh();
      },
    }),
    [navigate, playTitle, toast, refresh, onError, follow],
  );

  const openLibraryDialog = async (library?: MediaLibrary) => {
    try {
      setEditing({ library, spaces: await listSpaces() });
    } catch (caught) {
      onError(messageOf(caught));
    }
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
    if (!trimmed) {
      if (place.kind === "search") back();
      return;
    }
    if (place.kind === "search") setStack((current) => [...current.slice(0, -1), { kind: "search", query: trimmed }]);
    else navigate({ kind: "search", query: trimmed });
  };

  const removeLibrary = async (library: MediaLibrary) => {
    const ok = await dialogs.confirm({
      title: `删除媒体库“${library.name}”？`,
      danger: true,
      confirm: "删除媒体库",
      message: (
        <>
          只会删除影视中心里的目录、播放进度和收藏。<strong>文件夹中的视频文件不会被删除</strong>，以后可以重新添加。
        </>
      ),
    });
    if (!ok) return;
    try {
      await deleteLibrary(library.id);
      toast({ text: `已删除媒体库“${library.name}”` });
      follow();
      refresh();
    } catch (caught) {
      onError(messageOf(caught));
    }
  };

  const libraryOf = (id: string) => libraries?.find((library) => library.id === id);
  const detail = place.kind === "video" || place.kind === "show";
  // Every page takes files dropped on it, so a video dropped anywhere is
  // uploaded rather than opened by the browser in place of the NAS. A
  // category page offers the libraries whose titles it lists.
  const uploadScope: Category = place.kind === "category" ? place.category : "all";
  const uploadPreferred = place.kind === "library" ? place.id : undefined;
  const dropLabel =
    place.kind === "library"
      ? `“${libraryOf(place.id)?.name ?? "媒体库"}”`
      : place.kind === "category"
        ? { all: "影视中心", movie: "“电影”", show: "“电视剧”", other: "“其他”" }[place.category]
        : "媒体库";

  let page;
  switch (place.kind) {
    case "home":
      page = !home ? (
        <Loading />
      ) : (
        <HomePage
          home={home}
          canCreate={canCreate}
          onLibraries={() => navigate({ kind: "libraries" }, false)}
          onCreateLibrary={() => void openLibraryDialog()}
          onUpload={uploadTargets(libraries ?? [], "all").length ? () => openUpload("all") : undefined}
          onCategory={(category) => {
            if (category === "favorites") navigate({ kind: "favorites" }, false);
            else if (category === "history") navigate({ kind: "history" }, false);
            else navigate({ kind: "category", category }, false);
          }}
        />
      );
      break;
    case "history":
      page = (
        <HistoryPage
          refreshKey={refreshKey}
          onError={onError}
          onRemove={(title) => {
            removeHistory(title.id).then(refresh, (caught) => onError(messageOf(caught)));
          }}
          onClear={async () => {
            if (
              await dialogs.confirm({
                title: "清空播放记录？",
                message: "只清除列表，播放进度与已看标记会保留。",
                confirm: "清空",
                danger: true,
              })
            ) {
              clearHistory().then(
                () => {
                  toast({ text: "已清空播放记录" });
                  refresh();
                },
                (caught) => onError(messageOf(caught)),
              );
            }
          }}
        />
      );
      break;
    case "favorites":
      page = <FavoritesPage refreshKey={refreshKey} onError={onError} />;
      break;
    case "folders":
      page = (
        <FoldersPage
          place={place.folder}
          refreshKey={refreshKey}
          onError={onError}
          onPlace={(folder) => setStack((current) => [...current.slice(0, -1), { kind: "folders", folder }])}
        />
      );
      break;
    case "collections":
      page = (
        <CollectionsPage
          refreshKey={refreshKey}
          onError={onError}
          onOpen={(collection) => navigate({ kind: "collection", id: collection.id })}
          onCreate={async () => {
            const name = await dialogs.prompt({ title: "新建合集", label: "合集名称", confirm: "新建" });
            if (!name) return;
            createCollection(name).then(
              (collection) => {
                toast({ text: `已新建合集“${collection.name}”` });
                refresh();
              },
              (caught) => onError(messageOf(caught)),
            );
          }}
        />
      );
      break;
    case "collection":
      page = (
        <CollectionPage
          id={place.id}
          refreshKey={refreshKey}
          onError={onError}
          onBack={back}
          onRename={async (collection) => {
            const name = await dialogs.prompt({
              title: "重命名合集",
              label: "合集名称",
              value: collection.name,
              confirm: "保存",
            });
            if (name && name !== collection.name)
              renameCollection(collection.id, name).then(refresh, (caught) => onError(messageOf(caught)));
          }}
          onDelete={async (collection) => {
            if (
              await dialogs.confirm({
                title: `删除合集“${collection.name}”？`,
                message: "合集里的影片不会被删除。",
                confirm: "删除合集",
                danger: true,
              })
            ) {
              deleteCollection(collection.id).then(
                () => {
                  toast({ text: "已删除合集" });
                  back();
                  refresh();
                },
                (caught) => onError(messageOf(caught)),
              );
            }
          }}
          onRemove={(collection, title) => {
            removeFromCollection(collection.id, title.id).then(
              () => {
                toast({ text: `已从“${collection.name}”移除` });
                refresh();
              },
              (caught) => onError(messageOf(caught)),
            );
          }}
        />
      );
      break;
    case "libraries":
      page = (
        <LibrariesPage
          libraries={libraries}
          processing={processing}
          canCreate={canCreate}
          onOpen={(library) => navigate({ kind: "library", id: library.id })}
          onCreate={() => void openLibraryDialog()}
          onEdit={(library) => void openLibraryDialog(library)}
          onDelete={(library) => void removeLibrary(library)}
          onScan={(library) => rescan([library], `正在重新扫描“${library.name}”`)}
          onUpload={(library) => openUpload("all", library?.id)}
        />
      );
      break;
    case "library": {
      const library = libraryOf(place.id);
      page = library ? (
        <CategoryPage
          category="all"
          library={library}
          libraries={libraries ?? []}
          refreshKey={refreshKey}
          onError={onError}
          onUpload={() => openUpload("all", library.id)}
        />
      ) : (
        <Loading />
      );
      break;
    }
    case "category":
      page = (
        <CategoryPage
          key={place.category}
          category={place.category}
          libraries={libraries ?? []}
          refreshKey={refreshKey}
          onError={onError}
          onUpload={() => openUpload(place.category)}
        />
      );
      break;
    case "search":
      page = (
        <CategoryPage
          key="search"
          category="all"
          search={place.query}
          libraries={libraries ?? []}
          refreshKey={refreshKey}
          onError={onError}
        />
      );
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
      <div
        ref={root}
        className={`${styles.mcApp} ${narrow ? "narrow" : ""} ${sidebarOpen ? "with-sidebar" : ""}`}
        data-user={userId}
        onDragOver={(event) => {
          if (hasFiles(event)) {
            event.preventDefault();
            if (play) event.dataTransfer.dropEffect = "none";
          }
        }}
        onDrop={(event) => {
          if (hasFiles(event)) event.preventDefault();
        }}
        onMouseDown={(event) => {
          if (event.button === 3 || event.button === 4) event.preventDefault();
        }}
        onMouseUp={(event) => {
          // The mouse's back button steps back inside the media center
          // instead of taking the browser away from the NAS desktop.
          if (event.button !== 3 && event.button !== 4) return;
          event.preventDefault();
          if (event.button === 4) return;
          if (play) closePlayer();
          else back();
        }}
      >
        {(sidebarOpen || narrow) && (
          <div className={`${styles.mcSide} ${narrow ? styles.drawer : ""} ${sidebarOpen ? styles.open : ""}`}>
            <Sidebar active={sectionOf(place)} libraries={libraries} processing={processing} onSelect={select} />
          </div>
        )}
        {narrow && drawer && <div className={styles.mcScrim} onPointerDown={() => setDrawer(false)} />}
        <main
          className={`${styles.mcMain} ${detail ? "detail" : ""}`}
          onDragEnter={(event) => {
            if (hasFiles(event) && !uploading) {
              event.preventDefault();
              setDropping(true);
            }
          }}
        >
          <div className={styles.mcTopbar}>
            <button
              type="button"
              className={styles.mcIconButton}
              aria-label={sidebarOpen ? "收起侧栏" : "展开侧栏"}
              onClick={() => (narrow ? setDrawer(!drawer) : setDocked(!docked))}
            >
              <PanelLeft />
            </button>
            {stack.length > 1 && (
              <button type="button" className={styles.mcIconButton} aria-label="返回" onClick={back}>
                <ArrowLeft />
              </button>
            )}
            <label className={styles.mcSearch}>
              <Search />
              <input
                value={query}
                placeholder="搜索电影、电视剧、视频"
                aria-label="搜索影视"
                onChange={(event) => submitSearch(event.target.value)}
                onKeyDown={(event) => {
                  if (event.key === "Escape") submitSearch("");
                }}
              />
              {query && (
                <button type="button" aria-label="清除搜索" onClick={() => submitSearch("")}>
                  <X />
                </button>
              )}
            </label>
            <span className={styles.mcTopbarGap} />
            {!uploading && <UploadPill uploads={uploads} onOpen={() => setUploading(lastUpload.current)} />}
            {place.kind === "libraries" && libraries?.some((library) => library.canManage) && (
              <button
                type="button"
                className={styles.mcIconButton}
                aria-label="全部重新扫描"
                title="全部重新扫描"
                onClick={() =>
                  rescan(
                    libraries.filter((library) => library.canManage),
                    "已开始重新扫描全部媒体库",
                  )
                }
              >
                <RefreshCw />
              </button>
            )}
          </div>
          <div ref={content} className={styles.mcContent}>
            {failure && !libraries ? (
              <div className={styles.mcState}>
                <CircleAlert />
                <h2>影视中心暂时不可用</h2>
                <p>{failure}</p>
                <button type="button" className={styles.mcGlass} onClick={() => void loadLibraries()}>
                  重试
                </button>
              </div>
            ) : (
              page
            )}
          </div>
          {dropping && (
            <DropOverlay
              label={dropLabel}
              onLeave={() => setDropping(false)}
              onDrop={(event) => {
                setDropping(false);
                void pickedFromDrop(event.dataTransfer).then(uploads.add);
                openUpload(uploadScope, uploadPreferred);
              }}
            />
          )}
        </main>
        {play && (
          <Player
            request={play}
            onClose={closePlayer}
            onNavigate={(videoId) => setPlay({ videoId })}
            onSaved={() => undefined}
          />
        )}
        {renaming && (
          <RenameDialog
            title={renaming}
            onClose={() => setRenaming(undefined)}
            onDone={(message) => {
              setRenaming(undefined);
              toast({ text: message });
              follow();
              refresh();
            }}
          />
        )}
        {picker && (
          <CollectionPicker
            title={picker}
            onClose={() => setPicker(undefined)}
            onDone={(text) => {
              setPicker(undefined);
              toast({ text });
              refresh();
            }}
          />
        )}
        {editing && (
          <LibraryDialog
            library={editing.library}
            spaces={editing.spaces}
            isAdmin={isAdmin}
            onClose={() => setEditing(undefined)}
            onSaved={(library) => {
              setEditing(undefined);
              toast({
                text: editing.library ? `已保存“${library.name}”，正在重新扫描` : `已创建“${library.name}”，正在扫描`,
              });
              follow();
              refresh();
            }}
          />
        )}
        {uploading && (
          <UploadDialog
            uploads={uploads}
            libraries={libraries ?? []}
            scope={uploading.scope}
            preferred={uploading.preferred}
            onClose={closeUpload}
            onCreateLibrary={
              canCreate
                ? () => {
                    closeUpload();
                    void openLibraryDialog();
                  }
                : undefined
            }
          />
        )}
        {dialogs.dialog}
        {toasts}
      </div>
    </ActionsContext.Provider>
  );
}
