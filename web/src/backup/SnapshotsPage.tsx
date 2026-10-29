import {
  ArchiveRestore,
  ArrowLeft,
  CalendarClock,
  Camera,
  ChevronRight,
  Download,
  FolderOpen,
  Pin,
  RefreshCw,
  Trash2,
} from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";

import { formatBytes } from "../lib/bytes";
import { Dialog, EmptyState, EntryIcon, Notice, Spinner } from "../ui/workspace";
import workspaceStyles from "../ui/workspace.module.css";
import {
  deleteSnapshot,
  keepSnapshot,
  listSnapshotEntries,
  messageOf,
  type Snapshot,
  type SnapshotChange,
  type SnapshotEntry,
  snapshotDownloadURL,
} from "./api";
import { useBackup } from "./BackupCenter";
import styles from "./backup.module.css";
import { formatClock, formatCount, formatDateTime, formatDay, restoredSnapshotName, spaceLabel } from "./format";
import { OriginBadge, SpaceSwitcher } from "./parts";

interface Crumb {
  id: string;
  name: string;
}

const twoPaneWidth = 700;

const changeBadge: Record<SnapshotChange, { label: string; title: string }> = {
  removed: { label: "已不在空间中", title: "现在的空间里没有它：已删除、移走或改了名" },
  modified: { label: "已修改", title: "现在空间里的这个文件与快照中的不同" },
  inside: { label: "内有变化", title: "文件夹里有文件已删除或修改" },
};

// changeSince words how a snapshot's totals differ from the one before it,
// so a long run of alike scheduled snapshots shows where something happened.
// Equal totals say nothing: files can change without changing them.
function changeSince(snapshot: Snapshot, before?: Snapshot): { text: string; tone: "more" | "less" } | undefined {
  if (!before) return undefined;
  const files = snapshot.fileCount - before.fileCount;
  const bytes = snapshot.sizeBytes - before.sizeBytes;
  if (files)
    return {
      text: `${files > 0 ? "+" : "−"}${formatCount(Math.abs(files))} 个文件`,
      tone: files > 0 ? "more" : "less",
    };
  if (bytes)
    return { text: `${bytes > 0 ? "+" : "−"}${formatBytes(Math.abs(bytes))}`, tone: bytes > 0 ? "more" : "less" };
  return undefined;
}

// snapshotTitle drops the timestamp a scheduled snapshot's name repeats.
export const snapshotTitle = (snapshot: Pick<Snapshot, "name" | "origin">) =>
  snapshot.origin === "scheduled" && snapshot.name.startsWith("定时快照 ") ? "定时快照" : snapshot.name;

export function SnapshotsPage() {
  const { spaces, data, spaceId, setSpaceId, focus, consumeFocus, canManage, createSnapshot, navigate, refresh } =
    useBackup();
  const space = spaces.find((item) => item.id === spaceId) ?? spaces[0];
  const spaceData = space ? data[space.id] : undefined;
  const snapshots = spaceData?.snapshots;
  const [selectedId, setSelectedId] = useState<string>();
  const [showDetail, setShowDetail] = useState(false);
  const [width, setWidth] = useState(1000);
  const root = useRef<HTMLDivElement>(null);
  const twoPane = width >= twoPaneWidth;

  useEffect(() => {
    const element = root.current;
    if (!element || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(([entry]) => setWidth(entry.contentRect.width));
    observer.observe(element);
    return () => observer.disconnect();
  }, []);

  // Keep the selection while snapshots refresh; follow a requested focus.
  const focused = Boolean(focus && snapshots?.some((snapshot) => snapshot.id === focus));
  const [selectedFor, setSelectedFor] = useState<{ snapshots: Snapshot[]; focus: typeof focus }>();
  if (snapshots && (selectedFor?.snapshots !== snapshots || selectedFor.focus !== focus)) {
    setSelectedFor({ snapshots, focus });
    if (focused) {
      setSelectedId(focus);
      setShowDetail(true);
    } else
      setSelectedId((current) =>
        current && snapshots.some((snapshot) => snapshot.id === current) ? current : snapshots[0]?.id,
      );
  }
  useEffect(() => {
    if (focused) consumeFocus();
  }, [focused, consumeFocus]);

  const groups = useMemo(() => {
    const result: { day: string; items: { snapshot: Snapshot; before?: Snapshot }[] }[] = [];
    (snapshots ?? []).forEach((snapshot, index, all) => {
      const day = formatDay(snapshot.createdAt);
      const item = { snapshot, before: all[index + 1] };
      if (result.at(-1)?.day === day) result.at(-1)!.items.push(item);
      else result.push({ day, items: [item] });
    });
    return result;
  }, [snapshots]);

  if (!space)
    return (
      <div className={styles.bkPage}>
        <EmptyState icon={<Camera />} title="没有可用的空间" />
      </div>
    );
  const selected = snapshots?.find((snapshot) => snapshot.id === selectedId);
  const manage = canManage(space);

  return (
    <div ref={root} className={`${styles.bkPage} ${styles.bkSnapshotsPage}`}>
      <header className={styles.bkPageHead}>
        <div>
          <h2>快照</h2>
          <p>选一个时间点，浏览{spaceLabel(space)}当时的样子。</p>
        </div>
        <div className={styles.bkHeadActions}>
          <SpaceSwitcher
            spaces={spaces}
            value={space.id}
            onChange={(id) => {
              setSpaceId(id);
              setShowDetail(false);
            }}
          />
          <button
            type="button"
            className={workspaceStyles.bkIconButton}
            aria-label="刷新快照"
            onClick={() => void refresh(space.id)}
          >
            <RefreshCw />
          </button>
          <button
            type="button"
            className={workspaceStyles.bkPrimary}
            disabled={!manage || spaceData?.offline}
            title={manage ? undefined : "共享空间的快照由管理员创建"}
            onClick={() => createSnapshot(space)}
          >
            <Camera />
            创建快照
          </button>
        </div>
      </header>

      {spaceData?.error ? (
        <Notice tone={spaceData.offline ? "warning" : "danger"}>
          {spaceData.offline ? "数据卷当前不可用，恢复后才能浏览快照。" : spaceData.error}
        </Notice>
      ) : !snapshots ? (
        <div className={`${styles.bkLoading} ${styles.inline}`}>
          <Spinner />
          正在读取快照…
        </div>
      ) : snapshots.length === 0 ? (
        <EmptyState
          icon={<Camera />}
          title={`${spaceLabel(space)}还没有快照`}
          actions={
            <>
              {manage && (
                <button type="button" className={workspaceStyles.bkPrimary} onClick={() => createSnapshot(space)}>
                  <Camera />
                  创建第一份快照
                </button>
              )}
              <button
                type="button"
                className={workspaceStyles.bkSecondary}
                onClick={() => navigate("schedules", { spaceId: space.id })}
              >
                <CalendarClock />
                设置定时快照
              </button>
            </>
          }
        >
          快照会冻结空间此刻的全部文件。之后误删或改坏文件，可以从快照里找回。
        </EmptyState>
      ) : (
        <div className={`${styles.bkSnapshots}${twoPane ? "" : ` ${styles.stacked}`}`}>
          {(twoPane || !showDetail) && (
            <div className={styles.bkTimeline} role="listbox" aria-label="快照列表">
              {groups.map((group) => (
                <div key={group.day} className={styles.bkTimelineGroup}>
                  <p className={styles.bkTimelineDay}>
                    {group.day}
                    <span>{group.items.length} 份</span>
                  </p>
                  {group.items.map(({ snapshot, before }) => {
                    const change = changeSince(snapshot, before);
                    return (
                      <button
                        key={snapshot.id}
                        type="button"
                        role="option"
                        aria-selected={snapshot.id === selected?.id}
                        className={`${styles.bkSnap}${snapshot.id === selected?.id ? ` ${styles.active} ${workspaceStyles.active}` : ""}${snapshot.origin === "manual" ? ` ${workspaceStyles.manual}` : ""}`}
                        onClick={() => {
                          setSelectedId(snapshot.id);
                          setShowDetail(true);
                        }}
                      >
                        <span className={styles.bkSnapTime}>{formatClock(snapshot.createdAt)}</span>
                        <span className={styles.bkSnapMain}>
                          <strong>{snapshotTitle(snapshot)}</strong>
                          <small>
                            {formatCount(snapshot.fileCount)} 个文件 · {formatBytes(snapshot.sizeBytes)}
                            {change && (
                              <b
                                className={`${styles.bkSnapChange} ${styles[change.tone] ?? ""}`}
                                title={`与前一份（${formatDateTime(before!.createdAt)}）相比`}
                              >
                                {change.text}
                              </b>
                            )}
                          </small>
                        </span>
                        {/* The title already says 定时快照; only a manual one needs a mark. */}
                        {snapshot.origin === "manual" && <OriginBadge origin="manual" />}
                      </button>
                    );
                  })}
                </div>
              ))}
            </div>
          )}
          {selected && (twoPane || showDetail) && (
            <SnapshotBrowser
              key={selected.id}
              snapshot={selected}
              manage={manage}
              onBack={twoPane ? undefined : () => setShowDetail(false)}
            />
          )}
        </div>
      )}
    </div>
  );
}

function SnapshotBrowser({ snapshot, manage, onBack }: { snapshot: Snapshot; manage: boolean; onBack?: () => void }) {
  const { spaces, refresh, restore, toast, restored } = useBackup();
  const space = spaces.find((item) => item.id === snapshot.spaceId)!;
  const [trail, setTrail] = useState<Crumb[]>([]);
  const [entries, setEntries] = useState<SnapshotEntry[]>();
  const [error, setError] = useState("");
  const [selected, setSelected] = useState<string>();
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [onlyChanged, setOnlyChanged] = useState(false);
  const parentId = trail.at(-1)?.id ?? "";

  // Another level starts empty; a restore changes what differs from the
  // space, so the level reloads.
  const [listedFor, setListedFor] = useState({ snapshotId: snapshot.id, parentId, restored });
  if (listedFor.snapshotId !== snapshot.id || listedFor.parentId !== parentId || listedFor.restored !== restored) {
    if (listedFor.snapshotId !== snapshot.id || listedFor.parentId !== parentId) setEntries(undefined);
    setListedFor({ snapshotId: snapshot.id, parentId, restored });
    setError("");
    setSelected(undefined);
  }
  useEffect(() => {
    let live = true;
    listSnapshotEntries(snapshot.id, parentId)
      .then((items) => {
        if (live) setEntries(items);
      })
      .catch((caught) => {
        if (live) setError(messageOf(caught));
      });
    return () => {
      live = false;
    };
  }, [snapshot.id, parentId, restored]);

  const changed = entries?.filter((entry) => entry.change) ?? [];
  const removedCount = changed.filter((entry) => entry.change === "removed").length;
  const modifiedCount = changed.length - removedCount;
  const shown = onlyChanged ? changed : entries;

  const open = (entry: SnapshotEntry) => {
    if (entry.kind === "directory") setTrail([...trail, { id: entry.id, name: entry.name }]);
  };
  const source = { id: snapshot.id, name: snapshotTitle(snapshot), createdAt: snapshot.createdAt };
  const restoreEntry = (entry: SnapshotEntry) =>
    restore({
      space,
      snapshot: source,
      entry: { id: entry.id, name: entry.name, kind: entry.kind },
      folders: trail.map((crumb) => crumb.name),
    });
  // At the top level everything comes back in a new folder; inside a folder,
  // that folder comes back to where it was.
  const restoreHere = () => {
    const folder = trail.at(-1);
    restore(
      folder
        ? {
            space,
            snapshot: source,
            entry: { id: folder.id, name: folder.name, kind: "directory" },
            folders: trail.slice(0, -1).map((crumb) => crumb.name),
          }
        : {
            space,
            snapshot: source,
            entry: {
              id: "",
              name: restoredSnapshotName(snapshotTitle(snapshot), snapshot.createdAt),
              kind: "directory",
            },
            folders: [],
            whole: true,
          },
    );
  };
  const keep = async () => {
    setBusy(true);
    try {
      await keepSnapshot(snapshot.id);
      await refresh(snapshot.spaceId);
      toast("已保留这份快照，它不会再被定时清理");
    } catch (caught) {
      toast(messageOf(caught), "error");
    } finally {
      setBusy(false);
    }
  };
  const remove = async () => {
    setBusy(true);
    try {
      await deleteSnapshot(snapshot.id);
      setConfirming(false);
      await refresh(snapshot.spaceId);
      toast(`已删除快照“${snapshotTitle(snapshot)}”`);
    } catch (caught) {
      toast(messageOf(caught), "error");
      setBusy(false);
    }
  };

  return (
    <section className={styles.bkBrowser} aria-label={`快照 ${snapshot.name}`} data-vnas-private="快照里的文件">
      <header className={styles.bkBrowserHead}>
        {onBack && (
          <button type="button" className={workspaceStyles.bkIconButton} aria-label="返回快照列表" onClick={onBack}>
            <ArrowLeft />
          </button>
        )}
        <div className={styles.bkBrowserTitle}>
          <h3>{snapshot.name}</h3>
          <p>
            {formatDateTime(snapshot.createdAt)} · <OriginBadge origin={snapshot.origin} /> ·{" "}
            {formatCount(snapshot.fileCount)} 个文件 · {formatBytes(snapshot.sizeBytes)}
          </p>
        </div>
        <div className={styles.bkBrowserActions}>
          <button
            type="button"
            className={`${workspaceStyles.bkSecondary} ${styles.small} ${workspaceStyles.small}`}
            disabled={busy}
            onClick={restoreHere}
          >
            <ArchiveRestore />
            {trail.length ? "恢复此文件夹" : "全部恢复"}
          </button>
          {manage && snapshot.origin === "scheduled" && (
            <button
              type="button"
              className={`${workspaceStyles.bkSecondary} ${styles.small} ${workspaceStyles.small}`}
              disabled={busy}
              title="转为手动快照，不会再被定时清理"
              onClick={() => void keep()}
            >
              <Pin />
              长期保留
            </button>
          )}
          {manage && (
            <button
              type="button"
              className={`${workspaceStyles.bkSecondary} ${styles.small} ${workspaceStyles.small} ${styles.danger} ${workspaceStyles.danger}`}
              disabled={busy}
              onClick={() => setConfirming(true)}
            >
              <Trash2 />
              删除
            </button>
          )}
        </div>
      </header>
      <div className={styles.bkBrowserBar}>
        <nav className={styles.bkCrumbs} aria-label="快照中的位置">
          <button type="button" disabled={!trail.length} onClick={() => setTrail([])}>
            <FolderOpen />
            {spaceLabel(space)}
          </button>
          {trail.map((crumb, index) => (
            <span key={crumb.id}>
              <ChevronRight />
              <button
                type="button"
                disabled={index === trail.length - 1}
                onClick={() => setTrail(trail.slice(0, index + 1))}
              >
                {crumb.name}
              </button>
            </span>
          ))}
        </nav>
        <div
          className={`${styles.bkSegmented} ${workspaceStyles.bkSegmented} ${styles.small} ${workspaceStyles.small}`}
          role="radiogroup"
          aria-label="显示"
        >
          <button
            type="button"
            role="radio"
            aria-checked={!onlyChanged}
            className={onlyChanged ? "" : `${styles.active} ${workspaceStyles.active}`}
            onClick={() => setOnlyChanged(false)}
          >
            全部
          </button>
          <button
            type="button"
            role="radio"
            aria-checked={onlyChanged}
            className={onlyChanged ? `${styles.active} ${workspaceStyles.active}` : ""}
            onClick={() => setOnlyChanged(true)}
          >
            与现在不同{entries ? <span className={styles.bkCount}>{changed.length}</span> : null}
          </button>
        </div>
      </div>
      {entries && changed.length > 0 && !onlyChanged && (
        <p className={styles.bkDiffSummary}>
          与现在的{spaceLabel(space)}相比，这里
          {removedCount > 0 && (
            <>
              <span className={`${styles.bkDiffDot} ${styles.removed}`} />
              {removedCount} 项已不在空间中
            </>
          )}
          {removedCount > 0 && modifiedCount > 0 && "，"}
          {modifiedCount > 0 && (
            <>
              <span className={`${styles.bkDiffDot} ${styles.modified}`} />
              {modifiedCount} 项有改动
            </>
          )}
          <button
            type="button"
            className={`${styles.bkLink} ${workspaceStyles.bkLink}`}
            onClick={() => setOnlyChanged(true)}
          >
            只看这些
          </button>
        </p>
      )}
      <div className={styles.bkEntries} role="grid" aria-label="快照中的文件">
        <div className={styles.bkEntriesHead} role="row">
          <span role="columnheader">名称</span>
          <span role="columnheader">修改时间</span>
          <span role="columnheader">大小</span>
          <span role="columnheader" className={styles.bkSr}>
            操作
          </span>
        </div>
        {error ? (
          <Notice tone="danger">{error}</Notice>
        ) : !shown ? (
          <div className={`${styles.bkLoading} ${styles.inline}`}>
            <Spinner small />
            正在读取…
          </div>
        ) : shown.length === 0 ? (
          <p className={styles.bkEntriesEmpty}>
            {onlyChanged ? "这一层与现在的空间相同" : trail.length ? "这个文件夹是空的" : "这份快照里没有文件"}
          </p>
        ) : (
          shown.map((entry) => (
            <div
              key={entry.id}
              role="row"
              aria-selected={selected === entry.id}
              tabIndex={0}
              className={`${styles.bkEntry}${selected === entry.id ? ` ${styles.selected}` : ""}`}
              onClick={() => setSelected(entry.id)}
              onDoubleClick={() => open(entry)}
              onKeyDown={(event) => {
                if (event.key === "Enter") open(entry);
              }}
            >
              <span role="gridcell" className={styles.bkEntryName}>
                <EntryIcon kind={entry.kind} className={styles.bkEntryIcon} />
                {entry.kind === "directory" ? (
                  <button
                    type="button"
                    className={styles.bkEntryLink}
                    onClick={(event) => {
                      event.stopPropagation();
                      open(entry);
                    }}
                  >
                    {entry.name}
                  </button>
                ) : (
                  <span>{entry.name}</span>
                )}
                {entry.change && (
                  <span
                    className={`${styles.bkChange} ${styles[entry.change] ?? ""}`}
                    title={changeBadge[entry.change].title}
                  >
                    {changeBadge[entry.change].label}
                  </span>
                )}
              </span>
              <span role="gridcell" className={styles.bkEntryMeta}>
                {entry.modifiedAt ? formatDateTime(entry.modifiedAt) : "—"}
              </span>
              <span role="gridcell" className={styles.bkEntryMeta}>
                {entry.kind === "file" ? formatBytes(entry.sizeBytes) : "文件夹"}
              </span>
              <span role="gridcell" className={styles.bkEntryActions}>
                {entry.kind === "file" && (
                  <a
                    className={workspaceStyles.bkIconButton}
                    href={snapshotDownloadURL(snapshot.id, entry.id)}
                    download={entry.name}
                    aria-label={`下载 ${entry.name}`}
                    title="下载"
                    onClick={(event) => event.stopPropagation()}
                  >
                    <Download />
                  </a>
                )}
                <button
                  type="button"
                  className={workspaceStyles.bkRestoreButton}
                  aria-label={`恢复 ${entry.name}`}
                  onClick={(event) => {
                    event.stopPropagation();
                    restoreEntry(entry);
                  }}
                >
                  <ArchiveRestore />
                  恢复
                </button>
              </span>
            </div>
          ))
        )}
      </div>
      {confirming && (
        <Dialog
          title="删除这份快照？"
          icon={<Trash2 />}
          tone="danger"
          busy={busy}
          onClose={() => setConfirming(false)}
          footer={
            <>
              <button
                type="button"
                className={workspaceStyles.bkSecondary}
                disabled={busy}
                onClick={() => setConfirming(false)}
              >
                取消
              </button>
              <button type="button" className={workspaceStyles.bkDanger} disabled={busy} onClick={() => void remove()}>
                {busy ? (
                  <>
                    <Spinner small />
                    正在删除…
                  </>
                ) : (
                  "删除快照"
                )}
              </button>
            </>
          }
        >
          <p className={workspaceStyles.bkDialogText}>
            “{snapshot.name}”会被永久删除，之后无法再从这份快照找回文件。{spaceLabel(space)}中现有的文件不受影响。
          </p>
        </Dialog>
      )}
    </section>
  );
}
