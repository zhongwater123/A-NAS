import { call, client } from "../api";
import type { ActivityCategory, SystemLogEntry, SystemLogService } from "../api.generated";

export type {
  ActivityAccount,
  ActivityCategory,
  ActivityEvent,
  ActivityPage,
  SystemLogEntry,
  SystemLogPage,
  SystemLogService,
} from "../api.generated";

// The time ranges both logs offer; "all" sends no lower bound.
export type TimeRange = "1h" | "24h" | "7d" | "30d" | "all";

export const timeRanges: Array<{ id: TimeRange; label: string }> = [
  { id: "1h", label: "近 1 小时" },
  { id: "24h", label: "近 24 小时" },
  { id: "7d", label: "近 7 天" },
  { id: "30d", label: "近 30 天" },
  { id: "all", label: "全部时间" },
];

const rangeHours: Record<TimeRange, number> = { "1h": 1, "24h": 24, "7d": 24 * 7, "30d": 24 * 30, all: 0 };

export function sinceOf(range: TimeRange, now = Date.now()): string | undefined {
  const hours = rangeHours[range];
  return hours ? new Date(now - hours * 3_600_000).toISOString().replace(/\.\d{3}Z$/, "Z") : undefined;
}

export interface ActivityFilters {
  category?: ActivityCategory;
  actor?: string;
  range: TimeRange;
  q: string;
}

export const listActivity = (filters: ActivityFilters, before?: string, limit = 50, signal?: AbortSignal) =>
  call(
    client.GET("/api/v1/activity", {
      params: {
        query: {
          limit,
          category: filters.category,
          actor: filters.actor || undefined,
          since: sinceOf(filters.range),
          q: filters.q.trim() || undefined,
          before,
        },
      },
      signal,
    }),
  );

export type LogLevel = SystemLogEntry["level"];

export const systemLogServices: Array<{ id: SystemLogService; label: string; unit: string }> = [
  { id: "api", label: "产品服务", unit: "anas-api" },
  { id: "host-agent", label: "Host Agent", unit: "anas-host-agent" },
  { id: "photos", label: "相册服务", unit: "anas-photos" },
  { id: "shares", label: "分享服务", unit: "anas-shares" },
  { id: "containers", label: "容器代理", unit: "anas-container-agent" },
  { id: "ai", label: "本地 AI", unit: "anas-ai" },
  { id: "media", label: "FFmpeg Worker", unit: "anas-ffmpeg@" },
  { id: "kiosk", label: "本机屏幕", unit: "anas-kiosk@" },
  { id: "samba", label: "文件共享 Samba", unit: "smbd" },
  { id: "caddy", label: "局域网入口 Caddy", unit: "caddy" },
];

// "warning" includes errors: the level is the least severe one to show.
export interface SystemLogFilters {
  service?: SystemLogService;
  level?: "warning" | "error";
  range: TimeRange;
  q: string;
}

export const listSystemLogs = (
  filters: SystemLogFilters,
  cursor: { before?: string; after?: string } = {},
  limit = 200,
  signal?: AbortSignal,
) =>
  call(
    client.GET("/api/v1/system-logs", {
      params: {
        query: {
          limit,
          service: filters.service,
          level: filters.level,
          since: sinceOf(filters.range),
          q: filters.q.trim() || undefined,
          before: cursor.before || undefined,
          after: cursor.after || undefined,
        },
      },
      signal,
    }),
  );
