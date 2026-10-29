import {
  ArrowDown,
  ArrowDownUp,
  ArrowUp,
  ChevronDown,
  CircleAlert,
  CircleCheck,
  CopyPlus,
  File,
  FileArchive,
  FileImage,
  FileMusic,
  FileText,
  FileVideo,
  FolderOpen,
  FolderUp,
  HardDrive,
  History,
  Info,
  LogIn,
  Pause,
  Play,
  Replace,
  RotateCcw,
  SkipForward,
  Trash2,
  WifiOff,
  X,
} from "lucide-react";
import { type ReactNode, useEffect, useMemo, useRef, useState } from "react";

import { formatBytes, formatRate } from "../lib/bytes";
import type { TransferLocation } from "./api";
import {
  dayLabel,
  formatClock,
  formatRemaining,
  groupItems,
  groupStatus,
  holdText,
  type ItemStatus,
  isCurrent,
  locationParts,
  speedText,
  statusText,
  type TransferGroup,
  type TransferItem,
  transferItems,
  transferTotals,
} from "./model";
import notices from "./notices.module.css";
import { type ConflictChoice, type Hold, transferStore, useTransfers } from "./store";
import styles from "./transfers.module.css";

type View = "current" | "uploads" | "downloads" | "history";
type HistoryFilter = "all" | "upload" | "download" | "unfinished";

// visibleFiles bounds how many rows an expanded batch shows at first.
const visibleFiles = 100;

export function TransferCenter({
  onOpenFiles,
  onOpenLocation,
}: {
  onOpenFiles: () => void;
  onOpenLocation: (location: TransferLocation) => void;
}) {
  const snapshot = useTransfers();
  useEffect(() => transferStore.watch(), []);
  const [view, setView] = useState<View>("current");
  const [filter, setFilter] = useState<HistoryFilter>("all");
  const [notice, setNotice] = useState("");
  const items = useMemo(() => transferItems(snapshot), [snapshot]);
  const totals = useMemo(() => transferTotals(items, snapshot.rates), [items, snapshot.rates]);
  const current = items.filter(isCurrent);
  const history = items.filter((item) => !isCurrent(item));
  // A batch stays in progress, finished files included, until its last file
  // finishes; then all of it moves to the history.
  const groups = useMemo(() => {
    const scoped =
      view === "uploads"
        ? items.filter((item) => item.direction === "upload")
        : view === "downloads"
          ? items.filter((item) => item.direction === "download")
          : view === "history"
            ? items.filter(
                (item) =>
                  filter === "all" ||
                  (filter === "unfinished" ? item.status !== "completed" : item.direction === filter),
              )
            : items;
    return groupItems(scoped, snapshot.rates.batches).filter(
      (group) => group.items.some(isCurrent) !== (view === "history"),
    );
  }, [items, view, filter, snapshot.rates.batches]);
  const leftBehind = snapshot.server.filter(
    (transfer) =>
      transfer.direction === "upload" &&
      (transfer.state === "interrupted" || transfer.state === "paused") &&
      !snapshot.uploads.some((upload) => upload.transferId === transfer.id),
  );
  const running = snapshot.uploads.some((upload) => ["queued", "running", "waiting"].includes(upload.phase));
  const paused = snapshot.uploads.some((upload) => upload.phase === "paused");
  const titles: Record<View, [string, string]> = {
    current: ["正在传输", current.length ? `${current.length} 个任务` : "没有进行中的任务"],
    uploads: ["上传", totals.uploads ? `${totals.uploads} 个上传任务` : "没有进行中的上传"],
    downloads: ["下载", totals.downloads ? `${totals.downloads} 个下载任务` : "没有进行中的下载"],
    history: [
      "传输记录",
      history.length ? `最近 ${history.length} 条，保留 30 天` : "完成、失败和取消的传输会记录在这里",
    ],
  };

  return (
    <div className={`${styles.tcShell} ${notices.tcShell}`}>
      <aside className={styles.tcSide} aria-label="传输分类">
        <Overview totals={totals} active={current.length} />
        <nav className={styles.tcNav}>
          <NavItem
            icon={<ArrowDownUp />}
            tone="all"
            label="正在传输"
            count={current.length}
            active={view === "current"}
            onClick={() => setView("current")}
          />
          <NavItem
            icon={<ArrowUp />}
            tone="up"
            label="上传"
            count={totals.uploads}
            active={view === "uploads"}
            onClick={() => setView("uploads")}
          />
          <NavItem
            icon={<ArrowDown />}
            tone="down"
            label="下载"
            count={totals.downloads}
            active={view === "downloads"}
            onClick={() => setView("downloads")}
          />
          <p className={styles.tcNavLabel}>记录</p>
          <NavItem
            icon={<History />}
            tone="history"
            label="传输记录"
            count={history.length}
            muted
            active={view === "history"}
            onClick={() => setView("history")}
          />
        </nav>
        <div className={styles.tcSideFoot}>
          <Info />
          <span>关闭窗口不影响传输。刷新或关闭网页会中断本机上传，之后重新选择同一文件即可从中断处继续。</span>
        </div>
      </aside>

      <main className={styles.tcMain}>
        <header className={styles.tcHead}>
          <div>
            <h2>{titles[view][0]}</h2>
            <p>{titles[view][1]}</p>
          </div>
          <div className={styles.tcHeadActions}>
            {view !== "history" && paused && (
              <button
                type="button"
                className={`${styles.tcButton} ${notices.tcButton}`}
                onClick={() => transferStore.resumeAll()}
              >
                <Play />
                全部继续
              </button>
            )}
            {view !== "history" && running && (
              <button
                type="button"
                className={`${styles.tcButton} ${notices.tcButton}`}
                onClick={() => transferStore.pauseAll()}
              >
                <Pause />
                全部暂停
              </button>
            )}
            {view === "history" && history.length > 0 && (
              <button
                type="button"
                className={`${styles.tcButton} ${notices.tcButton}`}
                onClick={() => void transferStore.clearFinished()}
              >
                <Trash2 />
                清除记录
              </button>
            )}
          </div>
        </header>

        <div className={styles.tcScroll}>
          {view !== "history" && snapshot.holds.map((hold) => <HoldBanner key={hold} hold={hold} />)}
          {view !== "history" && view !== "downloads" && leftBehind.length > 0 && (
            <LeftBehind
              count={leftBehind.length}
              ids={leftBehind.map((transfer) => transfer.id)}
              onResult={setNotice}
            />
          )}
          {notice && (
            <div className={`${styles.tcBanner} ${styles.info}`} role="status">
              <span className={styles.tcBannerIcon}>
                <Info />
              </span>
              <div>
                <span>{notice}</span>
              </div>
              <button type="button" className={styles.tcIconButton} aria-label="关闭提示" onClick={() => setNotice("")}>
                <X />
              </button>
            </div>
          )}
          {view === "history" && history.length > 0 && (
            <div className={styles.tcFilters} role="radiogroup" aria-label="筛选传输记录">
              {(
                [
                  ["all", "全部"],
                  ["upload", "上传"],
                  ["download", "下载"],
                  ["unfinished", "失败与取消"],
                ] as Array<[HistoryFilter, string]>
              ).map(([value, label]) => (
                <button
                  key={value}
                  type="button"
                  role="radio"
                  aria-checked={filter === value}
                  className={filter === value ? styles.active : ""}
                  onClick={() => setFilter(value)}
                >
                  {label}
                </button>
              ))}
            </div>
          )}
          {!snapshot.loaded && !items.length ? (
            <div className={styles.tcEmpty}>
              <span className="loader" />
              <strong>正在读取传输…</strong>
            </div>
          ) : !groups.length ? (
            <Empty view={view} onOpenFiles={onOpenFiles} />
          ) : view === "history" ? (
            <HistoryList groups={groups} onOpenLocation={onOpenLocation} />
          ) : (
            <div className={styles.tcList}>
              {groups.map((group) =>
                group.batch && group.items.length > 1 ? (
                  <BatchCard key={group.key} group={group} onOpenLocation={onOpenLocation} />
                ) : (
                  <ItemRow key={group.key} item={group.items[0]} onOpenLocation={onOpenLocation} />
                ),
              )}
            </div>
          )}
        </div>
      </main>
    </div>
  );
}

function Overview({ totals, active }: { totals: ReturnType<typeof transferTotals>; active: number }) {
  const percent = Math.round(totals.progress * 100);
  const speed = totals.uploadSpeed + totals.downloadSpeed;
  return (
    <section className={`${styles.tcOverview} ${active ? styles.busy : ""}`} aria-label="传输概况">
      <div
        className={styles.tcRing}
        role="progressbar"
        aria-label="总体进度"
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={active ? percent : undefined}
      >
        <svg viewBox="0 0 44 44" aria-hidden="true">
          <circle className={`${styles.track} ${notices.track}`} cx="22" cy="22" r="18" />
          <circle
            className={`${styles.value} ${notices.value}`}
            cx="22"
            cy="22"
            r="18"
            pathLength={100}
            strokeDasharray={`${active ? percent : 0} 100`}
          />
        </svg>
        <span>{active ? `${percent}%` : <CircleCheck />}</span>
      </div>
      <div className={styles.tcOverviewCopy}>
        <strong>{active ? `${active} 个任务` : "全部完成"}</strong>
        <span className={styles.up}>
          <ArrowUp />
          {formatRate(totals.uploadSpeed)}
        </span>
        <span className={styles.down}>
          <ArrowDown />
          {formatRate(totals.downloadSpeed)}
        </span>
        {active > 0 && totals.remainingBytes > 0 && (
          <small>
            剩余{" "}
            {[formatBytes(totals.remainingBytes), formatRemaining(totals.remainingBytes, speed)]
              .filter(Boolean)
              .join(" · ")}
          </small>
        )}
      </div>
    </section>
  );
}

function NavItem({
  icon,
  tone,
  label,
  count,
  active,
  muted,
  onClick,
}: {
  icon: ReactNode;
  tone: string;
  label: string;
  count: number;
  active: boolean;
  muted?: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      className={`${styles.tcNavItem} ${active ? styles.active : ""}`}
      aria-current={active ? "page" : undefined}
      onClick={onClick}
    >
      <span className={`${styles.tcTile} ${styles[tone] ?? ""}`}>{icon}</span>
      <span>{label}</span>
      {count > 0 && <span className={`${styles.tcCount} ${muted ? styles.muted : ""}`}>{count}</span>}
    </button>
  );
}

function HoldBanner({ hold }: { hold: Hold }) {
  const icon = { offline: <WifiOff />, session: <LogIn />, volume: <HardDrive />, space: <HardDrive /> }[hold];
  const text = holdText[hold];
  return (
    <div
      className={`${styles.tcBanner} ${hold === "space" || hold === "volume" ? `${styles.danger} ${notices.danger}` : `${styles.warning} ${notices.warning}`}`}
      role="status"
    >
      <span className={styles.tcBannerIcon}>{icon}</span>
      <div>
        <strong>{text.title}</strong>
        <span>{text.detail}</span>
      </div>
      {hold !== "session" && (
        <button
          type="button"
          className={`${styles.tcButton} ${notices.tcButton}`}
          onClick={() => transferStore.release(hold)}
        >
          <RotateCcw />
          {hold === "space" ? "继续上传" : "立即重试"}
        </button>
      )}
    </div>
  );
}

// LeftBehind offers to continue uploads that a closed or reloaded page
// stopped: picking the same files again resumes them where they stopped.
function LeftBehind({ count, ids, onResult }: { count: number; ids: string[]; onResult: (message: string) => void }) {
  const input = useRef<HTMLInputElement>(null);
  return (
    <div className={`${styles.tcBanner} ${styles.info}`} role="status">
      <span className={styles.tcBannerIcon}>
        <FolderUp />
      </span>
      <div>
        <strong>{count} 个上传在上次中断</strong>
        <span>浏览器不能自动读取原来的文件。重新选择相同的文件，上传会从中断处继续，已上传的部分不必重传。</span>
      </div>
      <button
        type="button"
        className={`${styles.tcButton} ${notices.tcButton} primary`}
        onClick={() => input.current?.click()}
      >
        <Play />
        选择文件继续
      </button>
      <button
        type="button"
        className={`${styles.tcButton} ${notices.tcButton}`}
        onClick={() => transferStore.abandon(ids)}
      >
        全部放弃
      </button>
      <input
        ref={input}
        type="file"
        multiple
        hidden
        onChange={(event) => {
          const files = Array.from(event.target.files ?? []);
          event.target.value = "";
          if (!files.length) return;
          const { matched, unmatched } = transferStore.reattach(files);
          onResult(
            matched
              ? `已继续 ${matched} 个上传${unmatched ? `，${unmatched} 个文件与中断的上传不一致` : ""}。`
              : "选择的文件与中断的上传不一致：名称、大小和修改时间都需要相同。",
          );
        }}
      />
    </div>
  );
}

function BatchCard({
  group,
  onOpenLocation,
}: {
  group: TransferGroup;
  onOpenLocation: (location: TransferLocation) => void;
}) {
  const [open, setOpen] = useState(false);
  const [limit, setLimit] = useState(visibleFiles);
  const [remember, setRemember] = useState(true);
  const batch = group.batch!;
  const status = groupStatus(group);
  const count = group.items.length;
  const done = group.counts.completed ?? 0;
  const failed = group.counts.failed ?? 0;
  const conflicts = group.items.filter((item) => item.status === "conflict");
  const moving = (group.counts.running ?? 0) + (group.counts.queued ?? 0) + (group.counts.waiting ?? 0);
  const percent = group.total ? Math.round((group.transferred / group.total) * 100) : done === count ? 100 : 0;
  const order: ItemStatus[] = [
    "conflict",
    "failed",
    "running",
    "waiting",
    "queued",
    "paused",
    "interrupted",
    "completed",
    "skipped",
    "canceled",
  ];
  const sorted = [...group.items].sort((left, right) => order.indexOf(left.status) - order.indexOf(right.status));
  const answer = (choice: ConflictChoice) => transferStore.resolveConflicts(batch, choice, remember);
  const local = group.items.some((item) => item.local);
  return (
    <section className={`${styles.tcBatch} ${styles[status] ?? ""}`} aria-label={`上传 ${count} 个文件`}>
      <div className={styles.tcBatchHead}>
        <span className={`${styles.tcFileTile} ${styles.batch}`}>
          <FolderUp />
          <i className={styles.up}>
            <ArrowUp />
          </i>
        </span>
        <div className={styles.tcBatchCopy}>
          <div className={styles.tcTitleLine}>
            <strong>上传 {count} 个文件</strong>
            <StatusChip status={status} />
          </div>
          {group.label ? (
            <span className={styles.tcLocation}>{group.label}</span>
          ) : (
            <Location prefix="到" location={group.location} onOpen={onOpenLocation} />
          )}
        </div>
        <div className={styles.tcActions}>
          {local && moving > 0 && (
            <IconButton label="暂停这一批" onClick={() => transferStore.pauseBatch(batch)}>
              <Pause />
            </IconButton>
          )}
          {local && (group.counts.paused ?? 0) > 0 && (
            <IconButton label="继续这一批" onClick={() => transferStore.resumeBatch(batch)}>
              <Play />
            </IconButton>
          )}
          {local && (moving > 0 || (group.counts.paused ?? 0) > 0 || conflicts.length > 0) && (
            <IconButton label="取消这一批" danger onClick={() => transferStore.cancelBatch(batch)}>
              <X />
            </IconButton>
          )}
          <IconButton label={open ? "收起文件" : "展开文件"} onClick={() => setOpen(!open)}>
            <ChevronDown className={open ? styles.flipped : ""} />
          </IconButton>
        </div>
      </div>
      <Progress percent={percent} status={status} />
      <div className={styles.tcMeta}>
        <span>
          {formatBytes(group.transferred)} / {formatBytes(group.total)}
        </span>
        <span>
          已完成 {done}
          {failed ? <em className={`${styles.danger} ${notices.danger}`}> · 失败 {failed}</em> : null}
          {count - done - failed > 0 ? ` · 剩余 ${count - done - failed}` : ""}
        </span>
        {group.speed > 0 && (
          <span className={styles.tcSpeed}>{speedText(group.speed, group.total - group.transferred)}</span>
        )}
      </div>
      {conflicts.length > 0 && (
        <ConflictBar
          title={`${conflicts.length} 个文件与目标位置的文件同名`}
          detail={`${conflicts
            .slice(0, 3)
            .map((item) => item.name)
            .join("、")}${conflicts.length > 3 ? " 等" : ""}`}
          onAnswer={answer}
        >
          <label className={styles.tcRemember}>
            <input type="checkbox" checked={remember} onChange={(event) => setRemember(event.target.checked)} />
            这一批之后的同名文件也这样处理
          </label>
        </ConflictBar>
      )}
      {open && (
        <div className={styles.tcBatchFiles}>
          {sorted.slice(0, limit).map((item) => (
            <ItemRow key={item.id} item={item} compact onOpenLocation={onOpenLocation} />
          ))}
          {sorted.length > limit && (
            <button type="button" className={styles.tcMore} onClick={() => setLimit(limit + visibleFiles)}>
              再显示 {Math.min(visibleFiles, sorted.length - limit)} 个（共 {sorted.length} 个）
            </button>
          )}
        </div>
      )}
    </section>
  );
}

function ItemRow({
  item,
  compact,
  onOpenLocation,
}: {
  item: TransferItem;
  compact?: boolean;
  onOpenLocation: (location: TransferLocation) => void;
}) {
  const percent = item.total
    ? Math.round((Math.min(item.transferred, item.total) / item.total) * 100)
    : item.status === "completed"
      ? 100
      : 0;
  return (
    <article
      className={`${styles.tcRow} ${styles[item.status] ?? ""} ${compact ? styles.compact : ""}`}
      aria-label={`${item.direction === "upload" ? "上传" : "下载"} ${item.name}`}
    >
      <FileTile item={item} />
      <div className={styles.tcRowBody}>
        <div className={styles.tcTitleLine}>
          <strong title={item.name}>{item.name}</strong>
          {item.status !== "running" && <StatusChip status={item.status} />}
        </div>
        {!compact &&
          (item.label ? (
            <span className={styles.tcLocation}>{item.label}</span>
          ) : (
            <Location
              prefix={item.direction === "upload" ? "到" : "来自"}
              location={item.location}
              relativePath={item.relativePath}
              onOpen={onOpenLocation}
            />
          ))}
        <Progress percent={percent} status={item.status} />
        <div className={styles.tcMeta}>
          <span>
            {item.status === "completed"
              ? formatBytes(item.total)
              : `${formatBytes(item.transferred)} / ${formatBytes(item.total)}`}
          </span>
          <RowDetail item={item} />
        </div>
      </div>
      <div className={styles.tcActions}>
        <RowActions item={item} />
      </div>
      {item.status === "conflict" && item.local && !compact && (
        <ConflictBar
          title="目标位置已有同名文件"
          detail="选择如何处理这个文件；替换时原文件会移到回收站。"
          onAnswer={(choice) => transferStore.resolveConflict(item.local!.key, choice)}
        />
      )}
    </article>
  );
}

function ConflictBar({
  title,
  detail,
  onAnswer,
  children,
}: {
  title: string;
  detail: string;
  onAnswer: (choice: ConflictChoice) => void;
  children?: ReactNode;
}) {
  return (
    <div className={styles.tcConflict} role="group" aria-label="同名文件">
      <CircleAlert />
      <div>
        <strong>{title}</strong>
        <span>{detail}</span>
      </div>
      <div className={styles.tcConflictActions}>
        <button type="button" className={`${styles.tcButton} ${notices.tcButton}`} onClick={() => onAnswer("skip")}>
          <SkipForward />
          跳过
        </button>
        <button type="button" className={`${styles.tcButton} ${notices.tcButton}`} onClick={() => onAnswer("rename")}>
          <CopyPlus />
          保留两者
        </button>
        <button
          type="button"
          className={`${styles.tcButton} ${notices.tcButton} ${styles.warning} ${notices.warning}`}
          onClick={() => onAnswer("replace")}
        >
          <Replace />
          替换
        </button>
      </div>
      {children}
    </div>
  );
}

function RowDetail({ item }: { item: TransferItem }) {
  if (item.status === "running") {
    return (
      <>
        <span className={styles.tcSpeed}>
          {item.speed > 0 ? speedText(item.speed, item.total - item.transferred) : "正在连接…"}
        </span>
        {item.restarted && (
          <span className={`${styles.warning} ${notices.warning}`}>NAS 丢失了进度，已从头重新上传</span>
        )}
      </>
    );
  }
  if (item.failure && (item.status === "failed" || item.status === "canceled"))
    return <span className={`${styles.danger} ${notices.danger}`}>{item.failure}</span>;
  if (item.status === "waiting" && item.hold)
    return <span className={`${styles.warning} ${notices.warning}`}>{holdText[item.hold].title}</span>;
  if (item.status === "conflict")
    return <span className={`${styles.warning} ${notices.warning}`}>目标位置已有同名文件</span>;
  if (item.status === "interrupted" && item.direction === "upload" && !item.local)
    return <span className={`${styles.warning} ${notices.warning}`}>在其他页面中断，可选择文件继续</span>;
  if (item.status === "interrupted")
    return <span className={`${styles.warning} ${notices.warning}`}>连接中断，浏览器可继续下载</span>;
  if (item.status === "pending") return <span>等待浏览器开始下载</span>;
  if (item.restarted && item.status !== "completed")
    return <span className={`${styles.warning} ${notices.warning}`}>NAS 丢失了进度，已从头开始</span>;
  return null;
}

function RowActions({ item }: { item: TransferItem }) {
  const local = item.local;
  if (item.external)
    return item.status !== "running" ? (
      <IconButton label="从列表移除" onClick={() => transferStore.dismiss(item.id)}>
        <X />
      </IconButton>
    ) : null;
  if (local) {
    return (
      <>
        {(item.status === "running" || item.status === "queued" || item.status === "waiting") && (
          <IconButton label="暂停" onClick={() => transferStore.pause(local.key)}>
            <Pause />
          </IconButton>
        )}
        {item.status === "paused" && (
          <IconButton label="继续" onClick={() => transferStore.resume(local.key)}>
            <Play />
          </IconButton>
        )}
        {item.status === "failed" && (
          <IconButton label="重试" onClick={() => transferStore.resume(local.key)}>
            <RotateCcw />
          </IconButton>
        )}
        {["running", "queued", "waiting", "paused", "conflict", "failed"].includes(item.status) && (
          <IconButton label="取消" danger onClick={() => transferStore.cancel(local.key)}>
            <X />
          </IconButton>
        )}
        {["completed", "canceled", "skipped"].includes(item.status) && (
          <IconButton label="从列表移除" onClick={() => transferStore.dismiss(item.id)}>
            <X />
          </IconButton>
        )}
      </>
    );
  }
  const server = item.server!;
  return (
    <>
      {server.direction === "download" && ["interrupted", "failed", "canceled", "completed"].includes(server.state) && (
        <IconButton label="重新下载" onClick={() => transferStore.retryDownload(server)}>
          <RotateCcw />
        </IconButton>
      )}
      {!server.finishedAt && (
        <IconButton label="取消" danger onClick={() => void transferStore.cancelServer(server.id)}>
          <X />
        </IconButton>
      )}
      {server.finishedAt && (
        <IconButton label="从列表移除" onClick={() => transferStore.dismiss(server.id)}>
          <X />
        </IconButton>
      )}
    </>
  );
}

function HistoryList({
  groups,
  onOpenLocation,
}: {
  groups: TransferGroup[];
  onOpenLocation: (location: TransferLocation) => void;
}) {
  const days = new Map<string, TransferGroup[]>();
  for (const group of [...groups].sort(
    (left, right) => (right.finishedAt ?? right.createdAt) - (left.finishedAt ?? left.createdAt),
  )) {
    const label = dayLabel(group.finishedAt ?? group.createdAt);
    days.set(label, [...(days.get(label) ?? []), group]);
  }
  return (
    <div className={styles.tcHistory}>
      {[...days].map(([day, entries]) => (
        <section key={day} aria-label={day}>
          <h3>{day}</h3>
          <div className={styles.tcHistoryCard}>
            {entries.map((group) =>
              group.batch && group.items.length > 1 ? (
                <HistoryBatch key={group.key} group={group} onOpenLocation={onOpenLocation} />
              ) : (
                <HistoryRow key={group.key} item={group.items[0]} onOpenLocation={onOpenLocation} />
              ),
            )}
          </div>
        </section>
      ))}
    </div>
  );
}

function HistoryRow({
  item,
  onOpenLocation,
}: {
  item: TransferItem;
  onOpenLocation: (location: TransferLocation) => void;
}) {
  const notes: string[] = [];
  if (item.savedName) notes.push(`已保存为“${item.savedName}”`);
  if (item.replaced) notes.push("原文件已移到回收站");
  if (item.skipped) notes.push(`${item.skipped} 个文件无法读取，未打包`);
  if (item.failure && item.status !== "completed") notes.push(item.failure);
  return (
    <div className={`${styles.tcHistoryRow} ${styles[item.status] ?? ""}`}>
      <FileTile item={item} />
      <div className={styles.tcRowBody}>
        <div className={styles.tcTitleLine}>
          <strong title={item.name}>{item.name}</strong>
          {item.status !== "completed" && <StatusChip status={item.status} />}
        </div>
        <div className={styles.tcHistorySub}>
          {item.label ? (
            <span className={styles.tcLocation}>{item.label}</span>
          ) : (
            <Location
              prefix={item.direction === "upload" ? "上传到" : "下载自"}
              location={item.location}
              relativePath={item.relativePath}
              onOpen={onOpenLocation}
            />
          )}
          {notes.length > 0 && (
            <span className={item.status === "completed" ? "" : `${styles.danger} ${notices.danger}`}>
              {notes.join(" · ")}
            </span>
          )}
        </div>
      </div>
      <span className={styles.tcHistorySize}>{formatBytes(item.total)}</span>
      <time>{formatClock(item.finishedAt ?? item.createdAt)}</time>
      <div className={styles.tcActions}>
        <RowActions item={item} />
      </div>
    </div>
  );
}

function HistoryBatch({
  group,
  onOpenLocation,
}: {
  group: TransferGroup;
  onOpenLocation: (location: TransferLocation) => void;
}) {
  const [open, setOpen] = useState(false);
  const done = group.counts.completed ?? 0;
  const other = group.items.length - done;
  return (
    <div className="tc-history-batch">
      <div className={`${styles.tcHistoryRow} ${other ? `${styles.failed} ${notices.failed}` : styles.completed}`}>
        <span className={`${styles.tcFileTile} ${styles.batch}`}>
          <FolderUp />
          <i className={styles.up}>
            <ArrowUp />
          </i>
        </span>
        <div className={styles.tcRowBody}>
          <div className={styles.tcTitleLine}>
            <strong>上传 {group.items.length} 个文件</strong>
            {other > 0 && <StatusChip status="failed" label={`${other} 个未完成`} />}
          </div>
          <div className={styles.tcHistorySub}>
            {group.label ? (
              <span className={styles.tcLocation}>{group.label}</span>
            ) : (
              <Location prefix="上传到" location={group.location} onOpen={onOpenLocation} />
            )}
          </div>
        </div>
        <span className={styles.tcHistorySize}>{formatBytes(group.total)}</span>
        <time>{formatClock(group.finishedAt ?? group.createdAt)}</time>
        <div className={styles.tcActions}>
          <IconButton label={open ? "收起文件" : "展开文件"} onClick={() => setOpen(!open)}>
            <ChevronDown className={open ? styles.flipped : ""} />
          </IconButton>
        </div>
      </div>
      {open && (
        <div className={styles.tcHistoryChildren}>
          {group.items.map((item) => (
            <HistoryRow key={item.id} item={item} onOpenLocation={onOpenLocation} />
          ))}
        </div>
      )}
    </div>
  );
}

function Location({
  prefix,
  location,
  relativePath,
  onOpen,
}: {
  prefix: string;
  location?: TransferLocation;
  relativePath?: string;
  onOpen: (location: TransferLocation) => void;
}) {
  const parts = locationParts(location);
  if (!location || !parts.length) return null;
  return (
    <button
      type="button"
      className={`${styles.tcLocation} ${styles.link}`}
      title={`在文件管理中打开：${parts.join(" / ")}`}
      onClick={() => onOpen(location)}
    >
      <FolderOpen />
      <span>
        {prefix} {parts.join(" › ")}
      </span>
      {relativePath && <small>（文件夹上传）</small>}
    </button>
  );
}

function FileTile({ item }: { item: TransferItem }) {
  const extension = item.name.includes(".") ? item.name.split(".").pop()!.toLowerCase() : "";
  const [Icon, tone] =
    item.kind === "archive"
      ? [FileArchive, "archive"]
      : ["jpg", "jpeg", "png", "gif", "webp", "heic", "bmp", "avif"].includes(extension)
        ? [FileImage, "image"]
        : ["mp4", "mkv", "mov", "avi", "webm", "m4v", "ts"].includes(extension)
          ? [FileVideo, "video"]
          : ["mp3", "flac", "wav", "m4a", "aac", "ogg", "ape"].includes(extension)
            ? [FileMusic, "audio"]
            : ["txt", "md", "pdf", "doc", "docx", "xls", "xlsx", "ppt", "pptx", "csv"].includes(extension)
              ? [FileText, "document"]
              : [File, "other"];
  return (
    <span className={`${styles.tcFileTile} ${styles[tone] ?? ""}`}>
      <Icon />
      <i className={item.direction === "upload" ? styles.up : styles.down}>
        {item.direction === "upload" ? <ArrowUp /> : <ArrowDown />}
      </i>
    </span>
  );
}

function Progress({ percent, status }: { percent: number; status: ItemStatus }) {
  return (
    <div
      className={`${styles.tcProgress} ${styles[status] ?? ""}`}
      role="progressbar"
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={percent}
    >
      <span style={{ width: `${percent}%` }} />
    </div>
  );
}

function StatusChip({ status, label }: { status: ItemStatus; label?: string }) {
  return <span className={`${styles.tcChip} ${styles[status] ?? ""}`}>{label ?? statusText[status]}</span>;
}

function IconButton({
  label,
  danger,
  onClick,
  children,
}: {
  label: string;
  danger?: boolean;
  onClick: () => void;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      className={`${styles.tcIconButton} ${danger ? `${styles.danger} ${notices.danger}` : ""}`}
      aria-label={label}
      title={label}
      onClick={onClick}
    >
      {children}
    </button>
  );
}

function Empty({ view, onOpenFiles }: { view: View; onOpenFiles: () => void }) {
  const history = view === "history";
  return (
    <div className={styles.tcEmpty}>
      <span className={styles.tcEmptyArt}>{history ? <History /> : <ArrowDownUp />}</span>
      <strong>
        {history
          ? "还没有传输记录"
          : view === "downloads"
            ? "没有正在进行的下载"
            : view === "uploads"
              ? "没有正在进行的上传"
              : "没有正在进行的传输"}
      </strong>
      <span>
        {history
          ? "上传和下载完成后会出现在这里，记录保留 30 天。"
          : "在文件管理中上传文件或文件夹、下载文件，进度和异常都会显示在这里。"}
      </span>
      {!history && (
        <button type="button" className={`${styles.tcButton} ${notices.tcButton} primary`} onClick={onOpenFiles}>
          <FolderOpen />
          打开文件管理
        </button>
      )}
    </div>
  );
}
