import { APIError } from "../api";
import type { Job, Recipient, Share, ShareEntry } from "./api";

// Paths inside a share are relative to the share, "资料/计划.txt".
export const leaf = (path: string) => path.split("/").at(-1) ?? path;
export const parentPath = (path: string) => path.split("/").slice(0, -1).join("/");
export const topLevel = (share: Share) => share.entries.filter((entry) => !parentPath(entry.path));
export const fileBytes = (entries: ShareEntry[]) => entries.filter((entry) => !entry.isDir).reduce((sum, entry) => sum + entry.size, 0);

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
export const phaseLabel: Record<Phase, string> = { preparing: "正在准备", active: "分享中", failed: "未能发出", revoked: "已撤回", expired: "已到期" };

const pad = (value: number) => String(value).padStart(2, "0");
const startOfDay = (date: Date) => new Date(date.getFullYear(), date.getMonth(), date.getDate()).getTime();
const daysAgo = (date: Date, now: Date) => Math.round((startOfDay(now) - startOfDay(date)) / 86_400_000);
export const clock = (value: string) => { const date = new Date(value); return `${pad(date.getHours())}:${pad(date.getMinutes())}`; };

// when is the short time a list shows: 17:08, 昨天, 10月18日.
export function when(value: string | undefined, now = new Date()): string {
  if (!value) return "";
  const date = new Date(value), days = daysAgo(date, now);
  if (days <= 0) return clock(value);
  if (days === 1) return "昨天";
  if (date.getFullYear() === now.getFullYear()) return `${date.getMonth() + 1}月${date.getDate()}日`;
  return `${date.getFullYear()}/${date.getMonth() + 1}/${date.getDate()}`;
}
// moment is a day and a time: 今天 17:08, 10月18日 09:30.
export function moment(value: string | undefined, now = new Date()): string {
  if (!value) return "";
  const date = new Date(value), days = daysAgo(date, now);
  const day = days === 0 ? "今天" : days === 1 ? "昨天" : days === -1 ? "明天" : date.getFullYear() === now.getFullYear() ? `${date.getMonth() + 1}月${date.getDate()}日` : `${date.getFullYear()}年${date.getMonth() + 1}月${date.getDate()}日`;
  return `${day} ${clock(value)}`;
}
// dayLabel separates a conversation by day.
export function dayLabel(value: string, now = new Date()): string {
  const date = new Date(value), days = daysAgo(date, now);
  if (days === 0) return "今天";
  if (days === 1) return "昨天";
  const week = "日一二三四五六"[date.getDay()];
  return date.getFullYear() === now.getFullYear() ? `${date.getMonth() + 1}月${date.getDate()}日 周${week}` : `${date.getFullYear()}年${date.getMonth() + 1}月${date.getDate()}日`;
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
  if (share.sender.id !== userId && phaseOf(share) === "active" && share.recipients.some((item) => item.userId === userId && !item.viewedAt)) return true;
  return unreadThreads(share, userId).length > 0;
}

// receipt is what a sender sees about one recipient.
export function receipt(item: Recipient): { text: string; tone: "muted" | "seen" | "saved" | "uncertain" } {
  if (item.saved) return { text: `已保存 ${item.savedFiles} 个文件${item.saveUncertain ? "，另有结果待确认" : ""}`, tone: "saved" };
  if (item.saveUncertain) return { text: "保存结果待确认", tone: "uncertain" };
  return item.viewedAt ? { text: "已查看", tone: "seen" } : { text: "未查看", tone: "muted" };
}

export const jobLabel: Record<string, string> = { running: "正在保存", completed: "已保存", failed: "保存失败", cancelled: "已停止", interrupted: "保存中断" };
export const jobDone = (job: Job) => job.completedIds?.length ?? 0;

const apiMessages: Record<string, string> = {
  share_not_found: "找不到这次分享，可能已被删除",
  authentication_required: "登录已失效或账号不可用，请重新登录",
  share_recipient_unavailable: "所选成员的账号当前不可用",
  share_inactive: "分享已到期或已撤回",
  invalid_share_request: "请求无效，请刷新后重试",
  share_request_conflict: "请求与之前的提交不一致，请刷新后重试",
  share_busy: "正在处理的分享任务较多，请稍后重试",
  shares_unavailable: "分享服务暂不可用，请稍后重试",
};
// errorText turns a failed request into a sentence for the person using it.
export function errorText(error: unknown): string {
  if (error instanceof APIError) return apiMessages[error.code] ?? error.message;
  if (error instanceof TypeError) return "网络连接中断，请检查网络后重试";
  return error instanceof Error ? error.message : "请求失败，请重试";
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
