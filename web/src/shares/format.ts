import { formatClock, calendarDays, relativeUnit, calendarDate, calendarLabel, calendarMoment } from "../lib/time";
import { errorText as requestErrorText } from "../api/errorMessages";
import type { Job, Recipient, Share, ShareEntry } from "./api";

// Paths inside a share are relative to the share, "资料/计划.txt".
export const leaf = (path: string) => path.split("/").at(-1) ?? path;
export const parentPath = (path: string) => path.split("/").slice(0, -1).join("/");
export const topLevel = (share: Share) => share.entries.filter((entry) => !parentPath(entry.path));
export const fileBytes = (entries: ShareEntry[]) =>
  entries.filter((entry) => !entry.isDir).reduce((sum, entry) => sum + entry.size, 0);

// shareTitle names a share by its first top-level item and how many there are.
export function shareTitle(share: Share): string {
  const top = topLevel(share);
  if (!top.length) return share.status === "preparing" ? "正在准备文件…" : "没有发出的分享";
  return top.length > 1 ? `${leaf(top[0].path)} 等 ${top.length} 项` : leaf(top[0].path);
}

// Phase is what the share allows now; the server only says "published"
// until it has expired.
export type Phase = "preparing" | "active" | "failed" | "revoked" | "expired";
export function phaseOf(share: Share, now = Date.now()): Phase {
  if (["preparing", "failed", "revoked", "expired"].includes(share.status)) return share.status as Phase;
  return share.expiresAt && new Date(share.expiresAt).getTime() <= now ? "expired" : "active";
}
export const isActive = (share: Share) => phaseOf(share) === "active";
export const phaseLabel: Record<Phase, string> = {
  preparing: "正在准备",
  active: "分享中",
  failed: "未能发出",
  revoked: "已撤回",
  expired: "已到期",
};

export const clock = formatClock;

// when is the short time a list shows: 17:08, 昨天, 10月18日.
export function when(value: string | undefined, now = new Date()): string {
  if (!value) return "";
  const days = calendarDays(value, now);
  if (days === 0) return formatClock(value);
  if (Math.abs(days) === 1) return relativeUnit(days, "day", "auto");
  return calendarDate(value, now);
}
// moment is a day and a time: 今天 17:08, 10月18日 09:30.
export function moment(value: string | undefined, now = new Date()): string {
  return value ? calendarMoment(value, now) : "";
}
// dayLabel separates a conversation by day.
export function dayLabel(value: string, now = new Date()): string {
  return calendarLabel(value, now, new Date(value).getFullYear() === now.getFullYear() ? "short" : undefined);
}

// remaining says how long an active share is still open; the last day is urgent.
export function remaining(share: Share, now = Date.now()): { text: string; urgent: boolean } {
  if (!share.expiresAt) return { text: "", urgent: false };
  const left = new Date(share.expiresAt).getTime() - now;
  if (left <= 0) return { text: "已到期", urgent: false };
  if (left < 3_600_000) return { text: "1 小时内到期", urgent: true };
  if (left < 86_400_000) return { text: `${Math.ceil(left / 3_600_000)} 小时后到期`, urgent: true };
  return { text: `还剩 ${Math.ceil(left / 86_400_000)} 天`, urgent: false };
}

// Unread is what a list row and the rail count: a received share not opened
// yet, or replies past the reader's own cursor.
export function unreadThreads(share: Share, userId: string): Recipient[] {
  const sender = share.sender.id === userId;
  return share.recipients.filter((item) => item.lastMessage > (sender ? item.senderReadThrough : item.readThrough));
}
export function isUnread(share: Share, userId: string): boolean {
  // A share that ended before it was opened is no longer news.
  if (
    share.sender.id !== userId &&
    phaseOf(share) === "active" &&
    share.recipients.some((item) => item.userId === userId && !item.viewedAt)
  )
    return true;
  return unreadThreads(share, userId).length > 0;
}

// receipt is what a sender sees about one recipient.
export function receipt(item: Recipient): { text: string; tone: "muted" | "seen" | "saved" | "uncertain" } {
  if (item.saved)
    return { text: `已保存 ${item.savedFiles} 个文件${item.saveUncertain ? "，另有结果待确认" : ""}`, tone: "saved" };
  if (item.saveUncertain) return { text: "保存结果待确认", tone: "uncertain" };
  return item.viewedAt ? { text: "已查看", tone: "seen" } : { text: "未查看", tone: "muted" };
}

export const jobLabel: Record<string, string> = {
  running: "正在保存",
  completed: "已保存",
  failed: "保存失败",
  cancelled: "已停止",
  interrupted: "保存中断",
};
export const jobDone = (job: Job) => job.completedIds?.length ?? 0;

// errorText turns a failed request into a sentence for the person using it.
export function errorText(error: unknown): string {
  return requestErrorText(error, "shares");
}

// The share service records why a share or a save stopped in English; the
// most specific match wins.
const reasons: [RegExp, string][] = [
  [/task state could not be recorded/i, "无法记录任务状态，已经完成的文件会保留"],
  [/^Publication could not be confirmed/i, "保存结果待确认，部分文件可能已写入"],
  [/^Save interrupted by service restart/i, "服务重启，保存中断；已经完成的文件会保留"],
  [/^Preparation interrupted by service restart/i, "服务重启，副本准备中断"],
  [/^Share service stopped/i, "分享服务已停止，任务中断"],
  [/^Sender account is unavailable/i, "分享者的账号已停用"],
  [/^Unable to persist prepared share/i, "副本已复制，但未能记录这次分享"],
  [/invalid or changed source content/i, "准备期间源文件有变化，请停止修改后重新分享"],
  [/permission denied/i, "没有相应文件或文件夹的权限"],
  [/session or account unavailable/i, "登录已失效，任务中断"],
  [/share withdrawn/i, "分享已撤回，任务停止"],
  [/operation stopped/i, "任务已停止"],
  [/^Unable to prepare share/i, "副本准备失败"],
  [/^Unable to save share/i, "保存失败"],
];
export function reasonText(text?: string): string {
  if (!text) return "";
  return reasons.find(([pattern]) => pattern.test(text))?.[1] ?? text;
}
