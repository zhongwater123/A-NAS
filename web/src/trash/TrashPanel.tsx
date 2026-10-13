import { ArchiveRestore, CalendarClock, HardDrive, MapPin, RefreshCw, RotateCcw, Trash2, UsersRound } from "lucide-react";
import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";

import { APIError, listEntries, listSpaces, listTrash, purgeTrash, restoreTrash, type Space, type TrashItem } from "../api";
import { formatDateTime } from "../backup/format";
import { Dialog, EmptyState, EntryIcon, Notice, Spinner, useToasts } from "../backup/parts";
import { formatBytes } from "../bytes";
import { fileTypeLabel } from "../fileTypes";
import type { TransferLocation } from "../transfers/transfersApi";
import { expiryOf, locationOf, messageOf, restoredName, retentionDays, sortItems, spaceName, type TrashSort } from "./model";
import "../backup/backup.css";
import "./trash.css";

// TrashPanel lists what the signed-in account deleted: where each item came
// from and goes back to, what it holds, when it was deleted and when the
// recycle bin deletes it for good (docs/specs/basic-storage-and-sharing.md).
export function TrashPanel({ onShowInFiles }: { onShowInFiles: (location: TransferLocation) => void }) {
  const [items, setItems] = useState<TrashItem[]>();
  const [spaces, setSpaces] = useState<Space[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [sort, setSort] = useState<TrashSort>("deleted");
  const [restoring, setRestoring] = useState<TrashItem>();
  const [purging, setPurging] = useState<TrashItem>();
  const { toast, element: toasts } = useToasts();

  const refresh = useCallback(async () => {
    setLoading(true);
    try {
      const [next, visible] = await Promise.all([listTrash(), listSpaces()]);
      setItems(next); setSpaces(visible); setError("");
    } catch (caught) {
      setError(messageOf(caught));
    } finally {
      setLoading(false);
    }
  }, []);
  useEffect(() => { void refresh(); }, [refresh]);

  const spaceOf = useCallback((item: TrashItem) => spaces.find((space) => space.id === item.spaceId), [spaces]);
  const sorted = useMemo(() => sortItems(items ?? [], sort), [items, sort]);
  const days = retentionDays(items ?? []);
  const total = (items ?? []).reduce((sum, item) => sum + item.sizeBytes, 0);
  const forget = (item: TrashItem) => setItems((current) => current?.filter((candidate) => candidate.id !== item.id));

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

  return (
    <div className="bk-app tr-app">
      <div className="tr-scroll">
        <div className="tr-page">
          <header className="tr-head">
            <div>
              <h2>回收站</h2>
              <p>删除的文件和文件夹在这里保留 {days} 天，到期后自动永久删除。只有删除它的人能看到和恢复。</p>
            </div>
            <button type="button" className="bk-icon-button" aria-label="刷新" title="刷新" disabled={loading} onClick={() => void refresh()}>
              <RefreshCw className={loading ? "tr-spin" : ""} />
            </button>
          </header>

          {error && <Notice tone="danger">{error}</Notice>}

          {items === undefined ? (
            !error && <div className="tr-loading"><Spinner />正在读取回收站…</div>
          ) : items.length === 0 ? (
            <EmptyState icon={<Trash2 />} title="回收站是空的">在文件管理或通过 SMB 删除的文件和文件夹会先放到这里，到期前随时可以恢复到原来的位置。</EmptyState>
          ) : (
            <section className="tr-card" aria-label="回收站项目">
              <div className="tr-toolbar">
                <span className="tr-summary">{items.length} 个项目 · 共 {formatBytes(total)}</span>
                <div className="bk-segmented" role="radiogroup" aria-label="排序">
                  <button type="button" role="radio" aria-checked={sort === "deleted"} className={sort === "deleted" ? "active" : ""} onClick={() => setSort("deleted")}>最近删除</button>
                  <button type="button" role="radio" aria-checked={sort === "expiry"} className={sort === "expiry" ? "active" : ""} onClick={() => setSort("expiry")}>即将清理</button>
                </div>
              </div>
              <ul className="tr-list">
                {sorted.map((item) => {
                  const space = spaceOf(item);
                  const expiry = expiryOf(item);
                  return (
                    <li key={item.id} className="tr-row">
                      <EntryIcon kind={item.kind} />
                      <div className="tr-row-main">
                        <strong title={item.name}>{item.name}</strong>
                        <span className="tr-row-meta">
                          <span className="tr-origin" title={item.originalGone ? "原文件夹已不在，恢复时放回空间的根目录" : "原位置，恢复时放回这里"}>
                            {space?.kind === "shared" ? <UsersRound /> : <HardDrive />}
                            <span>{[spaceName(space), ...(item.originalPath ? item.originalPath.split("/") : [])].join(" / ")}</span>
                            {item.originalGone && <em>已不在</em>}
                          </span>
                          <span>{contentOf(item)}</span>
                          <span>删除于 {formatDateTime(item.deletedAt)}</span>
                        </span>
                      </div>
                      <span className={`tr-expiry ${expiry.tone}`} title={`将于 ${formatDateTime(item.purgeAt)} 永久删除`}><CalendarClock />{expiry.label}</span>
                      <div className="tr-actions">
                        <button type="button" className="bk-restore-button" aria-label={`恢复 ${item.name}`} onClick={() => setRestoring(item)}><RotateCcw />恢复</button>
                        <button type="button" className="bk-icon-button tr-purge" aria-label={`永久删除 ${item.name}`} title="永久删除" onClick={() => setPurging(item)}><Trash2 /></button>
                      </div>
                    </li>
                  );
                })}
              </ul>
            </section>
          )}
        </div>
      </div>

      {restoring && (
        <RestoreDialog
          item={restoring}
          space={spaceOf(restoring)}
          onClose={() => setRestoring(undefined)}
          onGone={(message) => { setRestoring(undefined); forget(restoring); toast(message, "error"); }}
          onRestored={(name, location) => {
            const item = restoring;
            setRestoring(undefined); forget(item);
            toast(`已恢复“${name}”到 ${location.text}`, "success", { label: "在文件管理中显示", run: () => void showInFiles(spaceOf(item), location.folders) });
          }}
        />
      )}
      {purging && (
        <PurgeDialog
          item={purging}
          onClose={() => setPurging(undefined)}
          onPurged={() => { const item = purging; setPurging(undefined); forget(item); toast(`已永久删除“${item.name}”`); }}
          onFailed={(message) => { setPurging(undefined); toast(message, "error"); void refresh(); }}
        />
      )}
      {toasts}
    </div>
  );
}

// contentOf says what an item is and holds; a folder of empty files or one
// not yet indexed has no size worth showing.
function contentOf(item: TrashItem): string {
  if (item.kind === "directory") return item.sizeBytes > 0 ? `文件夹 · ${formatBytes(item.sizeBytes)}` : "文件夹";
  return `${fileTypeLabel(item.name)} · ${formatBytes(item.sizeBytes)}`;
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
        <p className="bk-fine">
          {item.kind === "directory" ? "文件夹里的全部内容会一起恢复。" : ""}恢复不会覆盖已有的文件：如果那里已有同名项目，换个名称再恢复。
        </p>
      </form>
    </Dialog>
  );
}

function PurgeDialog({ item, onClose, onPurged, onFailed }: { item: TrashItem; onClose: () => void; onPurged: () => void; onFailed: (message: string) => void }) {
  const [busy, setBusy] = useState(false);
  const purge = async () => {
    setBusy(true);
    try {
      await purgeTrash(item.id);
      onPurged();
    } catch (caught) {
      onFailed(messageOf(caught));
    }
  };
  return (
    <Dialog
      title="永久删除？"
      icon={<Trash2 />}
      tone="danger"
      busy={busy}
      onClose={onClose}
      footer={<>
        <button type="button" className="bk-secondary" disabled={busy} onClick={onClose}>取消</button>
        <button type="button" className="bk-danger" disabled={busy} onClick={() => void purge()}>{busy ? <><Spinner small />正在删除…</> : "永久删除"}</button>
      </>}
    >
      <p className="bk-dialog-text">
        “{item.name}”{item.kind === "directory" ? "及其中的全部内容" : ""}将被立即删除，无法再恢复。
      </p>
    </Dialog>
  );
}
