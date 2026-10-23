import {
  ComponentProps,
  MouseEvent as ReactMouseEvent,
  PointerEvent as ReactPointerEvent,
  useEffect,
  useReducer,
  useRef,
  useState,
} from "react";

import { type AssistantTaskNotice, Session, User } from "../api";
import { assistantStatus } from "../assistant/assistantApi";
import { TaskToasts } from "../assistant/TaskToasts";
import { whenText } from "../assistant/taskTime";
import { shortcutApp, useAppShortcuts } from "../appShortcuts";
import { DesktopGrid } from "../DesktopGrid";
import { Dock } from "../Dock";
import { appProject } from "../docker/model";
import { FeedbackChannel } from "../feedback/FeedbackChannel";
import { forgetPeople, loadPeople, rememberPerson } from "../people";
import { SettingsSection, useStoragePlan } from "../SettingsPanel";
import { SharesProvider, useShares } from "../shares/SharesProvider";
import { StatusBar } from "../StatusBar";
import { DesktopView, SystemRail } from "../SystemRail";
import { transferStore } from "../transfers/store";
import { TransferIndicator, TransferToasts } from "../transfers/TransferNotices";
import type { TransferLocation } from "../transfers/transfersApi";
import { useHostState } from "../useHostState";
import { useWallpaper, wallpaperBackground } from "../wallpaper";
import { apps, desktopApps, WindowContext, WindowFocus, WindowID, windowIDs } from "./apps";
import { AppWindow } from "./AppWindow";
import { NotificationCenter } from "./NotificationCenter";
import { closedWindow, windowReducer } from "./windows";

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
  const [windows, dispatch] = useReducer(windowReducer, undefined, () =>
    windowIDs.map((id) => closedWindow(id, apps[id].bounds)),
  );
  const isOpen = (id: WindowID) => windows.some((window) => window.id === id && window.open);
  const openWindow = (id: WindowID) => dispatch({ type: "open", id });
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
  const [assistantReady, setAssistantReady] = useState(false);
  useEffect(() => {
    void assistantStatus()
      .then((status) => setAssistantReady(status.available))
      .catch(() => setAssistantReady(false));
  }, [session.user.id]);
  const [focus, setFocus] = useState<WindowFocus>({});
  const showInFiles = (location: TransferLocation) => {
    setFocus((current) => ({ ...current, files: location }));
    openWindow("files");
  };
  const showInDocker = (appID: string) => {
    setFocus((current) => ({ ...current, docker: { project: appProject(appID), at: (current.docker?.at ?? 0) + 1 } }));
    openWindow("docker");
  };
  const showPhoto = (assetId: string) => {
    setFocus((current) => ({ ...current, photos: { assetId, at: Date.now() } }));
    openWindow("photos");
  };
  // A toast of a scheduled task opens its run in VNAS, or the tasks.
  const showTaskRun = (run: AssistantTaskNotice) => {
    setFocus((current) => ({
      ...current,
      assistant: {
        sessionId: run.sessionId,
        title: `${run.name} · ${whenText(Date.parse(run.ranAt) / 1000)}`,
        at: Date.now(),
      },
    }));
    openWindow("assistant");
  };
  const isAdmin = session.user.role === "admin";
  const context: WindowContext = {
    session,
    isAdmin,
    host,
    assistantReady,
    open: openWindow,
    showInFiles,
    showInDocker,
    showPhoto,
    focus,
    focusHandled: (id) => setFocus((current) => ({ ...current, [id]: undefined })),
    settingsSection,
    setSettingsSection,
    storagePlan,
    changeUser,
    logout: onLogout,
  };
  const shortcuts = useAppShortcuts();
  const launcherApps = [
    ...desktopApps(context, isOpen),
    // Installed apps open their own web interface; a stopped one shows its
    // containers in Docker to administrators.
    ...shortcuts.map((shortcut) => shortcutApp(shortcut, isAdmin ? () => showInDocker(shortcut.id) : undefined)),
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
          apps={launcherApps}
          session={session}
          desktopView={desktopView}
          settingsFocused={focusedID === "settings"}
          sharesFocused={focusedID === "shares"}
          onOpenShares={() => openWindow("shares")}
          onToggleDesktop={toggleDesktop}
          onOpenSettings={openSettings}
          onLogout={onLogout}
        />

        <DesktopGrid apps={launcherApps} />

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
                {apps[window.id].render(context)}
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
              title: apps[window.id].title,
              minimized: window.minimized,
              icon: apps[window.id].icon,
              tone: apps[window.id].tone,
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
