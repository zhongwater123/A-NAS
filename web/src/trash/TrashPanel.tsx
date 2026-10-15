import { ArchiveRestore, Check, CircleAlert, CopyPlus, FolderOpen, HardDrive, MapPin, Minus, RefreshCw, Replace, RotateCcw, Search, Trash2, UsersRound, X } from "lucide-react";
import { FormEvent, KeyboardEvent, useCallback, useEffect, useMemo, useRef, useState } from "react";

import { APIError, listEntries, listSpaces, listTrash, purgeTrash, type RestoreConflict, type RestoredEntry, restoreTrash, type Space, type TrashItem } from "../api";
import { formatClock, formatDateTime } from "../backup/format";
import { Dialog, EmptyState, EntryIcon, Notice, Spinner, useToasts } from "../backup/parts";
import { formatBytes } from "../bytes";
import { fileTypeLabel } from "../fileTypes";
import type { TransferLocation } from "../transfers/transfersApi";
import {
  type BatchOutcome, emptyOutcome, expiryOf, type ExpiryTone, groupItems, locationOf, matches, messageOf, restoredName, restoredText, restoreSummary,
  sortItems, spaceName, type TrashSort,
} from "./model";
import "../backup/backup.css";
import "./trash.css";

// TrashPanel lists what the signed-in account deleted, grouped by the day
// it was deleted: where each item goes back to, what it is and holds, and
// how long it stays before the recycle bin deletes it for good
// (docs/specs/basic-storage-and-sharing.md). Items can be restored or
// deleted one by one or as a selection.
export function TrashPanel({ onShowInFiles }: { onShowInFiles: (location: TransferLocation) => void }) {
  const [items, setItems] = useState<TrashItem[]>();
  const [spaces, setSpaces] = useState<Space[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [sort, setSort] = useState<TrashSort>("deleted");
  const [query, setQuery] = useState("");
  const [spaceFilter, setSpaceFilter] = useState("");
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set());
  const [restoring, setRestoring] = useState<TrashItem>();
  const [purging, setPurging] = useState<{ items: TrashItem[]; everything: boolean }>();
  // conflicts are the items of a selection whose name was taken where they
  // go back, waiting for the user to say what to do with them.
  const [conflicts, setConflicts] = useState<{ items: TrashItem[]; outcome: BatchOutcome }>();
  const [progress, setProgress] = useState<{ label: string; done: number; total: number }>();
  const { toast, element: toasts } = useToasts();
  const root = useRef<HTMLDivElement>(null);
  // Buttons are disabled while a batch runs, which drops focus to the page;
  // the panel takes it back so Escape and Tab keep working.
  const refocus = () => { if (!root.current?.contains(document.activeElement)) root.current?.focus({ preventScroll: true }); };
  // The same after the dialog for taken names closes with the focus in it.
  const refocusAfterConflicts = useRef(false);

  const refresh = useCallback(async () => {
    setLoading(true);
    try {
      const [next, visible] = await Promise.all([listTrash(), listSpaces()]);
      setItems(next); setSpaces(visible); setError("");
      setSelected((current) => new Set([...current].filter((id) => next.some((item) => item.id === id))));
    } catch (caught) {
      setError(messageOf(caught));
    } finally {
      setLoading(false);
    }
  }, []);
  useEffect(() => { void refresh(); }, [refresh]);

  useEffect(() => {
    if (conflicts || !refocusAfterConflicts.current) return;
    refocusAfterConflicts.current = false;
    if (!root.current?.contains(document.activeElement)) root.current?.focus({ preventScroll: true });
  }, [conflicts]);

  const all = useMemo(() => items ?? [], [items]);
  const spaceOf = useCallback((item: TrashItem) => spaces.find((space) => space.id === item.spaceId), [spaces]);
  const visible = useMemo(() => sortItems(all.filter((item) => matches(item, spaceFilter, query)), sort), [all, spaceFilter, query, sort]);
  const groups = useMemo(() => groupItems(visible, sort), [visible, sort]);
  const total = all.reduce((sum, item) => sum + item.sizeBytes, 0);
  const soon = all.filter((item) => expiryOf(item).tone !== "calm").length;
  const trashSpaces = spaces.filter((space) => all.some((item) => item.spaceId === space.id));
  const chosen = visible.filter((item) => selected.has(item.id));
  const allChosen = visible.length > 0 && chosen.length === visible.length;
  const busy = Boolean(progress);

  const forget = (gone: TrashItem[]) => {
    const ids = new Set(gone.map((item) => item.id));
    setItems((current) => current?.filter((item) => !ids.has(item.id)));
    setSelected((current) => new Set([...current].filter((id) => !ids.has(id))));
  };
  const toggle = (item: TrashItem) => setSelected((current) => {
    const next = new Set(current);
    if (!next.delete(item.id)) next.add(item.id);
    return next;
  });
  const toggleAll = () => setSelected(allChosen ? new Set() : new Set(visible.map((item) => item.id)));

  // The restored item's folder is found by name, as the file manager knows
  // folders by ID.
  const showInFiles = async (space: Space | undefined, folders: string[]) => {
    if (!space) return;
    const trail: TransferLocation["folders"] = [];
    try {
      for (const name of folders) {
        const match = (await listEntries(space.id, trail.at(-1)?.id ?? "")).find((entry) => entry.kind === "directory" && entry.name === name);
        if (!match) break;
        trail.push({ id: match.id, name: match.name });
      }
    } catch {
      // Open what was found; the file manager reports what it cannot list.
    }
    onShowInFiles({ spaceId: space.id, spaceKind: space.kind, folders: trail });
  };

  // restoreBatch puts each item back where it came from, under its own name.
  // With "ask", the items whose name is taken there are returned for the
  // user to decide; a folder is never replaced and keeps both instead.
  const restoreBatch = async (batch: TrashItem[], choice: RestoreConflict, outcome: BatchOutcome) => {
    const taken: TrashItem[] = [];
    for (const [index, item] of batch.entries()) {
      setProgress({ label: "正在恢复", done: index, total: batch.length });
      try {
        const restored = await restoreTrash(item.id, item.name, choice === "replace" && item.kind === "directory" ? "rename" : choice);
        outcome.done.push(item);
        if (restored.replaced) outcome.replaced += 1;
        else if (restored.name !== item.name) outcome.renamed += 1;
      } catch (caught) {
        if (choice === "ask" && caught instanceof APIError && caught.code === "name_conflict") taken.push(item);
        else if (caught instanceof APIError && caught.code === "not_found") outcome.gone.push(item);
        else outcome.failed.push(messageOf(caught));
      }
    }
    setProgress(undefined);
    refocus();
    forget([...outcome.done, ...outcome.gone]);
    return taken;
  };

  const finishBatch = (outcome: BatchOutcome) => {
    setSelected(new Set(outcome.skipped.map((item) => item.id)));
    // A replaced file went to the recycle bin and is listed from now on.
    if (outcome.replaced) void refresh();
    const summary = restoreSummary(outcome);
    toast(summary.text, summary.tone);
  };

  const restoreChosen = async () => {
    const outcome = emptyOutcome();
    const taken = await restoreBatch(chosen, "ask", outcome);
    if (taken.length) setConflicts({ items: taken, outcome });
    else finishBatch(outcome);
  };

  const resolveConflicts = async (choice: RestoreConflict | "skip") => {
    if (!conflicts) return;
    const { items: taken, outcome } = conflicts;
    refocusAfterConflicts.current = true;
    setConflicts(undefined);
    if (choice === "skip") outcome.skipped.push(...taken);
    else await restoreBatch(taken, choice, outcome);
    finishBatch(outcome);
  };

  const purgeItems = async (batch: TrashItem[]) => {
    const removed: TrashItem[] = [];
    let failure = "";
    for (const [index, item] of batch.entries()) {
      setProgress({ label: "正在永久删除", done: index, total: batch.length });
      try {
        await purgeTrash(item.id);
        removed.push(item);
      } catch (caught) {
        if (caught instanceof APIError && caught.code === "not_found") removed.push(item);
        else { failure = messageOf(caught); break; }
      }
    }
    setProgress(undefined);
    setPurging(undefined);
    refocus();
    forget(removed);
    if (failure) toast(`已永久删除 ${removed.length} 项，其余未删除：${failure}`, "error");
    else toast(batch.length === 1 ? `已永久删除“${batch[0].name}”` : `已永久删除 ${removed.length} 项`);
  };

  const keyDown = (event: KeyboardEvent) => {
    if (event.key === "Escape" && selected.size && !restoring && !purging && !conflicts && !busy) setSelected(new Set());
  };

  return (
    <div ref={root} className="bk-app tr-app" tabIndex={-1} onKeyDown={keyDown}>
      <header className="tr-head">
        <span className="tr-mark" aria-hidden="true"><Trash2 /></span>
        <div className="tr-title">
          <h2>回收站</h2>
        </div>
        <div className="tr-head-actions">
          <button type="button" className="bk-icon-button" aria-label="刷新" title="刷新" disabled={loading || busy} onClick={() => void refresh()}>
            <RefreshCw className={loading ? "tr-spin" : ""} />
          </button>
          <button type="button" className="bk-secondary danger small" disabled={!all.length || busy} onClick={() => setPurging({ items: all, everything: true })}>
            <Trash2 />清空回收站
          </button>
        </div>
      </header>

      {all.length > 0 && (
        <div className="tr-stats" aria-label="回收站概况">
          <div><span>项目</span><strong>{all.length}</strong></div>
          <div><span>占用空间</span><strong>{formatBytes(total)}</strong></div>
          <div className={soon ? "soon" : ""}><span>7 天内清理</span><strong>{soon}</strong></div>
        </div>
      )}

      {error && <div className="tr-notice"><Notice tone="danger">{error}</Notice></div>}

      {items === undefined ? (
        !error && <div className="tr-loading"><Spinner />正在读取回收站…</div>
      ) : all.length === 0 ? (
        <EmptyState icon={<Trash2 />} title="回收站是空的">在文件管理或通过 SMB 删除的文件和文件夹会先放到这里，到期前随时可以恢复到原来的位置。</EmptyState>
      ) : (
        <section className="tr-card" aria-label="回收站项目">
          {chosen.length > 0 || busy ? (
            <div className="tr-toolbar selecting" role="toolbar" aria-label="所选项目的操作">
              {busy && progress ? (
                <span className="tr-progress" role="status"><Spinner small />{progress.label} {progress.done + 1}/{progress.total}…</span>
              ) : (
                <>
                  <button type="button" className="bk-icon-button" aria-label="取消选择" title="取消选择 (Esc)" onClick={() => setSelected(new Set())}><X /></button>
                  <strong>已选 {chosen.length} 项</strong>
                  <span className="tr-toolbar-meta">共 {formatBytes(chosen.reduce((sum, item) => sum + item.sizeBytes, 0))}</span>
                  <span className="tr-toolbar-gap" />
                  <button type="button" className="bk-primary small" onClick={() => void restoreChosen()}><RotateCcw />恢复</button>
                  <button type="button" className="bk-secondary danger small" onClick={() => setPurging({ items: chosen, everything: false })}><Trash2 />永久删除</button>
                </>
              )}
            </div>
          ) : (
            <div className="tr-toolbar">
              <label className="tr-search">
                <Search />
                <input type="search" placeholder="搜索名称" aria-label="搜索回收站" value={query} onChange={(event) => setQuery(event.target.value)} />
              </label>
              {trashSpaces.length > 1 && (
                <div className="bk-segmented" role="radiogroup" aria-label="空间">
                  <button type="button" role="radio" aria-checked={!spaceFilter} className={!spaceFilter ? "active" : ""} onClick={() => setSpaceFilter("")}>全部</button>
                  {trashSpaces.map((space) => (
                    <button key={space.id} type="button" role="radio" aria-checked={spaceFilter === space.id} className={spaceFilter === space.id ? "active" : ""} onClick={() => setSpaceFilter(space.id)}>
                      {space.kind === "shared" ? <UsersRound /> : <HardDrive />}{spaceName(space)}
                    </button>
                  ))}
                </div>
              )}
              <span className="tr-toolbar-gap" />
              <div className="bk-segmented" role="radiogroup" aria-label="排序">
                <button type="button" role="radio" aria-checked={sort === "deleted"} className={sort === "deleted" ? "active" : ""} onClick={() => setSort("deleted")}>最近删除</button>
                <button type="button" role="radio" aria-checked={sort === "expiry"} className={sort === "expiry" ? "active" : ""} onClick={() => setSort("expiry")}>即将清理</button>
              </div>
            </div>
          )}

          {visible.length === 0 ? (
            <div className="tr-no-match">
              <Search />
              <strong>没有匹配的项目</strong>
              <button type="button" className="bk-link" onClick={() => { setQuery(""); setSpaceFilter(""); }}>清除筛选</button>
            </div>
          ) : (
            <>
              <div className="tr-columns">
                <Checkbox checked={allChosen} mixed={chosen.length > 0 && !allChosen} disabled={busy} label={allChosen ? "取消全选" : "全选"} onChange={toggleAll} />
                <span>名称</span>
                <span className="tr-col-origin">原位置</span>
                <span className="tr-col-size">大小</span>
                <span className="tr-col-time">删除时间</span>
                <span className="tr-col-expiry">剩余</span>
                <span />
              </div>
              <div className="tr-groups">
                {groups.map((group) => (
                  <section key={group.key} className="tr-group" aria-label={group.label}>
                    <h3>{group.label}<span>{group.items.length} 项</span></h3>
                    <ul>
                      {group.items.map((item) => (
                        <TrashRow
                          key={item.id}
                          item={item}
                          space={spaceOf(item)}
                          timeOnly={sort === "deleted"}
                          selected={selected.has(item.id)}
                          disabled={busy}
                          onToggle={() => toggle(item)}
                          onRestore={() => setRestoring(item)}
                          onPurge={() => setPurging({ items: [item], everything: false })}
                          onOpenOrigin={() => void showInFiles(spaceOf(item), locationOf(item, spaceOf(item)).folders)}
                        />
                      ))}
                    </ul>
                  </section>
                ))}
              </div>
            </>
          )}
        </section>
      )}

      {restoring && (
        <RestoreDialog
          item={restoring}
          space={spaceOf(restoring)}
          onClose={() => setRestoring(undefined)}
          onGone={(message) => { setRestoring(undefined); forget([restoring]); toast(message, "error"); }}
          onRestored={(restored, text, location) => {
            const item = restoring;
            setRestoring(undefined); forget([item]);
            if (restored.replaced) void refresh();
            toast(text, "success", { label: "在文件管理中显示", run: () => void showInFiles(spaceOf(item), location.folders) });
          }}
        />
      )}
      {conflicts && (
        <ConflictDialog
          items={conflicts.items}
          locate={(item) => locationOf(item, spaceOf(item)).text}
          onChoose={(choice) => void resolveConflicts(choice)}
        />
      )}
      {purging && (
        <PurgeDialog
          items={purging.items}
          everything={purging.everything}
          busy={busy}
          onClose={() => setPurging(undefined)}
          onConfirm={() => void purgeItems(purging.items)}
        />
      )}
      {toasts}
    </div>
  );
}

function Checkbox({ checked, mixed, disabled, label, onChange }: { checked: boolean; mixed?: boolean; disabled?: boolean; label: string; onChange: () => void }) {
  return (
    <button type="button" role="checkbox" aria-checked={mixed ? "mixed" : checked} aria-label={label} disabled={disabled}
      className={`tr-check${checked ? " on" : ""}${mixed ? " mixed" : ""}`} onClick={onChange}>
      {mixed ? <Minus strokeWidth={3} /> : <Check strokeWidth={3} />}
    </button>
  );
}

function TrashRow({ item, space, timeOnly, selected, disabled, onToggle, onRestore, onPurge, onOpenOrigin }: {
  item: TrashItem; space?: Space; timeOnly: boolean; selected: boolean; disabled: boolean;
  onToggle: () => void; onRestore: () => void; onPurge: () => void; onOpenOrigin: () => void;
}) {
  const expiry = expiryOf(item);
  const path = [spaceName(space), ...(item.originalPath ? item.originalPath.split("/") : [])].join(" / ");
  const SpaceIcon = space?.kind === "shared" ? UsersRound : HardDrive;
  return (
    <li className={`tr-row${selected ? " selected" : ""}`}>
      <Checkbox checked={selected} disabled={disabled} label={`选择 ${item.name}`} onChange={onToggle} />
      <div className="tr-name">
        <EntryIcon kind={item.kind} />
        <div>
          <strong title={item.name}>{item.name}</strong>
          <small>{item.kind === "directory" ? "文件夹" : fileTypeLabel(item.name)}</small>
        </div>
      </div>
      <div className="tr-cells">
        <div className="tr-origin">
          {item.originalGone ? (
            <span className="tr-origin-path gone" title={`${path}\n原文件夹已不在，恢复时放回${spaceName(space)}的根目录`}>
              <SpaceIcon /><span>{path}</span><em>已不在</em>
            </span>
          ) : (
            <button type="button" className="tr-origin-path" title={`${path}\n点击在文件管理中打开`} aria-label={`在文件管理中打开 ${path}`} onClick={onOpenOrigin}>
              <SpaceIcon /><span>{path}</span><FolderOpen className="tr-origin-open" />
            </button>
          )}
        </div>
        {item.kind === "directory" && item.sizeBytes === 0
          ? <span className="tr-size unknown" title="文件夹中没有已统计的文件">—</span>
          : <span className="tr-size">{formatBytes(item.sizeBytes)}</span>}
        <span className="tr-time" title={`删除于 ${formatDateTime(item.deletedAt)}`}><span className="tr-label">删除于 </span>{timeOnly ? formatClock(item.deletedAt) : formatDateTime(item.deletedAt)}</span>
        <span className={`tr-expiry ${expiry.tone}`} title={`将于 ${formatDateTime(item.purgeAt)} 永久删除`}>
          <ExpiryRing left={expiry.left} tone={expiry.tone} />
          <span><span className="tr-label">剩余 </span>{expiry.short}</span>
        </span>
      </div>
      <div className="tr-actions">
        <button type="button" className="bk-restore-button" aria-label={`恢复 ${item.name}`} disabled={disabled} onClick={onRestore}><RotateCcw />恢复</button>
        <button type="button" className="bk-icon-button tr-purge" aria-label={`永久删除 ${item.name}`} title="永久删除" disabled={disabled} onClick={onPurge}><Trash2 /></button>
      </div>
    </li>
  );
}

// ExpiryRing shows how much of an item's time in the bin is left.
function ExpiryRing({ left, tone }: { left: number; tone: ExpiryTone }) {
  const radius = 6.5;
  const circumference = 2 * Math.PI * radius;
  return (
    <svg className={`tr-ring ${tone}`} viewBox="0 0 16 16" aria-hidden="true">
      <circle cx="8" cy="8" r={radius} />
      <circle cx="8" cy="8" r={radius} strokeDasharray={circumference} strokeDashoffset={circumference * (1 - Math.max(left, 0.04))} />
    </svg>
  );
}

function RestoreDialog({ item, space, onClose, onRestored, onGone }: {
  item: TrashItem;
  space?: Space;
  onClose: () => void;
  onRestored: (restored: RestoredEntry, text: string, location: { text: string; folders: string[] }) => void;
  onGone: (message: string) => void;
}) {
  const location = locationOf(item, space);
  const [name, setName] = useState(item.name);
  const [busy, setBusy] = useState<RestoreConflict>();
  const [error, setError] = useState("");
  // taken is the name found taken where the item goes back.
  const [taken, setTaken] = useState("");
  const choices = useRef<HTMLDivElement>(null);
  // The name field leaves with the choices' arrival; focus goes to them.
  useEffect(() => { if (taken) choices.current?.querySelector<HTMLElement>("button")?.focus(); }, [taken]);

  const restore = async (choice: RestoreConflict, event?: FormEvent) => {
    event?.preventDefault();
    const finalName = name.trim();
    if (!finalName || busy) return;
    setBusy(choice); setError("");
    try {
      const restored = await restoreTrash(item.id, finalName, choice);
      onRestored(restored, restoredText(restored, finalName, location.text, choice), location);
    } catch (caught) {
      if (caught instanceof APIError && caught.code === "not_found") {
        onGone(messageOf(caught));
        return;
      }
      if (caught instanceof APIError && caught.code === "name_conflict") setTaken(finalName);
      else setError(messageOf(caught));
    } finally {
      setBusy(undefined);
    }
  };

  const file = item.kind === "file";
  return (
    <Dialog
      title={`恢复${file ? "文件" : "文件夹"}`}
      icon={<ArchiveRestore />}
      busy={Boolean(busy)}
      onClose={onClose}
      footer={<>
        <button type="button" className="bk-secondary" disabled={Boolean(busy)} onClick={onClose}>取消</button>
        {!taken && <button type="submit" form="tr-restore-form" className="bk-primary" disabled={Boolean(busy) || !name.trim()}>{busy ? <><Spinner small />正在恢复…</> : "恢复"}</button>}
      </>}
    >
      <form id="tr-restore-form" className="bk-restore" onSubmit={(event) => void restore("ask", event)}>
        <div className="bk-restore-source">
          <EntryIcon kind={item.kind} />
          <div>
            <strong>{item.name}</strong>
            <small>删除于 {formatDateTime(item.deletedAt)} · {contentOf(item)}</small>
          </div>
        </div>

        <div className="bk-field">
          <span className="bk-field-label">恢复到</span>
          <div className="bk-location">
            <MapPin />
            <span className="bk-location-path" title={location.text}>{location.text}</span>
            {!item.originalGone && <span className="bk-badge neutral">原位置</span>}
          </div>
          {item.originalGone && (
            <small className="bk-field-hint">
              {item.originalPath ? `原文件夹“${item.originalPath}”已不在，` : "原文件夹已不在，"}将恢复到{spaceName(space)}的根目录。
            </small>
          )}
        </div>

        {taken ? (
          <section className="tr-conflict" role="group" aria-label="同名项目">
            <div className="tr-conflict-head">
              <CircleAlert />
              <p><strong>这里已有“{taken}”</strong><span>选择怎么恢复；不会覆盖任何文件。</span></p>
            </div>
            <div ref={choices} className="tr-choices">
              <button type="button" className="tr-choice" disabled={Boolean(busy)} onClick={() => void restore("rename")}>
                <span className="tr-choice-icon">{busy === "rename" ? <Spinner small /> : <CopyPlus />}</span>
                <span className="tr-choice-text"><strong>保留两者</strong><small>改用新名称放在旁边，如“{restoredName(taken, item.kind)}”</small></span>
              </button>
              {file && (
                <button type="button" className="tr-choice warning" disabled={Boolean(busy)} onClick={() => void restore("replace")}>
                  <span className="tr-choice-icon">{busy === "replace" ? <Spinner small /> : <Replace />}</span>
                  <span className="tr-choice-text"><strong>替换</strong><small>现有的“{taken}”移到回收站，之后仍可恢复</small></span>
                </button>
              )}
            </div>
            {!file && <p className="tr-conflict-note">文件夹不会被替换或合并。</p>}
            <button type="button" className="bk-link tr-conflict-rename" disabled={Boolean(busy)} onClick={() => { setTaken(""); setName(restoredName(taken, item.kind)); }}>自己起个名称</button>
          </section>
        ) : (
          <>
            <label className="bk-field">
              <span className="bk-field-label">名称</span>
              <input data-autofocus value={name} disabled={Boolean(busy)} aria-label="恢复后的名称"
                onChange={(event) => { setName(event.target.value); setError(""); }} />
            </label>
            {error && <div className="bk-inline-error" role="alert"><span>{error}</span></div>}
            <p className="bk-fine">
              {file ? "" : "文件夹里的全部内容会一起恢复。"}恢复不会覆盖已有的文件；那里已有同名项目时，可以选择保留两者{file ? "或替换" : ""}。
            </p>
          </>
        )}
      </form>
    </Dialog>
  );
}

// ConflictDialog asks once what to do with every item of a selection whose
// name is taken where it goes back.
function ConflictDialog({ items, locate, onChoose }: {
  items: TrashItem[];
  locate: (item: TrashItem) => string;
  onChoose: (choice: RestoreConflict | "skip") => void;
}) {
  const files = items.filter((item) => item.kind === "file").length;
  const shown = items.slice(0, 5);
  return (
    <Dialog
      title={items.length === 1 ? `“${items[0].name}”的原位置已有同名项目` : `${items.length} 个项目的原位置已有同名项目`}
      icon={<CopyPlus />}
      label="同名项目"
      onClose={() => onChoose("skip")}
      footer={<>
        <button type="button" className="bk-secondary" onClick={() => onChoose("skip")}>跳过</button>
        {files > 0 && <button type="button" className="bk-secondary tr-replace" onClick={() => onChoose("replace")}><Replace />替换</button>}
        <button type="button" className="bk-primary" onClick={() => onChoose("rename")}><CopyPlus />保留两者</button>
      </>}
    >
      <p className="bk-dialog-text">
        保留两者时，恢复的项目改用“名称 (恢复)”这样的新名称放在旁边。{files > 0 ? `替换时，现有的同名文件移到回收站${files < items.length ? "；文件夹不会被替换，仍然保留两者" : ""}。` : "文件夹不会被替换或合并。"}跳过的项目留在回收站中。
      </p>
      <ul className="tr-conflict-list">
        {shown.map((item) => (
          <li key={item.id}>
            <EntryIcon kind={item.kind} />
            <span><strong title={item.name}>{item.name}</strong><small title={locate(item)}>{locate(item)}</small></span>
          </li>
        ))}
      </ul>
      {items.length > shown.length && <p className="bk-fine">以及另外 {items.length - shown.length} 项</p>}
    </Dialog>
  );
}

// contentOf says what an item is and holds; a folder of empty files or one
// not yet indexed has no size worth showing.
function contentOf(item: TrashItem): string {
  if (item.kind === "directory") return item.sizeBytes > 0 ? `文件夹 · ${formatBytes(item.sizeBytes)}` : "文件夹";
  return `${fileTypeLabel(item.name)} · ${formatBytes(item.sizeBytes)}`;
}

function PurgeDialog({ items, everything, busy, onClose, onConfirm }: { items: TrashItem[]; everything: boolean; busy: boolean; onClose: () => void; onConfirm: () => void }) {
  const size = formatBytes(items.reduce((sum, item) => sum + item.sizeBytes, 0));
  const single = items.length === 1 ? items[0] : undefined;
  return (
    <Dialog
      title={everything ? "清空回收站？" : "永久删除？"}
      icon={<Trash2 />}
      tone="danger"
      busy={busy}
      onClose={onClose}
      footer={<>
        <button type="button" className="bk-secondary" disabled={busy} onClick={onClose}>取消</button>
        <button type="button" className="bk-danger" disabled={busy} onClick={onConfirm}>{busy ? <><Spinner small />正在删除…</> : everything ? "清空回收站" : "永久删除"}</button>
      </>}
    >
      <p className="bk-dialog-text">
        {everything ? `回收站中的全部 ${items.length} 个项目（共 ${size}）将被立即删除，无法再恢复。`
          : single ? `“${single.name}”${single.kind === "directory" ? "及其中的全部内容" : ""}将被立即删除，无法再恢复。`
            : `所选的 ${items.length} 个项目（共 ${size}）将被立即删除，无法再恢复。`}
      </p>
    </Dialog>
  );
}
