const clock = (value: Date) => `${String(value.getHours()).padStart(2, "0")}:${String(value.getMinutes()).padStart(2, "0")}`;
const sameDay = (a: Date, b: Date) => a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate();

// describeTime says when something happened the way a list of notes does:
// recent times relative, older ones as a date.
export function describeTime(iso: string, now = new Date()): string {
  const at = new Date(iso);
  const seconds = (now.getTime() - at.getTime()) / 1000;
  if (seconds < 60) return "刚刚";
  if (seconds < 3600) return `${Math.floor(seconds / 60)} 分钟前`;
  if (sameDay(at, now)) return `今天 ${clock(at)}`;
  const yesterday = new Date(now);
  yesterday.setDate(now.getDate() - 1);
  if (sameDay(at, yesterday)) return `昨天 ${clock(at)}`;
  if (at.getFullYear() === now.getFullYear()) return `${at.getMonth() + 1}月${at.getDate()}日 ${clock(at)}`;
  return `${at.getFullYear()}年${at.getMonth() + 1}月${at.getDate()}日`;
}

export const fullTime = (iso: string) => new Date(iso).toLocaleString("zh-CN", { hour12: false });
