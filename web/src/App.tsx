import {
  Activity,
  Bell,
  Bot,
  Box,
  Camera,
  ChevronDown,
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
  LogOut,
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
import { Component, ErrorInfo, FormEvent, PointerEvent as ReactPointerEvent, ReactNode, useCallback, useEffect, useReducer, useState } from "react";

import {
  APIError, DiskRole, FileEntry, Health, HostState, Session, Snapshot, SnapshotEntry, Space, StoragePlan, TrashItem, User,
  confirmStoragePlan, createDirectory, createMember, createSnapshot, createStoragePlan, currentSession, deleteFile,
  deleteSnapshot, disableMember, executeStoragePlan, fileDownloadURL, getSetupStatus, listEntries, listSnapshots,
  listSnapshotEntries, listSpaces, listTrash, listUsers, listVolumes, login, logout, purgeTrash, resetMember,
  restoreSnapshotEntry, restoreTrash, setupAdministrator, uploadFile,
} from "./api";
import { TerminalPanel } from "./TerminalPanel";
import { useHostState } from "./useHostState";
import "./styles.css";

type WindowID = "files" | "trash" | "snapshots" | "accounts" | "storage" | "resources" | "settings" | "terminal";

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
	{ id: "files", title: "文件管理", open: false, minimized: false, maximized: false, x: 230, y: 70, width: 900, height: 620, z: 3 },
	{ id: "trash", title: "回收站", open: false, minimized: false, maximized: false, x: 280, y: 90, width: 760, height: 540, z: 2 },
	{ id: "snapshots", title: "文件快照", open: false, minimized: false, maximized: false, x: 300, y: 100, width: 800, height: 560, z: 2 },
	{ id: "accounts", title: "账号管理", open: false, minimized: false, maximized: false, x: 330, y: 110, width: 760, height: 540, z: 2 },
	{ id: "storage", title: "存储初始化", open: false, minimized: false, maximized: false, x: 260, y: 80, width: 850, height: 590, z: 2 },
  { id: "resources", title: "资源管理", open: false, minimized: false, maximized: false, x: 340, y: 94, width: 880, height: 610, z: 2 },
  { id: "settings", title: "系统设置", open: false, minimized: false, maximized: false, x: 390, y: 126, width: 760, height: 550, z: 1 },
  { id: "terminal", title: "终端", open: false, minimized: false, maximized: false, x: 300, y: 70, width: 820, height: 520, z: 0 },
];

const windowIcons: Record<WindowID, LucideIcon> = {
  files: FolderClosed,
  trash: Trash2,
  snapshots: Camera,
  accounts: UserRound,
  storage: Database,
  resources: Activity,
  settings: Settings,
  terminal: SquareTerminal,
};

export default function App() {
	const [session, setSessionState] = useState<Session>();
	const [mode, setMode] = useState<"loading" | "setup" | "login">("loading");
	const [error, setError] = useState("");
	useEffect(() => {
		let active = true;
		void getSetupStatus().then(async ({ setupRequired }) => {
			if (!active) return;
			if (setupRequired) setMode("setup");
			else {
				try { const value = await currentSession(); if (active) setSessionState(value); }
				catch { if (active) setMode("login"); }
			}
		}).catch(() => { if (active) { setError("无法连接 A-NAS 产品服务"); setMode("login"); } });
		return () => { active = false; };
	}, []);
	if (!session) return <Authentication mode={mode} error={error} onAuthenticated={setSessionState} />;
	return <Desktop session={session} onLogout={() => { void logout().finally(() => { setSessionState(undefined); setMode("login"); }); }} />;
}

function Authentication({ mode, error: initialError, onAuthenticated }: { mode: "loading" | "setup" | "login"; error: string; onAuthenticated: (session: Session) => void }) {
	const [error, setError] = useState(initialError);
	const [submitting, setSubmitting] = useState(false);
	if (mode === "loading") return <main className="auth-shell"><div className="auth-card"><span className="loader" /><h1>正在启动 A-NAS…</h1></div></main>;
	const submit = async (event: FormEvent<HTMLFormElement>) => {
		event.preventDefault(); setSubmitting(true); setError("");
		const data = new FormData(event.currentTarget);
		try {
			const session = mode === "setup"
				? await setupAdministrator(String(data.get("username")), String(data.get("password")))
				: await login(String(data.get("username")), String(data.get("password")));
			onAuthenticated(session);
		} catch (caught) { setError(caught instanceof APIError ? caught.message : "请求失败，请稍后再试"); }
		finally { setSubmitting(false); }
	};
	return <main className="auth-shell"><form className="auth-card" autoComplete="off" onSubmit={(event) => void submit(event)}>
		<span className="brand-mark large">A</span><p className="section-label">A-NAS v1.0.1 PREVIEW</p><h1>{mode === "setup" ? "启用 A-NAS" : "登录 A-NAS"}</h1>
		{mode === "setup" && <p>创建本机管理员账号以启用设备。</p>}
		<label>账号<input name="username" required autoComplete="username" /></label>
		<label>密码<input name="password" type="password" minLength={12} required autoComplete={mode === "setup" ? "new-password" : "current-password"} /></label>
		{error && <p className="form-error">{error}</p>}<button className="primary" disabled={submitting}>{submitting ? "处理中…" : mode === "setup" ? "创建管理员并启用" : "登录"}</button>
	</form></main>;
}

function Desktop({ session, onLogout }: { session: Session; onLogout: () => void }) {
  const host = useHostState(true);
  const [windows, dispatch] = useReducer(windowReducer, initialWindows);
  const isOpen = (id: WindowID) => windows.some((window) => window.id === id && window.open);
  const now = useCurrentMinute();

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
		  <span className="rail-avatar" aria-label={`当前用户 ${session.user.username}`}>{session.user.username.slice(0, 1).toUpperCase()}</span>
		  <button className="rail-button" aria-label="退出登录" onClick={onLogout}><LogOut /></button>
          <button className="rail-button" aria-label="打开系统设置" onClick={() => dispatch({ type: "open", id: "settings" })}><Settings /></button>
        </div>
      </aside>

      <section className="desktop-grid" aria-label="桌面应用">
		<DesktopShortcut label="文件管理" ariaLabel="打开文件管理" tone="files" icon={<FolderClosed />} active={windows.find((item) => item.id === "files")?.open} onClick={() => dispatch({ type: "open", id: "files" })} />
		<DesktopShortcut label="回收站" ariaLabel="打开回收站" tone="trash" icon={<Trash2 />} active={windows.find((item) => item.id === "trash")?.open} onClick={() => dispatch({ type: "open", id: "trash" })} />
		<DesktopShortcut label="系统设置" ariaLabel="打开系统设置" tone="settings" icon={<Settings />} active={windows.find((item) => item.id === "settings")?.open} onClick={() => dispatch({ type: "open", id: "settings" })} />
		<DesktopShortcut label="资源管理" ariaLabel="打开资源管理" tone="resources" icon={<Activity />} active={windows.find((item) => item.id === "resources")?.open} onClick={() => dispatch({ type: "open", id: "resources" })} />
		{session.user.role === "admin" && <DesktopShortcut label="终端" ariaLabel="打开终端" tone="terminal" icon={<SquareTerminal />} active={isOpen("terminal")} onClick={() => dispatch({ type: "open", id: "terminal" })} />}
        <DesktopShortcut label="应用中心" ariaLabel="应用中心，规划中" tone="store" icon={<ShoppingBag />} disabled />
        <DesktopShortcut label="影视" ariaLabel="影视，规划中" tone="video" icon={<PlaySquare />} disabled />
        <DesktopShortcut label="下载" ariaLabel="下载，规划中" tone="download" icon={<Download />} disabled />
		<DesktopShortcut label="文件快照" ariaLabel="打开文件快照" tone="snapshot" icon={<Camera />} onClick={() => dispatch({ type: "open", id: "snapshots" })} />
		{session.user.role === "admin" && <DesktopShortcut label="账号管理" ariaLabel="打开账号管理" tone="settings" icon={<UserRound />} onClick={() => dispatch({ type: "open", id: "accounts" })} />}
		{session.user.role === "admin" && <DesktopShortcut label="存储初始化" ariaLabel="打开存储初始化" tone="resources" icon={<Database />} onClick={() => dispatch({ type: "open", id: "storage" })} />}
        <DesktopShortcut label="Docker" ariaLabel="Docker，规划中" tone="docker" icon={<Box />} disabled />
        <DesktopShortcut label="相册" ariaLabel="相册，规划中" tone="photos" icon={<Image />} disabled />
        <DesktopShortcut label="日志" ariaLabel="日志，规划中" tone="logs" icon={<FileText />} disabled />
        <DesktopShortcut label="虚拟机" ariaLabel="虚拟机，规划中" tone="vm" icon={<Monitor />} disabled />
        <DesktopShortcut label="备份" ariaLabel="备份，规划中" tone="backup" icon={<ShieldCheck />} disabled />
        <DesktopShortcut label="音乐" ariaLabel="音乐，规划中" tone="music" icon={<Music2 />} disabled />
        <DesktopShortcut label="AI 助手" ariaLabel="AI 助手，规划中" tone="ai" icon={<Bot />} disabled />
      </section>

      <div className="resource-pill" aria-label="设备连接状态">
        <div className={`connection ${host.disconnected ? "offline" : "online"}`}><span className="connection-dot" />{host.disconnected ? "连接中断" : host.snapshot ? "设备在线" : "正在连接"}</div>
        <div className="resource-divider" />
        <div className="resource-copy">
          <SourceBadge state={host.snapshot} />
          <span className="clock">{now}</span>
        </div>
        <ChevronDown className="resource-chevron" />
      </div>

      <section className="window-layer" aria-label="A-NAS 桌面窗口">
        {windows.map((window) => {
          // Minimized windows stay mounted so a running terminal session survives.
          if (!window.open) return null;
          return (
            <AppWindow key={window.id} model={window} dispatch={dispatch}>
			  {window.id === "resources" && <ResourcePanel host={host} />}
			  {window.id === "settings" && <SettingsPanel state={host.snapshot} />}
			  {window.id === "files" && <FilePanel />}
			  {window.id === "trash" && <TrashPanel />}
			  {window.id === "snapshots" && <SnapshotPanel />}
			  {window.id === "accounts" && <AccountsPanel currentUser={session.user} />}
			  {window.id === "storage" && <StoragePanel state={host.snapshot} />}
			  {window.id === "terminal" && <TerminalPanel />}
            </AppWindow>
          );
        })}
      </section>

      <nav className={`task-shelf ${windows.some((window) => window.open) ? "visible" : ""}`} aria-label="已打开窗口">
        {windows.filter((window) => window.open).map((window) => {
          const Icon = windowIcons[window.id];
          return (
            <button
              key={window.id}
              className={window.minimized ? "task-button minimized" : "task-button"}
              aria-label={window.minimized ? `恢复${window.title}` : `聚焦${window.title}`}
              onClick={() => dispatch({ type: "open", id: window.id })}
            >
              <Icon size={18} />
              <span>{window.title}</span>
            </button>
          );
        })}
      </nav>
    </main>
  );
}

function useCurrentMinute(): string {
  const [now, setNow] = useState(() => new Date());

  useEffect(() => {
    let timeout: number;
    const scheduleNextMinute = () => {
      const current = new Date();
      const millisecondsIntoMinute = current.getSeconds() * 1_000 + current.getMilliseconds();
      timeout = window.setTimeout(() => {
        setNow(new Date());
        scheduleNextMinute();
      }, 60_000 - millisecondsIntoMinute);
    };

    scheduleNextMinute();
    return () => window.clearTimeout(timeout);
  }, []);

  return new Intl.DateTimeFormat("zh-CN", { hour: "2-digit", minute: "2-digit" }).format(now);
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
      <div className={`window-content window-content-${model.id}`}><PanelErrorBoundary>{children}</PanelErrorBoundary></div>
      {!model.maximized && <div className="resize-handle" aria-hidden="true" onPointerDown={beginResize} />}
    </section>
  );
}

class PanelErrorBoundary extends Component<{ children: ReactNode }, { failed: boolean }> {
  state = { failed: false };

  static getDerivedStateFromError() { return { failed: true }; }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error("A-NAS panel render failed", error, info.componentStack);
  }

  render() {
    if (!this.state.failed) return this.props.children;
    return <div className="center-state"><CircleAlert /><h2>这个页面暂时无法显示</h2><p>桌面和后台服务仍在运行，可以重新载入界面后继续。</p><button className="primary" onClick={() => window.location.reload()}>重新载入界面</button></div>;
  }
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

function FilePanel() {
	const [spaces, setSpaces] = useState<Space[]>([]);
	const [spaceID, setSpaceID] = useState("");
	const [entries, setEntries] = useState<FileEntry[]>([]);
	const [trail, setTrail] = useState<Array<{ id: string; name: string }>>([]);
	const [error, setError] = useState("");
	const parentID = trail.at(-1)?.id ?? "";
	const refresh = useCallback(async (selected = spaceID, parent = parentID) => {
		if (!selected) return;
		try { setEntries(await listEntries(selected, parent)); setError(""); }
		catch (caught) { setError(messageOf(caught)); }
	}, [spaceID, parentID]);
	useEffect(() => { void listSpaces().then((items) => { setSpaces(items); if (items[0]) setSpaceID(items[0].id); }).catch((caught) => setError(messageOf(caught))); }, []);
	useEffect(() => { setTrail([]); if (spaceID) void refresh(spaceID, ""); }, [spaceID]);
	const makeDirectory = async (event: FormEvent<HTMLFormElement>) => {
		event.preventDefault(); const form = event.currentTarget; const name = String(new FormData(form).get("name"));
		try { await createDirectory(spaceID, parentID, name); form.reset(); await refresh(); } catch (caught) { setError(messageOf(caught)); }
	};
	return <div className="product-page"><div className="page-heading"><div><p className="section-label">FILES</p><h2>文件管理</h2></div><select aria-label="空间" value={spaceID} onChange={(event) => setSpaceID(event.target.value)}>{spaces.map((space) => <option key={space.id} value={space.id}>{space.kind === "shared" ? "共享空间" : `个人空间 · ${space.name}`}</option>)}</select></div>
		{error && <PanelNotice error={error} />}
		<div className="file-toolbar"><button onClick={() => { setTrail((value) => value.slice(0, -1)); }} disabled={!trail.length}>返回上级</button><span>/{trail.map((item) => item.name).join("/")}</span><button onClick={() => void refresh()}>刷新</button></div>
		<form className="inline-form" onSubmit={(event) => void makeDirectory(event)}><input name="name" aria-label="新目录名称" placeholder="新目录名称" required /><button>新建目录</button><label className="upload-button">上传文件<input type="file" onChange={(event) => { const file = event.target.files?.[0]; if (file) void uploadFile(spaceID, parentID, file).then(() => refresh()).catch((caught) => setError(messageOf(caught))); }} /></label></form>
		<div className="data-list">{entries.map((entry) => <div className="data-row" key={entry.id}><span>{entry.kind === "directory" ? <FolderClosed /> : <FileText />}</span>{entry.kind === "directory" ? <button className="link-button" onClick={() => setTrail((value) => [...value, { id: entry.id, name: entry.name }])}>{entry.name}</button> : <a href={fileDownloadURL(entry.id)}>{entry.name}</a>}<small>{entry.kind === "file" ? formatCapacity(entry.sizeBytes) : "目录"}</small><button className="danger-link" onClick={() => void deleteFile(entry.id).then(() => refresh()).catch((caught) => setError(messageOf(caught)))}>删除</button></div>)}</div>
		{!entries.length && <div className="empty-compact">这个目录是空的</div>}
	</div>;
}

function TrashPanel() {
	const [items, setItems] = useState<TrashItem[]>([]); const [error, setError] = useState("");
	const refresh = useCallback(() => listTrash().then(setItems).catch((caught) => setError(messageOf(caught))), []);
	useEffect(() => { void refresh(); }, [refresh]);
	return <div className="product-page"><div className="page-heading"><div><p className="section-label">RECYCLE BIN</p><h2>回收站</h2><p>项目默认保留 30 天</p></div><button onClick={() => void refresh()}>刷新</button></div>{error && <PanelNotice error={error} />}<div className="data-list">{items.map((item) => <div className="data-row" key={item.id}><Trash2 /><strong>{item.name}</strong><small>{new Date(item.deletedAt).toLocaleString("zh-CN")}</small><button onClick={() => void restoreTrash(item.id, item.name).then(refresh).catch((caught) => setError(messageOf(caught)))}>恢复</button><button className="danger-link" onClick={() => void purgeTrash(item.id).then(refresh).catch((caught) => setError(messageOf(caught)))}>永久删除</button></div>)}</div>{!items.length && <div className="empty-compact">回收站为空</div>}</div>;
}

function SnapshotPanel() {
	const [spaces, setSpaces] = useState<Space[]>([]); const [spaceID, setSpaceID] = useState(""); const [snapshots, setSnapshots] = useState<Snapshot[]>([]); const [selected, setSelected] = useState<Snapshot>(); const [entries, setEntries] = useState<SnapshotEntry[]>([]); const [error, setError] = useState("");
	const refresh = useCallback(async (id = spaceID) => { if (!id) return; try { setSnapshots(await listSnapshots(id)); setError(""); } catch (caught) { setError(messageOf(caught)); } }, [spaceID]);
	useEffect(() => { void listSpaces().then((items) => { setSpaces(items); if (items[0]) setSpaceID(items[0].id); }).catch((caught) => setError(messageOf(caught))); }, []);
	useEffect(() => { setSelected(undefined); setEntries([]); if (spaceID) void refresh(spaceID); }, [spaceID]);
	const create = async (event: FormEvent<HTMLFormElement>) => { event.preventDefault(); const form = event.currentTarget; try { await createSnapshot(spaceID, String(new FormData(form).get("name"))); form.reset(); await refresh(); } catch (caught) { setError(messageOf(caught)); } };
	return <div className="product-page"><div className="page-heading"><div><p className="section-label">READ-ONLY SNAPSHOTS</p><h2>文件快照</h2></div><select aria-label="快照空间" value={spaceID} onChange={(event) => setSpaceID(event.target.value)}>{spaces.map((space) => <option key={space.id} value={space.id}>{space.name}</option>)}</select></div>{error && <PanelNotice error={error} />}<form className="inline-form" onSubmit={(event) => void create(event)}><input name="name" placeholder="快照名称" required /><button>创建只读快照</button></form><div className="split-list"><div className="data-list">{snapshots.map((snapshot) => <div className="data-row" key={snapshot.id}><Camera /><button className="link-button" onClick={() => { setSelected(snapshot); void listSnapshotEntries(snapshot.id).then(setEntries).catch((caught) => setError(messageOf(caught))); }}>{snapshot.name}</button><button className="danger-link" onClick={() => void deleteSnapshot(snapshot.id).then(() => refresh()).catch((caught) => setError(messageOf(caught)))}>删除</button></div>)}</div><div className="data-list">{selected ? entries.map((entry) => <div className="data-row" key={entry.id}><FileText /><strong>{entry.name}</strong>{entry.kind === "file" && <button onClick={() => void restoreSnapshotEntry(selected.id, entry.id, entry.name).then(() => setError("")).catch((caught) => setError(messageOf(caught)))}>恢复为新文件</button>}</div>) : <div className="empty-compact">选择快照浏览</div>}</div></div></div>;
}

function AccountsPanel({ currentUser }: { currentUser: User }) {
	const [users, setUsers] = useState<User[]>([]); const [error, setError] = useState("");
	const refresh = useCallback(() => listUsers().then(setUsers).catch((caught) => setError(messageOf(caught))), []);
	useEffect(() => { void refresh(); }, [refresh]);
	const create = async (event: FormEvent<HTMLFormElement>) => { event.preventDefault(); const form = event.currentTarget; const data = new FormData(form); try { await createMember(String(data.get("username")), String(data.get("password"))); form.reset(); await refresh(); } catch (caught) { setError(messageOf(caught)); } };
	return <div className="product-page"><div className="page-heading"><div><p className="section-label">ACCOUNTS</p><h2>账号与权限</h2></div></div>{error && <PanelNotice error={error} />}<form className="inline-form" autoComplete="off" onSubmit={(event) => void create(event)}><input name="username" placeholder="成员账号" required /><input name="password" type="password" minLength={12} autoComplete="new-password" placeholder="初始密码（至少 12 位）" required /><button>创建成员</button></form><div className="data-list">{users.map((user) => <div className="data-row" key={user.id}><UserRound /><strong>{user.username}</strong><small>{user.role} · {user.status}</small>{user.id !== currentUser.id && <><button onClick={() => { const password = window.prompt("输入至少 12 位的新密码"); if (password) void resetMember(user.id, password).then(refresh).catch((caught) => setError(messageOf(caught))); }}>重置密码</button><button className="danger-link" disabled={user.status === "disabled"} onClick={() => void disableMember(user.id).then(refresh).catch((caught) => setError(messageOf(caught)))}>禁用</button></>}</div>)}</div></div>;
}

function StoragePanel({ state }: { state?: HostState }) {
	const [volumes, setVolumes] = useState<Array<{ id: string; state: string; filesystemUuid?: string }>>([]);
	const [plan, setPlan] = useState<StoragePlan>();
	const [phrase, setPhrase] = useState("");
	const [error, setError] = useState("");
	const [busy, setBusy] = useState<"plan" | "confirm" | "execute">();
	const refresh = useCallback(() => listVolumes().then(setVolumes).catch((caught) => setError(messageOf(caught))), []);
	useEffect(() => { void refresh(); }, [refresh]);

	const generate = async (diskID: string) => {
		setBusy("plan"); setError("");
		try { setPlan(await createStoragePlan(diskID)); }
		catch (caught) { setError(messageOf(caught)); }
		finally { setBusy(undefined); }
	};
	const confirm = async () => {
		if (!plan) return;
		setBusy("confirm"); setError("");
		try { setPlan(await confirmStoragePlan(plan.id, phrase)); }
		catch (caught) { setError(messageOf(caught)); }
		finally { setBusy(undefined); }
	};
	const execute = async () => {
		if (!plan) return;
		setBusy("execute"); setError("");
		try { setPlan(await executeStoragePlan(plan.id)); await refresh(); }
		catch (caught) { setError(messageOf(caught)); }
		finally { setBusy(undefined); }
	};
	const signatures = plan?.signatures ?? [];
	const actions = plan?.actions ?? [];

	return <div className="product-page">
		<div className="page-heading"><div><p className="section-label">DATA VOLUME</p><h2>存储初始化</h2><p>只允许非系统、非 USB、未占用且身份稳定的磁盘</p></div></div>
		{error && <PanelNotice error={error} />}
		{busy && <div className="status-banner"><span className="loader" />{busy === "plan" ? "正在生成计划…" : busy === "confirm" ? "正在确认计划…" : "正在创建数据卷，请勿关闭设备…"}</div>}
		<div className="data-list">{volumes.map((volume) => <div className="data-row" key={volume.id}><Database /><strong>{volume.id}</strong><small>{volume.state} · {volume.filesystemUuid ?? "等待 UUID"}</small></div>)}</div>
		{state?.disks.filter((disk) => disk.role === "unassigned").map((disk) => <div className="danger-zone" key={disk.id}><strong>{disk.model} · {formatCapacity(disk.capacityBytes)}</strong><p>{disk.eligibleForDataVolume ? `稳定 ID：${shortID(disk.id)}` : disk.ineligibleReasons.join("；")}</p><button disabled={!disk.eligibleForDataVolume || Boolean(plan) || Boolean(busy)} onClick={() => void generate(disk.id)}>{busy === "plan" ? "正在生成…" : "生成格式化计划"}</button></div>)}
		{plan && <div className="plan-card"><h3>破坏性操作计划</h3><p>身份指纹：<code>{plan.fingerprint}</code></p><p>将清除的已知签名：{signatures.length ? signatures.join("、") : "未检测到文件系统签名"}</p>{actions.map((action) => <p key={action.kind}>• {action.description}</p>)}<p>有效期至 {new Date(plan.expiresAt).toLocaleString("zh-CN")}</p><label>输入确认短语 <code>{plan.confirmationPhrase}</code><input value={phrase} onChange={(event) => setPhrase(event.target.value)} /></label>{plan.state === "planned" && <button disabled={phrase !== plan.confirmationPhrase || Boolean(busy)} onClick={() => void confirm()}>{busy === "confirm" ? "正在确认…" : "确认计划"}</button>}{plan.state === "confirmed" && <button className="danger-button" disabled={Boolean(busy)} onClick={() => void execute()}>{busy === "execute" ? "正在创建数据卷…" : "执行清除并创建数据卷"}</button>}<strong>状态：{busy === "execute" ? "running" : plan.state}</strong></div>}
	</div>;
}

function PanelNotice({ error }: { error: string }) { return <div className="status-banner error"><CircleAlert />{error}</div>; }
function messageOf(error: unknown) { return error instanceof Error ? error.message : "请求失败"; }

function DesktopShortcut({ label, ariaLabel, tone, icon, onClick, active, disabled }: { label: string; ariaLabel: string; tone: string; icon: ReactNode; onClick?: () => void; active?: boolean; disabled?: boolean }) {
  return <button className={`desktop-shortcut ${active ? "running" : ""}`} aria-label={ariaLabel} title={disabled ? `${label} · 规划中` : label} disabled={disabled} onClick={onClick}><span className={`desktop-icon icon-${tone}`}>{icon}</span><span>{label}</span>{disabled && <small>规划中</small>}</button>;
}

function MetricCard({ label, value, detail }: { label: string; value: ReactNode; detail: ReactNode }) {
  return <article className="metric-card"><span>{label}</span><strong>{value}</strong><small>{detail}</small></article>;
}

function SourceBadge({ state }: { state?: HostState }) {
  if (!state) return <span className="source-badge pending">等待数据</span>;
  return <span className={`source-badge ${state.dataSource}`}>{state.dataSource === "live" ? "实时主机" : "模拟数据"}</span>;
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
  return windows.map((window) => {
    if (window.id !== action.id) return window;
    switch (action.type) {
      case "open": return { ...window, open: true, minimized: false, z: top };
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
