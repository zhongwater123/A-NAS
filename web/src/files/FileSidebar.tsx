import { ChevronDown, FolderClosed, FolderOpen, FolderPlus, ShieldCheck, UsersRound } from "lucide-react";

import type { Space, Volume } from "../api.generated";
import { formatBytes } from "../lib/bytes";
import { FileIcon } from "./FileIcon";
import { FileFolder, FileResource, othersSpace, personalFileResources, spaceLabel, volumeUsage } from "./model";

// FileSidebar is the folder tree: each space, the open one unfolded down to
// the open folder, and the data volume's usage below.
export function FileSidebar({
  spaces,
  spaceID,
  collapsedSpaceIDs,
  trail,
  volume,
  ownerName,
  canCreate,
  onCreateDirectory,
  onToggleSpace,
  onNavigate,
  onOpenResource,
}: {
  spaces: Space[];
  spaceID: string;
  collapsedSpaceIDs: string[];
  trail: FileFolder[];
  volume?: Volume;
  ownerName: (space: Space) => string;
  canCreate: boolean;
  onCreateDirectory: () => void;
  onToggleSpace: (space: Space) => void;
  // onNavigate opens the folder at that depth of the trail; 0 is the space's root.
  onNavigate: (depth: number) => void;
  onOpenResource: (resource: FileResource) => void;
}) {
  const usage = volumeUsage(volume);
  return (
    <aside className="file-sidebar" aria-label="目录树">
      <div className="file-sidebar-heading">
        <strong>目录</strong>
        <button aria-label="新建文件夹" disabled={!canCreate} onClick={onCreateDirectory}>
          <FolderPlus />
        </button>
      </div>
      <p className="file-sidebar-label">空间</p>
      <nav className="file-tree">
        {spaces.map((space) => {
          const active = space.id === spaceID;
          const collapsed = collapsedSpaceIDs.includes(space.id);
          return (
            <div className="file-tree-group" key={space.id}>
              <button
                className={`file-tree-root ${active ? "active" : ""}`}
                aria-expanded={active && !collapsed}
                onClick={() => onToggleSpace(space)}
              >
                <ChevronDown className={!active || collapsed ? "collapsed" : ""} />
                <span className={`file-tree-icon ${space.kind}`}>
                  {space.kind === "shared" ? <UsersRound /> : space.viewing ? <ShieldCheck /> : <FolderOpen />}
                </span>
                <strong>{spaceLabel(space, ownerName(space))}</strong>
                <small>{space.kind === "shared" ? "共享" : space.name}</small>
              </button>
              {active && !collapsed && (
                <div className="file-tree-branch" data-vnas-private={space.viewing ? othersSpace : undefined}>
                  {space.kind === "private" &&
                    !space.viewing &&
                    personalFileResources.map((resource) => (
                      <button
                        key={resource.id}
                        className="file-tree-child resource"
                        disabled={resource.planned}
                        onClick={() => resource.resource && onOpenResource(resource.resource)}
                      >
                        <span>
                          <FileIcon item={resource} />
                        </span>
                        <strong>{resource.name}</strong>
                        <small>{resource.planned ? "规划中" : "打开"}</small>
                      </button>
                    ))}
                  <button
                    className={`file-tree-child ${trail.length === 0 ? "current" : ""}`}
                    onClick={() => onNavigate(0)}
                  >
                    <span>
                      <FolderClosed />
                    </span>
                    <strong>根目录</strong>
                  </button>
                  {trail.map((item, index) => (
                    <button
                      key={item.id}
                      className={`file-tree-child path ${index === trail.length - 1 ? "current" : ""}`}
                      onClick={() => onNavigate(index + 1)}
                    >
                      <span>
                        <FolderClosed />
                      </span>
                      <strong>{item.name}</strong>
                    </button>
                  ))}
                </div>
              )}
            </div>
          );
        })}
      </nav>
      <div className="file-volume-card">
        <header>
          <strong>数据卷</strong>
          <span className={`file-volume-state ${volume?.state ?? "unavailable"}`}>
            {volume?.state === "available" ? "在线" : "不可用"}
          </span>
        </header>
        <div
          className={`file-volume-track ${usage.known ? "" : "unknown"}`}
          role="progressbar"
          aria-label={usage.known ? `数据卷已使用 ${usage.usedPercent}%` : "数据卷使用情况暂不可用"}
          aria-valuenow={usage.known ? usage.usedPercent : undefined}
          aria-valuemin={0}
          aria-valuemax={100}
        >
          <span style={{ width: `${usage.usedPercent}%` }} />
        </div>
        <div className="file-volume-values">
          <span>已用空间</span>
          <span>总空间</span>
          <strong>{usage.known ? formatBytes(usage.usedBytes) : "—"}</strong>
          <strong>{usage.capacityBytes ? formatBytes(usage.capacityBytes) : "—"}</strong>
        </div>
      </div>
    </aside>
  );
}
