import { ArchiveRestore, ArrowLeft, CalendarClock, Camera, ChevronRight, Download, FolderOpen, Pin, RefreshCw, Trash2 } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";

import { useBackup } from "./BackupCenter";
import { deleteSnapshot, keepSnapshot, listSnapshotEntries, messageOf, snapshotDownloadURL, type Snapshot, type SnapshotEntry } from "./backupApi";
import { formatBytes, formatClock, formatCount, formatDateTime, formatDay, spaceLabel } from "./format";
import { Dialog, EmptyState, EntryIcon, Notice, OriginBadge, SpaceSwitcher, Spinner } from "./parts";

interface Crumb { id: string; name: string }

const twoPaneWidth = 700;

// snapshotTitle drops the timestamp a scheduled snapshot's name repeats.
export const snapshotTitle = (snapshot: Pick<Snapshot, "name" | "origin">) => (snapshot.origin === "scheduled" && snapshot.name.startsWith("定时快照 ") ? "定时快照" : snapshot.name);

export function SnapshotsPage() {
  const { spaces, data, spaceId, setSpaceId, focus, consumeFocus, canManage, createSnapshot, navigate, refresh } = useBackup();
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
  useEffect(() => {
    if (!snapshots) return;
    if (focus && snapshots.some((snapshot) => snapshot.id === focus)) {
      setSelectedId(focus);
      setShowDetail(true);
      consumeFocus();
      return;
    }
    setSelectedId((current) => (current && snapshots.some((snapshot) => snapshot.id === current) ? current : snapshots[0]?.id));
  }, [snapshots, focus, consumeFocus]);

  const groups = useMemo(() => {
    const result: { day: string; items: Snapshot[] }[] = [];
    for (const snapshot of snapshots ?? []) {
      const day = formatDay(snapshot.createdAt);
      if (result.at(-1)?.day === day) result.at(-1)!.items.push(snapshot);
      else result.push({ day, items: [snapshot] });
    }
    return result;
  }, [snapshots]);

  if (!space) return <div className="bk-page"><EmptyState icon={<Camera />} title="没有可用的空间" /></div>;
  const selected = snapshots?.find((snapshot) => snapshot.id === selectedId);
  const manage = canManage(space);

  return (
    <div ref={root} className="bk-page bk-snapshots-page">
      <header className="bk-page-head">
        <div><h2>快照</h2><p>{spaceLabel(space)}的只读快照，按时间排列。</p></div>
        <div className="bk-head-actions">
          <SpaceSwitcher spaces={spaces} value={space.id} onChange={(id) => { setSpaceId(id); setShowDetail(false); }} />
          <button type="button" className="bk-icon-button" aria-label="刷新快照" onClick={() => void refresh(space.id)}><RefreshCw /></button>
          <button type="button" className="bk-primary" disabled={!manage || spaceData?.offline} title={manage ? undefined : "共享空间的快照由管理员创建"} onClick={() => createSnapshot(space)}><Camera />创建快照</button>
        </div>
      </header>

      {spaceData?.error ? (
        <Notice tone={spaceData.offline ? "warning" : "danger"}>{spaceData.offline ? "数据卷当前不可用，恢复后才能浏览快照。" : spaceData.error}</Notice>
      ) : !snapshots ? (
        <div className="bk-loading inline"><Spinner />正在读取快照…</div>
      ) : snapshots.length === 0 ? (
        <EmptyState
          icon={<Camera />}
          title={`${spaceLabel(space)}还没有快照`}
          actions={<>
            {manage && <button type="button" className="bk-primary" onClick={() => createSnapshot(space)}><Camera />创建第一份快照</button>}
            <button type="button" className="bk-secondary" onClick={() => navigate("schedules", { spaceId: space.id })}><CalendarClock />设置定时快照</button>
          </>}
        >
          快照会冻结空间此刻的全部文件。之后误删或改坏文件，可以从快照里找回。
        </EmptyState>
      ) : (
        <div className={`bk-snapshots${twoPane ? "" : " stacked"}`}>
          {(twoPane || !showDetail) && (
            <div className="bk-timeline" role="listbox" aria-label="快照列表">
              {groups.map((group) => (
                <div key={group.day} className="bk-timeline-group">
                  <p className="bk-timeline-day">{group.day}<span>{group.items.length} 份</span></p>
                  {group.items.map((snapshot) => (
                    <button
                      key={snapshot.id}
                      type="button"
                      role="option"
                      aria-selected={snapshot.id === selected?.id}
                      className={`bk-snap${snapshot.id === selected?.id ? " active" : ""}`}
                      onClick={() => { setSelectedId(snapshot.id); setShowDetail(true); }}
                    >
                      <span className="bk-snap-time">{formatClock(snapshot.createdAt)}</span>
                      <span className="bk-snap-main"><strong>{snapshotTitle(snapshot)}</strong><small>{formatCount(snapshot.fileCount)} 个文件 · {formatBytes(snapshot.sizeBytes)}</small></span>
                      <OriginBadge origin={snapshot.origin} />
                    </button>
                  ))}
                </div>
              ))}
            </div>
          )}
          {selected && (twoPane || showDetail) && (
            <SnapshotBrowser key={selected.id} snapshot={selected} manage={manage} onBack={twoPane ? undefined : () => setShowDetail(false)} />
          )}
        </div>
      )}
    </div>
  );
}

function SnapshotBrowser({ snapshot, manage, onBack }: { snapshot: Snapshot; manage: boolean; onBack?: () => void }) {
  const { spaces, refresh, restore, toast } = useBackup();
  const space = spaces.find((item) => item.id === snapshot.spaceId)!;
  const [trail, setTrail] = useState<Crumb[]>([]);
  const [entries, setEntries] = useState<SnapshotEntry[]>();
  const [error, setError] = useState("");
  const [selected, setSelected] = useState<string>();
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const parentId = trail.at(-1)?.id ?? "";

  useEffect(() => {
    let live = true;
    setEntries(undefined); setError(""); setSelected(undefined);
    listSnapshotEntries(snapshot.id, parentId)
      .then((items) => { if (live) setEntries(items); })
      .catch((caught) => { if (live) setError(messageOf(caught)); });
    return () => { live = false; };
  }, [snapshot.id, parentId]);

  const open = (entry: SnapshotEntry) => { if (entry.kind === "directory") setTrail([...trail, { id: entry.id, name: entry.name }]); };
  const restoreEntry = (entry: SnapshotEntry) => restore({
    space, snapshot: { id: snapshot.id, name: snapshotTitle(snapshot), createdAt: snapshot.createdAt },
    entry: { id: entry.id, name: entry.name, kind: entry.kind }, folders: trail.map((crumb) => crumb.name),
  });
  const keep = async () => {
    setBusy(true);
    try {
      await keepSnapshot(snapshot.id);
      await refresh(snapshot.spaceId);
      toast("已保留这份快照，它不会再被定时清理");
    } catch (caught) { toast(messageOf(caught), "error"); } finally { setBusy(false); }
  };
  const remove = async () => {
    setBusy(true);
    try {
      await deleteSnapshot(snapshot.id);
      setConfirming(false);
      await refresh(snapshot.spaceId);
      toast(`已删除快照“${snapshotTitle(snapshot)}”`);
    } catch (caught) { toast(messageOf(caught), "error"); setBusy(false); }
  };

  return (
    <section className="bk-browser" aria-label={`快照 ${snapshot.name}`}>
      <header className="bk-browser-head">
        {onBack && <button type="button" className="bk-icon-button" aria-label="返回快照列表" onClick={onBack}><ArrowLeft /></button>}
        <div className="bk-browser-title">
          <h3>{snapshot.name}</h3>
          <p>{formatDateTime(snapshot.createdAt)} · <OriginBadge origin={snapshot.origin} /> · {formatCount(snapshot.fileCount)} 个文件 · {formatBytes(snapshot.sizeBytes)}</p>
        </div>
        {manage && (
          <div className="bk-browser-actions">
            {snapshot.origin === "scheduled" && <button type="button" className="bk-secondary small" disabled={busy} title="转为手动快照，不再被定时清理" onClick={() => void keep()}><Pin />保留</button>}
            <button type="button" className="bk-secondary small danger" disabled={busy} onClick={() => setConfirming(true)}><Trash2 />删除</button>
          </div>
        )}
      </header>
      <nav className="bk-crumbs" aria-label="快照中的位置">
        <button type="button" disabled={!trail.length} onClick={() => setTrail([])}><FolderOpen />{spaceLabel(space)}</button>
        {trail.map((crumb, index) => (
          <span key={crumb.id}><ChevronRight /><button type="button" disabled={index === trail.length - 1} onClick={() => setTrail(trail.slice(0, index + 1))}>{crumb.name}</button></span>
        ))}
      </nav>
      <div className="bk-entries" role="grid" aria-label="快照中的文件">
        <div className="bk-entries-head" role="row"><span role="columnheader">名称</span><span role="columnheader">修改时间</span><span role="columnheader">大小</span><span role="columnheader" className="bk-sr">操作</span></div>
        {error ? <Notice tone="danger">{error}</Notice>
          : !entries ? <div className="bk-loading inline"><Spinner small />正在读取…</div>
            : entries.length === 0 ? <p className="bk-entries-empty">{trail.length ? "这个文件夹是空的" : "这份快照里没有文件"}</p>
              : entries.map((entry) => (
                <div
                  key={entry.id}
                  role="row"
                  aria-selected={selected === entry.id}
                  tabIndex={0}
                  className={`bk-entry${selected === entry.id ? " selected" : ""}`}
                  onClick={() => setSelected(entry.id)}
                  onDoubleClick={() => open(entry)}
                  onKeyDown={(event) => { if (event.key === "Enter") open(entry); }}
                >
                  <span role="gridcell" className="bk-entry-name">
                    <EntryIcon kind={entry.kind} />
                    {entry.kind === "directory" ? <button type="button" className="bk-entry-link" onClick={(event) => { event.stopPropagation(); open(entry); }}>{entry.name}</button> : <span>{entry.name}</span>}
                  </span>
                  <span role="gridcell" className="bk-entry-meta">{entry.modifiedAt ? formatDateTime(entry.modifiedAt) : "—"}</span>
                  <span role="gridcell" className="bk-entry-meta">{entry.kind === "file" ? formatBytes(entry.sizeBytes) : "文件夹"}</span>
                  <span role="gridcell" className="bk-entry-actions">
                    {entry.kind === "file" && <a className="bk-icon-button" href={snapshotDownloadURL(snapshot.id, entry.id)} download={entry.name} aria-label={`下载 ${entry.name}`} title="下载" onClick={(event) => event.stopPropagation()}><Download /></a>}
                    <button type="button" className="bk-restore-button" aria-label={`恢复 ${entry.name}`} onClick={(event) => { event.stopPropagation(); restoreEntry(entry); }}><ArchiveRestore />恢复</button>
                  </span>
                </div>
              ))}
      </div>
      {confirming && (
        <Dialog
          title="删除这份快照？"
          icon={<Trash2 />}
          tone="danger"
          busy={busy}
          onClose={() => setConfirming(false)}
          footer={<>
            <button type="button" className="bk-secondary" disabled={busy} onClick={() => setConfirming(false)}>取消</button>
            <button type="button" className="bk-danger" disabled={busy} onClick={() => void remove()}>{busy ? <><Spinner small />正在删除…</> : "删除快照"}</button>
          </>}
        >
          <p className="bk-dialog-text">“{snapshot.name}”会被永久删除，之后无法再从这份快照找回文件。{spaceLabel(space)}中现有的文件不受影响。</p>
        </Dialog>
      )}
    </section>
  );
}
