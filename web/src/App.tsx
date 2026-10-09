import {
  Activity,
  ArrowLeft,
  ArrowRight,
  ArrowUp,
  ArrowUpDown,
  Bell,
  Bot,
  Box,
  Camera,
  ChevronDown,
  ChevronRight,
  CircleAlert,
  ClipboardPaste,
  Clock3,
  Copy,
  Database,
  Download,
  Ellipsis,
  FileText,
  Film,
  FolderClosed,
  FolderOpen,
  FolderPlus,
  Grid2X2,
  HardDrive,
  Home,
  Image,
  List,
  LogOut,
  Maximize2,
  Minus,
  Monitor,
  Music2,
  PanelLeftClose,
  Pencil,
  PlaySquare,
  RefreshCw,
  RotateCcw,
  Scissors,
  Search,
  Settings,
  Share2,
  ShieldCheck,
  ShoppingBag,
  Sparkles,
  SquareTerminal,
  Trash2,
  Upload,
  UserRound,
  UsersRound,
  X,
  type LucideIcon,
} from "lucide-react";
import { Component, CSSProperties, ErrorInfo, FormEvent, KeyboardEvent as ReactKeyboardEvent, PointerEvent as ReactPointerEvent, ReactNode, useCallback, useEffect, useReducer, useRef, useState } from "react";

import {
  APIError, DiskRole, FileEntry, Health, HostState, Notification, Session, Snapshot, SnapshotEntry, Space, StoragePlan, TrashItem, User, ViewingScope, Volume,
  acknowledgeNotification, changePassword, confirmStoragePlan, copyFile, createDirectory, createMember, createSnapshot, createStoragePlan, currentSession, deleteFile,
  deleteSnapshot, disableMember, endViewing, executeStoragePlan, fileDownloadURL, filePreviewURL, getSetupStatus, listEntries, listNotifications, listSnapshots,
  listSnapshotEntries, listSpaces, listTrash, listUsers, listVolumes, login, logout, moveFile, onSessionEnded, purgeTrash, resetMember,
  restoreSnapshotEntry, restoreTrash, setSession, setupAdministrator, startViewing, uploadFile,
} from "./api";
import { AppCenterPanel } from "./AppCenterPanel";
import { DesktopApp, DesktopGrid } from "./DesktopGrid";
import { Dock } from "./Dock";
import { DockerPanel } from "./DockerPanel";
import { isLocalConsole, LocalConsoleScreenSaver } from "./LocalConsoleScreenSaver";
import { PhotosPanel } from "./PhotosPanel";
import { SourceBadge, StatusBar } from "./StatusBar";
import { TerminalPanel } from "./TerminalPanel";
import { useHostState } from "./useHostState";
import "./styles.css";

type WindowID = "files" | "photos" | "trash" | "snapshots" | "accounts" | "storage" | "resources" | "settings" | "terminal" | "docker" | "store";

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
	{ id: "files", title: "文件管理", open: false, minimized: false, maximized: false, x: 230, y: 70, width: 900, height: 620, z: 3, opened: 0 },
	{ id: "photos", title: "相册", open: false, minimized: false, maximized: false, x: 250, y: 64, width: 960, height: 660, z: 2, opened: 0 },
	{ id: "trash", title: "回收站", open: false, minimized: false, maximized: false, x: 280, y: 90, width: 760, height: 540, z: 2, opened: 0 },
	{ id: "snapshots", title: "文件快照", open: false, minimized: false, maximized: false, x: 300, y: 100, width: 800, height: 560, z: 2, opened: 0 },
	{ id: "accounts", title: "账号管理", open: false, minimized: false, maximized: false, x: 330, y: 110, width: 760, height: 540, z: 2, opened: 0 },
	{ id: "storage", title: "存储初始化", open: false, minimized: false, maximized: false, x: 260, y: 80, width: 850, height: 590, z: 2, opened: 0 },
  { id: "resources", title: "资源管理", open: false, minimized: false, maximized: false, x: 340, y: 94, width: 880, height: 610, z: 2, opened: 0 },
  { id: "settings", title: "系统设置", open: false, minimized: false, maximized: false, x: 390, y: 126, width: 760, height: 550, z: 1, opened: 0 },
  { id: "terminal", title: "终端", open: false, minimized: false, maximized: false, x: 300, y: 70, width: 820, height: 520, z: 0, opened: 0 },
  { id: "docker", title: "Docker", open: false, minimized: false, maximized: false, x: 320, y: 82, width: 900, height: 620, z: 0, opened: 0 },
  { id: "store", title: "应用中心", open: false, minimized: false, maximized: false, x: 300, y: 60, width: 940, height: 660, z: 0, opened: 0 },
];

const windowIcons: Record<WindowID, LucideIcon> = {
  files: FolderClosed,
  photos: Image,
  trash: Trash2,
  snapshots: Camera,
  accounts: UserRound,
  storage: Database,
  resources: Activity,
  settings: Settings,
  terminal: SquareTerminal,
  docker: Box,
  store: ShoppingBag,
};
// Dock icons reuse the desktop shortcut gradients so an app looks the same in both places.
const windowTones: Record<WindowID, string> = {
  files: "files",
  photos: "photos",
  trash: "trash",
  snapshots: "snapshot",
  accounts: "settings",
  storage: "resources",
  resources: "resources",
  settings: "settings",
  terminal: "terminal",
  docker: "docker",
  store: "store",
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
	// Browser sessions last 12 hours and any session can be revoked; once
	// polls are rejected, sign in again rather than report a lost connection.
	useEffect(() => {
		if (!session) return;
		return onSessionEnded(() => {
			setSession();
			setSessionState(undefined);
			setError("登录已过期，请重新登录");
			setMode("login");
		});
	}, [session]);
	const authenticated = (value: Session) => { setError(""); setSessionState(value); };
	if (!session) return <Authentication mode={mode} error={error} onAuthenticated={authenticated} />;
	const signOut = () => { void logout().finally(() => { setSessionState(undefined); setMode("login"); }); };
	if (session.user.mustChangePassword) {
		return <PasswordChange onChanged={() => setSessionState({ ...session, user: { ...session.user, mustChangePassword: false } })} onLogout={signOut} />;
	}
	return <Desktop session={session} onLogout={signOut} />;
}

// PasswordChange blocks the desktop after an administrator reset the
// password, so only the member knows the password in use.
function PasswordChange({ onChanged, onLogout }: { onChanged: () => void; onLogout: () => void }) {
	const [error, setError] = useState("");
	const [submitting, setSubmitting] = useState(false);
	const submit = async (event: FormEvent<HTMLFormElement>) => {
		event.preventDefault(); setError("");
		const data = new FormData(event.currentTarget);
		const next = String(data.get("newPassword"));
		if (next !== String(data.get("confirmPassword"))) { setError("两次输入的新密码不一致"); return; }
		setSubmitting(true);
		try { await changePassword(String(data.get("currentPassword")), next); onChanged(); }
		catch (caught) { setError(messageOf(caught)); }
		finally { setSubmitting(false); }
	};
	return <main className="auth-shell"><form className="auth-card" autoComplete="off" onSubmit={(event) => void submit(event)}>
		<span className="brand-mark large">A</span><p className="section-label">PASSWORD RESET</p><h1>设置新密码</h1>
		<p>管理员重置了你的密码。继续使用 A-NAS 和 SMB 共享前，请设置只有你知道的新密码。</p>
		<label>当前密码<input name="currentPassword" type="password" required autoComplete="current-password" /></label>
		<label>新密码<input name="newPassword" type="password" minLength={12} required autoComplete="new-password" /></label>
		<label>确认新密码<input name="confirmPassword" type="password" minLength={12} required autoComplete="new-password" /></label>
		{error && <p className="form-error">{error}</p>}
		<button className="primary" disabled={submitting}>{submitting ? "处理中…" : "保存新密码"}</button>
		<button type="button" className="link-button" onClick={onLogout}>退出登录</button>
	</form></main>;
}

// NotificationCenter shows what administrators did to the account since the
// user last acknowledged it: viewing the private space or resetting the password.
function NotificationCenter() {
	const [items, setItems] = useState<Notification[]>([]);
	useEffect(() => { void listNotifications().then(setItems).catch(() => undefined); }, []);
	if (!items.length) return null;
	const acknowledge = async () => {
		await Promise.allSettled(items.map((item) => acknowledgeNotification(item.id)));
		setItems([]);
	};
	return <div className="notice-layer"><section className="notice-card" role="alertdialog" aria-label="账号通知">
		<p className="section-label">ACCOUNT NOTICE</p><h2>账号通知</h2>
		<ul>{items.map((item) => <li key={item.id}>{describeNotification(item)}</li>)}</ul>
		<button className="primary" onClick={() => void acknowledge()}>知道了</button>
	</section></div>;
}

function describeNotification(item: Notification): string {
	const when = new Date(item.createdAt).toLocaleString("zh-CN");
	if (item.kind === "admin_viewing" || item.kind === "admin_library_viewing") {
		const what = item.kind === "admin_library_viewing" ? "私有图库" : "个人空间";
		const until = item.expiresAt ? `，${new Date(item.expiresAt).toLocaleString("zh-CN")} 自动结束` : "";
		return `管理员 ${item.actorUsername} 于 ${when} 开启了对你${what}的只读查看${until}。原因：${item.reason ?? "未填写"}`;
	}
	return `管理员 ${item.actorUsername} 于 ${when} 重置了你的密码。`;
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
				? await setupAdministrator(String(data.get("username")), String(data.get("password")), isLocalConsole())
				: await login(String(data.get("username")), String(data.get("password")), isLocalConsole());
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
  const openWindow = (id: WindowID) => dispatch({ type: "open", id });
  const desktopApps: DesktopApp[] = [
    { id: "files", label: "文件管理", ariaLabel: "打开文件管理", tone: "files", icon: <FolderClosed />, active: isOpen("files"), onClick: () => openWindow("files") },
    { id: "trash", label: "回收站", ariaLabel: "打开回收站", tone: "trash", icon: <Trash2 />, active: isOpen("trash"), onClick: () => openWindow("trash") },
    { id: "settings", label: "系统设置", ariaLabel: "打开系统设置", tone: "settings", icon: <Settings />, active: isOpen("settings"), onClick: () => openWindow("settings") },
    { id: "resources", label: "资源管理", ariaLabel: "打开资源管理", tone: "resources", icon: <Activity />, active: isOpen("resources"), onClick: () => openWindow("resources") },
    ...(session.user.role === "admin" ? [{ id: "terminal", label: "终端", ariaLabel: "打开终端", tone: "terminal", icon: <SquareTerminal />, active: isOpen("terminal"), onClick: () => openWindow("terminal") }] : []),
    // Docker and the App Center run containers with root-equivalent engine access: administrators only.
    ...(session.user.role === "admin" ? [{ id: "store", label: "应用中心", ariaLabel: "打开应用中心", tone: "store", icon: <ShoppingBag />, active: isOpen("store"), onClick: () => openWindow("store") }] : []),
    { id: "video", label: "影视", ariaLabel: "影视，规划中", tone: "video", icon: <PlaySquare />, disabled: true },
    { id: "download", label: "下载", ariaLabel: "下载，规划中", tone: "download", icon: <Download />, disabled: true },
    { id: "snapshot", label: "文件快照", ariaLabel: "打开文件快照", tone: "snapshot", icon: <Camera />, active: isOpen("snapshots"), onClick: () => openWindow("snapshots") },
    ...(session.user.role === "admin" ? [
      { id: "accounts", label: "账号管理", ariaLabel: "打开账号管理", tone: "settings", icon: <UserRound />, active: isOpen("accounts"), onClick: () => openWindow("accounts") },
      { id: "storage", label: "存储初始化", ariaLabel: "打开存储初始化", tone: "resources", icon: <Database />, active: isOpen("storage"), onClick: () => openWindow("storage") },
    ] : []),
    ...(session.user.role === "admin" ? [{ id: "docker", label: "Docker", ariaLabel: "打开 Docker", tone: "docker", icon: <Box />, active: isOpen("docker"), onClick: () => openWindow("docker") }] : []),
    { id: "photos", label: "相册", ariaLabel: "打开相册", tone: "photos", icon: <Image />, active: isOpen("photos"), onClick: () => openWindow("photos") },
    { id: "logs", label: "日志", ariaLabel: "日志，规划中", tone: "logs", icon: <FileText />, disabled: true },
    { id: "vm", label: "虚拟机", ariaLabel: "虚拟机，规划中", tone: "vm", icon: <Monitor />, disabled: true },
    { id: "backup", label: "备份", ariaLabel: "备份，规划中", tone: "backup", icon: <ShieldCheck />, disabled: true },
    { id: "music", label: "音乐", ariaLabel: "音乐，规划中", tone: "music", icon: <Music2 />, disabled: true },
    { id: "ai", label: "AI 助手", ariaLabel: "AI 助手，规划中", tone: "ai", icon: <Bot />, disabled: true },
  ];
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
		  <span className="rail-avatar" aria-label={`当前用户 ${session.user.username}`}>{session.user.username.slice(0, 1).toUpperCase()}</span>
		  <button className="rail-button" aria-label="退出登录" onClick={onLogout}><LogOut /></button>
          <button className="rail-button" aria-label="打开系统设置" onClick={() => dispatch({ type: "open", id: "settings" })}><Settings /></button>
        </div>
      </aside>

      <DesktopGrid apps={desktopApps} />

      <StatusBar host={host} />

      <NotificationCenter />

      <section className="window-layer" aria-label="A-NAS 桌面窗口">
        {windows.map((window) => {
          // Minimized windows stay mounted so a running terminal session survives.
          if (!window.open) return null;
          return (
            <AppWindow key={window.id} model={window} dispatch={dispatch}>
			  {window.id === "resources" && <ResourcePanel host={host} />}
			  {window.id === "settings" && <SettingsPanel state={host.snapshot} />}
			  {window.id === "files" && <FilePanel onOpenPhotos={() => dispatch({ type: "open", id: "photos" })} />}
			  {window.id === "photos" && <PhotosPanel userId={session.user.id} isAdmin={session.user.role === "admin"} />}
			  {window.id === "trash" && <TrashPanel />}
			  {window.id === "snapshots" && <SnapshotPanel />}
			  {window.id === "accounts" && <AccountsPanel currentUser={session.user} />}
			  {window.id === "storage" && <StoragePanel state={host.snapshot} />}
			  {window.id === "terminal" && <TerminalPanel />}
			  {window.id === "docker" && <DockerPanel />}
			  {window.id === "store" && <AppCenterPanel />}
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
      <LocalConsoleScreenSaver enabled={isLocalConsole()} />
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

type FileSort = "name" | "modifiedAt" | "type" | "sizeBytes";
type FileView = "list" | "icons";
type FileResource = "photos" | "music" | "movies";
type FileBrowserItem = {
	id: string;
	name: string;
	kind: "directory" | "file" | "resource";
	type: string;
	detail: string;
	modifiedAt: string;
	sizeBytes: number;
	entry?: FileEntry;
	resource?: FileResource;
	planned?: boolean;
};

const personalFileResources: FileBrowserItem[] = [
	{ id: "resource:photos", name: "图库", kind: "resource", type: "图库投影", detail: "个人图库", modifiedAt: "", sizeBytes: 0, resource: "photos" },
	{ id: "resource:music", name: "音乐", kind: "resource", type: "功能入口", detail: "音乐资源 · 规划中", modifiedAt: "", sizeBytes: 0, resource: "music", planned: true },
	{ id: "resource:movies", name: "电影", kind: "resource", type: "功能入口", detail: "影视中心 · 规划中", modifiedAt: "", sizeBytes: 0, resource: "movies", planned: true },
];

function FilePanel({ onOpenPhotos }: { onOpenPhotos: () => void }) {
	const [spaces, setSpaces] = useState<Space[]>([]);
	const [spaceID, setSpaceID] = useState("");
	const [entries, setEntries] = useState<FileEntry[]>([]);
	const [volume, setVolume] = useState<Volume>();
	const [trail, setTrail] = useState<Array<{ id: string; name: string }>>([]);
	const [query, setQuery] = useState("");
	const [sortBy, setSortBy] = useState<FileSort>("name");
	const [sortDirection, setSortDirection] = useState<"ascending" | "descending">("ascending");
	const [view, setView] = useState<FileView>("list");
	const [viewMenuOpen, setViewMenuOpen] = useState(false);
	const [selectedItemID, setSelectedItemID] = useState("");
	const [collapsedSpaceIDs, setCollapsedSpaceIDs] = useState<string[]>([]);
	const [clipboard, setClipboard] = useState<{ entry: FileEntry; mode: "copy" | "move" }>();
	const [paneWidths, setPaneWidths] = useState({ left: 205, right: 238 });
	const [loading, setLoading] = useState(false);
	const [activity, setActivity] = useState("");
	const [creatingDirectory, setCreatingDirectory] = useState(false);
	const [renaming, setRenaming] = useState<FileEntry>();
	const [pendingDelete, setPendingDelete] = useState<FileEntry>();
	const [failedPreviewID, setFailedPreviewID] = useState("");
	const [error, setError] = useState("");
	const managerRef = useRef<HTMLDivElement>(null);
	const parentID = trail.at(-1)?.id ?? "";

	const refresh = useCallback(async (selected: string, parent: string) => {
		if (!selected) { setEntries([]); return; }
		setLoading(true);
		try { setEntries(await listEntries(selected, parent)); setError(""); }
		catch (caught) { setError(messageOf(caught)); }
		finally { setLoading(false); }
	}, []);
	const refreshVolume = useCallback(async () => {
		try {
			const volumes = await listVolumes();
			setVolume(volumes.find((item) => item.id === "volume:data") ?? volumes[0]);
		} catch { setVolume(undefined); }
	}, []);
	const loadSpaces = useCallback(async () => {
		try {
			const items = await listSpaces();
			setSpaces(items);
			setSpaceID((current) => items.some((space) => space.id === current) ? current : (items[0]?.id ?? ""));
		} catch (caught) { setError(messageOf(caught)); }
	}, []);
	useEffect(() => { void loadSpaces(); void refreshVolume(); }, [loadSpaces, refreshVolume]);
	useEffect(() => { if (spaceID) void refresh(spaceID, parentID); }, [spaceID, parentID, refresh]);
	useEffect(() => { setFailedPreviewID(""); }, [selectedItemID]);
	useEffect(() => {
		if (!pendingDelete && !renaming && !viewMenuOpen) return;
		const closeOnEscape = (event: KeyboardEvent) => {
			if (event.key !== "Escape" || activity) return;
			setPendingDelete(undefined); setRenaming(undefined); setViewMenuOpen(false);
		};
		window.addEventListener("keydown", closeOnEscape);
		return () => window.removeEventListener("keydown", closeOnEscape);
	}, [pendingDelete, renaming, viewMenuOpen, activity]);

	const selectedSpace = spaces.find((space) => space.id === spaceID);
	const viewing = selectedSpace?.viewing;
	const showResources = selectedSpace?.kind === "private" && !viewing && trail.length === 0;
	const browserItems: FileBrowserItem[] = [
		...(showResources ? personalFileResources : []),
		...entries.map((entry) => ({
			id: entry.id, name: entry.name, kind: entry.kind, type: entry.kind === "directory" ? "文件夹" : fileTypeLabel(entry.name),
			detail: entry.kind === "directory" ? "文件夹" : "双击名称下载", modifiedAt: entry.modifiedAt, sizeBytes: entry.sizeBytes, entry,
		} as FileBrowserItem)),
	];
	const normalizedQuery = query.trim().toLocaleLowerCase("zh-CN");
	const visibleItems = browserItems
		.filter((item) => `${item.name} ${item.type}`.toLocaleLowerCase("zh-CN").includes(normalizedQuery))
		.sort((left, right) => compareFileBrowserItems(left, right, sortBy, sortDirection));
	const selectedItem = browserItems.find((item) => item.id === selectedItemID);
	const selectedEntry = selectedItem?.entry;
	const fileCount = visibleItems.filter((item) => item.kind === "file").length;
	const directoryCount = visibleItems.filter((item) => item.kind === "directory").length;
	const resourceCount = visibleItems.filter((item) => item.kind === "resource").length;
	const totalSize = visibleItems.reduce((total, item) => total + (item.kind === "file" ? item.sizeBytes : 0), 0);
	const selectedSpaceLabel = selectedSpace?.kind === "shared" ? "共享空间" : selectedSpace?.viewing ? `只读查看 · ${selectedSpace.name}` : "个人空间";
	const capacityBytes = volume?.capacityBytes ?? 0;
	const usageKnown = capacityBytes > 0 && volume?.availableBytes !== undefined;
	const availableBytes = usageKnown ? Math.min(capacityBytes, Math.max(0, volume.availableBytes!)) : 0;
	const usedBytes = usageKnown ? capacityBytes - availableBytes : 0;
	const usedPercent = usageKnown ? Math.min(100, Math.round((usedBytes / capacityBytes) * 100)) : 0;
	const canMutateSelection = Boolean(selectedEntry && !viewing && !activity);
	const canPaste = Boolean(clipboard && !viewing && !activity && !(clipboard.mode === "move" && (clipboard.entry.parentId ?? "") === parentID));

	const chooseSpace = (nextSpaceID: string) => {
		setSpaceID(nextSpaceID); setTrail([]); setQuery(""); setSelectedItemID(""); setCreatingDirectory(false); setPendingDelete(undefined); setRenaming(undefined);
		setCollapsedSpaceIDs((current) => current.filter((id) => id !== nextSpaceID));
	};
	const toggleSpace = (space: Space) => {
		if (space.id !== spaceID) { chooseSpace(space.id); return; }
		setCollapsedSpaceIDs((current) => current.includes(space.id) ? current.filter((id) => id !== space.id) : [...current, space.id]);
	};
	const navigateTo = (index: number) => {
		setTrail((value) => value.slice(0, index)); setQuery(""); setSelectedItemID(""); setCreatingDirectory(false);
	};
	const openDirectory = (entry: FileEntry) => {
		setTrail((value) => [...value, { id: entry.id, name: entry.name }]); setQuery(""); setSelectedItemID(""); setCreatingDirectory(false);
	};
	const openItem = (item: FileBrowserItem) => {
		if (item.entry?.kind === "directory") openDirectory(item.entry);
		else if (item.resource === "photos") onOpenPhotos();
	};
	const changeSort = (nextSort: FileSort) => {
		if (sortBy === nextSort) setSortDirection((value) => value === "ascending" ? "descending" : "ascending");
		else { setSortBy(nextSort); setSortDirection(nextSort === "modifiedAt" || nextSort === "sizeBytes" ? "descending" : "ascending"); }
	};
	const setPaneWidth = useCallback((side: "left" | "right", value: number) => {
		setPaneWidths((current) => {
			const total = managerRef.current?.clientWidth ?? 900;
			const opposite = side === "left" ? current.right : current.left;
			const minimum = side === "left" ? 145 : 175;
			const maximum = Math.max(minimum, total - opposite - 295);
			return { ...current, [side]: Math.round(Math.min(maximum, Math.max(minimum, value))) };
		});
	}, []);
	const beginPaneResize = (side: "left" | "right", event: ReactPointerEvent<HTMLDivElement>) => {
		event.preventDefault(); event.currentTarget.focus();
		const startX = event.clientX; const startWidth = paneWidths[side];
		const move = (moveEvent: PointerEvent) => setPaneWidth(side, side === "left" ? startWidth + moveEvent.clientX - startX : startWidth - moveEvent.clientX + startX);
		const stop = () => { window.removeEventListener("pointermove", move); window.removeEventListener("pointerup", stop); };
		window.addEventListener("pointermove", move); window.addEventListener("pointerup", stop, { once: true });
	};
	const resizePaneWithKeyboard = (side: "left" | "right", event: ReactKeyboardEvent<HTMLDivElement>) => {
		if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
		event.preventDefault(); const delta = event.key === "ArrowRight" ? 12 : -12;
		setPaneWidth(side, paneWidths[side] + (side === "left" ? delta : -delta));
	};
	const stopViewing = async () => {
		if (!viewing) return;
		setActivity("正在结束只读查看…");
		try { await endViewing(viewing.grantId); setTrail([]); await loadSpaces(); setError(""); }
		catch (caught) { setError(messageOf(caught)); }
		finally { setActivity(""); }
	};
	const makeDirectory = async (event: FormEvent<HTMLFormElement>) => {
		event.preventDefault(); const form = event.currentTarget; const name = String(new FormData(form).get("name"));
		setActivity(`正在创建“${name}”…`);
		try { await createDirectory(spaceID, parentID, name); form.reset(); setCreatingDirectory(false); await refresh(spaceID, parentID); }
		catch (caught) { setError(messageOf(caught)); }
		finally { setActivity(""); }
	};
	const uploadFiles = async (files: FileList | null) => {
		if (!files?.length) return;
		try {
			for (let index = 0; index < files.length; index += 1) {
				setActivity(`正在上传 ${index + 1}/${files.length} · ${files[index].name}`);
				await uploadFile(spaceID, parentID, files[index]);
			}
			await Promise.all([refresh(spaceID, parentID), refreshVolume()]); setError("");
		} catch (caught) { setError(messageOf(caught)); }
		finally { setActivity(""); }
	};
	const pasteEntry = async () => {
		if (!clipboard || !canPaste) return;
		const source = clipboard.entry;
		const name = clipboard.mode === "copy" && (source.parentId ?? "") === parentID ? duplicateFileName(source.name) : source.name;
		setActivity(`${clipboard.mode === "copy" ? "正在复制" : "正在移动"}“${source.name}”…`);
		try {
			if (clipboard.mode === "copy") await copyFile(source.id, parentID, name);
			else await moveFile(source.id, parentID, name);
			if (clipboard.mode === "move") setClipboard(undefined);
			await refresh(spaceID, parentID); setError("");
		} catch (caught) { setError(messageOf(caught)); }
		finally { setActivity(""); }
	};
	const renameEntry = async (event: FormEvent<HTMLFormElement>) => {
		event.preventDefault(); if (!renaming) return;
		const name = String(new FormData(event.currentTarget).get("name")); setActivity(`正在重命名“${renaming.name}”…`);
		try { await moveFile(renaming.id, parentID, name); setRenaming(undefined); setSelectedItemID(""); await refresh(spaceID, parentID); setError(""); }
		catch (caught) { setError(messageOf(caught)); }
		finally { setActivity(""); }
	};
	const removeEntry = async () => {
		if (!pendingDelete) return;
		setActivity(`正在移动“${pendingDelete.name}”…`);
		try { await deleteFile(pendingDelete.id); setPendingDelete(undefined); setSelectedItemID(""); await Promise.all([refresh(spaceID, parentID), refreshVolume()]); setError(""); }
		catch (caught) { setError(messageOf(caught)); }
		finally { setActivity(""); }
	};
	const itemIcon = (item: FileBrowserItem) => item.kind === "directory" ? <FolderClosed /> : item.kind === "file" ? <FileText /> : item.resource === "photos" ? <Image /> : item.resource === "music" ? <Music2 /> : <Film />;
	const managerStyle = { "--file-left-pane": `${paneWidths.left}px`, "--file-right-pane": `${paneWidths.right}px` } as CSSProperties;

	return <div className="file-manager" ref={managerRef} style={managerStyle}>
		<aside className="file-sidebar" aria-label="目录树">
			<div className="file-sidebar-heading"><strong>目录</strong><button aria-label="新建文件夹" disabled={!spaceID || Boolean(viewing) || Boolean(activity)} onClick={() => setCreatingDirectory(true)}><FolderPlus /></button></div>
			<p className="file-sidebar-label">空间</p>
			<nav className="file-tree">{spaces.map((space) => {
				const active = space.id === spaceID; const collapsed = collapsedSpaceIDs.includes(space.id);
				const label = space.kind === "shared" ? "共享空间" : space.viewing ? `只读查看 · ${space.name}` : "个人空间";
				return <div className="file-tree-group" key={space.id}>
					<button className={`file-tree-root ${active ? "active" : ""}`} aria-expanded={active && !collapsed} onClick={() => toggleSpace(space)}>
						<ChevronDown className={!active || collapsed ? "collapsed" : ""} /><span className={`file-tree-icon ${space.kind}`}>{space.kind === "shared" ? <UsersRound /> : space.viewing ? <ShieldCheck /> : <FolderOpen />}</span><strong>{label}</strong><small>{space.kind === "shared" ? "共享" : space.name}</small>
					</button>
					{active && !collapsed && <div className="file-tree-branch">
						{space.kind === "private" && !space.viewing && personalFileResources.map((resource) => <button key={resource.id} className="file-tree-child resource" disabled={resource.planned} onClick={() => resource.resource === "photos" && onOpenPhotos()}><span>{itemIcon(resource)}</span><strong>{resource.name}</strong><small>{resource.planned ? "规划中" : "打开"}</small></button>)}
						<button className={`file-tree-child ${trail.length === 0 ? "current" : ""}`} onClick={() => navigateTo(0)}><span><FolderClosed /></span><strong>根目录</strong></button>
						{trail.map((item, index) => <button key={item.id} className={`file-tree-child path ${index === trail.length - 1 ? "current" : ""}`} onClick={() => navigateTo(index + 1)}><span><FolderClosed /></span><strong>{item.name}</strong></button>)}
					</div>}
				</div>;
			})}</nav>
			<div className="file-volume-card">
				<header><strong>数据卷</strong><span className={`file-volume-state ${volume?.state ?? "unavailable"}`}>{volume?.state === "available" ? "在线" : "不可用"}</span></header>
				<div className={`file-volume-track ${usageKnown ? "" : "unknown"}`} role="progressbar" aria-label={usageKnown ? `数据卷已使用 ${usedPercent}%` : "数据卷使用情况暂不可用"} aria-valuenow={usageKnown ? usedPercent : undefined} aria-valuemin={0} aria-valuemax={100}><span style={{ width: `${usedPercent}%` }} /></div>
				<div className="file-volume-values"><span>已用空间</span><span>总空间</span><strong>{usageKnown ? formatFileSize(usedBytes) : "—"}</strong><strong>{capacityBytes ? formatFileSize(capacityBytes) : "—"}</strong></div>
			</div>
		</aside>

		<div className="file-pane-resizer" role="separator" aria-label="调整目录栏宽度" aria-orientation="vertical" tabIndex={0} onPointerDown={(event) => beginPaneResize("left", event)} onKeyDown={(event) => resizePaneWithKeyboard("left", event)} />

		<section className="file-browser" aria-label="文件浏览器">
			<div className="file-navigation-bar">
				<div className="file-history-actions">
					<button className="file-icon-button" aria-label="后退" title="后退" disabled={!trail.length || loading} onClick={() => navigateTo(trail.length - 1)}><ArrowLeft /></button>
					<button className="file-icon-button" aria-label="前进" title="前进" disabled><ArrowRight /></button>
					<button className="file-icon-button" aria-label="向上一级" title="向上一级" disabled={!trail.length || loading} onClick={() => navigateTo(trail.length - 1)}><ArrowUp /></button>
					<button className="file-icon-button" aria-label="刷新文件列表" title="刷新" disabled={!spaceID || loading} onClick={() => { void refresh(spaceID, parentID); void refreshVolume(); }}><RefreshCw className={loading ? "spinning" : ""} /></button>
				</div>
				<select className="file-space-select" aria-label="空间" value={spaceID} onChange={(event) => chooseSpace(event.target.value)}>{spaces.map((space) => <option key={space.id} value={space.id}>{space.kind === "shared" ? "共享空间" : space.viewing ? `只读查看 · ${space.name}` : `个人空间 · ${space.name}`}</option>)}</select>
				<nav className="file-breadcrumb" aria-label="当前路径"><FolderOpen /><button onClick={() => navigateTo(0)}>{selectedSpaceLabel || "空间"}</button>{trail.map((item, index) => <span key={item.id}><ChevronRight /><button aria-current={index === trail.length - 1 ? "page" : undefined} onClick={() => navigateTo(index + 1)}>{item.name}</button></span>)}</nav>
				<label className="file-search"><Search /><input type="search" aria-label="搜索当前目录" placeholder={`在${trail.at(-1)?.name ?? selectedSpaceLabel ?? "当前目录"}中搜索`} value={query} onChange={(event) => setQuery(event.target.value)} /></label>
			</div>

			<div className="file-action-bar" aria-label="文件命令">
				<button className="file-command-button" disabled={!spaceID || Boolean(viewing) || Boolean(activity)} onClick={() => setCreatingDirectory((value) => !value)}><FolderPlus /><span>新建</span></button>
				<i />
				<button className="file-command-button" aria-label="剪切" title="剪切" disabled={!canMutateSelection} onClick={() => selectedEntry && setClipboard({ entry: selectedEntry, mode: "move" })}><Scissors /><span>剪切</span></button>
				<button className="file-command-button" aria-label="复制" title="复制" disabled={!canMutateSelection} onClick={() => selectedEntry && setClipboard({ entry: selectedEntry, mode: "copy" })}><Copy /><span>复制</span></button>
				<button className="file-command-button" aria-label="粘贴" title="粘贴到当前目录" disabled={!canPaste} onClick={() => void pasteEntry()}><ClipboardPaste /><span>粘贴</span></button>
				<button className="file-command-button" aria-label="重命名" title="重命名" disabled={!canMutateSelection} onClick={() => setRenaming(selectedEntry)}><Pencil /><span>重命名</span></button>
				<button className="file-command-button" aria-label="共享" title="共享功能规划中" disabled><Share2 /><span>共享</span></button>
				<button className="file-command-button danger" aria-label="删除" title="移到回收站" disabled={!canMutateSelection} onClick={() => setPendingDelete(selectedEntry)}><Trash2 /><span>删除</span></button>
				<i />
				<button className="file-command-button" aria-label="切换排序方向" title={`按${fileSortLabel(sortBy)}${sortDirection === "ascending" ? "升序" : "降序"}`} onClick={() => setSortDirection((value) => value === "ascending" ? "descending" : "ascending")}><ArrowUpDown /><span>排序</span></button>
				<div className="file-view-control"><button className="file-command-button" aria-expanded={viewMenuOpen} onClick={() => setViewMenuOpen((value) => !value)}>{view === "list" ? <List /> : <Grid2X2 />}<span>查看</span><ChevronDown /></button>{viewMenuOpen && <div className="file-view-menu"><button className={view === "list" ? "active" : ""} onClick={() => { setView("list"); setViewMenuOpen(false); }}><List />列表视图</button><button className={view === "icons" ? "active" : ""} onClick={() => { setView("icons"); setViewMenuOpen(false); }}><Grid2X2 />图标视图</button></div>}</div>
				<button className="file-command-button" aria-label="更多操作" title="更多操作" disabled><Ellipsis /></button>
				{activity && <span className="file-activity" role="status"><span className="loader" />{activity}</span>}
			</div>

			{error && <PanelNotice error={error} />}
			{viewing && <div className="status-banner viewing-banner" role="status"><ShieldCheck /><span>正在只读查看他人个人空间；访问已审计，将于 {formatFileDate(viewing.expiresAt)} 自动结束。</span><button disabled={Boolean(activity)} onClick={() => void stopViewing()}>结束查看</button></div>}
			{clipboard && <div className="file-clipboard-state" role="status">{clipboard.mode === "copy" ? "已复制" : "已剪切"}“{clipboard.entry.name}”</div>}

			{creatingDirectory && !viewing && <form className="file-create-form" aria-label="新建文件夹" onSubmit={(event) => void makeDirectory(event)}><FolderPlus /><input autoFocus name="name" aria-label="新文件夹名称" placeholder="输入文件夹名称" required /><button disabled={Boolean(activity)}>创建</button><button type="button" onClick={() => setCreatingDirectory(false)}>取消</button></form>}

			<div className="file-list-shell">
				{view === "list" ? <div className="file-list" role="table" aria-label="文件列表" aria-busy={loading}>
					<div className="file-list-header" role="row">
						<div role="columnheader" aria-sort={sortBy === "name" ? sortDirection : "none"}><button onClick={() => changeSort("name")}>名称{sortBy === "name" && <ChevronDown className={sortDirection === "ascending" ? "ascending" : ""} />}</button></div>
						<div className="file-date-column" role="columnheader" aria-sort={sortBy === "modifiedAt" ? sortDirection : "none"}><button onClick={() => changeSort("modifiedAt")}>修改时间{sortBy === "modifiedAt" && <ChevronDown className={sortDirection === "ascending" ? "ascending" : ""} />}</button></div>
						<div className="file-type-column" role="columnheader" aria-sort={sortBy === "type" ? sortDirection : "none"}><button onClick={() => changeSort("type")}>类型{sortBy === "type" && <ChevronDown className={sortDirection === "ascending" ? "ascending" : ""} />}</button></div>
						<div role="columnheader" aria-sort={sortBy === "sizeBytes" ? sortDirection : "none"}><button onClick={() => changeSort("sizeBytes")}>大小{sortBy === "sizeBytes" && <ChevronDown className={sortDirection === "ascending" ? "ascending" : ""} />}</button></div>
					</div>
					{!loading && visibleItems.map((item) => <div className={`file-list-row ${selectedItemID === item.id ? "selected" : ""}`} role="row" aria-selected={selectedItemID === item.id} key={item.id} onClick={() => setSelectedItemID(item.id)} onDoubleClick={() => openItem(item)}>
						<div className="file-name-cell" role="cell"><span className={`file-type-icon ${item.kind} ${item.resource ?? ""}`}>{itemIcon(item)}</span><span className="file-name-copy">{item.entry?.kind === "directory" ? <button onClick={(event) => { event.stopPropagation(); openDirectory(item.entry!); }}><strong>{item.name}</strong><small>文件夹</small></button> : item.entry?.kind === "file" ? <a href={fileDownloadURL(item.entry.id)} onClick={(event) => event.stopPropagation()}><strong>{item.name}</strong><small>点击下载</small></a> : <button disabled={item.planned} onClick={(event) => { event.stopPropagation(); openItem(item); }}><strong>{item.name}{item.kind === "resource" && <em>{item.planned ? "规划中" : "应用资源"}</em>}</strong><small>{item.detail}</small></button>}</span></div>
						<div className="file-date-cell file-date-column" role="cell">{item.modifiedAt ? <><Clock3 />{formatFileDate(item.modifiedAt)}</> : "—"}</div>
						<div className="file-type-cell file-type-column" role="cell"><span className={item.kind === "directory" ? "directory" : item.kind === "resource" ? "resource" : ""}>{item.type}</span></div>
						<div className="file-size-cell" role="cell">{item.kind === "file" ? formatFileSize(item.sizeBytes) : "—"}</div>
					</div>)}
				</div> : <div className="file-icon-view" role="listbox" aria-label="文件图标">
					{!loading && visibleItems.map((item) => <button className={`file-icon-card ${selectedItemID === item.id ? "selected" : ""}`} role="option" aria-selected={selectedItemID === item.id} key={item.id} onClick={() => setSelectedItemID(item.id)} onDoubleClick={() => openItem(item)}><span className={`file-type-icon ${item.kind} ${item.resource ?? ""}`}>{itemIcon(item)}</span><strong>{item.name}</strong><small>{item.detail}</small></button>)}
				</div>}
				{loading ? <div className="file-empty-state" role="status"><span className="loader" /><strong>正在读取文件…</strong><span>正在加载这个目录的内容</span></div>
					: !spaceID ? <div className="file-empty-state"><FolderClosed /><strong>还没有可用空间</strong><span>完成数据卷与账号设置后，空间会显示在这里。</span></div>
					: !visibleItems.length && query ? <div className="file-empty-state"><Search /><strong>没有匹配的项目</strong><span>请尝试其他名称，或清除搜索条件。</span><button onClick={() => setQuery("")}>清除搜索</button></div>
					: !visibleItems.length ? <div className="file-empty-state"><FolderPlus /><strong>这个文件夹是空的</strong><span>{viewing ? "这里暂时没有内容。" : "上传文件或新建文件夹，开始整理内容。"}</span></div> : null}
			</div>
			{!viewing && <label className={`file-floating-upload ${activity ? "disabled" : ""}`} aria-label="上传资源到当前目录" title="上传资源到当前目录" tabIndex={activity ? -1 : 0} onKeyDown={(event) => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); event.currentTarget.querySelector("input")?.click(); } }}><Upload /><span className="sr-only">上传资源到当前目录</span><input type="file" multiple disabled={Boolean(activity)} onChange={(event) => { void uploadFiles(event.target.files); event.target.value = ""; }} /></label>}
			<footer className="file-status-bar"><span>{resourceCount ? `${resourceCount} 个应用资源，` : ""}{directoryCount ? `${directoryCount} 个文件夹` : ""}{directoryCount && fileCount ? "，" : ""}{fileCount ? `${fileCount} 个文件` : !directoryCount && !resourceCount ? "0 个项目" : ""}</span><span>{query && `已筛选 ${visibleItems.length}/${browserItems.length} 项`}</span><span>{totalSize > 0 && `文件共 ${formatFileSize(totalSize)}`}</span><span>{view === "list" ? "列表视图" : "图标视图"}</span></footer>
		</section>

		<div className="file-pane-resizer" role="separator" aria-label="调整详细信息栏宽度" aria-orientation="vertical" tabIndex={0} onPointerDown={(event) => beginPaneResize("right", event)} onKeyDown={(event) => resizePaneWithKeyboard("right", event)} />

		<aside className="file-inspector" aria-label="详细信息">
			<header>详细信息</header>
			{selectedItem ? <><div className={`file-inspector-preview ${selectedItem.kind} ${selectedItem.resource ?? ""} ${isBrowserPreviewableImage(selectedItem.name) ? "image" : ""}`}>{selectedItem.entry?.kind === "file" && isBrowserPreviewableImage(selectedItem.name) && failedPreviewID !== selectedItem.id ? <img src={filePreviewURL(selectedItem.entry.id)} alt={`${selectedItem.name} 的预览`} onError={() => setFailedPreviewID(selectedItem.id)} /> : itemIcon(selectedItem)}</div><div className="file-inspector-copy"><h3>{selectedItem.name}</h3><p>{selectedItem.type}</p><dl><dt>资源类型</dt><dd>{selectedItem.kind === "resource" ? "应用资源" : selectedItem.type}</dd><dt>内容</dt><dd>{selectedItem.kind === "file" ? formatFileSize(selectedItem.sizeBytes) : selectedItem.detail}</dd><dt>所在位置</dt><dd>{trail.at(-1)?.name ?? selectedSpaceLabel}</dd>{selectedItem.modifiedAt && <><dt>修改时间</dt><dd>{formatFileDate(selectedItem.modifiedAt)}</dd></>}</dl>{selectedItem.resource === "photos" && <p className="file-inspector-hint">这是相册中个人图库的图库投影。打开后交由相册处理，不暴露受管存储路径。</p>}{selectedItem.planned && <p className="file-inspector-hint">该资源入口已预留，功能尚未开发。</p>}</div></> : <div className="file-inspector-empty"><FolderOpen /><strong>选择一个项目</strong><span>详细信息会显示在这里</span></div>}
		</aside>

		{pendingDelete && <div className="file-dialog-backdrop"><section className="file-confirm-dialog" role="alertdialog" aria-modal="true" aria-labelledby="file-delete-title"><span className="file-confirm-icon"><Trash2 /></span><div><h3 id="file-delete-title">移到回收站？</h3><p>“{pendingDelete.name}”将移到回收站，可在 30 天内恢复。</p></div><div className="file-confirm-actions"><button disabled={Boolean(activity)} onClick={() => setPendingDelete(undefined)}>取消</button><button className="danger-button" disabled={Boolean(activity)} onClick={() => void removeEntry()}>{activity ? "正在移动…" : "移到回收站"}</button></div></section></div>}
		{renaming && <div className="file-dialog-backdrop"><form className="file-rename-dialog" aria-label={`重命名 ${renaming.name}`} onSubmit={(event) => void renameEntry(event)}><span><Pencil /></span><div><h3>重命名</h3><p>为“{renaming.name}”输入新名称。</p><input autoFocus name="name" aria-label="新名称" defaultValue={renaming.name} required /></div><footer><button type="button" disabled={Boolean(activity)} onClick={() => setRenaming(undefined)}>取消</button><button className="primary" disabled={Boolean(activity)}>保存</button></footer></form></div>}
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
	// Viewing a private space and viewing a private photo library are separate grants.
	const [viewingFor, setViewingFor] = useState<{ user: User; scope: ViewingScope }>(); const [notice, setNotice] = useState("");
	const viewingWhat = viewingFor?.scope === "library" ? "私有图库" : "个人空间";
	const [changingOwnPassword, setChangingOwnPassword] = useState(false);
	const beginViewing = async (event: FormEvent<HTMLFormElement>) => {
		event.preventDefault();
		if (!viewingFor) return;
		const data = new FormData(event.currentTarget);
		const { user, scope } = viewingFor;
		try {
			await startViewing(user.id, String(data.get("password")), String(data.get("reason")), scope);
			setNotice(scope === "library"
				? `已开启对 ${user.username} 私有图库的只读查看，可在相册中选择“只读查看 · ${user.username}”。`
				: `已开启对 ${user.username} 个人空间的只读查看，可在文件管理中选择“只读查看 · ${user.username}”。`);
			setViewingFor(undefined); setError("");
		} catch (caught) { setError(messageOf(caught)); }
	};
	const refresh = useCallback(() => listUsers().then(setUsers).catch((caught) => setError(messageOf(caught))), []);
	useEffect(() => { void refresh(); }, [refresh]);
	const create = async (event: FormEvent<HTMLFormElement>) => { event.preventDefault(); const form = event.currentTarget; const data = new FormData(form); try { await createMember(String(data.get("username")), String(data.get("password"))); form.reset(); await refresh(); } catch (caught) { setError(messageOf(caught)); } };
	const changeOwnPassword = async (event: FormEvent<HTMLFormElement>) => {
		event.preventDefault();
		const form = event.currentTarget;
		const data = new FormData(form);
		const next = String(data.get("newPassword"));
		if (next !== String(data.get("confirmPassword"))) { setError("两次输入的新密码不一致"); return; }
		try {
			await changePassword(String(data.get("currentPassword")), next);
			form.reset(); setChangingOwnPassword(false); setError(""); setNotice("密码已更新，SMB 凭据已同步。");
		} catch (caught) { setError(messageOf(caught)); }
	};
	const beginViewingOf = (user: User, scope: ViewingScope) => { setChangingOwnPassword(false); setNotice(""); setViewingFor({ user, scope }); };
	return <div className="product-page"><div className="page-heading"><div><p className="section-label">ACCOUNTS</p><h2>账号与权限</h2></div></div>{error && <PanelNotice error={error} />}<form className="inline-form" autoComplete="off" onSubmit={(event) => void create(event)}><input name="username" placeholder="成员账号" required /><input name="password" type="password" minLength={12} autoComplete="new-password" placeholder="初始密码（至少 12 位）" required /><button>创建成员</button></form><div className="data-list">{users.map((user) => <div className="data-row" key={user.id}><UserRound /><strong>{user.username}</strong><small>{user.role} · {user.status}</small>{user.id === currentUser.id ? <button onClick={() => { setViewingFor(undefined); setNotice(""); setChangingOwnPassword(true); }}>修改密码</button> : <><button onClick={() => { const password = window.prompt("输入至少 12 位的新密码"); if (password) void resetMember(user.id, password).then(refresh).catch((caught) => setError(messageOf(caught))); }}>重置密码</button><button onClick={() => beginViewingOf(user, "space")}>查看个人空间</button><button onClick={() => beginViewingOf(user, "library")}>查看私有图库</button><button className="danger-link" disabled={user.status === "disabled"} onClick={() => void disableMember(user.id).then(refresh).catch((caught) => setError(messageOf(caught)))}>禁用</button></>}</div>)}</div>
		{changingOwnPassword && <form className="viewing-form" aria-label="修改自己的密码" autoComplete="off" onSubmit={(event) => void changeOwnPassword(event)}>
			<p>修改后将同时更新网页登录密码与 SMB 共享密码。</p>
			<input name="currentPassword" aria-label="当前密码" type="password" placeholder="当前密码" autoComplete="current-password" required />
			<input name="newPassword" aria-label="新密码" type="password" minLength={12} placeholder="新密码（至少 12 位）" autoComplete="new-password" required />
			<input name="confirmPassword" aria-label="确认新密码" type="password" minLength={12} placeholder="再次输入新密码" autoComplete="new-password" required />
			<button>保存新密码</button><button type="button" onClick={() => setChangingOwnPassword(false)}>取消</button>
		</form>}
		{viewingFor && <form className="viewing-form" aria-label={`查看 ${viewingFor.user.username} 的${viewingWhat}`} autoComplete="off" onSubmit={(event) => void beginViewing(event)}>
			<p>查看他人{viewingWhat}会写入审计，并在 {viewingFor.user.username} 下次登录时通知对方。访问为只读，24 小时后自动结束。</p>
			<input name="reason" aria-label="查看原因" placeholder="查看原因" maxLength={500} required />
			<input name="password" aria-label="你的密码" type="password" placeholder="你的密码" autoComplete="current-password" required />
			<button>开始只读查看</button><button type="button" onClick={() => setViewingFor(undefined)}>取消</button>
		</form>}
		{notice && <div className="status-banner" role="status"><ShieldCheck />{notice}</div>}</div>;
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

function formatFileSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let value = bytes / 1024;
  let unit = units[0];
  for (let index = 1; index < units.length && value >= 1024; index += 1) {
    value /= 1024;
    unit = units[index];
  }
  return `${value < 10 ? value.toFixed(1) : value.toFixed(0)} ${unit}`;
}

function formatFileDate(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "—";
  const today = new Date();
  const sameDay = date.getFullYear() === today.getFullYear() && date.getMonth() === today.getMonth() && date.getDate() === today.getDate();
  return new Intl.DateTimeFormat("zh-CN", sameDay
    ? { hour: "2-digit", minute: "2-digit" }
    : { year: date.getFullYear() === today.getFullYear() ? undefined : "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" }).format(date);
}

function compareFileBrowserItems(left: FileBrowserItem, right: FileBrowserItem, sortBy: FileSort, direction: "ascending" | "descending"): number {
	const group = { directory: 0, resource: 1, file: 2 };
	const groupResult = group[left.kind] - group[right.kind];
	if (groupResult) return groupResult;
	let result: number;
	if (sortBy === "modifiedAt") result = (Date.parse(left.modifiedAt) || 0) - (Date.parse(right.modifiedAt) || 0);
	else if (sortBy === "sizeBytes") result = left.sizeBytes - right.sizeBytes;
	else if (sortBy === "type") result = left.type.localeCompare(right.type, "zh-CN", { numeric: true, sensitivity: "base" });
	else result = left.name.localeCompare(right.name, "zh-CN", { numeric: true, sensitivity: "base" });
	if (!result) result = left.name.localeCompare(right.name, "zh-CN", { numeric: true, sensitivity: "base" });
	return direction === "ascending" ? result : -result;
}

function fileTypeLabel(name: string): string {
	const extension = name.includes(".") ? name.split(".").pop()?.toLocaleLowerCase("en-US") : "";
	if (["jpg", "jpeg", "png", "gif", "webp", "heic"].includes(extension ?? "")) return "图片";
	if (["mp4", "mkv", "mov", "avi", "webm"].includes(extension ?? "")) return "视频";
	if (["mp3", "flac", "wav", "m4a", "aac"].includes(extension ?? "")) return "音频";
	if (extension === "pdf") return "PDF 文档";
	if (["xls", "xlsx", "csv"].includes(extension ?? "")) return "电子表格";
	if (["doc", "docx", "txt", "md"].includes(extension ?? "")) return "文档";
	return extension ? `${extension.toLocaleUpperCase("en-US")} 文件` : "文件";
}

function isBrowserPreviewableImage(name: string): boolean {
	const extension = name.includes(".") ? name.split(".").pop()?.toLocaleLowerCase("en-US") : "";
	return ["jpg", "jpeg", "png", "gif", "webp", "bmp", "avif"].includes(extension ?? "");
}

function fileSortLabel(sort: FileSort): string {
	return { name: "名称", modifiedAt: "修改时间", type: "类型", sizeBytes: "大小" }[sort];
}

function duplicateFileName(name: string): string {
	const dot = name.lastIndexOf(".");
	if (dot <= 0) return `${name} - 副本`;
	return `${name.slice(0, dot)} - 副本${name.slice(dot)}`;
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
