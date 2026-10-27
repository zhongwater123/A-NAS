import {
  ClipboardPaste,
  Copy,
  Download,
  Eye,
  FolderOpen,
  FolderPlus,
  History,
  Pencil,
  Pin,
  RefreshCw,
  Scissors,
  Share2,
  Trash2,
} from "lucide-react";

import type { FileEntry } from "../api.generated";
import { FileCommands } from "./FileToolbar";
import { FileBrowserItem, isViewable, othersSpace, SelectionCommands } from "./model";

export type FileContextMenuState = { x: number; y: number; item?: FileBrowserItem };

// FileContextMenu is the right-click menu: on an item it offers what the
// toolbar does with the selection; on empty space, a new folder, paste and
// refresh. Choosing anything closes it.
export function FileContextMenu({
  menu,
  viewing,
  can,
  commands,
  canCreate,
  canRefresh,
  onOpen,
  onDownload,
  onCreateDirectory,
  onRefresh,
  onClose,
}: {
  menu: FileContextMenuState;
  viewing: boolean;
  can: SelectionCommands;
  commands: FileCommands;
  canCreate: boolean;
  canRefresh: boolean;
  onOpen: (item: FileBrowserItem) => void;
  onDownload: (entries: FileEntry[]) => void;
  onCreateDirectory: () => void;
  onRefresh: () => void;
  onClose: () => void;
}) {
  const choose = (action: () => void) => () => {
    action();
    onClose();
  };
  const { item } = menu;
  const file = item?.entry?.kind === "file" ? item.entry : undefined;
  return (
    <div
      className="file-context-menu"
      role="menu"
      aria-label="文件操作"
      data-vnas-private={viewing ? othersSpace : undefined}
      style={{ left: menu.x, top: menu.y }}
      onPointerDown={(event) => event.stopPropagation()}
    >
      {item ? (
        <>
          <button role="menuitem" disabled={Boolean(item.planned)} onClick={choose(() => onOpen(item))}>
            {file ? isViewable(item.name) ? <Eye /> : <Download /> : <FolderOpen />}
            <span>{file && isViewable(item.name) ? "预览" : file && can.entries.length <= 1 ? "下载" : "打开"}</span>
          </button>
          {file && isViewable(item.name) && can.entries.length <= 1 && (
            <button role="menuitem" onClick={choose(() => onDownload([file]))}>
              <Download />
              <span>下载</span>
            </button>
          )}
          {can.download && can.archive && (
            <button role="menuitem" onClick={choose(commands.download)}>
              <Download />
              <span>{can.entries.length > 1 ? `打包下载 ${can.entries.length} 项` : "打包下载"}</span>
              <kbd>ZIP</kbd>
            </button>
          )}
          <i />
          <button role="menuitem" disabled={!can.mutate} onClick={choose(commands.share)}>
            <Share2 />
            <span>分享给成员</span>
          </button>
          <button role="menuitem" disabled={!can.mutate} onClick={choose(commands.cut)}>
            <Scissors />
            <span>剪切</span>
            <kbd>Ctrl+X</kbd>
          </button>
          <button role="menuitem" disabled={!can.mutate} onClick={choose(commands.copy)}>
            <Copy />
            <span>复制</span>
            <kbd>Ctrl+C</kbd>
          </button>
          <button role="menuitem" disabled={!can.rename} onClick={choose(commands.rename)}>
            <Pencil />
            <span>重命名</span>
          </button>
          {can.versions && (
            <button role="menuitem" onClick={choose(commands.versions)}>
              <History />
              <span>历史版本</span>
            </button>
          )}
          {can.folders.length > 0 && (
            <button role="menuitem" disabled={!can.pin} onClick={choose(commands.pin)}>
              <Pin />
              <span>{can.allPinned ? "取消置顶" : "置顶文件夹"}</span>
            </button>
          )}
          <i />
          <button className="danger" role="menuitem" disabled={!can.mutate} onClick={choose(commands.remove)}>
            <Trash2 />
            <span>移到回收站</span>
          </button>
        </>
      ) : (
        <>
          <button role="menuitem" disabled={!canCreate} onClick={choose(onCreateDirectory)}>
            <FolderPlus />
            <span>新建文件夹</span>
          </button>
          <button role="menuitem" disabled={!can.paste} onClick={choose(commands.paste)}>
            <ClipboardPaste />
            <span>粘贴</span>
            <kbd>Ctrl+V</kbd>
          </button>
          <i />
          <button role="menuitem" disabled={!canRefresh} onClick={choose(onRefresh)}>
            <RefreshCw />
            <span>刷新</span>
          </button>
        </>
      )}
    </div>
  );
}
