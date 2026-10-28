import { ChevronDown, FolderClosed, FolderOpen, FolderPlus, ShieldCheck, UsersRound } from "lucide-react";

import type { Space, Volume } from "../api.generated";
import { formatBytes } from "../lib/bytes";
import { FileIcon } from "./FileIcon";
import styles from "./files.module.css";
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
    <aside className={styles.fileSidebar} aria-label="目录树">
      <div className={styles.fileSidebarHeading}>
        <strong>目录</strong>
        <button aria-label="新建文件夹" disabled={!canCreate} onClick={onCreateDirectory}>
          <FolderPlus />
        </button>
      </div>
      <p className={styles.fileSidebarLabel}>空间</p>
      <nav className={styles.fileTree}>
        {spaces.map((space) => {
          const active = space.id === spaceID;
          const collapsed = collapsedSpaceIDs.includes(space.id);
          return (
            <div className={styles.fileTreeGroup} key={space.id}>
              <button
                className={`${styles.fileTreeRoot} ${active ? styles.active : ""}`}
                aria-expanded={active && !collapsed}
                onClick={() => onToggleSpace(space)}
              >
                <ChevronDown className={!active || collapsed ? styles.collapsed : ""} />
                <span className={`${styles.fileTreeIcon} ${styles[space.kind] ?? ""}`}>
                  {space.kind === "shared" ? <UsersRound /> : space.viewing ? <ShieldCheck /> : <FolderOpen />}
                </span>
                <strong>{spaceLabel(space, ownerName(space))}</strong>
                <small>{space.kind === "shared" ? "共享" : space.name}</small>
              </button>
              {active && !collapsed && (
                <div className={styles.fileTreeBranch} data-vnas-private={space.viewing ? othersSpace : undefined}>
                  {space.kind === "private" &&
                    !space.viewing &&
                    personalFileResources.map((resource) => (
                      <button
                        key={resource.id}
                        className={`${styles.fileTreeChild} ${styles.resource}`}
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
                    className={`${styles.fileTreeChild} ${trail.length === 0 ? styles.current : ""}`}
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
                      className={`${styles.fileTreeChild} ${styles.path} ${index === trail.length - 1 ? styles.current : ""}`}
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
      <div className={styles.fileVolumeCard}>
        <header>
          <strong>数据卷</strong>
          <span className={`${styles.fileVolumeState} ${styles[volume?.state ?? "unavailable"] ?? ""}`}>
            {volume?.state === "available" ? "在线" : "不可用"}
          </span>
        </header>
        <div
          className={`${styles.fileVolumeTrack} ${usage.known ? "" : styles.unknown}`}
          role="progressbar"
          aria-label={usage.known ? `数据卷已使用 ${usage.usedPercent}%` : "数据卷使用情况暂不可用"}
          aria-valuenow={usage.known ? usage.usedPercent : undefined}
          aria-valuemin={0}
          aria-valuemax={100}
        >
          <span style={{ width: `${usage.usedPercent}%` }} />
        </div>
        <div className={styles.fileVolumeValues}>
          <span>已用空间</span>
          <span>总空间</span>
          <strong>{usage.known ? formatBytes(usage.usedBytes) : "—"}</strong>
          <strong>{usage.capacityBytes ? formatBytes(usage.capacityBytes) : "—"}</strong>
        </div>
      </div>
    </aside>
  );
}
