import type { ActivityAccount, ActivityCategory, ActivityEvent, LogLevel } from "./logsApi";

// A sentence is plain text with emphasised names in it.
export type Part = string | { name: string; muted?: boolean };

export interface Described {
  parts: Part[];
  // Where and how: the space, the client address, the reason.
  meta: string[];
  tone?: "warning" | "danger" | "success";
}

export const categoryInfo: Record<ActivityCategory | "other", { label: string; tone: string }> = {
  session: { label: "登录", tone: "session" },
  account: { label: "账号", tone: "account" },
  file: { label: "文件", tone: "file" },
  storage: { label: "存储", tone: "storage" },
  photo: { label: "相册", tone: "photo" },
  other: { label: "其他", tone: "other" },
};

export const levelLabels: Record<LogLevel, string> = { error: "错误", warning: "警告", info: "信息", debug: "调试" };

const name = (value: string, muted = false): Part => ({ name: value, muted });

function accountName(account: ActivityAccount | undefined, viewerId: string): Part {
  if (!account) return name("已删除的账号", true);
  return name(account.id === viewerId ? "你" : account.username);
}

function actorOf(event: ActivityEvent, viewerId: string): Part {
  if (event.system) return name("系统");
  if (event.actor) return accountName(event.actor, viewerId);
  return name("未知账号", true);
}

function parseDetail(detail?: string): Record<string, string> {
  if (!detail?.startsWith("{")) return {};
  try {
    const value: unknown = JSON.parse(detail);
    return value && typeof value === "object" ? value as Record<string, string> : {};
  } catch {
    return {};
  }
}

// originLabel says how a client reached the device when it signed in.
export function originLabel(detail?: string): string | undefined {
  const { via, address } = parseDetail(detail);
  if (via === "local-console") return "本机屏幕";
  if (via === "lan") return address ? `局域网 · ${address}` : "局域网";
  if (via === "loopback") return "本机回环（SSH 隧道或设备上的浏览器）";
  return undefined;
}

const signInFailures: Record<string, string> = {
  invalid_password: "密码错误",
  unknown_user: "账号不存在",
  disabled: "账号已禁用",
  unavailable: "账号暂不可用",
};

// Named actions show the file or photo name; counted ones say how many.
interface Verb { named?: (item: Part) => Part[]; counted: (count: number) => string; meta?: string }

const fileVerbs: Record<string, Verb> = {
  "file.uploaded": { named: (item) => ["上传了", item], counted: (n) => `上传了 ${n} 个文件` },
  "file.directory_created": { named: (item) => ["新建了文件夹", item], counted: (n) => `新建了 ${n} 个文件夹` },
  "file.trashed": { named: (item) => ["删除了", item], counted: (n) => `删除了 ${n} 个项目`, meta: "已移入回收站" },
  "file.trashed.smb": { named: (item) => ["通过 SMB 删除了", item], counted: (n) => `通过 SMB 删除了 ${n} 个项目`, meta: "已移入回收站" },
  "file.restored": { named: (item) => ["从回收站恢复了", item], counted: (n) => `从回收站恢复了 ${n} 个项目` },
  "file.purged": { named: (item) => ["彻底删除了", item], counted: (n) => `彻底删除了 ${n} 个项目` },
  "file.purged.retention": { named: (item) => ["清除了回收站中到期的", item], counted: (n) => `清除了回收站中 ${n} 个到期项目` },
  "file.moved": { named: (item) => ["移动或重命名了", item], counted: (n) => `移动或重命名了 ${n} 个项目` },
  "file.copied": { named: (item) => ["复制了", item], counted: (n) => `复制了 ${n} 个项目` },
  "snapshot.created": { named: (item) => ["创建了快照", item], counted: (n) => `创建了 ${n} 个快照` },
  "snapshot.file_restored": { counted: (n) => `从快照恢复了 ${n} 个文件` },
  "snapshot.deleted": { named: (item) => ["删除了快照", item], counted: (n) => `删除了 ${n} 个快照` },
  "snapshot.kept": { named: (item) => ["把定时快照保留为手动快照", item], counted: (n) => `保留了 ${n} 个定时快照` },
  "snapshot.expired": { named: (item) => ["清理了到期的定时快照", item], counted: (n) => `清理了 ${n} 个到期的定时快照` },
  "snapshot.folder_restored": { counted: (n) => `从快照恢复了 ${n} 个文件夹` },
};

const photoVerbs: Record<string, Verb> = {
  "photo.imported": { named: (item) => ["导入了照片", item], counted: (n) => `导入了 ${n} 张照片` },
  "photo.trashed": { named: (item) => ["删除了照片", item], counted: (n) => `删除了 ${n} 张照片`, meta: "已移入相册回收站" },
  "photo.restored": { named: (item) => ["恢复了照片", item], counted: (n) => `恢复了 ${n} 张照片` },
  "photo.purged": { named: (item) => ["彻底删除了照片", item], counted: (n) => `彻底删除了 ${n} 张照片` },
  "photo.renamed": { named: (item) => ["把照片重命名为", item], counted: (n) => `重命名了 ${n} 张照片` },
  "photo.moved": { counted: (n) => `移动了 ${n} 张照片` },
  "photo.copied": { counted: (n) => `复制了 ${n} 张照片` },
  "photo.album_created": { named: (item) => ["新建了相册", item], counted: (n) => `新建了 ${n} 个相册` },
  "photo.album_renamed": { named: (item) => ["把相册重命名为", item], counted: (n) => `重命名了 ${n} 个相册` },
  "photo.album_deleted": { named: (item) => ["删除了相册", item], counted: (n) => `删除了 ${n} 个相册` },
  "photo.album_added": { counted: (n) => `向相册添加了 ${n} 张照片` },
  "photo.album_removed": { counted: (n) => `从相册移除了 ${n} 张照片` },
  "photo.directory_created": { named: (item) => ["新建了文件夹", item], counted: (n) => `新建了 ${n} 个文件夹` },
  "photo.directory_renamed": { named: (item) => ["把文件夹重命名为", item], counted: (n) => `重命名了 ${n} 个文件夹` },
  "photo.directory_moved": { named: (item) => ["移动了文件夹", item], counted: (n) => `移动了 ${n} 个文件夹` },
  "photo.directory_deleted": { named: (item) => ["删除了文件夹", item], counted: (n) => `删除了 ${n} 个文件夹` },
  "photo.tag_added": { named: (item) => ["添加了标签", item], counted: (n) => `添加了 ${n} 个标签` },
  "photo.tag_removed": { named: (item) => ["移除了标签", item], counted: (n) => `移除了 ${n} 个标签` },
  "photo.ai_label_hidden": { counted: (n) => `从 AI 聚合中移出了 ${n} 张照片` },
};

const storageStates: Record<string, { text: string; tone?: Described["tone"] }> = {
  planned: { text: "生成了数据卷初始化计划" },
  confirmed: { text: "确认了数据卷初始化计划" },
  running: { text: "开始初始化数据卷" },
  succeeded: { text: "完成了数据卷初始化", tone: "success" },
  failed: { text: "数据卷初始化失败", tone: "danger" },
  needs_attention: { text: "数据卷初始化需要人工处理", tone: "danger" },
};

const frequencies: Record<string, string> = { hourly: "每小时", daily: "每天", weekly: "每周" };
const weekdayNames = ["周日", "周一", "周二", "周三", "周四", "周五", "周六"];

// scheduleLabel reads a snapshot schedule as recorded: "disabled", or
// "<frequency> <HH:MM> weekday=<0-6> keep=<n>".
function scheduleLabel(detail?: string): string | undefined {
  if (!detail) return undefined;
  if (detail === "disabled") return "已关闭定时快照";
  const match = /^(\w+) (\d{2}):(\d{2}) weekday=(\d) keep=(\d+)$/.exec(detail);
  if (!match) return detail;
  const [, frequency, hour, minute, weekday, keep] = match;
  const when = frequency === "hourly" ? `每小时第 ${Number(minute)} 分`
    : frequency === "weekly" ? `每周${weekdayNames[Number(weekday)]?.slice(1) ?? ""} ${hour}:${minute}`
      : `${frequencies[frequency] ?? frequency} ${hour}:${minute}`;
  return `${when} · 保留最近 ${keep} 份`;
}

function spaceLabel(event: ActivityEvent, viewerId: string): string | undefined {
  if (!event.space) return undefined;
  if (event.space.kind === "shared") return "共享空间";
  const owner = event.space.owner;
  return !owner || owner.id === viewerId ? "个人空间" : `${owner.username} 的个人空间`;
}

// hiddenIn says whose private space a hidden name was in, then what happened.
function hiddenIn(event: ActivityEvent, what: string): Part[] {
  const owner = event.space?.owner;
  if (!event.space || !owner) return [`在其他成员的空间${what}`];
  return owner.id === event.actor?.id ? [`在个人空间${what}`] : ["在", name(owner.username), `的个人空间${what}`];
}

// An upload that replaced a file of the same name says so after the name.
const replacedSuffix = " (replaced)";

function itemName(event: ActivityEvent): string | undefined {
  return event.detail?.endsWith(replacedSuffix) ? event.detail.slice(0, -replacedSuffix.length) : event.detail;
}

function verbOf(action: string): Verb | undefined {
  return fileVerbs[action] ?? photoVerbs[action];
}

// describeActivity turns an event into a sentence for the activity log.
export function describeActivity(event: ActivityEvent, viewerId: string): Described {
  const actor = actorOf(event, viewerId);
  const detail = parseDetail(event.detail);
  const subject = accountName(event.subject, viewerId);
  const owner = event.space?.owner ? accountName(event.space.owner, viewerId) : subject;
  const viewingMeta = () => [detail.reason ? `原因：${detail.reason}` : "", detail.expiresAt ? `${formatDateTime(detail.expiresAt)} 自动结束` : ""].filter(Boolean);
  switch (event.action) {
    case "session.signed_in": return { parts: [actor, "登录了"], meta: [originLabel(event.detail)].filter(Boolean) as string[] };
    case "session.signed_out": return { parts: [actor, "退出了登录"], meta: [originLabel(event.detail)].filter(Boolean) as string[] };
    case "session.sign_in_failed": {
      const meta = [signInFailures[detail.reason] ?? "登录失败", originLabel(event.detail)].filter(Boolean) as string[];
      if (event.actor) return { parts: [actor, "登录失败"], meta, tone: "warning" };
      return { parts: ["有人尝试以", name(detail.username || "（空）"), "登录，失败"], meta, tone: "warning" };
    }
    case "user.created":
      if (!event.actor) return { parts: ["设备初始化，创建了管理员", subject], meta: [] };
      return { parts: [actor, event.detail === "admin" ? "添加了管理员" : "添加了成员", subject], meta: [] };
    case "user.setup_recovered": return { parts: ["重新完成了设备初始化，管理员", subject], meta: [] };
    case "user.identity_conflict": return { parts: [subject, "的 Linux 账号与系统中已有的账号冲突"], meta: ["需要按运行手册处理"], tone: "danger" };
    case "user.disabled": return { parts: [actor, "禁用了", subject, "的账号"], meta: [], tone: "warning" };
    case "user.credential_reset": return { parts: [actor, "重置了", subject, "的密码"], meta: ["对方下次登录时需要设置新密码"], tone: "warning" };
    case "user.password_changed": return { parts: [actor, "修改了自己的密码"], meta: ["SMB 凭据已同步"] };
    case "space.viewing_started": return { parts: [actor, "开始只读查看", owner, "的个人空间"], meta: viewingMeta(), tone: "warning" };
    case "space.viewing_ended": return { parts: [actor, "结束了对", owner, "个人空间的只读查看"], meta: [] };
    case "photos.viewing_started": return { parts: [actor, "开始只读查看", subject, "的私有图库"], meta: viewingMeta(), tone: "warning" };
    case "photos.viewing_ended": return { parts: [actor, "结束了对", subject, "私有图库的只读查看"], meta: [] };
  }
  if (event.action === "snapshot.schedule_updated") {
    return { parts: [actor, "修改了定时快照"], meta: [spaceLabel(event, viewerId), scheduleLabel(event.detail)].filter(Boolean) as string[] };
  }
  if (event.action.startsWith("storage.plan.")) {
    const state = storageStates[event.action.slice("storage.plan.".length)];
    return { parts: [actor, state?.text ?? `更新了数据卷计划（${event.action}）`], meta: event.detail ? [event.detail] : [], tone: state?.tone };
  }
  const verb = verbOf(event.action);
  if (verb) {
    const where = spaceLabel(event, viewerId);
    const replaced = event.detail?.endsWith(replacedSuffix) ? "替换了同名文件" : "";
    const meta = [where, verb.meta, replaced].filter(Boolean) as string[];
    if (event.redacted) return { parts: [actor, ...hiddenIn(event, verb.counted(1))], meta: verb.meta ? [verb.meta] : [] };
    const item = itemName(event);
    if (verb.named && item) return { parts: [actor, ...verb.named(name(item))], meta };
    return { parts: [actor, verb.counted(1)], meta };
  }
  return { parts: [actor, "执行了", name(event.action)], meta: event.detail ? [event.detail] : [] };
}

// Runs of the same file or photo action by one account within a few minutes
// read as one entry, such as an upload of twelve files.
export interface ActivityGroup { key: string; events: ActivityEvent[] }

const groupWindow = 5 * 60_000;

export function groupActivity(events: ActivityEvent[]): ActivityGroup[] {
  const groups: ActivityGroup[] = [];
  for (const event of events) {
    const last = groups.at(-1);
    const previous = last?.events.at(-1);
    const sameRun = previous && verbOf(event.action) && previous.action === event.action
      && (previous.actor?.id ?? "") === (event.actor?.id ?? "") && previous.system === event.system
      && previous.space?.id === event.space?.id && Boolean(previous.redacted) === Boolean(event.redacted)
      && Date.parse(previous.occurredAt) - Date.parse(event.occurredAt) <= groupWindow
      && sameDay(new Date(previous.occurredAt), new Date(event.occurredAt));
    if (last && sameRun) last.events.push(event);
    else groups.push({ key: event.id, events: [event] });
  }
  return groups;
}

export function describeGroup(group: ActivityGroup, viewerId: string): Described {
  const [first] = group.events;
  if (group.events.length === 1) return describeActivity(first, viewerId);
  const verb = verbOf(first.action)!;
  const where = spaceLabel(first, viewerId);
  const counted = verb.counted(group.events.length);
  const parts: Part[] = [actorOf(first, viewerId), ...(first.redacted ? hiddenIn(first, counted) : [counted])];
  const names = first.redacted ? [] : group.events.map(itemName).filter(Boolean) as string[];
  const meta = [first.redacted ? "" : where, verb.named && names.length ? `${names.slice(0, 3).join("、")}${names.length > 3 ? " 等" : ""}` : "", verb.meta].filter(Boolean) as string[];
  return { parts, meta };
}

export function plainText(parts: Part[]): string {
  return parts.map((part) => typeof part === "string" ? part : part.name).join(" ").replace(/ (?=[，、。])/g, "");
}

const weekdays = ["星期日", "星期一", "星期二", "星期三", "星期四", "星期五", "星期六"];

export function sameDay(a: Date, b: Date): boolean {
  return a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate();
}

// dayLabel heads a day of the activity log.
export function dayLabel(value: Date, now = new Date()): string {
  const date = `${value.getMonth() + 1}月${value.getDate()}日 ${weekdays[value.getDay()]}`;
  if (sameDay(value, now)) return `今天 · ${date}`;
  const yesterday = new Date(now);
  yesterday.setDate(now.getDate() - 1);
  if (sameDay(value, yesterday)) return `昨天 · ${date}`;
  return value.getFullYear() === now.getFullYear() ? date : `${value.getFullYear()}年${date}`;
}

const two = (value: number) => String(value).padStart(2, "0");

export const clockTime = (iso: string, seconds = false) => {
  const value = new Date(iso);
  return `${two(value.getHours())}:${two(value.getMinutes())}${seconds ? `:${two(value.getSeconds())}` : ""}`;
};

// logTime is precise to the millisecond, as service logs need.
export function logTime(iso: string, now = new Date()): { day: string; clock: string } {
  const value = new Date(iso);
  const clock = `${two(value.getHours())}:${two(value.getMinutes())}:${two(value.getSeconds())}.${String(value.getMilliseconds()).padStart(3, "0")}`;
  return { day: sameDay(value, now) ? "今天" : `${two(value.getMonth() + 1)}-${two(value.getDate())}`, clock };
}

export function formatDateTime(iso: string): string {
  const value = new Date(iso);
  if (Number.isNaN(value.getTime())) return iso;
  return `${value.getFullYear()}-${two(value.getMonth() + 1)}-${two(value.getDate())} ${two(value.getHours())}:${two(value.getMinutes())}:${two(value.getSeconds())}`;
}

// fullTime names the time zone so exported and copied times are unambiguous.
export function fullTime(iso: string): string {
  const value = new Date(iso);
  const offset = -value.getTimezoneOffset();
  const sign = offset >= 0 ? "+" : "-";
  return `${formatDateTime(iso)} UTC${sign}${two(Math.floor(Math.abs(offset) / 60))}:${two(Math.abs(offset) % 60)}`;
}
