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
import { Companion } from "../companion/Companion";
import { useDeviceTrouble } from "../companion/health";
import { appProject } from "../docker/model";
import { FeedbackChannel } from "../feedback/FeedbackChannel";
import { montageEnabled } from "../montage/status";
import { forgetPeople, loadPeople, rememberPerson } from "../people/people";
import { noScoring, type ScoringAccess, scoringAccess } from "../scoring/status";
import type { SettingsSection } from "../settings/SettingsPanel";
import { useStoragePlan } from "../settings/storagePlan";
import { SharesProvider, useShares } from "../shares/SharesProvider";
import { transferStore } from "../transfers/store";
import { TransferIndicator, TransferToasts } from "../transfers/TransferNotices";
import { lazyCode } from "../ui/lazyCode";
import { useWebSearch } from "../websearch/status";
import { type WorkspaceAccess, workspaceAccess } from "../workspace/status";
import { AppWindow } from "./AppWindow";
import type { AssistantTaskNotice } from "./api";
import { shortcutApp, useAppShortcuts } from "./appShortcuts";
import {
  apps,
  desktopApps,
  OpenWindow,
  type SettingsState,
  SettingsStateContext,
  WindowContext,
  WindowID,
  WindowIntentPayloads,
  WindowIntents,
  windowIDs,
} from "./apps";
import { DesktopGrid } from "./DesktopGrid";
import { Dock } from "./Dock";
import styles from "./desktop.module.css";
import { isLocalConsole, onLocalConsoleDismissed } from "./LocalConsoleScreenSaver";
import { NotificationCenter } from "./NotificationCenter";
import { StatusBar } from "./StatusBar";
import { DesktopView, SystemRail } from "./SystemRail";
import { useHostState } from "./useHostState";
import { useReadOnShow } from "./useReadOnShow";
import { useWallpaper, wallpaperBackground } from "./wallpaper";
import { closedWindow, MAXIMIZED_INSET, windowReducer } from "./windows";

/**
 * The VNAS window as the companion opens it to talk: small, beside the companion. As narrow as a window may
 * be (desktop.module.css), which drops VNAS's conversation list (assistant.module.css).
 */
const VNAS_CHAT = { width: 520, height: 560 };
/** Between it and the companion, and between it and the page's edges. */
const GAP = 12;
const EDGE = 8;

// 家庭中心's hint, 剪辑中心's notices and 待办's reminder load after the
// desktop, so their code and styles stay out of the first load.
const FamilyNotices = lazyCode(() => import("../family/FamilyNotices").then((module) => module.FamilyNotices));
const MontageNotices = lazyCode(() => import("../montage/MontageNotices").then((module) => module.MontageNotices));
const TodoNotices = lazyCode(() => import("../todos/TodoNotices").then((module) => module.TodoNotices));

// An app's access read again that says the same keeps the old value.
const sameScoring = (current: ScoringAccess, next: ScoringAccess) =>
  current.admin === next.admin && current.rater === next.rater;
const sameWorkspace = (current: WorkspaceAccess | null, next: WorkspaceAccess | null) => current?.admin === next?.admin;

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
  // The companion's dot while the device is in critical trouble, held steady over the reads (health.ts).
  const trouble = useDeviceTrouble(host.snapshot, host.disconnected, host.readAt);
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
    <ID extends keyof WindowIntentPayloads>(id: ID, handled: WindowIntentPayloads[ID]) =>
      setIntents((current) => (current[id] === handled ? { ...current, [id]: undefined } : current)),
    [],
  );
  // The companion asked to talk (companion/petCard.tsx): the VNAS window, small, beside the companion, at
  // the latest conversation. A VNAS window already in view only comes to the front, as it is. Opened small,
  // it gets back the size, place and maximizing it had before once it is closed, so VNAS opens as ever from
  // the launcher.
  const chatOpened = useRef<{ x: number; y: number; width: number; height: number; maximized: boolean } | null>(null);
  const openChat = useCallback(
    (beside: DOMRect | null) => {
      const vnas = windows.find((window) => window.id === "assistant");
      if (vnas?.open && !vnas.minimized) {
        open("assistant");
        return;
      }
      // Opened small once already and not yet closed: what it had before that stays what it gets back.
      if (vnas && !chatOpened.current) {
        const { x, y, width, height, maximized } = vnas;
        chatOpened.current = { x, y, width, height, maximized };
      }
      open("assistant", { latest: true });
      if (vnas?.maximized) dispatch({ type: "maximize", id: "assistant" });
      const page = { width: window.innerWidth, height: window.innerHeight };
      const width = Math.min(VNAS_CHAT.width, page.width - MAXIMIZED_INSET.left - EDGE);
      const height = Math.min(VNAS_CHAT.height, page.height - 2 * EDGE);
      const clamp = (value: number, low: number, high: number) => Math.min(Math.max(value, low), Math.max(low, high));
      // To the companion's left, its bottom level with the companion's; to its right where the left has no room.
      const left = beside ? beside.left - GAP - width : page.width - width - EDGE;
      const x = beside && left < MAXIMIZED_INSET.left ? beside.right + GAP : left;
      const y = beside ? beside.bottom - height : page.height - height - EDGE;
      dispatch({
        type: "move",
        id: "assistant",
        x: clamp(x, MAXIMIZED_INSET.left, page.width - width - EDGE),
        y: clamp(y, EDGE, page.height - height - EDGE),
      });
      dispatch({ type: "resize", id: "assistant", width, height });
    },
    [open, windows],
  );
  const vnasOpen = isOpen("assistant");
  const vnasMaximized = windows.some((window) => window.id === "assistant" && window.maximized);
  useEffect(() => {
    const before = chatOpened.current;
    if (vnasOpen || !before) return;
    chatOpened.current = null;
    dispatch({ type: "move", id: "assistant", x: before.x, y: before.y });
    dispatch({ type: "resize", id: "assistant", width: before.width, height: before.height });
    // Maximized in the small window, or maximized before it: back as it was.
    if (vnasMaximized !== before.maximized) dispatch({ type: "maximize", id: "assistant" });
  }, [vnasOpen, vnasMaximized]);
  const [settingsSection, setSettingsSection] = useState<SettingsSection>("overview");
  const storagePlan = useStoragePlan();
  // anchor brings a row of the section into view, as the search does: one there already shows again.
  const openSettings = useCallback(
    (section?: SettingsSection, anchor?: string) => {
      if (section) setSettingsSection(section);
      if (anchor) open("settings", { anchor });
      else open("settings");
    },
    [open],
  );
  // Uploads of an earlier visit by the same account continue; another
  // account's are dropped.
  useEffect(() => {
    transferStore.attach(session.user.id);
  }, [session.user.id]);
  // VNAS opens when this device runs one for this account; without the administrator's key
  // nobody has one, and the desktop says so (ADR 0024).
  const [assistant, setAssistant] = useState({ ready: false, needsKey: false });
  const readAssistant = useCallback(() => {
    void assistantStatus()
      .then((status) =>
        setAssistant({ ready: status.available, needsKey: !status.available && status.reason === "no_key" }),
      )
      .catch(() => setAssistant({ ready: false, needsKey: false }));
  }, []);
  useEffect(() => readAssistant(), [session.user.id, readAssistant]);
  const assistantReady = assistant.ready;
  const assistantNeedsKey = assistant.needsKey;
  // Only the switch reaches the windows' context, not every minute's read.
  const webSearchEnabled = useWebSearch()?.enabled ?? false;
  // 照片打分, 剪辑中心 and 共享工作区 are read when the account signs in and
  // when the page comes back into view: a member an administrator just chose
  // finds them, one taken out loses them, and a failed read is tried again.
  const scoring = useReadOnShow(scoringAccess, noScoring, session.user.id, sameScoring);
  const montage = useReadOnShow(montageEnabled, false, session.user.id);
  const workspace = useReadOnShow(workspaceAccess, null, session.user.id, sameWorkspace);
  const showInDocker = (appID: string) => open("docker", { project: appProject(appID) });
  // A toast of a scheduled task opens its run in VNAS, or the tasks.
  const showTaskRun = (run: AssistantTaskNotice) =>
    open("assistant", { sessionId: run.sessionId, title: `${run.name} · ${whenText(Date.parse(run.ranAt) / 1000)}` });
  const isAdmin = session.user.role === "admin";
  // The windows' content re-renders only when this changes, not when a
  // window is dragged, resized or brought to the front, nor when the host's
  // state is polled: that reaches the settings window alone.
  const context = useMemo<WindowContext>(
    () => ({
      session,
      isAdmin,
      assistantReady,
      assistantNeedsKey,
      openSettings,
      webSearchEnabled,
      scoring,
      montage,
      workspace,
      open,
      intents,
      intentHandled,
      changeUser,
      logout: onLogout,
    }),
    [
      session,
      isAdmin,
      assistantReady,
      assistantNeedsKey,
      openSettings,
      webSearchEnabled,
      scoring,
      montage,
      workspace,
      open,
      intents,
      intentHandled,
      changeUser,
      onLogout,
    ],
  );
  const settingsState = useMemo<SettingsState>(
    () => ({
      host,
      section: settingsSection,
      setSection: setSettingsSection,
      storagePlan,
      assistantKeyChanged: readAssistant,
    }),
    [host, settingsSection, storagePlan, readAssistant],
  );
  const shortcuts = useAppShortcuts();
  const launcherApps = [
    ...desktopApps(context, isOpen),
    // Installed apps open their own web interface; a stopped one shows its
    // containers in Docker to administrators.
    ...shortcuts.map((shortcut) => shortcutApp(shortcut, isAdmin ? () => showInDocker(shortcut.id) : undefined)),
  ];
  const visible = windows.filter((window) => window.open && !window.minimized);
  const focusedWindow = visible.length ? visible.reduce((top, window) => (window.z > top.z ? window : top)) : undefined;
  const focusedID = focusedWindow?.id;
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
    <SharesProvider
      userId={session.user.id}
      active={focusedID === "shares"}
      windowVisible={windows.some((window) => window.id === "shares" && window.open && !window.minimized)}
      onOpen={() => open("shares")}
    >
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

        <SettingsStateContext.Provider value={settingsState}>
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
        </SettingsStateContext.Provider>

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
        <div className={styles.noticeStack}>
          <ShellPart name="task toasts">
            <TaskToasts enabled={assistantReady} onOpen={showTaskRun} />
          </ShellPart>
          <ShellPart name="family hints">
            <Suspense fallback={null}>
              <FamilyNotices onOpen={() => open("family")} />
            </Suspense>
          </ShellPart>
          {montage && (
            <ShellPart name="montage hints">
              <Suspense fallback={null}>
                <MontageNotices onOpen={() => open("montage")} />
              </Suspense>
            </ShellPart>
          )}
          <ShellPart name="todo reminders">
            <Suspense fallback={null}>
              <TodoNotices
                userId={session.user.id}
                shared={isLocalConsole()}
                onWake={isLocalConsole() ? onLocalConsoleDismissed : undefined}
                onOpen={(todoId) => open("todos", { todoId })}
              />
            </Suspense>
          </ShellPart>
        </div>
        <ShellPart name="companion">
          <Companion
            trouble={trouble}
            onOpenVnas={(atCards) =>
              atCards ? open("assistant", { conversationId: atCards.conversation }) : open("assistant")
            }
            onOpenTaskRun={showTaskRun}
            onOpenHealth={() => openSettings("overview", "health")}
            onOpenChat={openChat}
          />
        </ShellPart>
        <ShellPart name="feedback">
          <FeedbackChannel />
        </ShellPart>
      </main>
    </SharesProvider>
  );
}
