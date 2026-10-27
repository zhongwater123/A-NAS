import { formatDate, formatClock as clock } from "../format/time";
import { relativeUnit } from "../format/time";
import { relativeTime } from "../format/time";
import { ContainerImage, ContainerSummary } from "../containersApi";

export type Tone = "ok" | "warn" | "danger" | "idle";
export type Health = "healthy" | "unhealthy" | "starting";

// What a container's state means to the owner, read from the engine's state
// and its English status text ("Up 3 hours (healthy)", "Exited (1) 2 days ago").
export interface Condition {
  label: string;
  tone: Tone;
  // The status in Chinese, or the engine's own text when it has an unknown shape.
  detail: string;
  health?: Health;
  exitCode?: number;
  // Unhealthy, crashed or stuck restarting: worth the owner's attention.
  attention: boolean;
}

// docker stop sends SIGTERM, then SIGKILL after the grace period, so these
// codes come from a deliberate stop rather than a crash.
const stopExitCodes = new Set([0, 130, 137, 143]);

export function describeContainer(item: ContainerSummary): Condition {
  const status = item.status.trim();
  const up = /^Up (.+?)(?: \((healthy|unhealthy|health: starting|Paused)\))?$/i.exec(status);
  const ended = /^(?:Exited|Restarting) \((-?\d+)\) (.+) ago$/i.exec(status);
  const uptime = up ? duration(up[1]) : undefined;
  const marker = up?.[2]?.toLowerCase();
  const health: Health | undefined =
    marker === "healthy"
      ? "healthy"
      : marker === "unhealthy"
        ? "unhealthy"
        : marker === "health: starting"
          ? "starting"
          : undefined;
  const exitCode = ended ? Number(ended[1]) : undefined;
  const since = ended ? dockerAgo(ended[2]) : undefined;

  switch (item.state) {
    case "running": {
      const detail = uptime === undefined ? status : uptime === "" ? "刚刚启动" : after("已运行", uptime);
      if (health === "unhealthy") return { label: "不健康", tone: "danger", detail, health, attention: true };
      if (health === "starting") return { label: "启动中", tone: "warn", detail, health, attention: false };
      return { label: "运行中", tone: "ok", detail, health, attention: false };
    }
    case "paused":
      return {
        label: "已暂停",
        tone: "warn",
        detail: uptime ? `${after("已运行", uptime)}，已暂停` : status,
        attention: false,
      };
    case "restarting":
      return {
        label: "重启中",
        tone: "warn",
        detail: since === undefined ? status : `${since}退出 · 退出码 ${exitCode}`,
        exitCode,
        attention: true,
      };
    case "exited": {
      if (since === undefined || exitCode === undefined)
        return { label: "已停止", tone: "idle", detail: status, attention: false };
      const when = `${since}停止`;
      if (stopExitCodes.has(exitCode))
        return {
          label: "已停止",
          tone: "idle",
          detail: exitCode === 0 ? when : `${when} · 退出码 ${exitCode}`,
          exitCode,
          attention: false,
        };
      return { label: "异常退出", tone: "danger", detail: `${when} · 退出码 ${exitCode}`, exitCode, attention: true };
    }
    case "created":
      return { label: "未启动", tone: "idle", detail: "创建后还没有启动过", attention: false };
    case "removing":
      return { label: "删除中", tone: "idle", detail: "引擎正在删除这个容器", attention: false };
    case "dead":
      return { label: "异常", tone: "danger", detail: "引擎没能停止或删除这个容器", attention: true };
  }
}

// Durations as Docker's go-units prints them; "" stands for "Less than a second".
function duration(text: string): string | undefined {
  const value = text.trim();
  if (/^less than a second$/i.test(value)) return "";
  if (/^about a minute$/i.test(value)) return "约 1 分钟";
  if (/^about an hour$/i.test(value)) return "约 1 小时";
  const match = /^(\d+) (second|minute|hour|day|week|month|year)s?$/i.exec(value);
  if (!match) return undefined;
  const units: Record<string, string> = {
    second: "秒",
    minute: "分钟",
    hour: "小时",
    day: "天",
    week: "周",
    month: "个月",
    year: "年",
  };
  return `${match[1]} ${units[match[2].toLowerCase()]}`;
}

// A space separates Chinese from a number ("已运行 3 小时") but not from "约".
function after(prefix: string, value: string): string {
  return /^\d/.test(value) ? `${prefix} ${value}` : `${prefix}${value}`;
}

function dockerAgo(text: string): string | undefined {
  if (/^less than a second$/i.test(text)) return "刚刚";
  const approximate = /^about an? (minute|hour)$/i.exec(text);
  if (approximate) return `约 ${relativeUnit(-1, approximate[1].toLowerCase() as "minute" | "hour")}`;
  const match = /^(\d+) (second|minute|hour|day|week|month|year)s?$/i.exec(text);
  return match ? relativeUnit(-Number(match[1]), match[2].toLowerCase() as Intl.RelativeTimeFormatUnit) : undefined;
}

export type Filter = "all" | "running" | "stopped" | "attention";

export const filterLabels: Record<Filter, string> = {
  all: "全部容器",
  running: "运行中",
  stopped: "已停止",
  attention: "需要关注",
};

// Running covers every container the engine keeps alive, paused or restarting included.
export function isActive(item: ContainerSummary): boolean {
  return item.state === "running" || item.state === "paused" || item.state === "restarting";
}

export function matchesFilter(item: ContainerSummary, filter: Filter): boolean {
  switch (filter) {
    case "all":
      return true;
    case "running":
      return isActive(item);
    case "stopped":
      return !isActive(item);
    case "attention":
      return describeContainer(item).attention;
  }
}

// An installed App Center app, keyed by its catalog ID.
export interface AppRef {
  id: string;
  title: string;
  webPort?: number;
  scheme: string;
  path: string;
}

// Mirrors appstore.ProjectName: every App Center install is the Compose project a-nas-<id>.
const appProjectPrefix = "a-nas-";

export function appProject(appID: string): string {
  return appProjectPrefix + appID;
}

export interface Group {
  // The Compose project, or "" for containers started on their own.
  key: string;
  title: string;
  app?: AppRef;
  containers: ContainerSummary[];
}

export const standaloneTitle = "独立容器";

export function groupKey(item: ContainerSummary): string {
  return item.project ?? "";
}

export function appFor(project: string, apps: ReadonlyMap<string, AppRef>): AppRef | undefined {
  return project.startsWith(appProjectPrefix) ? apps.get(project.slice(appProjectPrefix.length)) : undefined;
}

// Containers grouped by Compose project, App Center apps shown by their name;
// groups sort by title with standalone containers last, containers by name.
export function groupContainers(items: ContainerSummary[], apps: ReadonlyMap<string, AppRef>): Group[] {
  const groups = new Map<string, Group>();
  for (const item of items) {
    const key = groupKey(item);
    let group = groups.get(key);
    if (!group) {
      const app = appFor(key, apps);
      group = { key, title: app?.title ?? (key || standaloneTitle), app, containers: [] };
      groups.set(key, group);
    }
    group.containers.push(item);
  }
  const collator = new Intl.Collator("zh-CN", { numeric: true, sensitivity: "base" });
  for (const group of groups.values()) group.containers.sort((a, b) => collator.compare(a.name, b.name));
  return [...groups.values()].sort(
    (a, b) => (a.key === "" ? 1 : 0) - (b.key === "" ? 1 : 0) || collator.compare(a.title, b.title),
  );
}

// Every word must appear in the name, image, project or app name.
export function matchesQuery(item: ContainerSummary, query: string, apps: ReadonlyMap<string, AppRef>): boolean {
  const words = query.trim().toLowerCase().split(/\s+/).filter(Boolean);
  if (words.length === 0) return true;
  const text = [item.name, item.image, item.project ?? "", appFor(groupKey(item), apps)?.title ?? ""]
    .join(" ")
    .toLowerCase();
  return words.every((word) => text.includes(word));
}

export function matchesImageQuery(image: ContainerImage, query: string): boolean {
  const words = query.trim().toLowerCase().split(/\s+/).filter(Boolean);
  const text = [...image.tags, shortID(image.id)].join(" ").toLowerCase();
  return words.every((word) => text.includes(word));
}

export interface Totals {
  total: number;
  running: number;
  cpuPercent: number;
  memoryBytes: number;
}

export function totals(items: ContainerSummary[]): Totals {
  return items.reduce<Totals>(
    (sum, item) => ({
      total: sum.total + 1,
      running: sum.running + (item.state === "running" ? 1 : 0),
      cpuPercent: sum.cpuPercent + (item.usage?.cpuPercent ?? 0),
      memoryBytes: sum.memoryBytes + (item.usage?.memoryBytes ?? 0),
    }),
    { total: 0, running: 0, cpuPercent: 0, memoryBytes: 0 },
  );
}

// The containers running an image: by engine image ID, or by reference from an
// older container agent that does not report one.
export function imageUsers(image: ContainerImage, items: ContainerSummary[]): ContainerSummary[] {
  const tags = new Set(image.tags.map(normalizeReference));
  return items.filter((item) =>
    item.imageId ? item.imageId === image.id : item.image === image.id || tags.has(normalizeReference(item.image)),
  );
}

// docker.io/library/nginx@sha256:… and nginx:latest name the same image.
function normalizeReference(reference: string): string {
  let value = reference.replace(/@sha256:[a-f0-9]+$/i, "");
  value = value.replace(/^(?:docker\.io|index\.docker\.io)\//, "").replace(/^library\//, "");
  const name = value.slice(value.lastIndexOf("/") + 1);
  return name.includes(":") ? value : `${value}:latest`;
}

// Registry, repository and tag of an image reference, split for display;
// the registry is "" for Docker Hub.
export function splitReference(reference: string): { registry: string; repository: string; tag: string } {
  let rest = reference.replace(/@sha256:[a-f0-9]+$/i, "");
  let registry = "";
  const first = rest.indexOf("/");
  // Like Docker, a first path part with a dot or port, or localhost, names a registry.
  if (first > 0 && /[.:]|^localhost$/.test(rest.slice(0, first))) {
    registry = rest.slice(0, first);
    rest = rest.slice(first + 1);
  }
  const colon = rest.lastIndexOf(":");
  if (colon > rest.lastIndexOf("/")) return { registry, repository: rest.slice(0, colon), tag: rest.slice(colon + 1) };
  return { registry, repository: rest, tag: "latest" };
}

export function shortID(id: string): string {
  return id.replace(/^sha256:/, "").slice(0, 12);
}

export interface WebPort {
  hostPort: number;
  containerPort: number;
  url: string;
}

// Published TCP ports, each opened on the address this desktop was loaded
// from; an App Center app's own web port keeps its scheme and path.
export function webPorts(
  item: ContainerSummary,
  app: AppRef | undefined,
  location: Location = window.location,
): WebPort[] {
  const seen = new Set<number>();
  const ports: WebPort[] = [];
  for (const port of item.ports) {
    if (!port.hostPort || port.protocol !== "tcp" || seen.has(port.hostPort)) continue;
    seen.add(port.hostPort);
    ports.push({
      hostPort: port.hostPort,
      containerPort: port.containerPort,
      url: portURL(port.hostPort, app, location),
    });
  }
  return ports;
}

// The web interface of an App Center app, if it has one.
export function appLink(app: AppRef, location: Location = window.location): string | undefined {
  return app.webPort ? portURL(app.webPort, app, location) : undefined;
}

function portURL(hostPort: number, app: AppRef | undefined, location: Location): string {
  const url = new URL(location.href);
  const own = app?.webPort === hostPort;
  url.protocol = `${(own && app?.scheme) || "http"}:`;
  url.port = String(hostPort);
  url.pathname = (own && app?.path) || "/";
  url.search = "";
  url.hash = "";
  return url.toString();
}

export function formatPercent(value: number): string {
  return `${value.toFixed(1)}%`;
}

export function formatDateTime(value: string): string {
  return formatDate(value, {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
  });
}

export function formatClock(value: string | number | Date): string {
  return Number.isNaN(new Date(value).getTime()) ? "" : clock(value, true);
}

// "3 周前" for an image's age; exact dates live in the tooltip.
export function formatAgo(value: string, now = Date.now()): string {
  return relativeTime(value, now, { dateAfterDays: Infinity, numeric: "auto" });
}
