import { ArrowDownWideNarrow, Check, ChevronDown, RefreshCw, RotateCcw, Search, Trash2, X } from "lucide-react";
import { AnimatePresence, motion } from "motion/react";
import { ContextMenu, DropdownMenu, Tooltip } from "radix-ui";
import { type KeyboardEvent, type MouseEvent, useCallback, useEffect, useMemo, useRef, useState } from "react";

import { APIError } from "../api/client";
import type { RestoredEntry, Space, TrashItem } from "../api.generated";
import { formatShortDay } from "../backup/format";
import { listEntries, listSpaces } from "../files/api";
import { formatBytes } from "../lib/bytes";
import type { TransferLocation } from "../transfers/api";
import { useToasts } from "../ui/workspace";
import workspaceStyles from "../ui/workspace.module.css";
import { listTrash, purgeTrash, type RestoreConflict, restoreTrash } from "./api";
import { ConflictDialog, PurgeDialog, TakenDialog } from "./dialogs";
import {
  type BatchOutcome,
  emptyOutcome,
  expiryOf,
  groupItems,
  locationOf,
  matches,
  messageOf,
  purgeSchedule,
  restoredText,
  restoreSummary,
  sortItems,
  sortLabels,
  spaceName,
  type TrashSort,
} from "./model";
import { PurgeStrip } from "./PurgeStrip";
import { Checkbox, TrashRow } from "./TrashRow";
import styles from "./trash.module.css";

const isCode = (caught: unknown, code: string) => caught instanceof APIError && caught.code === code;
const typing = (target: EventTarget) => target instanceof HTMLInputElement || target instanceof HTMLTextAreaElement;

// TrashPanel lists what the signed-in account deleted: how much the recycle
// bin holds and when it lets go of it, then the items, each with where it
// goes back to, its size, when it was deleted and how long it stays
// (docs/specs/basic-storage-and-sharing.md). One click restores an item to
// where it came from; a selection is restored or deleted from the bar that
// rises at the bottom.
export function TrashPanel({ onShowInFiles }: { onShowInFiles: (location: TransferLocation) => void }) {
  const [items, setItems] = useState<TrashItem[]>();
  const [spaces, setSpaces] = useState<Space[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [sort, setSort] = useState<TrashSort>("deleted");
  const [query, setQuery] = useState("");
  const [spaceFilter, setSpaceFilter] = useState("");
  const [purgeDay, setPurgeDay] = useState<number>();
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set());
  // restoring are the items a single click is putting back right now.
  const [restoring, setRestoring] = useState<ReadonlySet<string>>(new Set());
  // taken is the item whose name was found taken where it goes back.
  const [taken, setTaken] = useState<TrashItem>();
  const [purging, setPurging] = useState<{ items: TrashItem[]; everything: boolean }>();
  // conflicts are the items of a selection whose name was taken where they
  // go back, waiting for the user to say what to do with them.
  const [conflicts, setConflicts] = useState<{ items: TrashItem[]; outcome: BatchOutcome }>();
  const [progress, setProgress] = useState<{ label: string; done: number; total: number }>();
  // menuItem is the item the right-click menu was opened on.
  const [menuItem, setMenuItem] = useState<TrashItem>();
  const { toast, element: toasts } = useToasts(styles.trToasts);
  const root = useRef<HTMLDivElement>(null);
  // anchor is the item last picked, where a Shift-click's range starts.
  const anchor = useRef("");
  // Buttons are disabled while a batch runs, which drops focus to the page;
  // the panel takes it back so Escape and Tab keep working.
  const refocus = () => {
    if (!root.current?.contains(document.activeElement)) root.current?.focus({ preventScroll: true });
  };
  // The same after the dialog for taken names closes with the focus in it.
  const refocusAfterConflicts = useRef(false);

  const fetchTrash = useCallback(
    () =>
      Promise.all([listTrash(), listSpaces()])
        .then(([next, visible]) => {
          setItems(next);
          setSpaces(visible);
          setError("");
          setSelected((current) => new Set([...current].filter((id) => next.some((item) => item.id === id))));
        })
        .catch((caught: unknown) => {
          setError(messageOf(caught));
        })
        .finally(() => setLoading(false)),
    [],
  );
  const refresh = useCallback(() => {
    setLoading(true);
    return fetchTrash();
  }, [fetchTrash]);
  useEffect(() => {
    void fetchTrash();
  }, [fetchTrash]);

  useEffect(() => {
    if (conflicts || !refocusAfterConflicts.current) return;
    refocusAfterConflicts.current = false;
    if (!root.current?.contains(document.activeElement)) root.current?.focus({ preventScroll: true });
  }, [conflicts]);

  const all = useMemo(() => items ?? [], [items]);
  const spaceOf = useCallback((item: TrashItem) => spaces.find((space) => space.id === item.spaceId), [spaces]);
  const schedule = useMemo(() => purgeSchedule(all), [all]);
  // A day whose items are all gone no longer filters.
  const day = purgeDay !== undefined && schedule[purgeDay]?.count ? purgeDay : undefined;
  const visible = useMemo(
    () =>
      sortItems(
        all.filter((item) => matches(item, spaceFilter, query, day)),
        sort,
      ),
    [all, spaceFilter, query, day, sort],
  );
  const groups = useMemo(() => groupItems(visible, sort), [visible, sort]);
  const [total, unit] = formatBytes(all.reduce((sum, item) => sum + item.sizeBytes, 0)).split(" ");
  const soon = all.filter((item) => expiryOf(item).tone !== "calm").length;
  const trashSpaces = spaces.filter((space) => all.some((item) => item.spaceId === space.id));
  const chosen = visible.filter((item) => selected.has(item.id));
  const allChosen = visible.length > 0 && chosen.length === visible.length;
  const busy = Boolean(progress);
  const filtered = Boolean(query.trim() || spaceFilter || day !== undefined);

  const forget = (gone: TrashItem[]) => {
    const ids = new Set(gone.map((item) => item.id));
    setItems((current) => current?.filter((item) => !ids.has(item.id)));
    setSelected((current) => new Set([...current].filter((id) => !ids.has(id))));
  };
  // toggle picks or drops an item; with Shift, it picks every item from the
  // one picked last to this one.
  const toggle = (item: TrashItem, range = false) => {
    const from = visible.findIndex((candidate) => candidate.id === anchor.current);
    const to = visible.indexOf(item);
    anchor.current = item.id;
    setSelected((current) => {
      const next = new Set(current);
      if (range && from >= 0)
        for (const between of visible.slice(Math.min(from, to), Math.max(from, to) + 1)) next.add(between.id);
      else if (!next.delete(item.id)) next.add(item.id);
      return next;
    });
  };
  const chooseAll = () => setSelected(new Set(visible.map((item) => item.id)));
  const clearFilters = () => {
    setQuery("");
    setSpaceFilter("");
    setPurgeDay(undefined);
  };

  // The restored item's folder is found by name, as the file manager knows
  // folders by ID.
  const showInFiles = async (space: Space | undefined, folders: string[]) => {
    if (!space) return;
    const trail: TransferLocation["folders"] = [];
    try {
      for (const name of folders) {
        const match = (await listEntries(space.id, trail.at(-1)?.id ?? "")).find(
          (entry) => entry.kind === "directory" && entry.name === name,
        );
        if (!match) break;
        trail.push({ id: match.id, name: match.name });
      }
    } catch {
      // Open what was found; the file manager reports what it cannot list.
    }
    onShowInFiles({ spaceId: space.id, spaceKind: space.kind, folders: trail });
  };

  const restoredOne = (item: TrashItem, restored: RestoredEntry, asked: string, choice: RestoreConflict) => {
    const location = locationOf(item, spaceOf(item));
    forget([item]);
    // A replaced file went to the recycle bin and is listed from now on.
    if (restored.replaced) void refresh();
    toast(restoredText(restored, asked, location.text, choice), "success", {
      label: "在文件管理中显示",
      run: () => void showInFiles(spaceOf(item), location.folders),
    });
  };

  // restoreOne puts an item back where it came from at once; only a name
  // taken there asks what to do.
  const restoreOne = async (item: TrashItem) => {
    setRestoring((current) => new Set(current).add(item.id));
    try {
      restoredOne(item, await restoreTrash(item.id, item.name, "ask"), item.name, "ask");
    } catch (caught) {
      if (isCode(caught, "name_conflict")) setTaken(item);
      else {
        if (isCode(caught, "not_found")) forget([item]);
        toast(messageOf(caught), "error");
      }
    } finally {
      setRestoring((current) => new Set([...current].filter((id) => id !== item.id)));
    }
  };

  // restoreBatch puts each item back where it came from, under its own name.
  // With "ask", the items whose name is taken there are returned for the
  // user to decide; a folder is never replaced and keeps both instead.
  const restoreBatch = async (batch: TrashItem[], choice: RestoreConflict, outcome: BatchOutcome) => {
    const conflicting: TrashItem[] = [];
    for (const [index, item] of batch.entries()) {
      setProgress({ label: "正在恢复", done: index, total: batch.length });
      try {
        const restored = await restoreTrash(
          item.id,
          item.name,
          choice === "replace" && item.kind === "directory" ? "rename" : choice,
        );
        outcome.done.push(item);
        if (restored.replaced) outcome.replaced += 1;
        else if (restored.name !== item.name) outcome.renamed += 1;
      } catch (caught) {
        if (choice === "ask" && isCode(caught, "name_conflict")) conflicting.push(item);
        else if (isCode(caught, "not_found")) outcome.gone.push(item);
        else outcome.failed.push(messageOf(caught));
      }
    }
    setProgress(undefined);
    refocus();
    forget([...outcome.done, ...outcome.gone]);
    return conflicting;
  };

  const finishBatch = (outcome: BatchOutcome) => {
    setSelected(new Set(outcome.skipped.map((item) => item.id)));
    // A replaced file went to the recycle bin and is listed from now on.
    if (outcome.replaced) void refresh();
    const summary = restoreSummary(outcome);
    toast(summary.text, summary.tone);
  };

  const restoreItems = async (batch: TrashItem[]) => {
    const outcome = emptyOutcome();
    const conflicting = await restoreBatch(batch, "ask", outcome);
    if (conflicting.length) setConflicts({ items: conflicting, outcome });
    else finishBatch(outcome);
  };

  const resolveConflicts = async (choice: RestoreConflict | "skip") => {
    if (!conflicts) return;
    const { items: conflicting, outcome } = conflicts;
    refocusAfterConflicts.current = true;
    setConflicts(undefined);
    if (choice === "skip") outcome.skipped.push(...conflicting);
    else await restoreBatch(conflicting, choice, outcome);
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
        if (isCode(caught, "not_found")) removed.push(item);
        else {
          failure = messageOf(caught);
          break;
        }
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
    if (taken || purging || conflicts || busy || typing(event.target)) return;
    if (event.key === "Escape" && selected.size) setSelected(new Set());
    else if (event.key === "a" && (event.ctrlKey || event.metaKey) && visible.length) {
      event.preventDefault();
      chooseAll();
    } else if (event.key === "Delete" && chosen.length) setPurging({ items: chosen, everything: false });
  };

  // The menu acts on the whole selection when it opens on one of its items.
  const openMenu = (event: MouseEvent) => {
    const id = (event.target as HTMLElement).closest<HTMLElement>("[data-item]")?.dataset.item;
    setMenuItem(visible.find((item) => item.id === id));
  };
  const menuItems = menuItem && selected.has(menuItem.id) && chosen.length > 1 ? chosen : menuItem ? [menuItem] : [];
  const menuOrigin = menuItem && !menuItem.originalGone ? menuItem : undefined;

  const selecting = chosen.length > 0 || busy;
  return (
    <Tooltip.Provider delayDuration={120} skipDelayDuration={300}>
      <div
        ref={root}
        className={`${workspaceStyles.bkApp} ${styles.trApp}`}
        data-selecting={selecting || undefined}
        tabIndex={-1}
        onKeyDown={keyDown}
      >
        <div className={styles.trScroll}>
          {all.length > 0 && (
            <header className={styles.trSummary}>
              <div className={styles.trTotal} aria-label="回收站概况">
                <strong>
                  {total}
                  {unit && <small> {unit}</small>}
                </strong>
                <p>
                  <span>{all.length} 项</span>
                  {soon > 0 && (
                    <button type="button" onClick={() => setSort("expiry")}>
                      {soon} 项将在 7 天内清理
                    </button>
                  )}
                </p>
              </div>
              <PurgeStrip days={schedule} selected={day} onSelect={setPurgeDay} />
            </header>
          )}

          <section className={styles.trSheet} aria-label={items ? "回收站项目" : undefined}>
            {items === undefined ? (
              error ? (
                <div className={styles.trEmpty} role="alert">
                  <strong>无法读取回收站</strong>
                  <p>{error}</p>
                  <button
                    type="button"
                    className={`${styles.trBtn} ${styles.outline}`}
                    disabled={loading}
                    onClick={() => void refresh()}
                  >
                    重试
                  </button>
                </div>
              ) : (
                <Skeleton />
              )
            ) : (
              <>
                <div className={styles.trBar}>
                  {visible.length > 0 && (
                    <Checkbox
                      checked={allChosen}
                      mixed={chosen.length > 0 && !allChosen}
                      disabled={busy}
                      label={allChosen ? "取消全选" : "全选"}
                      onChange={() => (allChosen ? setSelected(new Set()) : chooseAll())}
                    />
                  )}
                  {trashSpaces.length > 1 && (
                    <div className={styles.trTabs} role="radiogroup" aria-label="空间">
                      <button type="button" role="radio" aria-checked={!spaceFilter} onClick={() => setSpaceFilter("")}>
                        全部
                        <span aria-hidden="true">{all.length}</span>
                      </button>
                      {trashSpaces.map((space) => (
                        <button
                          key={space.id}
                          type="button"
                          role="radio"
                          aria-checked={spaceFilter === space.id}
                          onClick={() => setSpaceFilter(space.id)}
                        >
                          {spaceName(space)}
                          <span aria-hidden="true">{all.filter((item) => item.spaceId === space.id).length}</span>
                        </button>
                      ))}
                    </div>
                  )}
                  {day !== undefined && (
                    <button
                      type="button"
                      className={styles.trChip}
                      title="取消筛选"
                      onClick={() => setPurgeDay(undefined)}
                    >
                      {formatShortDay(schedule[day].date)}清理
                      <X />
                    </button>
                  )}
                  <span className={styles.trGap} />
                  {all.length > 0 && (
                    <>
                      <label className={styles.trSearch}>
                        <Search />
                        <input
                          type="search"
                          placeholder="搜索"
                          aria-label="搜索回收站"
                          value={query}
                          onChange={(event) => setQuery(event.target.value)}
                        />
                      </label>
                      <DropdownMenu.Root modal={false}>
                        <DropdownMenu.Trigger className={`${styles.trBtn} ${styles.quiet}`} aria-label="排序">
                          <ArrowDownWideNarrow />
                          <span>{sortLabels[sort]}</span>
                          <ChevronDown className={styles.trCaret} />
                        </DropdownMenu.Trigger>
                        <DropdownMenu.Portal>
                          <DropdownMenu.Content
                            className={`${styles.trFloat} ${styles.trMenu}`}
                            align="end"
                            sideOffset={6}
                          >
                            <DropdownMenu.RadioGroup
                              value={sort}
                              onValueChange={(value) => setSort(value as TrashSort)}
                            >
                              {(Object.keys(sortLabels) as TrashSort[]).map((key) => (
                                <DropdownMenu.RadioItem key={key} value={key} className={styles.trMenuItem}>
                                  {sortLabels[key]}
                                  <DropdownMenu.ItemIndicator>
                                    <Check />
                                  </DropdownMenu.ItemIndicator>
                                </DropdownMenu.RadioItem>
                              ))}
                            </DropdownMenu.RadioGroup>
                          </DropdownMenu.Content>
                        </DropdownMenu.Portal>
                      </DropdownMenu.Root>
                    </>
                  )}
                  <button
                    type="button"
                    className={`${styles.trBtn} ${styles.quiet} ${styles.icon}`}
                    aria-label="刷新"
                    title="刷新"
                    disabled={loading || busy}
                    onClick={() => void refresh()}
                  >
                    <RefreshCw className={loading ? styles.trSpin : ""} />
                  </button>
                  <button
                    type="button"
                    className={`${styles.trBtn} ${styles.quiet} ${styles.risky}`}
                    disabled={!all.length || busy}
                    onClick={() => setPurging({ items: all, everything: true })}
                  >
                    清空回收站
                  </button>
                </div>

                {error && (
                  <p className={styles.trError} role="alert">
                    {error}
                  </p>
                )}

                {all.length === 0 ? (
                  <div className={styles.trEmpty}>
                    <EmptyBin />
                    <strong>回收站是空的</strong>
                    <p>删除的文件和文件夹会先放在这里，到期前可以恢复。</p>
                  </div>
                ) : visible.length === 0 ? (
                  <div className={styles.trEmpty}>
                    <strong>没有匹配的项目</strong>
                    <button type="button" className={`${styles.trBtn} ${styles.outline}`} onClick={clearFilters}>
                      清除筛选
                    </button>
                  </div>
                ) : (
                  <ContextMenu.Root modal={false}>
                    <ContextMenu.Trigger asChild disabled={busy}>
                      <div className={styles.trList} onContextMenuCapture={openMenu}>
                        {groups.map((group) => (
                          <section key={group.key} className={styles.trGroup} aria-label={group.label || undefined}>
                            {group.label && (
                              <h3>
                                {group.label}
                                <span>{group.items.length} 项</span>
                              </h3>
                            )}
                            <ul>
                              <AnimatePresence initial={false}>
                                {group.items.map((item) => (
                                  <TrashRow
                                    key={item.id}
                                    item={item}
                                    space={spaceOf(item)}
                                    timeOnly={sort === "deleted"}
                                    selected={selected.has(item.id)}
                                    restoring={restoring.has(item.id)}
                                    disabled={busy}
                                    onToggle={(range) => toggle(item, range)}
                                    onRestore={() => void restoreOne(item)}
                                    onPurge={() => setPurging({ items: [item], everything: false })}
                                    onOpenOrigin={() =>
                                      void showInFiles(spaceOf(item), locationOf(item, spaceOf(item)).folders)
                                    }
                                  />
                                ))}
                              </AnimatePresence>
                            </ul>
                          </section>
                        ))}
                        {filtered && (
                          <p className={styles.trListEnd}>
                            显示 {visible.length} / {all.length} 项
                            <button type="button" onClick={clearFilters}>
                              清除筛选
                            </button>
                          </p>
                        )}
                      </div>
                    </ContextMenu.Trigger>
                    <ContextMenu.Portal>
                      <ContextMenu.Content className={`${styles.trFloat} ${styles.trMenu}`}>
                        {menuItem ? (
                          <>
                            <ContextMenu.Item
                              className={styles.trMenuItem}
                              onSelect={() =>
                                menuItems.length > 1 ? void restoreItems(menuItems) : void restoreOne(menuItem)
                              }
                            >
                              {menuItems.length > 1 ? `恢复 ${menuItems.length} 项` : "恢复"}
                            </ContextMenu.Item>
                            {menuItems.length === 1 && (
                              <>
                                <ContextMenu.Item
                                  className={styles.trMenuItem}
                                  disabled={!menuOrigin}
                                  onSelect={() =>
                                    menuOrigin &&
                                    void showInFiles(
                                      spaceOf(menuOrigin),
                                      locationOf(menuOrigin, spaceOf(menuOrigin)).folders,
                                    )
                                  }
                                >
                                  打开原位置
                                </ContextMenu.Item>
                                <ContextMenu.Item className={styles.trMenuItem} onSelect={() => toggle(menuItem)}>
                                  {selected.has(menuItem.id) ? "取消选择" : "选择"}
                                </ContextMenu.Item>
                              </>
                            )}
                            <ContextMenu.Separator className={styles.trMenuLine} />
                            <ContextMenu.Item
                              className={`${styles.trMenuItem} ${styles.risky}`}
                              onSelect={() => setPurging({ items: menuItems, everything: false })}
                            >
                              {menuItems.length > 1 ? `永久删除 ${menuItems.length} 项` : "永久删除"}
                            </ContextMenu.Item>
                          </>
                        ) : (
                          <>
                            <ContextMenu.Item className={styles.trMenuItem} onSelect={chooseAll}>
                              全选
                            </ContextMenu.Item>
                            <ContextMenu.Item className={styles.trMenuItem} onSelect={() => void refresh()}>
                              刷新
                            </ContextMenu.Item>
                          </>
                        )}
                      </ContextMenu.Content>
                    </ContextMenu.Portal>
                  </ContextMenu.Root>
                )}
              </>
            )}
          </section>
        </div>

        <AnimatePresence>
          {selecting && (
            <motion.div
              className={styles.trIsland}
              role="toolbar"
              aria-label="所选项目的操作"
              style={{ x: "-50%" }}
              initial={{ y: 28, opacity: 0 }}
              animate={{ y: 0, opacity: 1 }}
              exit={{ y: 28, opacity: 0 }}
              transition={{ type: "spring", stiffness: 520, damping: 38 }}
            >
              {progress ? (
                <>
                  <span className={styles.trProgress} role="status">
                    <span className={styles.trSpinner} aria-hidden="true" />
                    {progress.label} {progress.done + 1}/{progress.total}…
                  </span>
                  <i style={{ width: `${((progress.done + 1) / progress.total) * 100}%` }} />
                </>
              ) : (
                <>
                  <button
                    type="button"
                    className={styles.icon}
                    aria-label="取消选择"
                    title="取消选择 (Esc)"
                    onClick={() => setSelected(new Set())}
                  >
                    <X />
                  </button>
                  <strong>已选 {chosen.length} 项</strong>
                  <span>{formatBytes(chosen.reduce((sum, item) => sum + item.sizeBytes, 0))}</span>
                  <hr />
                  <button type="button" className={styles.solid} onClick={() => void restoreItems(chosen)}>
                    <RotateCcw />
                    恢复
                  </button>
                  <button
                    type="button"
                    className={styles.risky}
                    onClick={() => setPurging({ items: chosen, everything: false })}
                  >
                    <Trash2 />
                    永久删除
                  </button>
                </>
              )}
            </motion.div>
          )}
        </AnimatePresence>

        {taken && (
          <TakenDialog
            item={taken}
            location={locationOf(taken, spaceOf(taken)).text}
            onClose={() => setTaken(undefined)}
            onGone={(message) => {
              setTaken(undefined);
              forget([taken]);
              toast(message, "error");
            }}
            onRestored={(restored, asked, choice) => {
              setTaken(undefined);
              restoredOne(taken, restored, asked, choice);
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
    </Tooltip.Provider>
  );
}

// Skeleton stands in for the list while the recycle bin is read.
function Skeleton() {
  return (
    <div className={styles.trSkeleton} role="status" aria-label="正在读取回收站">
      {["a", "b", "c", "d", "e", "f"].map((row) => (
        <div key={row}>
          <i />
          <span>
            <i />
            <i />
          </span>
          <i />
        </div>
      ))}
    </div>
  );
}

function EmptyBin() {
  return (
    <svg className={styles.trEmptyArt} viewBox="0 0 96 96" aria-hidden="true">
      <path d="M30 34h36l-2.6 38.2a6 6 0 0 1-6 5.8H38.6a6 6 0 0 1-6-5.8z" />
      <path d="M24 34h48M40 34v-5a4 4 0 0 1 4-4h8a4 4 0 0 1 4 4v5M42 46v20M54 46v20" />
      <path d="M74 14v8M70 18h8M18 52v6M15 55h6" />
    </svg>
  );
}
