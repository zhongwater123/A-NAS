import { request } from "../api";

export type ActivityCategory = "session" | "account" | "file" | "storage" | "photo";

export interface ActivityAccount { id: string; username: string }

export interface ActivityEvent {
  id: string;
  occurredAt: string;
  category: ActivityCategory | "other";
  action: string;
  actor?: ActivityAccount;
  system?: boolean;
  subject?: ActivityAccount;
  resourceType: string;
  resourceId?: string;
  detail?: string;
  space?: { id: string; kind: "private" | "shared"; owner?: ActivityAccount };
  redacted?: boolean;
}

export interface ActivityPage {
  items: ActivityEvent[];
  next?: string;
  scope: "all" | "self";
  unavailable?: ActivityCategory[];
}

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

export interface ActivityFilters { category?: ActivityCategory; actor?: string; range: TimeRange; q: string }

export function listActivity(filters: ActivityFilters, before?: string, limit = 50, signal?: AbortSignal): Promise<ActivityPage> {
  const params = new URLSearchParams({ limit: String(limit) });
  if (filters.category) params.set("category", filters.category);
  if (filters.actor) params.set("actor", filters.actor);
  const since = sinceOf(filters.range);
  if (since) params.set("since", since);
  if (filters.q.trim()) params.set("q", filters.q.trim());
  if (before) params.set("before", before);
  return request<ActivityPage>(`/api/v1/activity?${params}`, { signal });
}

export type SystemLogService = "api" | "host-agent" | "photos" | "containers" | "ai" | "media" | "kiosk" | "samba" | "caddy";
export type LogLevel = "error" | "warning" | "info" | "debug";

export const systemLogServices: Array<{ id: SystemLogService; label: string; unit: string }> = [
  { id: "api", label: "产品服务", unit: "anas-api" },
  { id: "host-agent", label: "Host Agent", unit: "anas-host-agent" },
  { id: "photos", label: "相册服务", unit: "anas-photos" },
  { id: "containers", label: "容器代理", unit: "anas-container-agent" },
  { id: "ai", label: "本地 AI", unit: "anas-ai" },
  { id: "media", label: "媒体 Worker", unit: "anas-media@" },
  { id: "kiosk", label: "本机屏幕", unit: "anas-kiosk@" },
  { id: "samba", label: "文件共享 Samba", unit: "smbd" },
  { id: "caddy", label: "局域网入口 Caddy", unit: "caddy" },
];

export interface SystemLogEntry {
  cursor: string;
  time: string;
  service: SystemLogService;
  unit?: string;
  level: LogLevel;
  message: string;
  fields?: Record<string, string>;
  pid?: number;
  truncated?: boolean;
}

export interface SystemLogPage {
  items: SystemLogEntry[];
  next?: string;
  scannedThrough?: string;
  overflow?: boolean;
  dataSource: "simulated" | "live";
}

// "warning" includes errors: the level is the least severe one to show.
export interface SystemLogFilters { service?: SystemLogService; level?: "warning" | "error"; range: TimeRange; q: string }

export function listSystemLogs(filters: SystemLogFilters, cursor: { before?: string; after?: string } = {}, limit = 200, signal?: AbortSignal): Promise<SystemLogPage> {
  const params = new URLSearchParams({ limit: String(limit) });
  if (filters.service) params.set("service", filters.service);
  if (filters.level) params.set("level", filters.level);
  const since = sinceOf(filters.range);
  if (since) params.set("since", since);
  if (filters.q.trim()) params.set("q", filters.q.trim());
  if (cursor.before) params.set("before", cursor.before);
  if (cursor.after) params.set("after", cursor.after);
  return request<SystemLogPage>(`/api/v1/system-logs?${params}`, { signal });
}
