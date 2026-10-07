import {
  Activity,
  Bell,
  Bot,
  Box,
  Camera,
  ChevronRight,
  CircleAlert,
  Database,
  Download,
  FileText,
  FolderClosed,
  Grid2X2,
  HardDrive,
  Home,
  Image,
  Maximize2,
  Minus,
  Monitor,
  Music2,
  PanelLeftClose,
  PlaySquare,
  RefreshCw,
  RotateCcw,
  Settings,
  ShieldCheck,
  ShoppingBag,
  Sparkles,
  SquareTerminal,
  Trash2,
  UserRound,
  X,
  type LucideIcon,
} from "lucide-react";
import { PointerEvent as ReactPointerEvent, ReactNode, useReducer } from "react";

import { DiskRole, Health, HostState } from "./api";
import { Dock } from "./Dock";
import { SourceBadge, StatusBar } from "./StatusBar";
import { TerminalPanel } from "./TerminalPanel";
import { useHostState } from "./useHostState";
import "./styles.css";

type WindowID = "resources" | "settings" | "terminal";

interface WindowModel {
  id: WindowID;
  title: string;
  open: boolean;
  minimized: boolean;
  maximized: boolean;
  x: number;
  y: number;
  width: number;
  height: number;
  z: number;
  // Sequence number of the latest open, so the dock lists apps in the order they were launched.
  opened: number;
}

type WindowAction =
  | { type: "open"; id: WindowID }
  | { type: "close"; id: WindowID }
  | { type: "minimize"; id: WindowID }
  | { type: "focus"; id: WindowID }
  | { type: "maximize"; id: WindowID }
  | { type: "move"; id: WindowID; x: number; y: number }
  | { type: "resize"; id: WindowID; width: number; height: number }
  | { type: "show-desktop" };

const initialWindows: WindowModel[] = [
  { id: "resources", title: "资源管理", open: false, minimized: false, maximized: false, x: 340, y: 94, width: 880, height: 610, z: 2, opened: 0 },
  { id: "settings", title: "系统设置", open: false, minimized: false, maximized: false, x: 390, y: 126, width: 760, height: 550, z: 1, opened: 0 },
  { id: "terminal", title: "终端", open: false, minimized: false, maximized: false, x: 300, y: 70, width: 820, height: 520, z: 0, opened: 0 },
];

const windowIcons: Record<WindowID, LucideIcon> = { resources: Activity, settings: Settings, terminal: SquareTerminal };
// Dock icons reuse the desktop shortcut gradients so an app looks the same in both places.
const windowTones: Record<WindowID, string> = { resources: "resources", settings: "settings", terminal: "terminal" };

export default function App() {
  const host = useHostState();
  const [windows, dispatch] = useReducer(windowReducer, initialWindows);
  const isOpen = (id: WindowID) => windows.some((window) => window.id === id && window.open);
  const visible = windows.filter((window) => window.open && !window.minimized);
  const focusedID = visible.length ? visible.reduce((top, window) => (window.z > top.z ? window : top)).id : undefined;

  return (
    <main className="desktop-shell">
      <div className="wallpaper-glow" />

      <aside className="system-rail" aria-label="系统快捷栏">
        <div className="rail-group">
          <button className="rail-button active" aria-label="显示桌面" onClick={() => dispatch({ type: "show-desktop" })}><Home /></button>
          <button className="rail-button" aria-label="全部应用"><Grid2X2 /></button>
        </div>
        <div className="rail-rule" />
        <div className="rail-group"><button className="rail-button" aria-label="AI 助手，规划中" disabled><Sparkles /></button></div>
        <div className="rail-spacer" />
        <div className="rail-group rail-secondary">
          <button className="rail-button" aria-label="任务历史，规划中" disabled><RotateCcw /></button>
          <button className="rail-button" aria-label="通知，规划中" disabled><Bell /></button>
          <span className="rail-avatar" aria-label="开发用户">A</span>
          <button className="rail-button" aria-label="打开系统设置" onClick={() => dispatch({ type: "open", id: "settings" })}><Settings /></button>
        </div>
      </aside>

      <section className="desktop-grid" aria-label="桌面应用">
        <DesktopShortcut label="文件管理" ariaLabel="文件管理，规划中" tone="files" icon={<FolderClosed />} disabled />
        <DesktopShortcut label="回收站" ariaLabel="回收站，规划中" tone="trash" icon={<Trash2 />} disabled />
        <DesktopShortcut label="系统设置" ariaLabel="打开系统设置" tone="settings" icon={<Settings />} active={isOpen("settings")} onClick={() => dispatch({ type: "open", id: "settings" })} />
        <DesktopShortcut label="资源管理" ariaLabel="打开资源管理" tone="resources" icon={<Activity />} active={isOpen("resources")} onClick={() => dispatch({ type: "open", id: "resources" })} />
        <DesktopShortcut label="终端" ariaLabel="打开终端" tone="terminal" icon={<SquareTerminal />} active={isOpen("terminal")} onClick={() => dispatch({ type: "open", id: "terminal" })} />
        <DesktopShortcut label="应用中心" ariaLabel="应用中心，规划中" tone="store" icon={<ShoppingBag />} disabled />
        <DesktopShortcut label="影视" ariaLabel="影视，规划中" tone="video" icon={<PlaySquare />} disabled />
        <DesktopShortcut label="下载" ariaLabel="下载，规划中" tone="download" icon={<Download />} disabled />
        <DesktopShortcut label="文件快照" ariaLabel="文件快照，规划中" tone="snapshot" icon={<Camera />} disabled />
        <DesktopShortcut label="Docker" ariaLabel="Docker，规划中" tone="docker" icon={<Box />} disabled />
        <DesktopShortcut label="相册" ariaLabel="相册，规划中" tone="photos" icon={<Image />} disabled />
        <DesktopShortcut label="日志" ariaLabel="日志，规划中" tone="logs" icon={<FileText />} disabled />
        <DesktopShortcut label="虚拟机" ariaLabel="虚拟机，规划中" tone="vm" icon={<Monitor />} disabled />
        <DesktopShortcut label="备份" ariaLabel="备份，规划中" tone="backup" icon={<ShieldCheck />} disabled />
        <DesktopShortcut label="音乐" ariaLabel="音乐，规划中" tone="music" icon={<Music2 />} disabled />
        <DesktopShortcut label="AI 助手" ariaLabel="AI 助手，规划中" tone="ai" icon={<Bot />} disabled />
      </section>

      <StatusBar host={host} />

      <section className="window-layer" aria-label="A-NAS 桌面窗口">
        {windows.map((window) => {
          // Minimized windows stay mounted so a running terminal session survives.
          if (!window.open) return null;
          return (
            <AppWindow key={window.id} model={window} dispatch={dispatch}>
              {window.id === "resources" && <ResourcePanel host={host} />}
              {window.id === "settings" && <SettingsPanel state={host.snapshot} />}
              {window.id === "terminal" && <TerminalPanel />}
            </AppWindow>
          );
        })}
      </section>

      <Dock
        entries={windows
          .filter((window) => window.open)
          .sort((a, b) => a.opened - b.opened)
          .map((window) => ({ id: window.id, title: window.title, minimized: window.minimized, icon: windowIcons[window.id], tone: windowTones[window.id] }))}
        focusedID={focusedID}
        onSelect={(id, state) => dispatch({ type: state === "focused" ? "minimize" : "open", id: id as WindowID })}
      />
    </main>
  );
}

function AppWindow({ model, dispatch, children }: { model: WindowModel; dispatch: (action: WindowAction) => void; children: ReactNode }) {
  const beginDrag = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (model.maximized || (event.target as HTMLElement).closest("button")) return;
    event.preventDefault();
    dispatch({ type: "focus", id: model.id });
    const origin = { pointerX: event.clientX, pointerY: event.clientY, x: model.x, y: model.y };
    const move = (moveEvent: PointerEvent) => dispatch({
      type: "move",
      id: model.id,
      x: Math.max(88, origin.x + moveEvent.clientX - origin.pointerX),
      y: Math.max(12, origin.y + moveEvent.clientY - origin.pointerY),
    });
    const stop = () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", stop);
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", stop, { once: true });
  };

  const beginResize = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (model.maximized) return;
    event.preventDefault();
    const origin = { pointerX: event.clientX, pointerY: event.clientY, width: model.width, height: model.height };
    const move = (moveEvent: PointerEvent) => dispatch({
      type: "resize",
      id: model.id,
      width: Math.max(520, origin.width + moveEvent.clientX - origin.pointerX),
      height: Math.max(330, origin.height + moveEvent.clientY - origin.pointerY),
    });
    const stop = () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", stop);
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", stop, { once: true });
  };

  const Icon = windowIcons[model.id];
  const style = model.maximized
    ? { left: 88, top: 12, right: 12, bottom: 12, zIndex: model.z }
    : { left: model.x, top: model.y, width: model.width, height: model.height, zIndex: model.z };

  return (
    <section className={`app-window ${model.maximized ? "maximized" : ""}`} hidden={model.minimized} style={style} role="dialog" aria-label={model.title} onPointerDown={() => dispatch({ type: "focus", id: model.id })}>
      <div className="window-titlebar" onPointerDown={beginDrag}>
        <div className="window-title"><span className="window-app-icon"><Icon size={17} /></span>{model.title}</div>
        <div className="window-actions">
          <button aria-label={`最小化${model.title}`} onClick={() => dispatch({ type: "minimize", id: model.id })}><Minus /></button>
          <button aria-label={`${model.maximized ? "还原" : "最大化"}${model.title}`} onClick={() => dispatch({ type: "maximize", id: model.id })}>{model.maximized ? <PanelLeftClose /> : <Maximize2 />}</button>
          <button className="close" aria-label={`关闭${model.title}`} onClick={() => dispatch({ type: "close", id: model.id })}><X /></button>
        </div>
      </div>
      <div className={`window-content window-content-${model.id}`}>{children}</div>
      {!model.maximized && <div className="resize-handle" aria-hidden="true" onPointerDown={beginResize} />}
    </section>
  );
}

function ResourcePanel({ host }: { host: ReturnType<typeof useHostState> }) {
  const { snapshot } = host;
  if (!snapshot && host.loading) {
    return <div className="center-state"><span className="loader" /><h2>正在读取设备状态…</h2><p>正在连接 A-NAS 产品服务</p></div>;
  }
  if (!snapshot) {
    return <div className="center-state"><CircleAlert /><h2>暂时无法读取设备状态</h2><p>请确认产品服务和 Host Agent 正在运行。</p><button className="primary" onClick={() => void host.refresh()}>重新连接</button></div>;
  }
  return (
    <div className="resource-page">
      <div className="page-heading">
        <div><p className="section-label">DEVICE OVERVIEW</p><h2>{snapshot.system.hostname}</h2><p>{snapshot.system.operatingSystem.name} {snapshot.system.operatingSystem.version} · {snapshot.system.architecture}</p></div>
        <button className="icon-button refresh" aria-label="刷新设备状态" disabled={host.refreshing} onClick={() => void host.refresh()}><RefreshCw className={host.refreshing ? "spinning" : ""} /></button>
      </div>
      {host.disconnected && <div className="status-banner error"><CircleAlert />连接中断，正在显示上次成功读取的数据</div>}
      {isStale(snapshot) && !host.disconnected && <div className="status-banner warning"><CircleAlert />实时观测时间较早，正在等待新数据</div>}

      <div className="overview-grid">
        <article className="hero-card"><div className="hero-icon"><Database /></div><div><span>已发现磁盘</span><strong>{snapshot.disks.length}</strong><small>{snapshot.disks.length ? `总容量 ${formatCapacity(snapshot.disks.reduce((sum, disk) => sum + disk.capacityBytes, 0))}` : "尚未发现块设备"}</small></div></article>
        <MetricCard label="系统健康" value={<HealthBadge value={snapshot.system.health} />} detail="未评估不会显示为健康" />
        <MetricCard label="运行时间" value={formatUptime(snapshot.system.uptimeSeconds)} detail={`观测于 ${formatTime(snapshot.observedAt)}`} />
        <MetricCard label="产品版本" value={snapshot.productVersion} detail={<SourceBadge state={snapshot} />} />
      </div>

      <div className="section-heading"><div><p className="section-label">STORAGE</p><h3>磁盘与设备</h3></div><span>{snapshot.disks.length} 个设备</span></div>
      {snapshot.disks.length === 0 ? (
        <div className="empty-state"><HardDrive /><h3>未检测到磁盘</h3><p>设备重新出现后会自动更新。</p></div>
      ) : (
        <div className="disk-list">
          {snapshot.disks.map((disk) => (
            <article className="disk-card" key={disk.id}>
              <div className={`disk-visual ${disk.rotational ? "rotational" : "solid"}`}><HardDrive /></div>
              <div className="disk-main"><div className="disk-title"><h4>{disk.model}</h4><RoleBadge role={disk.role} /></div><p>{disk.transport.toUpperCase()} · {disk.rotational ? "机械磁盘" : "固态存储"}</p><small>稳定 ID · {shortID(disk.id)}</small></div>
              <div className="disk-stats"><strong>{formatCapacity(disk.capacityBytes)}</strong><HealthBadge value={disk.health} /><span>{disk.temperatureCelsius === undefined ? "温度 —" : `${disk.temperatureCelsius}°C`}</span></div>
            </article>
          ))}
        </div>
      )}
    </div>
  );
}

function SettingsPanel({ state }: { state?: HostState }) {
  if (!state) return <div className="center-state"><span className="loader" /><h2>正在载入设置…</h2></div>;
  const rows = [
    ["设备名称", state.system.hostname],
    ["操作系统", `${state.system.operatingSystem.name} ${state.system.operatingSystem.version}`],
    ["系统架构", state.system.architecture],
    ["运行时间", formatUptime(state.system.uptimeSeconds)],
    ["产品版本", state.productVersion],
    ["数据来源", state.dataSource === "live" ? "实时主机" : "模拟数据"],
    ["观测时间", formatTime(state.observedAt)],
  ];
  return (
    <div className="settings-layout">
      <aside className="settings-sidebar">
        <p>设置</p>
        <button className="active"><Home />设备概览</button>
        <button disabled><UserRound />用户与权限<small>规划中</small></button>
        <button disabled><ShieldCheck />安全与远程<small>规划中</small></button>
      </aside>
      <div className="settings-page">
        <div className="settings-hero"><div className="settings-device"><span className="brand-mark large">A</span></div><div><p className="section-label">ABOUT THIS A-NAS</p><h2>{state.system.hostname}</h2><p>只读硬件集成基线 · 开发预览</p></div></div>
        <div className="settings-card">
          {rows.map(([label, value]) => <div className="settings-row" key={label}><span>{label}</span><strong>{value}</strong><ChevronRight /></div>)}
        </div>
        <div className="privacy-note"><ShieldCheck /><div><strong>本地优先</strong><p>当前页面通过同源只读接口获取状态，不会执行磁盘写操作。</p></div></div>
      </div>
    </div>
  );
}

function DesktopShortcut({ label, ariaLabel, tone, icon, onClick, active, disabled }: { label: string; ariaLabel: string; tone: string; icon: ReactNode; onClick?: () => void; active?: boolean; disabled?: boolean }) {
  return <button className={`desktop-shortcut ${active ? "running" : ""}`} aria-label={ariaLabel} title={disabled ? `${label} · 规划中` : label} disabled={disabled} onClick={onClick}><span className={`desktop-icon icon-${tone}`}>{icon}</span><span>{label}</span>{disabled && <small>规划中</small>}</button>;
}

function MetricCard({ label, value, detail }: { label: string; value: ReactNode; detail: ReactNode }) {
  return <article className="metric-card"><span>{label}</span><strong>{value}</strong><small>{detail}</small></article>;
}

function HealthBadge({ value }: { value: Health }) {
  const labels: Record<Health, string> = { healthy: "健康", warning: "注意", critical: "严重", unknown: "未知" };
  return <span className={`health-badge ${value}`}><span />{labels[value]}</span>;
}

function RoleBadge({ role }: { role: DiskRole }) {
  const labels: Record<DiskRole, string> = { system: "系统盘", data: "数据盘", unassigned: "未分配" };
  return <span className={`role-badge ${role}`}>{labels[role]}</span>;
}

function windowReducer(windows: WindowModel[], action: WindowAction): WindowModel[] {
  if (action.type === "show-desktop") return windows.map((window) => window.open ? { ...window, minimized: true } : window);
  const top = Math.max(...windows.map((window) => window.z)) + 1;
  const nextOpened = Math.max(...windows.map((window) => window.opened)) + 1;
  return windows.map((window) => {
    if (window.id !== action.id) return window;
    switch (action.type) {
      case "open": return { ...window, open: true, minimized: false, z: top, opened: window.open ? window.opened : nextOpened };
      case "close": return { ...window, open: false, minimized: false };
      case "minimize": return { ...window, minimized: true };
      case "focus": return window.z === top - 1 ? window : { ...window, z: top };
      case "maximize": return { ...window, maximized: !window.maximized, z: top };
      case "move": return { ...window, x: action.x, y: action.y };
      case "resize": return { ...window, width: action.width, height: action.height };
    }
  });
}

function formatCapacity(bytes: number): string {
  const gib = bytes / 1024 ** 3;
  if (gib >= 1024) return `${(gib / 1024).toFixed(1)} TiB`;
  return `${gib.toFixed(1)} GiB`;
}

function formatUptime(seconds: number): string {
  const days = Math.floor(seconds / 86400);
  const hours = Math.floor((seconds % 86400) / 3600);
  if (days > 0) return `${days} 天 ${hours} 小时`;
  const minutes = Math.floor((seconds % 3600) / 60);
  return `${hours} 小时 ${minutes} 分钟`;
}

function formatTime(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "—" : new Intl.DateTimeFormat("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit" }).format(date);
}

function shortID(value: string): string {
  return value.length > 24 ? `${value.slice(0, 14)}…${value.slice(-6)}` : value;
}

function isStale(state: HostState): boolean {
  return state.dataSource === "live" && Date.now() - new Date(state.observedAt).getTime() > 30_000;
}
