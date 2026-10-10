import {
  ArrowLeft,
  ArrowRight,
  ArrowUp,
  ArrowUpDown,
  Bot,
  Box,
  Camera,
  ChevronDown,
  ChevronRight,
  CircleAlert,
  ClipboardPaste,
  Clock3,
  Copy,
  Download,
  Ellipsis,
  FileText,
  Film,
  FolderClosed,
  FolderOpen,
  FolderPlus,
  Grid2X2,
  Image,
  List,
  Maximize2,
  Minus,
  Monitor,
  Music2,
  PanelLeftClose,
  Pencil,
  PlaySquare,
  RefreshCw,
  Scissors,
  Search,
  Settings,
  Share2,
  ShieldCheck,
  ShoppingBag,
  SquareTerminal,
  Trash2,
  Upload,
  UsersRound,
  X,
  type LucideIcon,
} from "lucide-react";
import { Component, CSSProperties, ErrorInfo, FormEvent, KeyboardEvent as ReactKeyboardEvent, MouseEvent as ReactMouseEvent, PointerEvent as ReactPointerEvent, ReactNode, useCallback, useEffect, useReducer, useRef, useState } from "react";

import {
  APIError, FileEntry, Notification, Session, Snapshot, SnapshotEntry, Space, TrashItem, Volume,
  acknowledgeNotification, changePassword, copyFile, createDirectory, createSnapshot, currentSession, deleteFile,
  deleteSnapshot, endViewing, fileDownloadURL, filePreviewURL, getSetupStatus, listEntries, listNotifications, listSnapshots,
  listSnapshotEntries, listSpaces, listTrash, listVolumes, login, logout, moveFile, onSessionEnded, purgeTrash, readFileTextPreview,
  restoreSnapshotEntry, restoreTrash, setSession, setupAdministrator, uploadFile,
} from "./api";
import { AppCenterPanel } from "./AppCenterPanel";
import { DesktopApp, DesktopGrid } from "./DesktopGrid";
import { Dock } from "./Dock";
import { DockerPanel } from "./DockerPanel";
import { isLocalConsole, LocalConsoleScreenSaver } from "./LocalConsoleScreenSaver";
import { PhotosPanel } from "./photos/PhotosPanel";
import { SettingsPanel, SettingsSection, useStoragePlan } from "./SettingsPanel";
import { StatusBar } from "./StatusBar";
import { DesktopView, SystemRail } from "./SystemRail";
import { TerminalPanel } from "./TerminalPanel";
import { useHostState } from "./useHostState";
import "./styles.css";

type WindowID = "files" | "photos" | "trash" | "snapshots" | "settings" | "terminal" | "docker" | "store";

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
  | { type: "show-desktop" }
  | { type: "restore"; ids: WindowID[] };

const initialWindows: WindowModel[] = [
	{ id: "files", title: "文件管理", open: false, minimized: false, maximized: false, x: 230, y: 70, width: 900, height: 620, z: 3, opened: 0 },
	{ id: "photos", title: "相册", open: false, minimized: false, maximized: false, x: 250, y: 64, width: 960, height: 660, z: 2, opened: 0 },
	{ id: "trash", title: "回收站", open: false, minimized: false, maximized: false, x: 280, y: 90, width: 760, height: 540, z: 2, opened: 0 },
	{ id: "snapshots", title: "文件快照", open: false, minimized: false, maximized: false, x: 300, y: 100, width: 800, height: 560, z: 2, opened: 0 },
  { id: "settings", title: "设置", open: false, minimized: false, maximized: false, x: 300, y: 76, width: 920, height: 620, z: 1, opened: 0 },
  { id: "terminal", title: "终端", open: false, minimized: false, maximized: false, x: 300, y: 70, width: 820, height: 520, z: 0, opened: 0 },
  { id: "docker", title: "Docker", open: false, minimized: false, maximized: false, x: 320, y: 82, width: 900, height: 620, z: 0, opened: 0 },
  { id: "store", title: "应用中心", open: false, minimized: false, maximized: false, x: 300, y: 60, width: 940, height: 660, z: 0, opened: 0 },
];

const windowIcons: Record<WindowID, LucideIcon> = {
  files: FolderClosed,
  photos: Image,
  trash: Trash2,
  snapshots: Camera,
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
  // The section and any pending storage plan outlive the settings window, so
  // reopening it returns to where the user left off.
  const [settingsSection, setSettingsSection] = useState<SettingsSection>("overview");
  const storagePlan = useStoragePlan();
  const openSettings = (section?: SettingsSection) => { if (section) setSettingsSection(section); openWindow("settings"); };
  // Keywords let the launcher find an app by what it holds, not only by its name.
  const desktopApps: DesktopApp[] = [
    { id: "files", label: "文件管理", ariaLabel: "打开文件管理", keywords: "文件 共享 上传 shared", tone: "files", icon: <FolderClosed />, active: isOpen("files"), onClick: () => openWindow("files") },
    { id: "trash", label: "回收站", ariaLabel: "打开回收站", keywords: "删除 恢复", tone: "trash", icon: <Trash2 />, active: isOpen("trash"), onClick: () => openWindow("trash") },
    { id: "settings", label: "设置", ariaLabel: "打开设置", keywords: "账号 密码 smb 用户 成员 权限 存储 磁盘 数据卷 系统", tone: "settings", icon: <Settings />, active: isOpen("settings"), onClick: () => openSettings() },
    ...(session.user.role === "admin" ? [{ id: "terminal", label: "终端", ariaLabel: "打开终端", keywords: "shell 命令行 bash", tone: "terminal", icon: <SquareTerminal />, active: isOpen("terminal"), onClick: () => openWindow("terminal") }] : []),
    // Docker and the App Center run containers with root-equivalent engine access: administrators only.
    ...(session.user.role === "admin" ? [{ id: "store", label: "应用中心", ariaLabel: "打开应用中心", keywords: "应用 安装 商店 app", tone: "store", icon: <ShoppingBag />, active: isOpen("store"), onClick: () => openWindow("store") }] : []),
    { id: "video", label: "影视", ariaLabel: "影视，规划中", tone: "video", icon: <PlaySquare />, disabled: true },
    { id: "download", label: "下载", ariaLabel: "下载，规划中", tone: "download", icon: <Download />, disabled: true },
    { id: "snapshot", label: "文件快照", ariaLabel: "打开文件快照", keywords: "快照 恢复 历史版本 snapshot", tone: "snapshot", icon: <Camera />, active: isOpen("snapshots"), onClick: () => openWindow("snapshots") },
    ...(session.user.role === "admin" ? [{ id: "docker", label: "Docker", ariaLabel: "打开 Docker", keywords: "容器 镜像 container", tone: "docker", icon: <Box />, active: isOpen("docker"), onClick: () => openWindow("docker") }] : []),
    { id: "photos", label: "相册", ariaLabel: "打开相册", keywords: "照片 图片 图库 photo", tone: "photos", icon: <Image />, active: isOpen("photos"), onClick: () => openWindow("photos") },
    { id: "logs", label: "日志", ariaLabel: "日志，规划中", tone: "logs", icon: <FileText />, disabled: true },
    { id: "vm", label: "虚拟机", ariaLabel: "虚拟机，规划中", tone: "vm", icon: <Monitor />, disabled: true },
    { id: "backup", label: "备份", ariaLabel: "备份，规划中", tone: "backup", icon: <ShieldCheck />, disabled: true },
    { id: "music", label: "音乐", ariaLabel: "音乐，规划中", tone: "music", icon: <Music2 />, disabled: true },
    { id: "ai", label: "AI 助手", ariaLabel: "AI 助手，规划中", tone: "ai", icon: <Bot />, disabled: true },
  ];
  const visible = windows.filter((window) => window.open && !window.minimized);
  const focusedID = visible.length ? visible.reduce((top, window) => (window.z > top.z ? window : top)).id : undefined;
  // Windows hidden by "show desktop" come back on the next press, as long as
  // nothing was reopened in between; windows minimized by hand stay down.
  const [revealed, setRevealed] = useState<WindowID[]>([]);
  const hidden = revealed.filter((id) => windows.some((window) => window.id === id && window.open && window.minimized));
  const desktopView: DesktopView = visible.length ? "windows" : hidden.length ? "revealed" : "clear";
  const toggleDesktop = () => {
    if (visible.length) {
      setRevealed(visible.map((window) => window.id));
      dispatch({ type: "show-desktop" });
    } else if (hidden.length) {
      setRevealed([]);
      dispatch({ type: "restore", ids: hidden });
    }
  };

  return (
    <main className="desktop-shell">
      <div className="wallpaper-glow" />

      <SystemRail
        apps={desktopApps}
        session={session}
        desktopView={desktopView}
        settingsFocused={focusedID === "settings"}
        onToggleDesktop={toggleDesktop}
        onOpenSettings={openSettings}
        onLogout={onLogout}
      />

      <DesktopGrid apps={desktopApps} />

      <StatusBar host={host} />

      <NotificationCenter />

      <section className="window-layer" aria-label="A-NAS 桌面窗口">
        {windows.map((window) => {
          // Minimized windows stay mounted so a running terminal session survives.
          if (!window.open) return null;
          return (
            <AppWindow key={window.id} model={window} dispatch={dispatch}>
			  {window.id === "settings" && <SettingsPanel session={session} host={host} section={settingsSection} onSectionChange={setSettingsSection} storagePlan={storagePlan} onLogout={onLogout} />}
			  {window.id === "files" && <FilePanel onOpenPhotos={() => dispatch({ type: "open", id: "photos" })} />}
			  {window.id === "photos" && <PhotosPanel userId={session.user.id} isAdmin={session.user.role === "admin"} />}
			  {window.id === "trash" && <TrashPanel />}
			  {window.id === "snapshots" && <SnapshotPanel />}
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

type FileSort = "name" | "modifiedAt" | "type" | "sizeBytes";
type FileView = "list" | "icons";
type FileResource = "photos" | "music" | "movies";
type FileSelectionRect = { left: number; top: number; width: number; height: number };
type FileContextMenu = { x: number; y: number; item?: FileBrowserItem };
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
	const [selectedItemIDs, setSelectedItemIDs] = useState<string[]>([]);
	const [collapsedSpaceIDs, setCollapsedSpaceIDs] = useState<string[]>([]);
	const [clipboard, setClipboard] = useState<{ entries: FileEntry[]; mode: "copy" | "move" }>();
	const [paneWidths, setPaneWidths] = useState({ left: 205, right: 238 });
	const [loading, setLoading] = useState(false);
	const [activity, setActivity] = useState("");
	const [creatingDirectory, setCreatingDirectory] = useState(false);
	const [renaming, setRenaming] = useState<FileEntry>();
	const [pendingDelete, setPendingDelete] = useState<FileEntry[]>();
	const [failedPreviewID, setFailedPreviewID] = useState("");
	const [textPreview, setTextPreview] = useState<{ id: string; content?: string; error?: string }>();
	const [selectionRect, setSelectionRect] = useState<FileSelectionRect>();
	const [contextMenu, setContextMenu] = useState<FileContextMenu>();
	const [error, setError] = useState("");
	const managerRef = useRef<HTMLDivElement>(null);
	const listShellRef = useRef<HTMLDivElement>(null);
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
		if (!pendingDelete && !renaming && !viewMenuOpen && !contextMenu) return;
		const closeOnEscape = (event: KeyboardEvent) => {
			if (event.key !== "Escape" || activity) return;
			setPendingDelete(undefined); setRenaming(undefined); setViewMenuOpen(false); setContextMenu(undefined);
		};
		window.addEventListener("keydown", closeOnEscape);
		return () => window.removeEventListener("keydown", closeOnEscape);
	}, [pendingDelete, renaming, viewMenuOpen, contextMenu, activity]);
	useEffect(() => {
		if (!contextMenu) return;
		const close = () => setContextMenu(undefined);
		window.addEventListener("pointerdown", close);
		window.addEventListener("blur", close);
		window.addEventListener("resize", close);
		return () => { window.removeEventListener("pointerdown", close); window.removeEventListener("blur", close); window.removeEventListener("resize", close); };
	}, [contextMenu]);

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
	const selectedItems = browserItems.filter((item) => selectedItemIDs.includes(item.id));
	const selectedItem = browserItems.find((item) => item.id === selectedItemID) ?? selectedItems.at(-1);
	const selectedEntry = selectedItem?.entry;
	const selectedEntries = selectedItems.flatMap((item) => item.entry ? [item.entry] : []);
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
	const canMutateSelection = Boolean(selectedEntries.length && selectedEntries.length === selectedItems.length && !viewing && !activity);
	const canRenameSelection = canMutateSelection && selectedEntries.length === 1;
	const canPaste = Boolean(clipboard?.entries.length && !viewing && !activity && !(clipboard.mode === "move" && clipboard.entries.every((entry) => (entry.parentId ?? "") === parentID)));

	useEffect(() => {
		if (!selectedItem?.entry || !isTextPreviewable(selectedItem.name) || selectedItems.length !== 1) { setTextPreview(undefined); return; }
		if (selectedItem.sizeBytes > 1024 * 1024) { setTextPreview({ id: selectedItem.id, error: "文本超过 1 MB，请下载后查看完整内容。" }); return; }
		let cancelled = false;
		setTextPreview({ id: selectedItem.id });
		void readFileTextPreview(selectedItem.entry.id).then((content) => {
			if (!cancelled) setTextPreview({ id: selectedItem.id, content });
		}).catch((caught) => {
			if (!cancelled) setTextPreview({ id: selectedItem.id, error: messageOf(caught) });
		});
		return () => { cancelled = true; };
	}, [selectedItem?.id, selectedItem?.entry, selectedItem?.name, selectedItem?.sizeBytes, selectedItems.length]);

	const chooseSpace = (nextSpaceID: string) => {
		setSpaceID(nextSpaceID); setTrail([]); setQuery(""); setSelectedItemID(""); setSelectedItemIDs([]); setCreatingDirectory(false); setPendingDelete(undefined); setRenaming(undefined); setContextMenu(undefined);
		setCollapsedSpaceIDs((current) => current.filter((id) => id !== nextSpaceID));
	};
	const toggleSpace = (space: Space) => {
		if (space.id !== spaceID) { chooseSpace(space.id); return; }
		setCollapsedSpaceIDs((current) => current.includes(space.id) ? current.filter((id) => id !== space.id) : [...current, space.id]);
	};
	const navigateTo = (index: number) => {
		setTrail((value) => value.slice(0, index)); setQuery(""); setSelectedItemID(""); setSelectedItemIDs([]); setCreatingDirectory(false); setContextMenu(undefined);
	};
	const openDirectory = (entry: FileEntry) => {
		setTrail((value) => [...value, { id: entry.id, name: entry.name }]); setQuery(""); setSelectedItemID(""); setSelectedItemIDs([]); setCreatingDirectory(false); setContextMenu(undefined);
	};
	const downloadEntry = (entry: FileEntry) => {
		const link = document.createElement("a");
		link.href = fileDownloadURL(entry.id); link.download = entry.name; link.hidden = true;
		document.body.appendChild(link); link.click(); link.remove();
	};
	const openItem = (item: FileBrowserItem) => {
		if (item.entry?.kind === "directory") openDirectory(item.entry);
		else if (item.entry?.kind === "file") downloadEntry(item.entry);
		else if (item.resource === "photos") onOpenPhotos();
	};
	const changeSort = (nextSort: FileSort) => {
		if (sortBy === nextSort) setSortDirection((value) => value === "ascending" ? "descending" : "ascending");
		else { setSortBy(nextSort); setSortDirection(nextSort === "modifiedAt" || nextSort === "sizeBytes" ? "descending" : "ascending"); }
	};
	const selectItem = (item: FileBrowserItem, event?: Pick<ReactMouseEvent, "ctrlKey" | "metaKey" | "shiftKey">) => {
		if (event?.shiftKey && selectedItemID) {
			const anchor = visibleItems.findIndex((candidate) => candidate.id === selectedItemID);
			const target = visibleItems.findIndex((candidate) => candidate.id === item.id);
			if (anchor >= 0 && target >= 0) {
				const range = visibleItems.slice(Math.min(anchor, target), Math.max(anchor, target) + 1).map((candidate) => candidate.id);
				setSelectedItemIDs(range); setSelectedItemID(item.id); return;
			}
		}
		if (event?.ctrlKey || event?.metaKey) {
			setSelectedItemIDs((current) => current.includes(item.id) ? current.filter((id) => id !== item.id) : [...current, item.id]);
			setSelectedItemID(item.id); return;
		}
		setSelectedItemIDs([item.id]); setSelectedItemID(item.id);
	};
	const showContextMenu = (event: ReactMouseEvent, item?: FileBrowserItem) => {
		event.preventDefault(); event.stopPropagation();
		if (item && !selectedItemIDs.includes(item.id)) selectItem(item);
		const bounds = managerRef.current?.getBoundingClientRect(); if (!bounds) return;
		const width = 178; const height = item ? 276 : 170; const edge = 8;
		setContextMenu({
			x: Math.max(edge, Math.min(event.clientX - bounds.left, bounds.width - width - edge)),
			y: Math.max(edge, Math.min(event.clientY - bounds.top, bounds.height - height - edge)),
			item,
		});
	};
	const beginBoxSelection = (event: ReactPointerEvent<HTMLDivElement>) => {
		if (event.button !== 0 || (event.target as HTMLElement).closest("[data-file-item], button, a, input, select")) return;
		const shell = listShellRef.current; if (!shell) return;
		event.preventDefault(); setContextMenu(undefined); setSelectedItemIDs([]); setSelectedItemID("");
		const bounds = shell.getBoundingClientRect(); const startX = event.clientX; const startY = event.clientY;
		const move = (moveEvent: PointerEvent) => {
			const left = Math.min(startX, moveEvent.clientX); const top = Math.min(startY, moveEvent.clientY);
			const right = Math.max(startX, moveEvent.clientX); const bottom = Math.max(startY, moveEvent.clientY);
			setSelectionRect({ left: left - bounds.left + shell.scrollLeft, top: top - bounds.top + shell.scrollTop, width: right - left, height: bottom - top });
			const matches = Array.from(shell.querySelectorAll<HTMLElement>("[data-file-item]")).filter((element) => {
				const rect = element.getBoundingClientRect();
				return rect.left < right && rect.right > left && rect.top < bottom && rect.bottom > top;
			}).map((element) => element.dataset.fileItem!).filter(Boolean);
			setSelectedItemIDs(matches); setSelectedItemID(matches.at(-1) ?? "");
		};
		const stop = () => { setSelectionRect(undefined); window.removeEventListener("pointermove", move); window.removeEventListener("pointerup", stop); };
		window.addEventListener("pointermove", move); window.addEventListener("pointerup", stop, { once: true });
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
		setActivity(`${clipboard.mode === "copy" ? "正在复制" : "正在移动"} ${clipboard.entries.length} 个项目…`);
		try {
			for (const source of clipboard.entries) {
				const name = clipboard.mode === "copy" && (source.parentId ?? "") === parentID ? duplicateFileName(source.name) : source.name;
				if (clipboard.mode === "copy") await copyFile(source.id, parentID, name);
				else await moveFile(source.id, parentID, name);
			}
			if (clipboard.mode === "move") setClipboard(undefined);
			setSelectedItemID(""); setSelectedItemIDs([]); await refresh(spaceID, parentID); setError("");
		} catch (caught) { setError(messageOf(caught)); }
		finally { setActivity(""); }
	};
	const renameEntry = async (event: FormEvent<HTMLFormElement>) => {
		event.preventDefault(); if (!renaming) return;
		const name = String(new FormData(event.currentTarget).get("name")); setActivity(`正在重命名“${renaming.name}”…`);
		try { await moveFile(renaming.id, parentID, name); setRenaming(undefined); setSelectedItemID(""); setSelectedItemIDs([]); await refresh(spaceID, parentID); setError(""); }
		catch (caught) { setError(messageOf(caught)); }
		finally { setActivity(""); }
	};
	const removeEntry = async () => {
		if (!pendingDelete?.length) return;
		setActivity(`正在移动 ${pendingDelete.length} 个项目…`);
		try {
			for (const entry of pendingDelete) await deleteFile(entry.id);
			setPendingDelete(undefined); setSelectedItemID(""); setSelectedItemIDs([]); await Promise.all([refresh(spaceID, parentID), refreshVolume()]); setError("");
		}
		catch (caught) { setError(messageOf(caught)); }
		finally { setActivity(""); }
	};
	const itemIcon = (item: FileBrowserItem) => item.kind === "directory" ? <FolderClosed /> : item.kind === "file" ? <FileText /> : item.resource === "photos" ? <Image /> : item.resource === "music" ? <Music2 /> : <Film />;
	const itemArtwork = (item: FileBrowserItem) => <>{itemIcon(item)}{item.entry?.kind === "file" && isBrowserPreviewableImage(item.name) && <img src={filePreviewURL(item.entry.id)} alt="" loading="lazy" onError={(event) => { event.currentTarget.hidden = true; }} />}</>;
	let inspectorPreview: ReactNode;
	if (selectedItem?.entry?.kind === "file" && isBrowserPreviewableImage(selectedItem.name) && failedPreviewID !== selectedItem.id) {
		inspectorPreview = <img src={filePreviewURL(selectedItem.entry.id)} alt={`${selectedItem.name} 的预览`} onError={() => setFailedPreviewID(selectedItem.id)} />;
	} else if (selectedItem?.entry?.kind === "file" && isPDFPreviewable(selectedItem.name)) {
		inspectorPreview = <iframe src={filePreviewURL(selectedItem.entry.id)} title={`${selectedItem.name} 的预览`} />;
	} else if (selectedItem?.entry?.kind === "file" && isTextPreviewable(selectedItem.name)) {
		inspectorPreview = textPreview?.content !== undefined ? <pre>{textPreview.content}</pre> : textPreview?.error ? <span className="file-preview-message">{textPreview.error}</span> : <span className="loader" />;
	} else if (selectedItem?.entry?.kind === "file" && isOfficeDocument(selectedItem.name)) {
		inspectorPreview = <div className="file-preview-unavailable"><FileText /><strong>需要文档转换服务</strong><span>Word 与 PowerPoint 无法由浏览器原生可靠预览。</span></div>;
	} else if (selectedItem) inspectorPreview = itemIcon(selectedItem);
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
				<button className="file-command-button" aria-label="剪切" title="剪切" disabled={!canMutateSelection} onClick={() => setClipboard({ entries: selectedEntries, mode: "move" })}><Scissors /><span>剪切</span></button>
				<button className="file-command-button" aria-label="复制" title="复制" disabled={!canMutateSelection} onClick={() => setClipboard({ entries: selectedEntries, mode: "copy" })}><Copy /><span>复制</span></button>
				<button className="file-command-button" aria-label="粘贴" title="粘贴到当前目录" disabled={!canPaste} onClick={() => void pasteEntry()}><ClipboardPaste /><span>粘贴</span></button>
				<button className="file-command-button" aria-label="重命名" title="重命名" disabled={!canRenameSelection} onClick={() => setRenaming(selectedEntry)}><Pencil /><span>重命名</span></button>
				<button className="file-command-button file-command-optional" aria-label="共享" title="共享功能规划中" disabled><Share2 /><span>共享</span></button>
				<button className="file-command-button danger" aria-label="删除" title="移到回收站" disabled={!canMutateSelection} onClick={() => setPendingDelete(selectedEntries)}><Trash2 /><span>删除</span></button>
				<i />
				<button className="file-command-button" aria-label="切换排序方向" title={`按${fileSortLabel(sortBy)}${sortDirection === "ascending" ? "升序" : "降序"}`} onClick={() => setSortDirection((value) => value === "ascending" ? "descending" : "ascending")}><ArrowUpDown /><span>排序</span></button>
				<div className="file-view-control"><button className="file-command-button" aria-expanded={viewMenuOpen} onClick={() => setViewMenuOpen((value) => !value)}>{view === "list" ? <List /> : <Grid2X2 />}<span>查看</span><ChevronDown /></button>{viewMenuOpen && <div className="file-view-menu"><button className={view === "list" ? "active" : ""} onClick={() => { setView("list"); setViewMenuOpen(false); }}><List />列表视图</button><button className={view === "icons" ? "active" : ""} onClick={() => { setView("icons"); setViewMenuOpen(false); }}><Grid2X2 />图标视图</button></div>}</div>
				<button className="file-command-button file-command-optional" aria-label="更多操作" title="更多操作" disabled><Ellipsis /></button>
				{activity && <span className="file-activity" role="status"><span className="loader" />{activity}</span>}
			</div>

			{error && <PanelNotice error={error} />}
			{viewing && <div className="status-banner viewing-banner" role="status"><ShieldCheck /><span>正在只读查看他人个人空间；访问已审计，将于 {formatFileDate(viewing.expiresAt)} 自动结束。</span><button disabled={Boolean(activity)} onClick={() => void stopViewing()}>结束查看</button></div>}
			{clipboard && <div className="file-clipboard-state" role="status">{clipboard.mode === "copy" ? "已复制" : "已剪切"}{clipboard.entries.length === 1 ? `“${clipboard.entries[0].name}”` : ` ${clipboard.entries.length} 个项目`}</div>}

			{creatingDirectory && !viewing && <form className="file-create-form" aria-label="新建文件夹" onSubmit={(event) => void makeDirectory(event)}><FolderPlus /><input autoFocus name="name" aria-label="新文件夹名称" placeholder="输入文件夹名称" required /><button disabled={Boolean(activity)}>创建</button><button type="button" onClick={() => setCreatingDirectory(false)}>取消</button></form>}

			<div className="file-list-shell" ref={listShellRef} onPointerDown={beginBoxSelection} onContextMenu={(event) => showContextMenu(event)}>
				{view === "list" ? <div className="file-list" role="table" aria-label="文件列表" aria-busy={loading}>
					<div className="file-list-header" role="row">
						<div role="columnheader" aria-sort={sortBy === "name" ? sortDirection : "none"}><button onClick={() => changeSort("name")}>名称{sortBy === "name" && <ChevronDown className={sortDirection === "ascending" ? "ascending" : ""} />}</button></div>
						<div className="file-date-column" role="columnheader" aria-sort={sortBy === "modifiedAt" ? sortDirection : "none"}><button onClick={() => changeSort("modifiedAt")}>修改时间{sortBy === "modifiedAt" && <ChevronDown className={sortDirection === "ascending" ? "ascending" : ""} />}</button></div>
						<div className="file-type-column" role="columnheader" aria-sort={sortBy === "type" ? sortDirection : "none"}><button onClick={() => changeSort("type")}>类型{sortBy === "type" && <ChevronDown className={sortDirection === "ascending" ? "ascending" : ""} />}</button></div>
						<div role="columnheader" aria-sort={sortBy === "sizeBytes" ? sortDirection : "none"}><button onClick={() => changeSort("sizeBytes")}>大小{sortBy === "sizeBytes" && <ChevronDown className={sortDirection === "ascending" ? "ascending" : ""} />}</button></div>
					</div>
					{!loading && visibleItems.map((item) => <div className={`file-list-row ${selectedItemIDs.includes(item.id) ? "selected" : ""}`} role="row" aria-selected={selectedItemIDs.includes(item.id)} data-file-item={item.id} key={item.id} onClick={(event) => selectItem(item, event)} onDoubleClick={() => openItem(item)} onContextMenu={(event) => showContextMenu(event, item)}>
						<div className="file-name-cell" role="cell"><span className={`file-type-icon ${item.kind} ${item.resource ?? ""}`}>{itemArtwork(item)}</span><span className="file-name-copy"><span><strong>{item.name}{item.kind === "resource" && <em>{item.planned ? "规划中" : "应用资源"}</em>}</strong><small>{item.entry?.kind === "file" ? "双击下载" : item.detail}</small></span></span></div>
						<div className="file-date-cell file-date-column" role="cell">{item.modifiedAt ? <><Clock3 />{formatFileDate(item.modifiedAt)}</> : "—"}</div>
						<div className="file-type-cell file-type-column" role="cell"><span className={item.kind === "directory" ? "directory" : item.kind === "resource" ? "resource" : ""}>{item.type}</span></div>
						<div className="file-size-cell" role="cell">{item.kind === "file" ? formatFileSize(item.sizeBytes) : "—"}</div>
					</div>)}
				</div> : <div className="file-icon-view" role="listbox" aria-label="文件图标">
					{!loading && visibleItems.map((item) => <button className={`file-icon-card ${selectedItemIDs.includes(item.id) ? "selected" : ""}`} role="option" aria-selected={selectedItemIDs.includes(item.id)} data-file-item={item.id} key={item.id} onClick={(event) => selectItem(item, event)} onDoubleClick={() => openItem(item)} onContextMenu={(event) => showContextMenu(event, item)}><span className={`file-type-icon ${item.kind} ${item.resource ?? ""}`}>{itemArtwork(item)}</span><strong>{item.name}</strong><small>{item.entry?.kind === "file" ? "双击下载" : item.detail}</small></button>)}
				</div>}
				{selectionRect && <div className="file-selection-box" aria-hidden="true" style={selectionRect} />}
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
			{selectedItems.length > 1 ? <div className="file-inspector-empty"><Copy /><strong>已选择 {selectedItems.length} 个项目</strong><span>可通过工具栏或右键菜单批量剪切、复制和删除</span></div> : selectedItem ? <><div className={`file-inspector-preview ${selectedItem.kind} ${selectedItem.resource ?? ""} ${isBrowserPreviewableImage(selectedItem.name) ? "image" : isPDFPreviewable(selectedItem.name) ? "pdf" : isTextPreviewable(selectedItem.name) ? "text" : isOfficeDocument(selectedItem.name) ? "office" : ""}`}>{inspectorPreview}</div><div className="file-inspector-copy"><h3>{selectedItem.name}</h3><p>{selectedItem.type}</p><dl><dt>资源类型</dt><dd>{selectedItem.kind === "resource" ? "应用资源" : selectedItem.type}</dd><dt>内容</dt><dd>{selectedItem.kind === "file" ? formatFileSize(selectedItem.sizeBytes) : selectedItem.detail}</dd><dt>所在位置</dt><dd>{trail.at(-1)?.name ?? selectedSpaceLabel}</dd>{selectedItem.modifiedAt && <><dt>修改时间</dt><dd>{formatFileDate(selectedItem.modifiedAt)}</dd></>}</dl>{selectedItem.resource === "photos" && <p className="file-inspector-hint">这是相册中个人图库的图库投影。打开后交由相册处理，不暴露受管存储路径。</p>}{selectedItem.planned && <p className="file-inspector-hint">该资源入口已预留，功能尚未开发。</p>}</div></> : <div className="file-inspector-empty"><FolderOpen /><strong>选择一个项目</strong><span>详细信息会显示在这里</span></div>}
		</aside>

		{contextMenu && <div className="file-context-menu" role="menu" aria-label="文件操作" style={{ left: contextMenu.x, top: contextMenu.y }} onPointerDown={(event) => event.stopPropagation()}>
			{contextMenu.item ? <>
				<button role="menuitem" disabled={Boolean(contextMenu.item.planned)} onClick={() => { openItem(contextMenu.item!); setContextMenu(undefined); }}>{contextMenu.item.entry?.kind === "file" ? <Download /> : <FolderOpen />}<span>{contextMenu.item.entry?.kind === "file" ? "下载" : "打开"}</span></button>
				<i />
				<button role="menuitem" disabled={!canMutateSelection} onClick={() => { setClipboard({ entries: selectedEntries, mode: "move" }); setContextMenu(undefined); }}><Scissors /><span>剪切</span><kbd>Ctrl+X</kbd></button>
				<button role="menuitem" disabled={!canMutateSelection} onClick={() => { setClipboard({ entries: selectedEntries, mode: "copy" }); setContextMenu(undefined); }}><Copy /><span>复制</span><kbd>Ctrl+C</kbd></button>
				<button role="menuitem" disabled={!canRenameSelection} onClick={() => { setRenaming(selectedEntry); setContextMenu(undefined); }}><Pencil /><span>重命名</span></button>
				<i />
				<button className="danger" role="menuitem" disabled={!canMutateSelection} onClick={() => { setPendingDelete(selectedEntries); setContextMenu(undefined); }}><Trash2 /><span>移到回收站</span></button>
			</> : <>
				<button role="menuitem" disabled={Boolean(viewing) || Boolean(activity)} onClick={() => { setCreatingDirectory(true); setContextMenu(undefined); }}><FolderPlus /><span>新建文件夹</span></button>
				<button role="menuitem" disabled={!canPaste} onClick={() => { setContextMenu(undefined); void pasteEntry(); }}><ClipboardPaste /><span>粘贴</span><kbd>Ctrl+V</kbd></button>
				<i />
				<button role="menuitem" disabled={!spaceID || loading} onClick={() => { setContextMenu(undefined); void refresh(spaceID, parentID); void refreshVolume(); }}><RefreshCw /><span>刷新</span></button>
			</>}
		</div>}

		{pendingDelete && <div className="file-dialog-backdrop"><section className="file-confirm-dialog" role="alertdialog" aria-modal="true" aria-labelledby="file-delete-title"><span className="file-confirm-icon"><Trash2 /></span><div><h3 id="file-delete-title">移到回收站？</h3><p>{pendingDelete.length === 1 ? `“${pendingDelete[0].name}”` : `选中的 ${pendingDelete.length} 个项目`}将移到回收站，可在 30 天内恢复。</p></div><div className="file-confirm-actions"><button disabled={Boolean(activity)} onClick={() => setPendingDelete(undefined)}>取消</button><button className="danger-button" disabled={Boolean(activity)} onClick={() => void removeEntry()}>{activity ? "正在移动…" : "移到回收站"}</button></div></section></div>}
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

function PanelNotice({ error }: { error: string }) { return <div className="status-banner error"><CircleAlert />{error}</div>; }
function messageOf(error: unknown) { return error instanceof Error ? error.message : "请求失败"; }

function windowReducer(windows: WindowModel[], action: WindowAction): WindowModel[] {
  if (action.type === "show-desktop") return windows.map((window) => window.open ? { ...window, minimized: true } : window);
  if (action.type === "restore") {
    // Bring the windows back above the rest, keeping their stacking order.
    const base = Math.max(...windows.map((window) => window.z)) + 1;
    const order = windows.filter((window) => window.open && action.ids.includes(window.id)).sort((a, b) => a.z - b.z).map((window) => window.id);
    return windows.map((window) => order.includes(window.id) ? { ...window, minimized: false, z: base + order.indexOf(window.id) } : window);
  }
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

function isPDFPreviewable(name: string): boolean {
	return name.toLocaleLowerCase("en-US").endsWith(".pdf");
}

function isTextPreviewable(name: string): boolean {
	const extension = name.includes(".") ? name.split(".").pop()?.toLocaleLowerCase("en-US") : "";
	return ["txt", "md", "markdown", "json", "yaml", "yml", "csv", "log", "xml", "html", "css", "js", "jsx", "ts", "tsx", "go", "py", "sh", "ini", "conf"].includes(extension ?? "");
}

function isOfficeDocument(name: string): boolean {
	const extension = name.includes(".") ? name.split(".").pop()?.toLocaleLowerCase("en-US") : "";
	return ["doc", "docx", "ppt", "pptx"].includes(extension ?? "");
}

function fileSortLabel(sort: FileSort): string {
	return { name: "名称", modifiedAt: "修改时间", type: "类型", sizeBytes: "大小" }[sort];
}

function duplicateFileName(name: string): string {
	const dot = name.lastIndexOf(".");
	if (dot <= 0) return `${name} - 副本`;
	return `${name.slice(0, dot)} - 副本${name.slice(dot)}`;
}
