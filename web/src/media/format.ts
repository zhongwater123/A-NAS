import type { Episode, Title } from "./mediaApi";

// timecode shows a position as 1:02:03 or 12:34.
export function timecode(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) seconds = 0;
  const whole = Math.floor(seconds);
  const hours = Math.floor(whole / 3600);
  const minutes = Math.floor((whole % 3600) / 60);
  const rest = String(whole % 60).padStart(2, "0");
  return hours ? `${hours}:${String(minutes).padStart(2, "0")}:${rest}` : `${minutes}:${rest}`;
}

// runtime shows a length as 2 小时 12 分 or 45 分钟.
export function runtime(seconds?: number): string {
  if (!seconds || seconds < 1) return "";
  const minutes = Math.round(seconds / 60);
  if (minutes < 1) return `${Math.round(seconds)} 秒`;
  if (minutes < 60) return `${minutes} 分钟`;
  const hours = Math.floor(minutes / 60);
  return minutes % 60 ? `${hours} 小时 ${minutes % 60} 分` : `${hours} 小时`;
}

export function remaining(position: number, duration: number): string {
  const left = Math.max(0, duration - position);
  return left < 60 ? "即将看完" : `剩余 ${runtime(left)}`;
}

export function bytes(value: number): string {
  if (!value) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  const exponent = Math.min(units.length - 1, Math.floor(Math.log(value) / Math.log(1024)));
  const scaled = value / 1024 ** exponent;
  return `${scaled >= 100 || exponent === 0 ? Math.round(scaled) : scaled.toFixed(1)} ${units[exponent]}`;
}

export function ago(iso?: string, now = Date.now()): string {
  if (!iso) return "";
  const elapsed = Math.max(0, now - new Date(iso).getTime()) / 1000;
  if (elapsed < 60) return "刚刚";
  if (elapsed < 3600) return `${Math.floor(elapsed / 60)} 分钟前`;
  if (elapsed < 86400) return `${Math.floor(elapsed / 3600)} 小时前`;
  if (elapsed < 86400 * 30) return `${Math.floor(elapsed / 86400)} 天前`;
  return new Date(iso).toLocaleDateString("zh-CN");
}

// dayLabel groups history: 今天, 昨天, or the date.
export function dayLabel(iso: string, now = new Date()): string {
  const day = new Date(iso);
  const start = (value: Date) => new Date(value.getFullYear(), value.getMonth(), value.getDate()).getTime();
  const days = Math.round((start(now) - start(day)) / 86400000);
  if (days === 0) return "今天";
  if (days === 1) return "昨天";
  if (days < 7) return `${days} 天前`;
  return day.toLocaleDateString("zh-CN", { month: "long", day: "numeric", ...(day.getFullYear() !== now.getFullYear() ? { year: "numeric" } : {}) });
}

export function episodeCode(season?: number, episode?: number): string {
  if (!episode) return season ? `第 ${season} 季` : "";
  return season ? `S${season} · E${episode}` : `第 ${episode} 集`;
}

export function episodeName(item: Pick<Episode, "season" | "episode" | "title">): string {
  const code = episodeCode(item.season, item.episode);
  if (!item.title || item.title === `第 ${item.episode} 集`) return code;
  return code ? `${code}  ${item.title}` : item.title;
}

// heading is what a card shows first: the show for an episode.
export function heading(title: Title): string {
  return title.type === "episode" && title.showTitle ? title.showTitle : title.title;
}

export function subheading(title: Title): string {
  switch (title.type) {
    case "episode": {
      const code = episodeCode(title.season, title.episode);
      return title.title && !title.title.startsWith("第 ") ? `${code} · ${title.title}` : code;
    }
    case "show":
      return [title.year, title.episodes ? `${title.episodes} 集` : ""].filter(Boolean).join(" · ");
    case "other":
      return runtime(title.duration);
    default:
      return [title.year, runtime(title.duration)].filter(Boolean).join(" · ");
  }
}

export const typeLabel: Record<string, string> = { movie: "电影", show: "电视剧", episode: "剧集", other: "其他" };

// hue gives every title a stable colour for its placeholder artwork.
export function hue(text: string): number {
  let hash = 0;
  for (const char of text) hash = (hash * 31 + char.codePointAt(0)!) | 0;
  return Math.abs(hash) % 360;
}

export function progressShare(progress?: { position: number; duration: number; watched: boolean }): number {
  if (!progress) return 0;
  if (progress.watched && !progress.position) return 1;
  return progress.duration > 0 ? Math.min(1, progress.position / progress.duration) : 0;
}
