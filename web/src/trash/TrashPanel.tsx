import { ArchiveRestore, Check, ChevronDown, FileText, Folder, FolderOpen, MapPin, Minus, RefreshCw, RotateCcw, Search, Trash2, X } from "lucide-react";
import { FormEvent, KeyboardEvent, MouseEvent, useCallback, useEffect, useMemo, useRef, useState } from "react";

import { APIError, listEntries, listSpaces, listTrash, purgeTrash, restoreTrash, type Space, type TrashItem } from "../api";
import { formatClock, formatDateTime } from "../backup/format";
import { Dialog, EntryIcon, Notice, Spinner, useToasts } from "../backup/parts";
import { formatBytes } from "../bytes";
import { fileTypeLabel } from "../fileTypes";
import type { TransferLocation } from "../transfers/transfersApi";
import {
  type BatchOutcome, expiryOf, firstDirection, groupItems, locationOf, matches, messageOf, restoredName, restoreSummary, type SortKey, sortItems, spaceName,
  type TrashSort,
} from "./model";
import "../backup/backup.css";
import "./trash.css";

// TrashPanel lists what the signed-in account deleted, like a folder of the
// file manager: where each item goes back to, what it is, when it was
// deleted and how long it stays (docs/specs/basic-storage-and-sharing.md).
// A click selects, Ctrl/⌘ and Shift extend the selection, a double click
// restores; the toolbar acts on the selection.
export function TrashPanel({ onShowInFiles }: { onShowInFiles: (location: TransferLocation) => void }) {
  const [items, setItems] = useState<TrashItem[]>();
  const [spaces, setSpaces] = useState<Space[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [sort, setSort] = useState<TrashSort>({ key: "deleted", ascending: false });
  const [query, setQuery] = useState("");
  const [spaceFilter, setSpaceFilter] = useState("");
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set());
  const [anchor, setAnchor] = useState("");
  const [restoring, setRestoring] = useState<TrashItem>();
  const [purging, setPurging] = useState<{ items: TrashItem[]; everything: boolean }>();
  const [progress, setProgress] = useState<{ label: string; done: number; total: number }>();
  const { toast, element: toasts } = useToasts();
  const root = useRef<HTMLDivElement>(null);
  // Buttons are disabled while a batch runs, which drops focus to the page;
  // the panel takes it back so Escape and Tab keep working.
  const refocus = () => { if (!root.current?.contains(document.activeElement)) root.current?.focus({ preventScroll: true }); };

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
  const filtered = visible.length !== all.length;

  const forget = (gone: TrashItem[]) => {
    const ids = new Set(gone.map((item) => item.id));
    setItems((current) => current?.filter((item) => !ids.has(item.id)));
    setSelected((current) => new Set([...current].filter((id) => !ids.has(id))));
  };
  const toggle = (item: TrashItem) => {
    setAnchor(item.id);
    setSelected((current) => {
      const next = new Set(current);
      if (!next.delete(item.id)) next.add(item.id);
      return next;
    });
  };
  const toggleAll = () => setSelected(allChosen ? new Set() : new Set(visible.map((item) => item.id)));
  // A click selects the item alone; Ctrl/⌘ adds or removes it, Shift
  // selects the range from the last item clicked.
  const choose = (item: TrashItem, event: MouseEvent) => {
    if (busy || (event.target as HTMLElement).closest("button, a")) return;
    if (event.ctrlKey || event.metaKey) { toggle(item); return; }
    const from = visible.findIndex((candidate) => candidate.id === anchor);
    const to = visible.findIndex((candidate) => candidate.id === item.id);
    if (event.shiftKey && from >= 0) {
      setSelected(new Set(visible.slice(Math.min(from, to), Math.max(from, to) + 1).map((candidate) => candidate.id)));
      return;
    }
    setAnchor(item.id);
    setSelected(new Set([item.id]));
  };
  const sortBy = (key: SortKey) => setSort((current) => current.key === key ? { key, ascending: !current.ascending } : { key, ascending: firstDirection[key] });

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

  // One selected item restores through the dialog, which can rename it.
  // Several go back under their own names; those whose name is taken stay
  // selected.
  const restoreChosen = async () => {
    if (chosen.length === 1) { setRestoring(chosen[0]); return; }
    const batch = chosen;
    const outcome: BatchOutcome = { done: [], conflicts: [], gone: [], failed: [] };
    for (const [index, item] of batch.entries()) {
      setProgress({ label: "正在恢复", done: index, total: batch.length });
      try {
        await restoreTrash(item.id, item.name);
        outcome.done.push(item);
      } catch (caught) {
        if (caught instanceof APIError && caught.code === "name_conflict") outcome.conflicts.push(item);
        else if (caught instanceof APIError && caught.code === "not_found") outcome.gone.push(item);
        else outcome.failed.push(messageOf(caught));
      }
    }
    setProgress(undefined);
    refocus();
    forget([...outcome.done, ...outcome.gone]);
    setSelected(new Set(outcome.conflicts.map((item) => item.id)));
    const summary = restoreSummary(outcome);
    toast(summary.text, summary.tone);
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
    if (restoring || purging || busy || !chosen.length || (event.target as HTMLElement).closest("input")) return;
    if (event.key === "Escape") setSelected(new Set());
    if (event.key === "Delete") { event.preventDefault(); setPurging({ items: chosen, everything: false }); }
  };

  const column = (key: SortKey, label: string, className = "") => (
    <button type="button" className={`tr-sort ${className}${sort.key === key ? " active" : ""}`} aria-label={`按${label}排序`} onClick={() => sortBy(key)}>
      {label}{sort.key === key && <ChevronDown className={sort.ascending ? "up" : ""} />}
    </button>
  );

  return (
    <div ref={root} className="bk-app tr-app" tabIndex={-1} onKeyDown={keyDown}>
      {items === undefined ? (
        error ? <div className="tr-state"><Notice tone="danger">{error}</Notice></div> : <div className="tr-state"><Spinner /></div>
      ) : all.length === 0 ? (
        <div className="tr-state">
          <Trash2 className="tr-state-mark" />
          <strong>回收站是空的</strong>
        </div>
      ) : (
        <>
          {chosen.length > 0 || busy ? (
            <div className="tr-bar selecting" role="toolbar" aria-label="所选项目的操作">
              {busy && progress ? (
                <span className="tr-progress" role="status"><Spinner small />{progress.label} {progress.done + 1}/{progress.total}…</span>
              ) : (
                <>
                  <button type="button" className="tr-icon" aria-label="取消选择" title="取消选择 (Esc)" onClick={() => setSelected(new Set())}><X /></button>
                  <span className="tr-count">已选 {chosen.length} 项<small>{formatBytes(chosen.reduce((sum, item) => sum + item.sizeBytes, 0))}</small></span>
                  <span className="tr-gap" />
                  <button type="button" className="tr-command" onClick={() => void restoreChosen()}><RotateCcw />恢复</button>
                  <button type="button" className="tr-command danger" onClick={() => setPurging({ items: chosen, everything: false })}><Trash2 />永久删除</button>
                </>
              )}
            </div>
          ) : (
            <div className="tr-bar">
              <label className="tr-search">
                <Search />
                <input type="search" placeholder="搜索" aria-label="搜索回收站" value={query} onChange={(event) => setQuery(event.target.value)} />
              </label>
              {trashSpaces.length > 1 && (
                <label className="tr-select">
                  <select aria-label="空间" value={spaceFilter} onChange={(event) => setSpaceFilter(event.target.value)}>
                    <option value="">全部空间</option>
                    {trashSpaces.map((space) => <option key={space.id} value={space.id}>{spaceName(space)}</option>)}
                  </select>
                  <ChevronDown />
                </label>
              )}
              <span className="tr-gap" />
              <button type="button" className="tr-icon" aria-label="刷新" title="刷新" disabled={loading} onClick={() => void refresh()}>
                <RefreshCw className={loading ? "tr-spin" : ""} />
              </button>
              <button type="button" className="tr-command danger" onClick={() => setPurging({ items: all, everything: true })}>清空回收站</button>
            </div>
          )}

          {error && <div className="tr-error"><Notice tone="danger">{error}</Notice></div>}

          <div className="tr-head">
            <Checkbox checked={allChosen} mixed={chosen.length > 0 && !allChosen} disabled={busy || !visible.length} label={allChosen ? "取消全选" : "全选"} onChange={toggleAll} />
            {column("name", "名称")}
            <span className="tr-head-label">原位置</span>
            {column("size", "大小", "end")}
            {column("deleted", "删除时间")}
            {column("expiry", "剩余")}
            <span />
          </div>

          <div className="tr-body" onClick={(event) => { if (event.target === event.currentTarget && !busy) setSelected(new Set()); }}>
            {visible.length === 0 ? (
              <div className="tr-state small">
                <strong>没有匹配的项目</strong>
                <button type="button" className="tr-link" onClick={() => { setQuery(""); setSpaceFilter(""); }}>清除筛选</button>
              </div>
            ) : groups.map((group) => (
              <section key={group.key} className="tr-group" aria-label={group.label ?? "回收站项目"}>
                {group.label && <h3>{group.label}</h3>}
                <ul>
                  {group.items.map((item) => (
                    <TrashRow
                      key={item.id}
                      item={item}
                      space={spaceOf(item)}
                      timeOnly={sort.key === "deleted"}
                      selected={selected.has(item.id)}
                      selecting={chosen.length > 0}
                      disabled={busy}
                      onClick={(event) => choose(item, event)}
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

          <footer className="tr-status">
            <span>{filtered ? `${visible.length} / ${all.length} 个项目` : `${all.length} 个项目`}，共 {formatBytes(total)}</span>
            {soon > 0 && <span className="soon">{soon} 项将在 7 天内清理</span>}
          </footer>
        </>
      )}

      {restoring && (
        <RestoreDialog
          item={restoring}
          space={spaceOf(restoring)}
          onClose={() => setRestoring(undefined)}
          onGone={(message) => { setRestoring(undefined); forget([restoring]); toast(message, "error"); }}
          onRestored={(name, location) => {
            const item = restoring;
            setRestoring(undefined); forget([item]);
            toast(`已恢复“${name}”到 ${location.text}`, "success", { label: "在文件管理中显示", run: () => void showInFiles(spaceOf(item), location.folders) });
          }}
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

function TrashRow({ item, space, timeOnly, selected, selecting, disabled, onClick, onToggle, onRestore, onPurge, onOpenOrigin }: {
  item: TrashItem; space?: Space; timeOnly: boolean; selected: boolean; selecting: boolean; disabled: boolean;
  onClick: (event: MouseEvent) => void; onToggle: () => void; onRestore: () => void; onPurge: () => void; onOpenOrigin: () => void;
}) {
  const expiry = expiryOf(item);
  const path = [spaceName(space), ...(item.originalPath ? item.originalPath.split("/") : [])].join(" / ");
  const unknownSize = item.kind === "directory" && item.sizeBytes === 0;
  return (
    <li className={`tr-row${selected ? " selected" : ""}${selecting ? " selecting" : ""}`} onClick={onClick} onDoubleClick={() => { if (!disabled) onRestore(); }}>
      <Checkbox checked={selected} disabled={disabled} label={`选择 ${item.name}`} onChange={onToggle} />
      <div className="tr-name">
        {item.kind === "directory" ? <Folder className="tr-glyph folder" /> : <FileText className="tr-glyph" />}
        <strong title={item.name}>{item.name}</strong>
      </div>
      <div className="tr-cells">
        <div className="tr-origin">
          {item.originalGone ? (
            <span className="tr-path gone" title={`${path}\n原文件夹已不在，恢复时放回${spaceName(space)}的根目录`}>
              <span>{path}</span><em>已不在</em>
            </span>
          ) : (
            <button type="button" className="tr-path" title={`${path}\n在文件管理中打开`} aria-label={`在文件管理中打开 ${path}`} onClick={onOpenOrigin}>
              <span>{path}</span><FolderOpen />
            </button>
          )}
        </div>
        <span className={`tr-size${unknownSize ? " unknown" : ""}`} title={item.kind === "directory" ? "文件夹" : fileTypeLabel(item.name)}>
          {unknownSize ? "—" : formatBytes(item.sizeBytes)}
        </span>
        <span className="tr-time" title={`删除于 ${formatDateTime(item.deletedAt)}`}>
          <span className="tr-label">删除于 </span>{timeOnly ? formatClock(item.deletedAt) : formatDateTime(item.deletedAt)}
        </span>
        <span className={`tr-expiry ${expiry.tone}`} title={`将于 ${formatDateTime(item.purgeAt)} 永久删除`}>
          <span className="tr-label">剩余 </span>{expiry.label}
        </span>
      </div>
      <div className="tr-actions">
        <button type="button" className="tr-restore" aria-label={`恢复 ${item.name}`} disabled={disabled} onClick={onRestore}>恢复</button>
        <button type="button" className="tr-icon tr-purge" aria-label={`永久删除 ${item.name}`} title="永久删除" disabled={disabled} onClick={onPurge}><Trash2 /></button>
      </div>
    </li>
  );
}

function RestoreDialog({ item, space, onClose, onRestored, onGone }: {
  item: TrashItem;
  space?: Space;
  onClose: () => void;
  onRestored: (name: string, location: { text: string; folders: string[] }) => void;
  onGone: (message: string) => void;
}) {
  const location = locationOf(item, space);
  const [name, setName] = useState(item.name);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [suggestion, setSuggestion] = useState<string>();
  const [attempt, setAttempt] = useState(1);

  const submit = async (event?: FormEvent) => {
    event?.preventDefault();
    const finalName = name.trim();
    if (!finalName || busy) return;
    setBusy(true); setError(""); setSuggestion(undefined);
    try {
      await restoreTrash(item.id, finalName);
      onRestored(finalName, location);
    } catch (caught) {
      if (caught instanceof APIError && caught.code === "not_found") {
        onGone(messageOf(caught));
        return;
      }
      if (caught instanceof APIError && caught.code === "name_conflict") {
        setError(`“${location.text}”中已有名为“${finalName}”的项目。`);
        setSuggestion(restoredName(item.name, item.kind, attempt));
        setAttempt(attempt + 1);
      } else {
        setError(messageOf(caught));
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      title={`恢复${item.kind === "directory" ? "文件夹" : "文件"}`}
      icon={<ArchiveRestore />}
      busy={busy}
      onClose={onClose}
      footer={<>
        <button type="button" className="bk-secondary" disabled={busy} onClick={onClose}>取消</button>
        <button type="submit" form="tr-restore-form" className="bk-primary" disabled={busy || !name.trim()}>{busy ? <><Spinner small />正在恢复…</> : "恢复"}</button>
      </>}
    >
      <form id="tr-restore-form" className="bk-restore" onSubmit={(event) => void submit(event)}>
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

        <label className="bk-field">
          <span className="bk-field-label">名称</span>
          <input data-autofocus value={name} disabled={busy} aria-label="恢复后的名称"
            onChange={(event) => { setName(event.target.value); setError(""); setSuggestion(undefined); }} />
        </label>

        {error && (
          <div className="bk-inline-error" role="alert">
            <span>{error}</span>
            {suggestion && <button type="button" className="bk-link" onClick={() => { setName(suggestion); setError(""); setSuggestion(undefined); }}>改用“{suggestion}”</button>}
          </div>
        )}
        <p className="bk-fine">已有同名项目时不会覆盖。</p>
      </form>
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
