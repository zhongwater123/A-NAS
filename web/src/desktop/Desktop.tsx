import {
  ComponentProps,
  memo,
  MouseEvent as ReactMouseEvent,
  ReactNode,
  PointerEvent as ReactPointerEvent,
  Suspense,
  useCallback,
  useEffect,
  useMemo,
  useReducer,
  useRef,
  useState,
} from "react";
import { ErrorBoundary } from "react-error-boundary";

import type { Session, User } from "../api.generated";
import { assistantStatus } from "../assistant/api";
import { TaskToasts } from "../assistant/TaskToasts";
import { whenText } from "../assistant/taskTime";
import { appProject } from "../docker/model";
import { FeedbackChannel } from "../feedback/FeedbackChannel";
import { forgetPeople, loadPeople, rememberPerson } from "../people/people";
import type { SettingsSection } from "../settings/SettingsPanel";
import { useStoragePlan } from "../settings/storagePlan";
import { SharesProvider, useShares } from "../shares/SharesProvider";
import { transferStore } from "../transfers/store";
import { TransferIndicator, TransferToasts } from "../transfers/TransferNotices";
import { AppWindow } from "./AppWindow";
import type { AssistantTaskNotice } from "./api";
import { shortcutApp, useAppShortcuts } from "./appShortcuts";
import { apps, desktopApps, OpenWindow, WindowContext, WindowID, WindowIntents, windowIDs } from "./apps";
import { DesktopGrid } from "./DesktopGrid";
import { Dock } from "./Dock";
import styles from "./desktop.module.css";
import { NotificationCenter } from "./NotificationCenter";
import { StatusBar } from "./StatusBar";
import { DesktopView, SystemRail } from "./SystemRail";
import { useHostState } from "./useHostState";
import { useWallpaper, wallpaperBackground } from "./wallpaper";
import { closedWindow, windowReducer } from "./windows";

// SharesRail is the system rail with the count of shares that have something
// new, which only the shares provider knows.
function SharesRail(props: Omit<ComponentProps<typeof SystemRail>, "sharesUnread">) {
  const { unread } = useShares();
  return <SystemRail {...props} sharesUnread={unread.received + unread.sent} />;
}

// A part of the desktop that fails disappears on its own; the windows and the
// rest of the desktop keep working.
function ShellPart({ name, children }: { name: string; children: ReactNode }) {
  return (
    <ErrorBoundary
      fallback={null}
      onError={(error, info) => console.error(`A-NAS ${name} failed`, error, info.componentStack)}
    >
      {children}
    </ErrorBoundary>
  );
}

// WindowContent is what an app shows in its window. Dragging, resizing and
// stacking change only the window around it.
const WindowContent = memo(function WindowContent({ id, context }: { id: WindowID; context: WindowContext }) {
  return <Suspense fallback={windowLoading}>{apps[id].render(context)}</Suspense>;
});

// What a window shows while its code loads, the first time it opens.
const windowLoading = (
  <div className={styles.centerState}>
    <span className="loader" />
  </div>
);

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
  const changeUser = useCallback(
    (user: User) => {
      rememberPerson(user);
      onUserChange(user);
    },
    [onUserChange],
  );
  const [windows, dispatch] = useReducer(windowReducer, undefined, () =>
    windowIDs.map((id) => closedWindow(id, apps[id].bounds)),
  );
  const isOpen = (id: WindowID) => windows.some((window) => window.id === id && window.open);
  // Where other windows asked an app to go. Each request is a new object, so
  // asking for the same place twice shows it twice.
  const [intents, setIntents] = useState<WindowIntents>({});
  const open = useCallback<OpenWindow>((id, intent?: object) => {
    if (intent) setIntents((current) => ({ ...current, [id]: { ...intent } }));
    dispatch({ type: "open", id });
  }, []);
  const intentHandled = useCallback(
    (id: keyof WindowIntents) => setIntents((current) => (current[id] ? { ...current, [id]: undefined } : current)),
    [],
  );
  const [settingsSection, setSettingsSection] = useState<SettingsSection>("overview");
  const storagePlan = useStoragePlan();
  const openSettings = (section?: SettingsSection) => {
    if (section) setSettingsSection(section);
    open("settings");
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
  const showInDocker = (appID: string) => open("docker", { project: appProject(appID) });
  // A toast of a scheduled task opens its run in VNAS, or the tasks.
  const showTaskRun = (run: AssistantTaskNotice) =>
    open("assistant", { sessionId: run.sessionId, title: `${run.name} · ${whenText(Date.parse(run.ranAt) / 1000)}` });
  const isAdmin = session.user.role === "admin";
  // The windows' content re-renders only when this changes, not when a
  // window is dragged, resized or brought to the front.
  const context = useMemo<WindowContext>(
    () => ({
      session,
      isAdmin,
      host,
      assistantReady,
      open,
      intents,
      intentHandled,
      settingsSection,
      setSettingsSection,
      storagePlan,
      changeUser,
      logout: onLogout,
    }),
    [
      session,
      isAdmin,
      host,
      assistantReady,
      open,
      intents,
      intentHandled,
      settingsSection,
      storagePlan,
      changeUser,
      onLogout,
    ],
  );
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
    const desktop = event.currentTarget;
    const bare =
      event.target === desktop ||
      (event.target instanceof Element && event.target.classList.contains(styles.desktopGrid));
    const railPopoverOpen = desktop.querySelector(`.${styles.systemRail} [aria-expanded="true"]`) !== null;
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
    <SharesProvider userId={session.user.id} active={focusedID === "shares"} onOpen={() => open("shares")}>
      {/* A click on the wallpaper is a pointer shortcut for 显示桌面, which the
          system rail offers to the keyboard. */}
      {/* oxlint-disable-next-line jsx-a11y/click-events-have-key-events, jsx-a11y/no-noninteractive-element-interactions */}
      <main
        className={styles.desktopShell}
        style={wallpaperBackground(wallpaper)}
        onPointerDownCapture={pressDesktop}
        onPointerCancelCapture={() => {
          wallpaperPress.current = undefined;
        }}
        onClick={clickDesktop}
      >
        {/* The glow belongs to the built-in wallpaper; it would wash out a chosen picture. */}
        {wallpaper && !wallpaper.image && <div className={styles.wallpaperGlow} />}

        <ShellPart name="system rail">
          <SharesRail
            apps={launcherApps}
            session={session}
            desktopView={desktopView}
            settingsFocused={focusedID === "settings"}
            sharesFocused={focusedID === "shares"}
            onOpenShares={() => open("shares")}
            onToggleDesktop={toggleDesktop}
            onOpenSettings={openSettings}
            onLogout={onLogout}
          />
        </ShellPart>

        <ShellPart name="desktop icons">
          <DesktopGrid apps={launcherApps} />
        </ShellPart>

        <ShellPart name="status bar">
          <StatusBar
            host={host}
            extra={<TransferIndicator onOpen={() => open("transfers")} />}
            onOpenTransfers={() => open("transfers")}
            onOpenTaskManager={() => openSettings("task-manager")}
          />
        </ShellPart>

        <ShellPart name="notifications">
          <NotificationCenter />
        </ShellPart>

        <section className={styles.windowLayer} aria-label="A-NAS 桌面窗口">
          {windows.map((window) => {
            // Minimized windows stay mounted so a running terminal session in Settings survives.
            if (!window.open) return null;
            return (
              <AppWindow key={window.id} model={window} dispatch={dispatch}>
                <WindowContent id={window.id} context={context} />
              </AppWindow>
            );
          })}
        </section>

        <ShellPart name="dock">
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
        </ShellPart>
        <ShellPart name="transfer toasts">
          <TransferToasts hidden={focusedID === "transfers"} onOpen={() => open("transfers")} />
        </ShellPart>
        <ShellPart name="task toasts">
          <TaskToasts enabled={assistantReady} onOpen={showTaskRun} />
        </ShellPart>
        <ShellPart name="feedback">
          <FeedbackChannel />
        </ShellPart>
      </main>
    </SharesProvider>
  );
}
