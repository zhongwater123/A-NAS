import { CircleAlert, FolderPlus, FolderUp, ShieldCheck } from "lucide-react";
import {
  DragEvent as ReactDragEvent,
  FormEvent,
  MouseEvent as ReactMouseEvent,
  useEffect,
  useEffectEvent,
  useRef,
  useState,
} from "react";

import { FileEntry, Space, copyFile, createDirectory, deleteFile, endViewing, moveFile } from "../api";
import { VersionsDialog } from "../backup/VersionsDialog";
import { nameProblem } from "../fileNames";
import { displayName, usePeople } from "../people";
import { useShares } from "../shares/SharesProvider";
import { carriesFiles, PickedFile, pickedFromDrop } from "../transfers/picker";
import { transferStore } from "../transfers/store";
import type { TransferLocation } from "../transfers/transfersApi";
import { FileSort, isPinned, reorderFiles, sortFiles, useFileOrder } from "../useFileOrder";
import { FileContextMenu, FileContextMenuState } from "./FileContextMenu";
import { DeleteDialog, RenameDialog } from "./FileDialogs";
import { FileInspector } from "./FileInspector";
import { FileList } from "./FileList";
import { FileSidebar } from "./FileSidebar";
import { FileStatusBar } from "./FileStatusBar";
import { FileCommandBar, FileCommands, FileNavigationBar, FileOrderBar } from "./FileToolbar";
import { FileUploadControl } from "./FileUploadControl";
import { FileViewer } from "./FileViewer";
import {
  browserItem,
  duplicateFileName,
  FileBrowserItem,
  FileClipboard,
  FileResource,
  FileView,
  formatFileDate,
  isViewable,
  messageOf,
  othersSpace,
  personalFileResources,
  selectionCommands,
  sortedBy,
  spaceLabel,
  withPins,
} from "./model";
import { useDragReorder } from "./useDragReorder";
import { useFileListing } from "./useFileListing";
import { usePaneWidths } from "./usePaneWidths";
import { useTextPreview } from "./useTextPreview";
import "./files.css";

interface FilePanelProps {
  userID: string;
  navigation?: TransferLocation;
  onNavigated: () => void;
  onOpenTransfers: () => void;
  onOpenPhotos: () => void;
  onOpenMedia: () => void;
  onOpenMusic: () => void;
}

export function FilePanel({
  userID,
  navigation,
  onNavigated,
  onOpenTransfers,
  onOpenPhotos,
  onOpenMedia,
  onOpenMusic,
}: FilePanelProps) {
  const shares = useShares();
  const people = usePeople();
  // A viewed private space is named by its owner, the way they go by.
  const ownerName = (space: Space) => {
    const owner = people.byId(space.ownerUserId);
    return owner ? displayName(owner) : space.name;
  };
  const {
    spaces,
    spaceID,
    setSpaceID,
    entries,
    volume,
    trail,
    setTrail,
    parentID,
    loading,
    error,
    setError,
    loadSpaces,
    reload,
    refreshVolume,
  } = useFileListing();
  const [query, setQuery] = useState("");
  const [view, setView] = useState<FileView>("list");
  const [viewMenuOpen, setViewMenuOpen] = useState(false);
  const [selectedItemID, setSelectedItemID] = useState("");
  const [selectedItemIDs, setSelectedItemIDs] = useState<string[]>([]);
  const [collapsedSpaceIDs, setCollapsedSpaceIDs] = useState<string[]>([]);
  const [clipboard, setClipboard] = useState<FileClipboard>();
  const [activity, setActivity] = useState("");
  const [creatingDirectory, setCreatingDirectory] = useState(false);
  const [renaming, setRenaming] = useState<FileEntry>();
  const [pendingDelete, setPendingDelete] = useState<FileEntry[]>();
  // The file whose snapshot versions 历史版本 shows.
  const [versionsOf, setVersionsOf] = useState<FileEntry>();
  const [failedPreviewID, setFailedPreviewID] = useState("");
  // The file the viewer shows; images and PDFs open there instead of downloading.
  const [viewingID, setViewingID] = useState("");
  const [contextMenu, setContextMenu] = useState<FileContextMenuState>();
  const [uploadMenuOpen, setUploadMenuOpen] = useState(false);
  const [dragging, setDragging] = useState(false);
  const managerRef = useRef<HTMLDivElement>(null);
  const dragDepth = useRef(0);
  const panes = usePaneWidths(managerRef);
  const { order, save: saveOrder, storageError, scope: orderScope } = useFileOrder(userID, spaceID, parentID);

  // A transfer's location opens here.
  const [navigatedTo, setNavigatedTo] = useState<TransferLocation>();
  if (navigatedTo !== navigation) {
    setNavigatedTo(navigation);
    if (navigation) {
      setSpaceID(navigation.spaceId);
      setTrail(navigation.folders.map((folder) => ({ id: folder.id, name: folder.name })));
      setQuery("");
      setSelectedItemID("");
      setSelectedItemIDs([]);
      setContextMenu(undefined);
    }
  }
  const navigated = useEffectEvent(onNavigated);
  useEffect(() => {
    if (navigation) navigated();
  }, [navigation]);
  // Another item gets another try at its preview.
  const [previewedID, setPreviewedID] = useState(selectedItemID);
  if (previewedID !== selectedItemID) {
    setPreviewedID(selectedItemID);
    setFailedPreviewID("");
  }
  useEffect(() => {
    if (!pendingDelete && !renaming && !viewMenuOpen && !contextMenu && !uploadMenuOpen) return;
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || activity) return;
      setPendingDelete(undefined);
      setRenaming(undefined);
      setViewMenuOpen(false);
      setContextMenu(undefined);
      setUploadMenuOpen(false);
    };
    window.addEventListener("keydown", closeOnEscape);
    return () => window.removeEventListener("keydown", closeOnEscape);
  }, [pendingDelete, renaming, viewMenuOpen, contextMenu, uploadMenuOpen, activity]);
  useEffect(() => {
    if (!contextMenu) return;
    const close = () => setContextMenu(undefined);
    window.addEventListener("pointerdown", close);
    window.addEventListener("blur", close);
    window.addEventListener("resize", close);
    return () => {
      window.removeEventListener("pointerdown", close);
      window.removeEventListener("blur", close);
      window.removeEventListener("resize", close);
    };
  }, [contextMenu]);

  const selectedSpace = spaces.find((space) => space.id === spaceID);
  const viewing = selectedSpace?.viewing;
  const showResources = selectedSpace?.kind === "private" && !viewing && trail.length === 0;
  const browserItems: FileBrowserItem[] = [
    ...(showResources ? personalFileResources : []),
    ...entries.map(browserItem),
  ];
  const normalizedQuery = query.trim().toLocaleLowerCase("zh-CN");
  const orderedItems = sortFiles(browserItems, order);
  const visibleItems = orderedItems.filter((item) =>
    `${item.name} ${item.type}`.toLocaleLowerCase("zh-CN").includes(normalizedQuery),
  );
  const selectedItems = browserItems.filter((item) => selectedItemIDs.includes(item.id));
  const selectedItem = browserItems.find((item) => item.id === selectedItemID) ?? selectedItems.at(-1);
  const selectedEntry = selectedItem?.entry;
  const viewable = visibleItems.flatMap((item) =>
    item.entry?.kind === "file" && isViewable(item.name) ? [item.entry] : [],
  );
  const viewingIndex = viewable.findIndex((entry) => entry.id === viewingID);
  const selectedSpaceLabel = selectedSpace ? spaceLabel(selectedSpace, ownerName(selectedSpace)) : "个人空间";
  const can = selectionCommands({
    items: selectedItems,
    item: selectedItem,
    order,
    clipboard,
    spaceID,
    parentID,
    viewing: Boolean(viewing),
    loading,
    busy: Boolean(activity),
  });
  const canCreate = Boolean(spaceID) && !viewing && !activity;
  const canReorder = order.sortBy === "manual" && !normalizedQuery && !loading && !activity;
  const reorderPeers = selectedItem
    ? orderedItems.filter((item) => isPinned(item, order) === isPinned(selectedItem, order))
    : [];
  const selectedPosition = reorderPeers.findIndex((item) => item.id === selectedItemID);
  const canMove = (delta: number) =>
    canReorder &&
    selectedItems.length === 1 &&
    selectedPosition >= 0 &&
    Boolean(reorderPeers[selectedPosition + delta]);
  const moveSelection = (delta: number) => {
    if (canMove(delta))
      saveOrder(reorderFiles(browserItems, order, selectedItemID, reorderPeers[selectedPosition + delta].id));
  };
  const changeSort = (sort: FileSort) => saveOrder(sortedBy(order, sort, orderedItems));
  const togglePinned = () => {
    if (can.pin) saveOrder(withPins(order, can.folders));
  };
  const dragProps = useDragReorder({
    items: browserItems,
    order,
    scope: orderScope,
    enabled: canReorder,
    save: saveOrder,
  });
  const textPreview = useTextPreview(selectedItem, selectedItems.length);

  const chooseSpace = (nextSpaceID: string) => {
    setSpaceID(nextSpaceID);
    setTrail([]);
    setQuery("");
    setSelectedItemID("");
    setSelectedItemIDs([]);
    setCreatingDirectory(false);
    setPendingDelete(undefined);
    setRenaming(undefined);
    setContextMenu(undefined);
    setCollapsedSpaceIDs((current) => current.filter((id) => id !== nextSpaceID));
  };
  const toggleSpace = (space: Space) => {
    if (space.id !== spaceID) {
      chooseSpace(space.id);
      return;
    }
    setCollapsedSpaceIDs((current) =>
      current.includes(space.id) ? current.filter((id) => id !== space.id) : [...current, space.id],
    );
  };
  const navigateTo = (index: number) => {
    setTrail((value) => value.slice(0, index));
    setQuery("");
    setSelectedItemID("");
    setSelectedItemIDs([]);
    setCreatingDirectory(false);
    setContextMenu(undefined);
  };
  const openDirectory = (entry: FileEntry) => {
    setTrail((value) => [...value, { id: entry.id, name: entry.name }]);
    setQuery("");
    setSelectedItemID("");
    setSelectedItemIDs([]);
    setCreatingDirectory(false);
    setContextMenu(undefined);
  };
  const refreshAll = () => {
    reload();
    void refreshVolume();
  };
  // Downloads go through the transfer center, which follows their progress;
  // a folder or several items arrive as one ZIP archive.
  const download = (targets: FileEntry[]) => {
    if (!targets.length) return;
    void transferStore
      .download(targets.map((entry) => entry.id))
      .then(() => setError(""))
      .catch((caught) => setError(messageOf(caught)));
  };
  const showViewer = (entry: FileEntry) => {
    setViewingID(entry.id);
    setSelectedItemIDs([entry.id]);
    setSelectedItemID(entry.id);
  };
  const openResource = (resource: FileResource) => {
    if (resource === "photos") onOpenPhotos();
    else if (resource === "movies") onOpenMedia();
    else onOpenMusic();
  };
  const openItem = (item: FileBrowserItem) => {
    if (item.entry?.kind === "directory") openDirectory(item.entry);
    else if (item.entry?.kind === "file" && isViewable(item.name)) showViewer(item.entry);
    else if (item.entry?.kind === "file") download([item.entry]);
    else if (item.resource) openResource(item.resource);
  };
  const selectItem = (item: FileBrowserItem, event?: Pick<ReactMouseEvent, "ctrlKey" | "metaKey" | "shiftKey">) => {
    if (event?.shiftKey && selectedItemID) {
      const anchor = visibleItems.findIndex((candidate) => candidate.id === selectedItemID);
      const target = visibleItems.findIndex((candidate) => candidate.id === item.id);
      if (anchor >= 0 && target >= 0) {
        const range = visibleItems
          .slice(Math.min(anchor, target), Math.max(anchor, target) + 1)
          .map((candidate) => candidate.id);
        setSelectedItemIDs(range);
        setSelectedItemID(item.id);
        return;
      }
    }
    if (event?.ctrlKey || event?.metaKey) {
      setSelectedItemIDs((current) =>
        current.includes(item.id) ? current.filter((id) => id !== item.id) : [...current, item.id],
      );
      setSelectedItemID(item.id);
      return;
    }
    setSelectedItemIDs([item.id]);
    setSelectedItemID(item.id);
  };
  const selectBox = (ids: string[]) => {
    setContextMenu(undefined);
    setSelectedItemIDs(ids);
    setSelectedItemID(ids.at(-1) ?? "");
  };
  const showContextMenu = (event: ReactMouseEvent, item?: FileBrowserItem) => {
    event.preventDefault();
    event.stopPropagation();
    if (item && !selectedItemIDs.includes(item.id)) selectItem(item);
    const bounds = managerRef.current?.getBoundingClientRect();
    if (!bounds) return;
    const width = 178;
    const height = item ? 320 : 170;
    const edge = 8;
    setContextMenu({
      x: Math.max(edge, Math.min(event.clientX - bounds.left, bounds.width - width - edge)),
      y: Math.max(edge, Math.min(event.clientY - bounds.top, bounds.height - height - edge)),
      item,
    });
  };
  const stopViewing = async () => {
    if (!viewing) return;
    setActivity("正在结束只读查看…");
    try {
      await endViewing(viewing.grantId);
      setTrail([]);
      await loadSpaces();
      setError("");
    } catch (caught) {
      setError(messageOf(caught));
    } finally {
      setActivity("");
    }
  };
  const makeDirectory = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const form = event.currentTarget;
    const name = String(new FormData(form).get("name"));
    const problem = nameProblem(name);
    if (problem) {
      setError(problem);
      return;
    }
    setActivity(`正在创建“${name}”…`);
    try {
      await createDirectory(spaceID, parentID, name);
      form.reset();
      setCreatingDirectory(false);
      reload();
    } catch (caught) {
      setError(messageOf(caught));
    } finally {
      setActivity("");
    }
  };
  // Uploads run in the transfer center, so the folder stays usable and a
  // failed file does not stop the others.
  const upload = (picked: PickedFile[]) => {
    if (!picked.length || !selectedSpace || viewing) return;
    transferStore.enqueue(picked, {
      spaceId: selectedSpace.id,
      spaceKind: selectedSpace.kind,
      parentId: parentID,
      folders: trail,
    });
  };
  const dragEvents = {
    onDragEnter: (event: ReactDragEvent) => {
      if (!carriesFiles(event.dataTransfer)) return;
      event.preventDefault();
      dragDepth.current += 1;
      setDragging(true);
    },
    onDragOver: (event: ReactDragEvent) => {
      if (!carriesFiles(event.dataTransfer)) return;
      event.preventDefault();
      event.dataTransfer.dropEffect = viewing || !spaceID ? "none" : "copy";
    },
    onDragLeave: (event: ReactDragEvent) => {
      if (!carriesFiles(event.dataTransfer)) return;
      dragDepth.current = Math.max(0, dragDepth.current - 1);
      if (!dragDepth.current) setDragging(false);
    },
    onDrop: (event: ReactDragEvent) => {
      if (!carriesFiles(event.dataTransfer)) return;
      event.preventDefault();
      dragDepth.current = 0;
      setDragging(false);
      if (viewing || !spaceID) return;
      void pickedFromDrop(event.dataTransfer)
        .then(upload)
        .catch(() => setError("无法读取拖入的文件，请改用上传按钮选择"));
    },
  };
  const pasteEntry = async () => {
    if (!clipboard || !can.paste) return;
    setActivity(`${clipboard.mode === "copy" ? "正在复制" : "正在移动"} ${clipboard.entries.length} 个项目…`);
    try {
      for (const source of clipboard.entries) {
        const here = source.spaceId === spaceID && (source.parentId ?? "") === parentID;
        const name = clipboard.mode === "copy" && here ? duplicateFileName(source.name) : source.name;
        if (clipboard.mode === "copy") await copyFile(source.id, parentID, name, spaceID);
        else if (!here) await moveFile(source.id, parentID, name, spaceID);
      }
      if (clipboard.mode === "move") setClipboard(undefined);
      setSelectedItemID("");
      setSelectedItemIDs([]);
      reload();
      await refreshVolume();
    } catch (caught) {
      setError(messageOf(caught));
    } finally {
      setActivity("");
    }
  };
  const renameEntry = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!renaming) return;
    const name = String(new FormData(event.currentTarget).get("name"));
    const problem = name === renaming.name ? undefined : nameProblem(name);
    if (problem) {
      setError(problem);
      return;
    }
    setActivity(`正在重命名“${renaming.name}”…`);
    try {
      await moveFile(renaming.id, renaming.parentId ?? "", name);
      setRenaming(undefined);
      setSelectedItemID("");
      setSelectedItemIDs([]);
      reload();
    } catch (caught) {
      setError(messageOf(caught));
    } finally {
      setActivity("");
    }
  };
  const removeEntry = async () => {
    if (!pendingDelete?.length) return;
    setActivity(`正在移动 ${pendingDelete.length} 个项目…`);
    try {
      for (const entry of pendingDelete) await deleteFile(entry.id);
      setPendingDelete(undefined);
      setSelectedItemID("");
      setSelectedItemIDs([]);
      reload();
      await refreshVolume();
    } catch (caught) {
      setError(messageOf(caught));
    } finally {
      setActivity("");
    }
  };
  const commands: FileCommands = {
    share: () => shares.compose(can.entries),
    cut: () => setClipboard({ entries: can.entries, mode: "move" }),
    copy: () => setClipboard({ entries: can.entries, mode: "copy" }),
    paste: () => void pasteEntry(),
    download: () => download(can.entries),
    rename: () => setRenaming(selectedEntry),
    versions: () => setVersionsOf(selectedEntry),
    pin: togglePinned,
    remove: () => setPendingDelete(can.entries),
  };

  return (
    <div className="file-manager" ref={managerRef} style={panes.style}>
      <FileSidebar
        spaces={spaces}
        spaceID={spaceID}
        collapsedSpaceIDs={collapsedSpaceIDs}
        trail={trail}
        volume={volume}
        ownerName={ownerName}
        canCreate={canCreate}
        onCreateDirectory={() => setCreatingDirectory(true)}
        onToggleSpace={toggleSpace}
        onNavigate={navigateTo}
        onOpenResource={openResource}
      />

      <div
        className="file-pane-resizer"
        role="separator"
        aria-label="调整目录栏宽度"
        aria-orientation="vertical"
        tabIndex={0}
        {...panes.resizer("left")}
      />

      <section
        className="file-browser"
        aria-label="文件浏览器"
        data-vnas-private={viewing ? othersSpace : undefined}
        {...dragEvents}
      >
        <FileNavigationBar
          spaces={spaces}
          spaceID={spaceID}
          trail={trail}
          loading={loading}
          ownerName={ownerName}
          locationLabel={selectedSpaceLabel}
          query={query}
          onQuery={setQuery}
          onNavigate={navigateTo}
          onRefresh={refreshAll}
          onChooseSpace={chooseSpace}
        />
        <FileCommandBar
          can={can}
          commands={commands}
          canCreate={canCreate}
          onToggleCreate={() => setCreatingDirectory((value) => !value)}
          order={order}
          onOrder={saveOrder}
          view={view}
          onView={setView}
          viewMenuOpen={viewMenuOpen}
          onViewMenuOpen={setViewMenuOpen}
          activity={activity}
        />
        <FileOrderBar
          sortBy={order.sortBy}
          disabled={loading || !spaceID}
          can={can}
          onSort={changeSort}
          onPin={togglePinned}
          canMove={canMove}
          onMove={moveSelection}
          searching={Boolean(normalizedQuery)}
        />
        {storageError && (
          <div className="file-order-notice" role="status">
            {storageError}
          </div>
        )}
        {error && (
          <div className="status-banner error">
            <CircleAlert />
            {error}
          </div>
        )}
        {viewing && (
          <div className="status-banner viewing-banner" role="status">
            <ShieldCheck />
            <span>正在只读查看他人个人空间；访问已审计，将于 {formatFileDate(viewing.expiresAt)} 自动结束。</span>
            <button disabled={Boolean(activity)} onClick={() => void stopViewing()}>
              结束查看
            </button>
          </div>
        )}
        {clipboard && (
          <div className="file-clipboard-state" role="status">
            {clipboard.mode === "copy" ? "已复制" : "已剪切"}
            {clipboard.entries.length === 1 ? `“${clipboard.entries[0].name}”` : ` ${clipboard.entries.length} 个项目`}
            {clipboard.mode === "move" && clipboard.entries.some((entry) => entry.spaceId !== spaceID) && (
              <span>；跨空间移动后，源内容保留在原空间回收站，清空或到期前仍占用容量。</span>
            )}
          </div>
        )}

        {creatingDirectory && !viewing && (
          <form className="file-create-form" aria-label="新建文件夹" onSubmit={(event) => void makeDirectory(event)}>
            <FolderPlus />
            <input autoFocus name="name" aria-label="新文件夹名称" placeholder="输入文件夹名称" required />
            <button disabled={Boolean(activity)}>创建</button>
            <button type="button" onClick={() => setCreatingDirectory(false)}>
              取消
            </button>
          </form>
        )}

        <FileList
          view={view}
          items={visibleItems}
          selectedIDs={selectedItemIDs}
          order={order}
          loading={loading}
          hasSpace={Boolean(spaceID)}
          query={query}
          viewing={Boolean(viewing)}
          dragProps={dragProps}
          onSort={changeSort}
          onSelect={selectItem}
          onBoxSelect={selectBox}
          onOpen={openItem}
          onContextMenu={showContextMenu}
          onClearQuery={() => setQuery("")}
        />
        {!viewing && spaceID && (
          <FileUploadControl menuOpen={uploadMenuOpen} onMenuOpen={setUploadMenuOpen} onUpload={upload} />
        )}
        {dragging && (
          <div className={`file-drop-overlay ${viewing ? "refused" : ""}`} aria-hidden="true">
            <div>
              <FolderUp />
              <strong>{viewing ? "只读查看时不能上传" : "松开即可上传"}</strong>
              <span>
                {viewing
                  ? "结束查看后再上传到自己的空间。"
                  : `上传到 ${[selectedSpaceLabel, ...trail.map((item) => item.name)].join(" › ")}，文件夹会保留结构`}
              </span>
            </div>
          </div>
        )}
        <FileStatusBar
          shown={visibleItems}
          total={browserItems.length}
          filtered={Boolean(query)}
          spaceID={spaceID}
          view={view}
          onOpenTransfers={onOpenTransfers}
        />
      </section>

      <div
        className="file-pane-resizer"
        role="separator"
        aria-label="调整详细信息栏宽度"
        aria-orientation="vertical"
        tabIndex={0}
        {...panes.resizer("right")}
      />

      <FileInspector
        item={selectedItem}
        count={selectedItems.length}
        location={trail.at(-1)?.name ?? selectedSpaceLabel}
        viewing={Boolean(viewing)}
        textPreview={textPreview}
        previewFailed={Boolean(selectedItem) && failedPreviewID === selectedItem?.id}
        onPreviewFailed={() => selectedItem && setFailedPreviewID(selectedItem.id)}
        onView={showViewer}
        onVersions={setVersionsOf}
      />

      {contextMenu && (
        <FileContextMenu
          menu={contextMenu}
          viewing={Boolean(viewing)}
          can={can}
          commands={commands}
          canCreate={!viewing && !activity}
          canRefresh={Boolean(spaceID) && !loading}
          onOpen={openItem}
          onDownload={download}
          onCreateDirectory={() => setCreatingDirectory(true)}
          onRefresh={refreshAll}
          onClose={() => setContextMenu(undefined)}
        />
      )}

      {versionsOf && selectedSpace && (
        <VersionsDialog
          file={versionsOf}
          space={selectedSpace}
          folders={trail.map((item) => item.name)}
          onClose={() => setVersionsOf(undefined)}
          onChanged={refreshAll}
        />
      )}
      {viewingIndex >= 0 && (
        <FileViewer
          entries={viewable}
          index={viewingIndex}
          onIndex={(index) => showViewer(viewable[index])}
          onClose={() => setViewingID("")}
          onDownload={(entry) => download([entry])}
          vnasPrivate={viewing ? othersSpace : undefined}
        />
      )}
      {pendingDelete && (
        <DeleteDialog
          entries={pendingDelete}
          busy={Boolean(activity)}
          onCancel={() => setPendingDelete(undefined)}
          onConfirm={() => void removeEntry()}
        />
      )}
      {renaming && (
        <RenameDialog
          entry={renaming}
          busy={Boolean(activity)}
          onCancel={() => setRenaming(undefined)}
          onSubmit={(event) => void renameEntry(event)}
        />
      )}
    </div>
  );
}
