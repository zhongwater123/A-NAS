import { CircleAlert, RefreshCw } from "lucide-react";
import { type FormEvent, useCallback, useLayoutEffect, useRef, useState } from "react";

import { useDialogs, useToasts } from "../ui/feedback";
import { useStoredState } from "../ui/useStoredState";
import { ActionDialogs } from "./ActionDialogs";
import type { PhotoAsset } from "./api";
import { Empty } from "./Collections";
import type { GridProps } from "./grid";
import { clampRowHeight, defaultRowHeight } from "./layout";
import { libraryOf, ownLibrary, placeLibrary, placeViewing } from "./model";
import type { ScrollerHandle } from "./PhotoScroller";
import { PlaceView } from "./PlaceView";
import styles from "./photos.module.css";
import { Sidebar } from "./Sidebar";
import { Slideshow } from "./Slideshow";
import { Subbar } from "./Subbar";
import { SourceBadge, searchLibraries } from "./scope";
import { useSelection } from "./selection";
import { type SearchBox, Toolbar } from "./Toolbar";
import { placeTitle } from "./title";
import { DropOverlay, UploadTray } from "./uploads";
import { useAIStatus } from "./useAIStatus";
import { useImageSearch } from "./useImageSearch";
import { useLibraries } from "./useLibraries";
import { usePanelInput } from "./usePanelInput";
import { usePhotoActions } from "./usePhotoActions";
import { usePlace } from "./usePlace";
import { usePlaceUploads } from "./usePlaceUploads";
import { useViewer } from "./useViewer";
import { Viewer } from "./Viewer";

interface Props {
  userId: string;
  userName: string;
  isAdmin: boolean;
  /** A photo to show on its own, asked for by another window; onIntentHandled clears it. */
  intent?: { assetId: string };
  onIntentHandled?(): void;
}

const narrowWidth = 720;

// PhotosPanel is the photos window: libraries and albums on the side, photos
// filling the rest, a viewer over everything. The photo service decides
// every permission; the panel only leaves out what it would refuse.
export function PhotosPanel({ userId, userName, isAdmin, intent, onIntentHandled }: Props) {
  const [rowHeight, setRowHeight] = useStoredState("a-nas.photos.rowHeight", defaultRowHeight);
  const [info, setInfo] = useStoredState("a-nas.photos.info", true);
  const [docked, setDocked] = useStoredState("a-nas.photos.sidebar", true);
  const [drawer, setDrawer] = useState(false);
  const [viewAssets, setViewAssets] = useState<PhotoAsset[]>([]);
  const selection = useSelection(viewAssets);
  const view = usePlace({ clearSelection: selection.clear, setDrawer });
  const {
    place,
    setPlace,
    setPlaceState,
    placeRef,
    count,
    videoCount,
    hidden,
    setHidden,
    closest,
    searchLibrary,
    setSearchLibrary,
    found,
    searchPeople,
    imageResult,
    query,
    setQuery,
  } = view;
  const { libraries, setLibraries, failure, caller, loadLibraries } = useLibraries({ userId, isAdmin, setPlaceState });
  const [refreshKey, setRefreshKey] = useState(0);
  const [albumsKey, setAlbumsKey] = useState(0);
  const [recent, setRecent] = useStoredState<string[]>("a-nas.photos.recentSearches", []);
  const [width, setWidth] = useState(1000);
  const [topInset, setTopInset] = useState(64);
  const root = useRef<HTMLDivElement>(null);
  const toolbar = useRef<HTMLElement>(null);
  const searchInput = useRef<HTMLInputElement>(null);
  const scroller = useRef<ScrollerHandle>(null);
  const dialogs = useDialogs();
  const { toast, element: toastElement } = useToasts();
  const reportError = useCallback((message: string) => toast({ text: message, tone: "error" }), [toast]);
  // Results that mix libraries mark the photos that are not the caller's own.
  const sourceBadge = useCallback((asset: PhotoAsset) => <SourceBadge caller={caller} asset={asset} />, [caller]);

  const mine = ownLibrary(caller);
  const narrow = width < narrowWidth;
  const sidebarOpen = narrow ? drawer : docked;

  const ai = useAIStatus();

  // The window's width decides whether the sidebar docks; the toolbar's
  // height is where the photos start.
  const librariesLoaded = libraries !== undefined;
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
  }, [librariesLoaded]);

  const libraryId = place ? placeLibrary(place) : "";
  // AI search keeps the sidebar on the library it came from.
  const [lastLibrary, setLastLibrary] = useState("");
  if (libraryId && libraryId !== lastLibrary) setLastLibrary(libraryId);
  const sideLibrary =
    libraryOf(caller, libraryId) ??
    (place && "viewing" in place && place.viewing
      ? libraryOf(caller, place.viewing)
      : libraryOf(caller, libraryId || lastLibrary)) ??
    mine;
  const viewingLibrary = sideLibrary?.viewing ? sideLibrary : undefined;
  const readOnlyPlace = Boolean(viewingLibrary);

  const { uploadTarget, uploads, pending, pickFiles } = usePlaceUploads({
    place,
    caller,
    mine,
    viewingLibrary,
    toast,
    setRefreshKey,
    setAlbumsKey,
  });

  const actions = usePhotoActions({
    place,
    setPlace,
    setPlaceState,
    caller,
    mine,
    sideLibrary,
    viewingLibrary,
    selection,
    viewAssets,
    setHidden,
    setAlbumsKey,
    setRefreshKey,
    setLibraries,
    loadLibraries,
    dialogs,
    toast,
    reportError,
  });
  const { busy, picker, photoPicker, setPhotoPicker, setPeopleSettings, newAlbum, dropOnAlbum, dropOnLibrary } =
    actions;

  const {
    viewer,
    setViewer,
    focused,
    setFocused,
    slideshow,
    setSlideshow,
    openViewer,
    viewerIndex,
    viewerActions,
    focusedActions,
    closeViewer,
  } = useViewer({
    intent,
    onIntentHandled,
    viewAssets,
    place,
    setPlace,
    caller,
    actions,
    dialogs,
    toast,
    reportError,
    setRefreshKey,
    root,
  });

  // ---- Grid wiring ----
  const zoomBy = useCallback(
    (factor: number) => setRowHeight((value) => clampRowHeight(value * factor)),
    [setRowHeight],
  );
  const grid: GridProps = {
    rowHeight,
    onZoom: zoomBy,
    selected: selection.selected,
    onOpen: openViewer,
    onToggle: selection.toggle,
    onBeginSwipe: selection.beginSwipe,
    onMarquee: selection.marquee,
    onGestureEnd: selection.endGesture,
    onBackgroundClick: selection.clear,
    onSelectGroup: selection.setGroup,
    onDragIds: readOnlyPlace
      ? undefined
      : (asset) => (selection.selected.has(asset.id) ? [...selection.selected] : [asset.id]),
    onAssets: setViewAssets,
    hidden,
    refreshKey,
    scroller,
    topInset,
  };

  // Every search opens AI search; a search from a member library being
  // viewed covers that library too.
  const startSearch = (text: string) => {
    if (!place) return;
    setPlace({ kind: "search", viewing: placeViewing(caller, place), query: text });
    setQuery(text);
    if (text) setRecent((current) => [text, ...current.filter((item) => item !== text)].slice(0, 8));
  };
  const submitSearch = (event: FormEvent) => {
    event.preventDefault();
    const text = query.trim();
    if (text) startSearch(text);
    else if (place?.kind === "search") setPlace({ ...place, query: "" });
  };

  const { keyDown, dropHandlers, dragging, imageOver, setImageOver, endDrag } = usePanelInput({
    viewer,
    dialogs,
    picker,
    photoPicker,
    selection,
    place,
    viewAssets,
    searchInput,
    uploadTarget,
    uploads,
  });

  const { pickSearchImage, imageDrop, imageDragOver } = useImageSearch({
    caller,
    placeRef,
    setPlace,
    setImageOver,
    endDrag,
    reportError,
  });

  if (!libraries || !place) {
    return (
      <div className={`${styles.phApp} ${styles.phAppState}`}>
        {failure ? (
          <Empty
            icon={<CircleAlert />}
            title="相册暂时不可用"
            text={failure}
            action={
              <button type="button" className={styles.phButton} onClick={() => void loadLibraries()}>
                <RefreshCw />
                重试
              </button>
            }
          />
        ) : (
          <div className={styles.phLoading}>
            <span className={styles.phSpinner} />
          </div>
        )}
      </div>
    );
  }

  // A narrowed search keeps its library only while that library is in scope.
  const scope =
    place.kind === "search" && searchLibraries(caller, place.viewing).some((library) => library.id === searchLibrary)
      ? searchLibrary
      : "";
  const title = placeTitle(place, {
    caller,
    sideLibrary,
    count,
    videoCount,
    closest,
    found,
    searchPeople,
    scope,
    imageResult,
  });
  const searchBox: SearchBox = {
    query,
    setQuery,
    submitSearch,
    searchInput,
    dragging,
    imageOver,
    imageDrop,
    imageDragOver,
    pickSearchImage,
  };

  return (
    <div
      ref={root}
      className={`${styles.phApp}${sidebarOpen ? " nav-open" : ""}${narrow ? ` ${styles.narrow}` : ""}${selection.selected.size ? ` ${styles.selecting}` : ""}${dragging ? ` ${styles.dragging}` : ""}`}
      tabIndex={-1}
      onKeyDown={keyDown}
      // Another member's library under a viewing grant is beyond what VNAS may
      // read (assistant/webosControl.ts); the member ends the viewing first.
      data-vnas-private={viewingLibrary ? "他人的图库（只读查看）" : undefined}
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
          onLibrary={(library) =>
            setPlace(
              place.kind === "albums"
                ? { kind: "albums", libraryId: library.id }
                : place.kind === "trash" && !library.viewing
                  ? { kind: "trash", libraryId: library.id }
                  : { kind: "timeline", libraryId: library.id },
            )
          }
          onCreateAlbum={() => void newAlbum()}
          onDropOnAlbum={dropOnAlbum}
          onDropOnLibrary={dropOnLibrary}
        />
      )}
      {narrow && drawer && <div className={styles.phScrim} onClick={() => setDrawer(false)} />}

      <main className={styles.phMain} aria-label={title.text}>
        <Toolbar
          toolbar={toolbar}
          place={place}
          title={title}
          caller={caller}
          selection={selection}
          actions={actions}
          sidebarOpen={sidebarOpen}
          onToggleSidebar={() => (narrow ? setDrawer(!drawer) : setDocked(!docked))}
          setPlace={setPlace}
          rowHeight={rowHeight}
          setRowHeight={setRowHeight}
          zoomBy={zoomBy}
          setPhotoPicker={setPhotoPicker}
          uploadTarget={uploadTarget}
          pickFiles={pickFiles}
          viewingLibrary={viewingLibrary}
          {...searchBox}
        >
          <Subbar
            place={place}
            caller={caller}
            setPlace={setPlace}
            scope={scope}
            setSearchLibrary={setSearchLibrary}
            found={found}
            viewAssets={viewAssets}
            imageResult={imageResult}
            ai={ai}
            {...searchBox}
          />
        </Toolbar>
        <PlaceView
          place={place}
          view={view}
          grid={grid}
          caller={caller}
          userName={userName}
          readOnlyPlace={readOnlyPlace}
          uploadTarget={uploadTarget}
          pickFiles={pickFiles}
          pending={pending}
          uploads={uploads}
          selection={selection}
          refreshKey={refreshKey}
          albumsKey={albumsKey}
          topInset={topInset}
          ai={ai}
          recent={recent}
          setRecent={setRecent}
          startSearch={startSearch}
          scope={scope}
          sourceBadge={sourceBadge}
          newAlbum={newAlbum}
          setPeopleSettings={setPeopleSettings}
          setPhotoPicker={setPhotoPicker}
          reportError={reportError}
          dragging={dragging}
          imageOver={imageOver}
          imageDrop={imageDrop}
          imageDragOver={imageDragOver}
          pickSearchImage={pickSearchImage}
        />
        {toastElement}
        <UploadTray items={uploads.items} onRetry={uploads.retry} onClear={uploads.clear} />
      </main>

      {dragging && <DropOverlay target={uploadTarget} search={Boolean(imageOver)} />}
      {viewer && viewerIndex >= 0 && !slideshow && (
        <Viewer
          assets={viewAssets}
          index={viewerIndex}
          onIndex={(position) => {
            const asset = viewAssets[position];
            if (asset) setViewer({ id: asset.id });
          }}
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
      {focused && !viewer && (
        <Viewer
          assets={[focused]}
          index={0}
          onIndex={() => undefined}
          onClose={() => {
            setFocused(undefined);
            root.current?.focus();
          }}
          returnRect={() => Promise.resolve(undefined)}
          caller={caller}
          info={info}
          onInfo={setInfo}
          onChanged={() => undefined}
          actions={focusedActions}
        />
      )}
      {slideshow && (
        <Slideshow
          assets={viewAssets}
          startId={slideshow}
          total={count}
          more={count === undefined || viewAssets.length < count}
          onReveal={(id) => void scroller.current?.revealAsset(id)}
          onExit={(id) => {
            setSlideshow(undefined);
            setViewer({ id });
          }}
        />
      )}
      <ActionDialogs place={place} caller={caller} actions={actions} />
      {dialogs.dialog}
      {busy && !selection.selected.size && (
        <div className={styles.phBusy} role="status">
          <span className={styles.phSpinner} />
          {busy.label} {busy.done}/{busy.total}
        </div>
      )}
    </div>
  );
}
