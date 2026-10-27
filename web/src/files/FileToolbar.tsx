import {
  ArrowDown,
  ArrowLeft,
  ArrowRight,
  ArrowUp,
  ArrowUpDown,
  ChevronDown,
  ChevronRight,
  ClipboardPaste,
  Copy,
  Download,
  Ellipsis,
  FolderOpen,
  FolderPlus,
  Grid2X2,
  List,
  Pencil,
  Pin,
  RefreshCw,
  Scissors,
  Search,
  Share2,
  Trash2,
} from "lucide-react";

import type { Space } from "../api.generated";
import { FileFolder, FileView, fileSortLabel, SelectionCommands } from "./model";
import { FileOrder, FileSort } from "./useFileOrder";

// FileCommands are what the toolbar and the context menu do with the selection.
export interface FileCommands {
  share: () => void;
  cut: () => void;
  copy: () => void;
  paste: () => void;
  download: () => void;
  rename: () => void;
  versions: () => void;
  pin: () => void;
  remove: () => void;
}

// FileNavigationBar moves between folders and spaces and searches the open folder.
export function FileNavigationBar({
  spaces,
  spaceID,
  trail,
  loading,
  ownerName,
  locationLabel,
  query,
  onQuery,
  onNavigate,
  onRefresh,
  onChooseSpace,
}: {
  spaces: Space[];
  spaceID: string;
  trail: FileFolder[];
  loading: boolean;
  ownerName: (space: Space) => string;
  // The open space's name.
  locationLabel: string;
  query: string;
  onQuery: (query: string) => void;
  onNavigate: (depth: number) => void;
  onRefresh: () => void;
  onChooseSpace: (spaceID: string) => void;
}) {
  return (
    <div className="file-navigation-bar">
      <div className="file-history-actions">
        <button
          className="file-icon-button"
          aria-label="后退"
          title="后退"
          disabled={!trail.length || loading}
          onClick={() => onNavigate(trail.length - 1)}
        >
          <ArrowLeft />
        </button>
        <button className="file-icon-button" aria-label="前进" title="前进" disabled>
          <ArrowRight />
        </button>
        <button
          className="file-icon-button"
          aria-label="向上一级"
          title="向上一级"
          disabled={!trail.length || loading}
          onClick={() => onNavigate(trail.length - 1)}
        >
          <ArrowUp />
        </button>
        <button
          className="file-icon-button"
          aria-label="刷新文件列表"
          title="刷新"
          disabled={!spaceID || loading}
          onClick={onRefresh}
        >
          <RefreshCw className={loading ? "spinning" : ""} />
        </button>
      </div>
      <select
        className="file-space-select"
        aria-label="空间"
        value={spaceID}
        onChange={(event) => onChooseSpace(event.target.value)}
      >
        {spaces.map((space) => (
          <option key={space.id} value={space.id}>
            {space.kind === "shared"
              ? "共享空间"
              : space.viewing
                ? `只读查看 · ${ownerName(space)}`
                : `个人空间 · ${space.name}`}
          </option>
        ))}
      </select>
      <nav className="file-breadcrumb" aria-label="当前路径">
        <FolderOpen />
        <button onClick={() => onNavigate(0)}>{locationLabel || "空间"}</button>
        {trail.map((item, index) => (
          <span key={item.id}>
            <ChevronRight />
            <button
              aria-current={index === trail.length - 1 ? "page" : undefined}
              onClick={() => onNavigate(index + 1)}
            >
              {item.name}
            </button>
          </span>
        ))}
      </nav>
      <label className="file-search">
        <Search />
        <input
          type="search"
          aria-label="搜索当前目录"
          placeholder={`在${trail.at(-1)?.name ?? locationLabel ?? "当前目录"}中搜索`}
          value={query}
          onChange={(event) => onQuery(event.target.value)}
        />
      </label>
    </div>
  );
}

// FileCommandBar holds the commands for the selection, the sort direction and the view.
export function FileCommandBar({
  can,
  commands,
  canCreate,
  onToggleCreate,
  order,
  onOrder,
  view,
  onView,
  viewMenuOpen,
  onViewMenuOpen,
  activity,
}: {
  can: SelectionCommands;
  commands: FileCommands;
  canCreate: boolean;
  onToggleCreate: () => void;
  order: FileOrder;
  onOrder: (order: FileOrder) => void;
  view: FileView;
  onView: (view: FileView) => void;
  viewMenuOpen: boolean;
  onViewMenuOpen: (open: boolean) => void;
  // What is running, if anything.
  activity: string;
}) {
  const { sortBy, direction } = order;
  return (
    <div className="file-action-bar" aria-label="文件命令">
      <button className="file-command-button" disabled={!canCreate} onClick={onToggleCreate}>
        <FolderPlus />
        <span>新建</span>
      </button>
      <i />
      <button
        className="file-command-button"
        aria-label="分享"
        title="分享给成员"
        disabled={!can.mutate}
        onClick={commands.share}
      >
        <Share2 />
        <span>分享</span>
      </button>
      <button
        className="file-command-button"
        aria-label="剪切"
        title="剪切"
        disabled={!can.mutate}
        onClick={commands.cut}
      >
        <Scissors />
        <span>剪切</span>
      </button>
      <button
        className="file-command-button"
        aria-label="复制"
        title="复制"
        disabled={!can.mutate}
        onClick={commands.copy}
      >
        <Copy />
        <span>复制</span>
      </button>
      <button
        className="file-command-button"
        aria-label="粘贴"
        title="粘贴到当前目录"
        disabled={!can.paste}
        onClick={commands.paste}
      >
        <ClipboardPaste />
        <span>粘贴</span>
      </button>
      <button
        className="file-command-button"
        aria-label={can.archive ? "打包下载" : "下载"}
        title={can.archive ? "打包为 ZIP 下载" : "下载"}
        disabled={!can.download}
        onClick={commands.download}
      >
        <Download />
        <span>下载</span>
      </button>
      <button
        className="file-command-button"
        aria-label="重命名"
        title="重命名"
        disabled={!can.rename}
        onClick={commands.rename}
      >
        <Pencil />
        <span>重命名</span>
      </button>
      <button className="file-command-button file-command-optional" aria-label="共享" title="共享功能规划中" disabled>
        <Share2 />
        <span>共享</span>
      </button>
      <button
        className="file-command-button danger"
        aria-label="删除"
        title="移到回收站"
        disabled={!can.mutate}
        onClick={commands.remove}
      >
        <Trash2 />
        <span>删除</span>
      </button>
      <i />
      <button
        className="file-command-button"
        aria-label="切换排序方向"
        title={`按${fileSortLabel(sortBy)}${direction === "ascending" ? "升序" : "降序"}`}
        disabled={sortBy === "manual"}
        onClick={() => onOrder({ ...order, direction: direction === "ascending" ? "descending" : "ascending" })}
      >
        <ArrowUpDown />
        <span>排序</span>
      </button>
      <div className="file-view-control">
        <button
          className="file-command-button"
          aria-expanded={viewMenuOpen}
          onClick={() => onViewMenuOpen(!viewMenuOpen)}
        >
          {view === "list" ? <List /> : <Grid2X2 />}
          <span>查看</span>
          <ChevronDown />
        </button>
        {viewMenuOpen && (
          <div className="file-view-menu">
            <button
              className={view === "list" ? "active" : ""}
              onClick={() => {
                onView("list");
                onViewMenuOpen(false);
              }}
            >
              <List />
              列表视图
            </button>
            <button
              className={view === "icons" ? "active" : ""}
              onClick={() => {
                onView("icons");
                onViewMenuOpen(false);
              }}
            >
              <Grid2X2 />
              图标视图
            </button>
          </div>
        )}
      </div>
      <button className="file-command-button file-command-optional" aria-label="更多操作" title="更多操作" disabled>
        <Ellipsis />
      </button>
      {activity && (
        <span className="file-activity" role="status">
          <span className="loader" />
          {activity}
        </span>
      )}
    </div>
  );
}

// FileOrderBar sorts the folder, pins folders first and, in manual order,
// moves the selected item up or down.
export function FileOrderBar({
  sortBy,
  disabled,
  can,
  onSort,
  onPin,
  canMove,
  onMove,
  searching,
}: {
  sortBy: FileSort;
  disabled: boolean;
  can: SelectionCommands;
  onSort: (sort: FileSort) => void;
  onPin: () => void;
  canMove: (delta: number) => boolean;
  onMove: (delta: number) => void;
  // Manual order cannot change while a search filters the folder.
  searching: boolean;
}) {
  return (
    <div className="file-order-bar" aria-label="文件排列">
      <label>
        排序方式
        <select
          aria-label="排序方式"
          value={sortBy}
          disabled={disabled}
          onChange={(event) => onSort(event.target.value as FileSort)}
        >
          <option value="name">名称</option>
          <option value="modifiedAt">修改时间</option>
          <option value="type">类型</option>
          <option value="sizeBytes">大小</option>
          <option value="manual">手动排序</option>
        </select>
      </label>
      <button disabled={!can.pin} onClick={onPin}>
        <Pin />
        {can.allPinned ? "取消置顶" : "置顶文件夹"}
      </button>
      {sortBy === "manual" && (
        <>
          <button aria-label="上移所选项目" disabled={!canMove(-1)} onClick={() => onMove(-1)}>
            <ArrowUp />
            上移
          </button>
          <button aria-label="下移所选项目" disabled={!canMove(1)} onClick={() => onMove(1)}>
            <ArrowDown />
            下移
          </button>
          <span>{searching ? "清除搜索后可调整顺序" : "拖动项目或选择后上移、下移；置顶项目保持在前"}</span>
        </>
      )}
    </div>
  );
}
