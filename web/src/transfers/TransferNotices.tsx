import {
  ArrowDown,
  ArrowDownUp,
  ArrowUp,
  CircleAlert,
  CircleCheck,
  CopyPlus,
  HardDrive,
  LogIn,
  Replace,
  RotateCcw,
  SkipForward,
  WifiOff,
  X,
} from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";

import { formatRate } from "../bytes";
import {
  holdText,
  isActive,
  isCurrent,
  type ItemStatus,
  locationParts,
  statusText,
  transferItems,
  transferTotals,
} from "./model";
import { type Hold, transferStore, useTransfers } from "./store";
import "./notices.css";

// listOrder puts what moves first in the indicator's list.
const listOrder: ItemStatus[] = [
  "running",
  "conflict",
  "waiting",
  "failed",
  "pending",
  "queued",
  "paused",
  "interrupted",
];
const listed = 6;

// TransferIndicator sits in the status bar while anything moves, so the
// transfer center is one click away. It names the file being sent and how
// many are left; hovering lists the first tasks.
export function TransferIndicator({ onOpen }: { onOpen: () => void }) {
  const snapshot = useTransfers();
  const items = useMemo(() => transferItems(snapshot), [snapshot]);
  const totals = useMemo(() => transferTotals(items, snapshot.rates), [items, snapshot.rates]);
  const current = useMemo(
    () =>
      items.filter(isCurrent).sort((left, right) => listOrder.indexOf(left.status) - listOrder.indexOf(right.status)),
    [items],
  );
  if (!current.length) return null;
  const percent = Math.round(totals.progress * 100);
  const attention = totals.attention > 0 || snapshot.holds.length > 0;
  const lead = current[0];
  const left = current.filter((item) => isActive(item.status)).length;
  const others = current.length - 1;
  const speed = totals.uploadSpeed + totals.downloadSpeed;
  const label = `传输中心：${lead.direction === "upload" ? "正在上传" : "正在下载"} ${lead.name}${others ? ` 等 ${current.length} 个任务` : ""}，${attention ? "有任务需要处理" : `已完成 ${percent}%`}`;
  const Direction = lead.direction === "upload" ? ArrowUp : ArrowDown;
  return (
    <div className="status-transfers-wrap">
      <button
        type="button"
        className={`status-transfers ${attention ? "attention" : ""}`}
        aria-label={label}
        onClick={onOpen}
      >
        <span className="status-transfers-ring" aria-hidden="true">
          <svg viewBox="0 0 36 36">
            <circle className="track" cx="18" cy="18" r="15" />
            <circle className="value" cx="18" cy="18" r="15" pathLength={100} strokeDasharray={`${percent} 100`} />
          </svg>
          <ArrowDownUp />
        </span>
        <span className="status-transfers-copy" aria-hidden="true">
          <strong>
            <Direction />
            {lead.name}
          </strong>
          <small>
            {attention
              ? `${totals.attention || snapshot.holds.length} 个需处理`
              : left > 1
                ? `还剩 ${left} 个 · ${percent}%`
                : `${percent}%`}
          </small>
        </span>
      </button>
      <div className="status-transfers-pop" aria-hidden="true">
        <header>
          <strong>{current.length} 个任务</strong>
          {speed > 0 && <span>{formatRate(speed)}</span>}
        </header>
        <ul>
          {current.slice(0, listed).map((item) => {
            const share = item.total ? Math.min(100, Math.round((item.transferred / item.total) * 100)) : 0;
            return (
              <li key={item.id} className={item.status}>
                <span className="name">
                  {item.direction === "upload" ? <ArrowUp /> : <ArrowDown />}
                  {item.name}
                </span>
                <span className="state">{item.status === "running" ? `${share}%` : statusText[item.status]}</span>
                <span className="bar">
                  <i style={{ width: `${share}%` }} />
                </span>
              </li>
            );
          })}
        </ul>
        <footer>{current.length > listed ? `还有 ${current.length - listed} 个，` : ""}点击打开传输中心</footer>
      </div>
    </div>
  );
}

const holdIcons: Record<Hold, typeof WifiOff> = {
  offline: WifiOff,
  session: LogIn,
  volume: HardDrive,
  space: HardDrive,
};

// TransferToasts raise what needs a decision while the transfer center is
// not on screen, and say when a batch finished.
export function TransferToasts({ hidden, onOpen }: { hidden: boolean; onOpen: () => void }) {
  const snapshot = useTransfers();
  const [finished, setFinished] = useState<Array<{ batch: string; done: number; failed: number; place: string }>>([]);
  const announced = useRef(new Set<string>());
  const [dismissed, setDismissed] = useState<Hold[]>([]);

  useEffect(() => {
    const fresh: typeof finished = [];
    for (const batch of snapshot.batches) {
      const uploads = snapshot.uploads.filter((upload) => upload.batch === batch.id);
      if (!uploads.length || announced.current.has(batch.id)) continue;
      if (uploads.some((upload) => ["queued", "running", "paused", "waiting", "conflict"].includes(upload.phase)))
        continue;
      announced.current.add(batch.id);
      const done = uploads.filter((upload) => upload.phase === "completed").length;
      const failed = uploads.filter((upload) => upload.phase === "failed").length;
      if (done + failed)
        fresh.push({
          batch: batch.id,
          done,
          failed,
          place: batch.target.label ?? locationParts(batch.target).join(" › "),
        });
    }
    if (fresh.length) setFinished((current) => [...current, ...fresh]);
  }, [snapshot.batches, snapshot.uploads]);

  // A hold that cleared may be raised again later.
  useEffect(() => {
    setDismissed((current) => current.filter((item) => snapshot.holds.includes(item)));
  }, [snapshot.holds]);

  useEffect(() => {
    if (!finished.length) return;
    const timer = setTimeout(() => setFinished((current) => current.slice(1)), 6000);
    return () => clearTimeout(timer);
  }, [finished]);

  const conflicts = snapshot.batches
    .map((batch) => ({
      batch,
      names: snapshot.uploads
        .filter((upload) => upload.batch === batch.id && upload.phase === "conflict")
        .map((upload) => upload.name),
    }))
    .filter((entry) => entry.names.length > 0);
  const hold = snapshot.holds.find((item) => !dismissed.includes(item));
  if (hidden || (!conflicts.length && !hold && !finished.length)) return null;

  return (
    <div className="tc-toasts" aria-live="polite">
      {conflicts.slice(0, 1).map(({ batch, names }) => (
        <section key={batch.id} className="tc-toast warning" role="alertdialog" aria-label="同名文件">
          <CircleAlert className="tc-toast-icon" />
          <div className="tc-toast-copy">
            <strong>{names.length} 个文件已存在</strong>
            <span>
              {batch.target.label ?? locationParts(batch.target).join(" › ")} 中已有 {names.slice(0, 2).join("、")}
              {names.length > 2 ? " 等文件" : ""}。这一批的同名文件都按你的选择处理。
            </span>
            <div className="tc-toast-actions">
              <button
                type="button"
                className="tc-button"
                onClick={() => transferStore.resolveConflicts(batch.id, "skip", true)}
              >
                <SkipForward />
                跳过
              </button>
              <button
                type="button"
                className="tc-button"
                onClick={() => transferStore.resolveConflicts(batch.id, "rename", true)}
              >
                <CopyPlus />
                保留两者
              </button>
              <button
                type="button"
                className="tc-button warning"
                onClick={() => transferStore.resolveConflicts(batch.id, "replace", true)}
              >
                <Replace />
                替换
              </button>
            </div>
          </div>
        </section>
      ))}
      {hold &&
        (() => {
          const Icon = holdIcons[hold];
          return (
            <section
              className={`tc-toast ${hold === "space" || hold === "volume" ? "danger" : "warning"}`}
              role="status"
            >
              <Icon className="tc-toast-icon" />
              <div className="tc-toast-copy">
                <strong>{holdText[hold].title}</strong>
                <span>{holdText[hold].detail}</span>
                <div className="tc-toast-actions">
                  {hold !== "session" && (
                    <button type="button" className="tc-button" onClick={() => transferStore.release(hold)}>
                      <RotateCcw />
                      {hold === "space" ? "继续上传" : "立即重试"}
                    </button>
                  )}
                  <button type="button" className="tc-button" onClick={onOpen}>
                    查看传输
                  </button>
                </div>
              </div>
              <button
                type="button"
                className="tc-toast-close"
                aria-label="关闭提示"
                onClick={() => setDismissed([...dismissed, hold])}
              >
                <X />
              </button>
            </section>
          );
        })()}
      {finished.slice(0, 1).map((entry) => (
        <section key={entry.batch} className={`tc-toast ${entry.failed ? "danger" : "success"}`} role="status">
          {entry.failed ? <CircleAlert className="tc-toast-icon" /> : <CircleCheck className="tc-toast-icon" />}
          <div className="tc-toast-copy">
            <strong>{entry.failed ? `${entry.failed} 个文件未能上传` : `已上传 ${entry.done} 个文件`}</strong>
            <span>{entry.failed ? `另有 ${entry.done} 个已上传到 ${entry.place}。` : `保存在 ${entry.place}。`}</span>
            <div className="tc-toast-actions">
              <button type="button" className="tc-button" onClick={onOpen}>
                查看传输
              </button>
            </div>
          </div>
          <button
            type="button"
            className="tc-toast-close"
            aria-label="关闭提示"
            onClick={() => setFinished((current) => current.filter((item) => item.batch !== entry.batch))}
          >
            <X />
          </button>
        </section>
      ))}
    </div>
  );
}
