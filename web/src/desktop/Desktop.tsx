import { ArrowDownUp, FolderClosed, Music2, Settings, ShoppingBag, Trash2 } from "lucide-react";
import {
  ComponentProps,
  lazy,
  MouseEvent as ReactMouseEvent,
  PointerEvent as ReactPointerEvent,
  Suspense,
  useCallback,
  useEffect,
  useReducer,
  useRef,
  useState,
} from "react";

import { type AssistantTaskNotice, Session, User } from "../api";
import { AppCenterPanel } from "../appcenter/AppCenterPanel";
import { assistantStatus } from "../assistant/assistantApi";
import { AssistantGlyph } from "../AssistantGlyph";
import { shortcutApp, useAppShortcuts } from "../appShortcuts";
import { BackupCenter } from "../backup/BackupCenter";
import { BackupCenterGlyph } from "../BackupCenterGlyph";
import { DesktopApp, DesktopGrid } from "../DesktopGrid";
import { Dock } from "../Dock";
import { DockerGlyph } from "../DockerGlyph";
import { DockerFocus, DockerPanel } from "../docker/DockerPanel";
import { appProject } from "../docker/model";
import { FeedbackChannel } from "../feedback/FeedbackChannel";
import { FilePanel } from "../files/FilePanel";
import { useWallpaper, wallpaperBackground } from "../wallpaper";
import { MediaCenter } from "../media/MediaCenter";
import { MediaCenterGlyph } from "../MediaCenterGlyph";
import { PhotosGlyph } from "../PhotosGlyph";
import { MusicPanel } from "../music/MusicPanel";
import { displayName, forgetPeople, loadPeople, rememberPerson } from "../people";
import { PhotosPanel } from "../photos/PhotosPanel";
import { SettingsPanel, SettingsSection, useStoragePlan } from "../SettingsPanel";
import { StatusBar } from "../StatusBar";
import { DesktopView, SystemRail } from "../SystemRail";
import { TrashPanel } from "../trash/TrashPanel";
import { transferStore } from "../transfers/store";
import { TransferIndicator, TransferToasts } from "../transfers/TransferNotices";
import { TaskToasts } from "../assistant/TaskToasts";
import { whenText } from "../assistant/taskTime";
import type { AssistantFocus } from "../assistant/AssistantPanel";
import type { TransferLocation } from "../transfers/transfersApi";
import { useHostState } from "../useHostState";
import { SharesProvider, useShares } from "../shares/SharesProvider";
import { SharesPanel } from "../shares/SharesPanel";
import { AppWindow } from "./AppWindow";
import { NotificationCenter } from "./NotificationCenter";
import { initialWindows, WindowID, windowIcons, windowReducer, windowTones } from "./windows";

// The assistant window, with its chat libraries, loads when first opened.
const AssistantPanel = lazy(() =>
  import("../assistant/AssistantPanel").then((module) => ({ default: module.AssistantPanel })),
);
// The transfer center loads when first opened; the desktop only needs its
// status-bar pill and toasts.
const TransferCenter = lazy(() =>
  import("../transfers/TransferCenter").then((module) => ({ default: module.TransferCenter })),
);

// SharesRail is the system rail with the count of shares that have something
// new, which only the shares provider knows.
function SharesRail(props: Omit<ComponentProps<typeof SystemRail>, "sharesUnread">) {
  const { unread } = useShares();
  return <SystemRail {...props} sharesUnread={unread.received + unread.sent} />;
}

// A press on the wallpaper that moves this many pixels or more is a drag, not a click.
const wallpaperClickSlop = 6;

export function Desktop({
  session,
  onLogout,
  onUserChange,
}: {
  session: Session;
  onLogout: () => void;
  onUserChange: (user: User) => void;
}) {
  const host = useHostState(true);
  const wallpaper = useWallpaper();
  // Names and avatars of every account; read again whenever the page comes
  // back into view, so someone else's new nickname shows up without a reload.
  useEffect(() => {
    void loadPeople();
    const refresh = () => {
      if (document.visibilityState === "visible") void loadPeople();
    };
    document.addEventListener("visibilitychange", refresh);
    return () => {
      document.removeEventListener("visibilitychange", refresh);
      forgetPeople();
    };
  }, [session.user.id]);
  const changeUser = (user: User) => {
    rememberPerson(user);
    onUserChange(user);
  };
  const [windows, dispatch] = useReducer(windowReducer, initialWindows);
  const isOpen = (id: WindowID) => windows.some((window) => window.id === id && window.open);
  const openWindow = (id: WindowID) => dispatch({ type: "open", id });
  // The section and any pending storage plan outlive the settings window, so
  // reopening it returns to where the user left off.
  const [settingsSection, setSettingsSection] = useState<SettingsSection>("overview");
  const storagePlan = useStoragePlan();
  const openSettings = (section?: SettingsSection) => {
    if (section) setSettingsSection(section);
    openWindow("settings");
  };
  // Uploads of an earlier visit by the same account continue; another
  // account's are dropped.
  useEffect(() => {
    transferStore.attach(session.user.id);
  }, [session.user.id]);
  // The assistant opens only when this device runs one for this account (ADR 0024).
  const [assistantReady, setAssistantReady] = useState(false);
  useEffect(() => {
    void assistantStatus()
      .then((status) => setAssistantReady(status.available))
      .catch(() => setAssistantReady(false));
  }, [session.user.id]);
  // A folder the transfer center asks the file manager to show.
  const [fileNavigation, setFileNavigation] = useState<TransferLocation>();
  const showInFiles = (location: TransferLocation) => {
    setFileNavigation(location);
    openWindow("files");
  };
  // The App Center's 在 Docker 中查看 opens Docker on that app's containers.
  const [dockerFocus, setDockerFocus] = useState<DockerFocus>();
  // A photo another window asks the photo app to show, such as one the assistant named.
  const [photoFocus, setPhotoFocus] = useState<{ assetId: string; at: number }>();
  // A toast of a scheduled task opens its run in VNAS, or the tasks.
  const [assistantFocus, setAssistantFocus] = useState<AssistantFocus>();
  const showTaskRun = (run: AssistantTaskNotice) => {
    setAssistantFocus({
      sessionId: run.sessionId,
      title: `${run.name} · ${whenText(Date.parse(run.ranAt) / 1000)}`,
      at: Date.now(),
    });
    openWindow("assistant");
  };
  const assistantFocusHandled = useCallback(() => setAssistantFocus(undefined), []);
  const showInDocker = (appID: string) => {
    setDockerFocus((current) => ({ project: appProject(appID), at: (current?.at ?? 0) + 1 }));
    openWindow("docker");
  };
  const showPhoto = (assetId: string) => {
    setPhotoFocus({ assetId, at: Date.now() });
    openWindow("photos");
  };
  const shortcuts = useAppShortcuts();
  // Keywords let the launcher find an app by what it holds, not only by its name.
  const desktopApps: DesktopApp[] = [
    {
      id: "files",
      label: "文件管理",
      ariaLabel: "打开文件管理",
      keywords: "文件 共享 上传 shared",
      tone: "files",
      icon: <FolderClosed />,
      active: isOpen("files"),
      onClick: () => openWindow("files"),
    },
    {
      id: "trash",
      label: "回收站",
      ariaLabel: "打开回收站",
      keywords: "删除 恢复",
      tone: "trash",
      icon: <Trash2 />,
      active: isOpen("trash"),
      onClick: () => openWindow("trash"),
    },
    // The administrator terminal lives in Settings → 终端, so searching for it in the launcher finds 设置.
    {
      id: "settings",
      label: "设置",
      ariaLabel: "打开设置",
      keywords: `账号 密码 smb 用户 成员 权限 存储 磁盘 数据卷 系统 日志 审计 登录记录 活动 log${session.user.role === "admin" ? " 终端 shell 命令行 bash" : ""}`,
      tone: "settings",
      icon: <Settings />,
      active: isOpen("settings"),
      onClick: () => openSettings(),
    },
    // Docker and the App Center run containers with root-equivalent engine access: administrators only.
    ...(session.user.role === "admin"
      ? [
          {
            id: "store",
            label: "应用中心",
            ariaLabel: "打开应用中心",
            keywords: "应用 安装 商店 app",
            tone: "store",
            icon: <ShoppingBag />,
            active: isOpen("store"),
            onClick: () => openWindow("store"),
          },
        ]
      : []),
    {
      id: "video",
      label: "影视中心",
      ariaLabel: "打开影视中心",
      keywords: "影视 电影 电视剧 视频 播放 movie video",
      tone: "video",
      icon: <MediaCenterGlyph />,
      active: isOpen("media"),
      onClick: () => openWindow("media"),
    },
    // The id stays "download" so the icon keeps its place on desktops arranged before the rename.
    {
      id: "download",
      label: "传输中心",
      ariaLabel: "打开传输中心",
      keywords: "传输 上传 下载 进度 续传 transfer upload download",
      tone: "download",
      icon: <ArrowDownUp />,
      active: isOpen("transfers"),
      onClick: () => openWindow("transfers"),
    },
    // 备份中心 replaced the separate 文件快照 window and the planned 备份 entry; it keeps the backup ID so stored icon orders still place it.
    {
      id: "backup",
      label: "备份中心",
      ariaLabel: "打开备份中心",
      keywords: "备份 快照 文件快照 恢复 找回 历史版本 定时 snapshot backup",
      tone: "backup",
      icon: <BackupCenterGlyph />,
      active: isOpen("backup"),
      onClick: () => openWindow("backup"),
    },
    ...(session.user.role === "admin"
      ? [
          {
            id: "docker",
            label: "Docker",
            ariaLabel: "打开 Docker",
            keywords: "容器 镜像 container",
            tone: "docker",
            icon: <DockerGlyph />,
            active: isOpen("docker"),
            onClick: () => openWindow("docker"),
          },
        ]
      : []),
    {
      id: "photos",
      label: "相册",
      ariaLabel: "打开相册",
      keywords: "照片 图片 图库 photo",
      tone: "photos",
      icon: <PhotosGlyph />,
      active: isOpen("photos"),
      onClick: () => openWindow("photos"),
    },
    {
      id: "music",
      label: "音乐",
      ariaLabel: "打开音乐",
      keywords: "歌曲 专辑 歌手 歌单 播放 music",
      tone: "music",
      icon: <Music2 />,
      active: isOpen("music"),
      onClick: () => openWindow("music"),
    },
    assistantReady
      ? {
          id: "ai",
          label: "VNAS",
          ariaLabel: "打开 VNAS",
          keywords: "vnas 助手 智能 问答 聊天 assistant ai",
          tone: "ai",
          icon: <AssistantGlyph />,
          active: isOpen("assistant"),
          onClick: () => openWindow("assistant"),
        }
      : { id: "ai", label: "VNAS", ariaLabel: "VNAS，规划中", tone: "ai", icon: <AssistantGlyph />, disabled: true },
    // Installed apps open their own web interface; a stopped one shows its
    // containers in Docker to administrators.
    ...shortcuts.map((shortcut) =>
      shortcutApp(shortcut, session.user.role === "admin" ? () => showInDocker(shortcut.id) : undefined),
    ),
  ];
  const visible = windows.filter((window) => window.open && !window.minimized);
  const focusedID = visible.length ? visible.reduce((top, window) => (window.z > top.z ? window : top)).id : undefined;
  // Windows hidden by "show desktop" come back on the next press, as long as
  // nothing was reopened in between; windows minimized by hand stay down.
  const [revealed, setRevealed] = useState<WindowID[]>([]);
  const hidden = revealed.filter((id) => windows.some((window) => window.id === id && window.open && window.minimized));
  const desktopView: DesktopView = visible.length ? "windows" : hidden.length ? "revealed" : "clear";
  const showDesktop = () => {
    setRevealed(visible.map((window) => window.id));
    dispatch({ type: "show-desktop" });
  };
  const toggleDesktop = () => {
    if (visible.length) showDesktop();
    else if (hidden.length) {
      setRevealed([]);
      dispatch({ type: "restore", ids: hidden });
    }
  };
  // A click on the bare wallpaper, or between desktop icons, puts the visible
  // windows away as 显示桌面 does. The press must start there and stay put, so
  // a window drag or a selection released over the wallpaper keeps the
  // windows, and the press that closes a rail popover only closes it.
  const wallpaperPress = useRef<{ x: number; y: number }>(undefined);
  const pressDesktop = (event: ReactPointerEvent<HTMLElement>) => {
    const shell = event.currentTarget;
    const bare =
      event.target === shell || (event.target instanceof Element && event.target.classList.contains("desktop-grid"));
    const railPopoverOpen = shell.querySelector('.system-rail [aria-expanded="true"]') !== null;
    wallpaperPress.current =
      bare && event.button === 0 && !railPopoverOpen ? { x: event.clientX, y: event.clientY } : undefined;
  };
  const clickDesktop = (event: ReactMouseEvent<HTMLElement>) => {
    const press = wallpaperPress.current;
    wallpaperPress.current = undefined;
    if (press && visible.length && Math.hypot(event.clientX - press.x, event.clientY - press.y) < wallpaperClickSlop)
      showDesktop();
  };

  return (
    <SharesProvider userId={session.user.id} active={focusedID === "shares"} onOpen={() => openWindow("shares")}>
      <main
        className="desktop-shell"
        style={wallpaperBackground(wallpaper)}
        onPointerDownCapture={pressDesktop}
        onPointerCancelCapture={() => {
          wallpaperPress.current = undefined;
        }}
        onClick={clickDesktop}
      >
        {/* The glow belongs to the built-in wallpaper; it would wash out a chosen picture. */}
        {wallpaper && !wallpaper.image && <div className="wallpaper-glow" />}

        <SharesRail
          apps={desktopApps}
          session={session}
          desktopView={desktopView}
          settingsFocused={focusedID === "settings"}
          sharesFocused={focusedID === "shares"}
          onOpenShares={() => openWindow("shares")}
          onToggleDesktop={toggleDesktop}
          onOpenSettings={openSettings}
          onLogout={onLogout}
        />

        <DesktopGrid apps={desktopApps} />

        <StatusBar
          host={host}
          extra={<TransferIndicator onOpen={() => openWindow("transfers")} />}
          onOpenTransfers={() => openWindow("transfers")}
          onOpenTaskManager={() => openSettings("task-manager")}
        />

        <NotificationCenter />

        <section className="window-layer" aria-label="A-NAS 桌面窗口">
          {windows.map((window) => {
            // Minimized windows stay mounted so a running terminal session in Settings survives.
            if (!window.open) return null;
            return (
              <AppWindow key={window.id} model={window} dispatch={dispatch}>
                {window.id === "shares" && <SharesPanel />}
                {window.id === "settings" && (
                  <SettingsPanel
                    session={session}
                    host={host}
                    section={settingsSection}
                    onSectionChange={setSettingsSection}
                    storagePlan={storagePlan}
                    onUserChange={changeUser}
                    onLogout={onLogout}
                  />
                )}
                {window.id === "files" && (
                  <FilePanel
                    userID={session.user.id}
                    navigation={fileNavigation}
                    onNavigated={() => setFileNavigation(undefined)}
                    onOpenTransfers={() => openWindow("transfers")}
                    onOpenPhotos={() => dispatch({ type: "open", id: "photos" })}
                    onOpenMedia={() => dispatch({ type: "open", id: "media" })}
                    onOpenMusic={() => dispatch({ type: "open", id: "music" })}
                  />
                )}
                {window.id === "transfers" && (
                  <Suspense
                    fallback={
                      <div className="center-state">
                        <span className="loader" />
                      </div>
                    }
                  >
                    <TransferCenter onOpenFiles={() => openWindow("files")} onOpenLocation={showInFiles} />
                  </Suspense>
                )}
                {window.id === "photos" && (
                  <PhotosPanel
                    userId={session.user.id}
                    userName={displayName(session.user)}
                    isAdmin={session.user.role === "admin"}
                    focus={photoFocus}
                    onFocusHandled={() => setPhotoFocus(undefined)}
                  />
                )}
                {window.id === "media" && (
                  <MediaCenter userId={session.user.id} isAdmin={session.user.role === "admin"} />
                )}
                {window.id === "music" && (
                  <MusicPanel userId={session.user.id} onOpenFiles={() => dispatch({ type: "open", id: "files" })} />
                )}
                {window.id === "trash" && <TrashPanel onShowInFiles={showInFiles} />}
                {window.id === "backup" && (
                  <BackupCenter
                    userId={session.user.id}
                    isAdmin={session.user.role === "admin"}
                    onShowInFiles={showInFiles}
                    onOpenTrash={() => openWindow("trash")}
                  />
                )}
                {window.id === "docker" && (
                  <DockerPanel
                    focus={dockerFocus}
                    onFocusHandled={() => setDockerFocus(undefined)}
                    onOpenStore={() => openWindow("store")}
                  />
                )}
                {window.id === "store" && <AppCenterPanel onOpenDocker={showInDocker} />}
                {window.id === "assistant" && (
                  <Suspense fallback={null}>
                    <AssistantPanel
                      username={session.user.username}
                      onShowPhoto={showPhoto}
                      focus={assistantFocus}
                      onFocusHandled={assistantFocusHandled}
                    />
                  </Suspense>
                )}
              </AppWindow>
            );
          })}
        </section>

        <Dock
          entries={windows
            .filter((window) => window.open)
            .sort((a, b) => a.opened - b.opened)
            .map((window) => ({
              id: window.id,
              title: window.title,
              minimized: window.minimized,
              icon: windowIcons[window.id],
              tone: windowTones[window.id],
            }))}
          focusedID={focusedID}
          onOpen={(id) => dispatch({ type: "open", id: id as WindowID })}
          onMinimize={(id) => dispatch({ type: "minimize", id: id as WindowID })}
          onClose={(id) => dispatch({ type: "close", id: id as WindowID })}
        />
        <TransferToasts hidden={focusedID === "transfers"} onOpen={() => openWindow("transfers")} />
        <TaskToasts enabled={assistantReady} onOpen={showTaskRun} />
        <FeedbackChannel />
      </main>
    </SharesProvider>
  );
}
