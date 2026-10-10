import { Clock3, Grid2X2, Home, LogOut, Monitor, Search, Settings, UserRound } from "lucide-react";
import { KeyboardEvent, ReactNode, RefObject, useEffect, useId, useRef, useState } from "react";

import type { Session } from "./api";
import type { DesktopApp } from "./DesktopGrid";
import { avatarTone } from "./SettingsPanel";
import { useDesktopOrder } from "./useDesktopOrder";

// "windows": some app window is visible; "revealed": the rail hid them and can
// bring them back; "clear": the desktop is already in view.
export type DesktopView = "windows" | "revealed" | "clear";

type Popover = "launcher" | "account";

interface SystemRailProps {
  apps: DesktopApp[];
  session: Session;
  desktopView: DesktopView;
  settingsFocused: boolean;
  onToggleDesktop: () => void;
  onOpenSettings: (section?: "account") => void;
  onLogout: () => void;
}

export function SystemRail({ apps, session, desktopView, settingsFocused, onToggleDesktop, onOpenSettings, onLogout }: SystemRailProps) {
  const [popover, setPopover] = useState<Popover>();
  const launcherButton = useRef<HTMLButtonElement>(null);
  const accountButton = useRef<HTMLButtonElement>(null);
  const launcherID = useId();
  const accountID = useId();
  const { username, role } = session.user;
  const roleLabel = role === "admin" ? "管理员" : "成员";
  const toggle = (next: Popover) => setPopover((current) => current === next ? undefined : next);
  const close = (returnFocus: boolean) => {
    const trigger = popover === "launcher" ? launcherButton : accountButton;
    setPopover(undefined);
    if (returnFocus) trigger.current?.focus();
  };

  return <>
    <aside className="system-rail" aria-label="系统快捷栏">
      <div className="rail-group">
        <RailButton
          label="显示桌面"
          tip={desktopView === "revealed" ? "恢复窗口" : "显示桌面"}
          on={desktopView !== "windows"}
          pressed={desktopView !== "windows"}
          onClick={() => { setPopover(undefined); onToggleDesktop(); }}
        ><Home /></RailButton>
        <RailButton
          ref={launcherButton}
          label="全部应用"
          popup={{ kind: "dialog", id: launcherID, expanded: popover === "launcher" }}
          onClick={() => toggle("launcher")}
        ><Grid2X2 /></RailButton>
      </div>
      <div className="rail-spacer" />
      <div className="rail-group">
        <RailButton label="打开设置" tip="设置" on={settingsFocused} onClick={() => { setPopover(undefined); onOpenSettings(); }}><Settings /></RailButton>
        <RailButton
          ref={accountButton}
          className="rail-account"
          label={`账号（${username}）`}
          tip={`${username} · ${roleLabel}`}
          popup={{ kind: "menu", id: accountID, expanded: popover === "account" }}
          onClick={() => toggle("account")}
        ><span className={`rail-face ${avatarTone(username)}`} aria-hidden="true">{initial(username)}</span></RailButton>
      </div>
    </aside>

    {popover === "launcher" && (
      <DismissableLayer id={launcherID} className="launcher" role="dialog" label="全部应用" trigger={launcherButton} onDismiss={close}>
        <Launcher apps={apps} onLaunch={(app) => { setPopover(undefined); app.onClick?.(); }} />
      </DismissableLayer>
    )}
    {popover === "account" && (
      <DismissableLayer id={accountID} className="account-popover" label="账号" trigger={accountButton} onDismiss={close}>
        <AccountMenu
          username={username}
          roleLabel={roleLabel}
          session={describeSession(session.expiresAt)}
          onAccount={() => { setPopover(undefined); onOpenSettings("account"); }}
          onLogout={() => { setPopover(undefined); onLogout(); }}
        />
      </DismissableLayer>
    )}
  </>;
}

interface RailButtonProps {
  label: string;
  tip?: string;
  on?: boolean;
  pressed?: boolean;
  popup?: { kind: "dialog" | "menu"; id: string; expanded: boolean };
  className?: string;
  ref?: RefObject<HTMLButtonElement | null>;
  onClick: () => void;
  children: ReactNode;
}

function RailButton({ label, tip = label, on, pressed, popup, className = "", ref, onClick, children }: RailButtonProps) {
  return (
    <button
      ref={ref}
      className={`rail-button ${on ? "on" : ""} ${className}`}
      aria-label={label}
      aria-pressed={pressed}
      aria-haspopup={popup?.kind}
      aria-expanded={popup?.expanded}
      aria-controls={popup?.expanded ? popup.id : undefined}
      onClick={onClick}
    >
      {children}
      <span className="rail-tip" aria-hidden="true">{tip}</span>
    </button>
  );
}

// DismissableLayer closes its popover on Escape, on a pointer press outside it
// and its trigger, and when keyboard focus leaves both.
function DismissableLayer({ id, className, role, label, trigger, onDismiss, children }: {
  id: string;
  className: string;
  role?: "dialog";
  label: string;
  trigger: RefObject<HTMLButtonElement | null>;
  onDismiss: (returnFocus: boolean) => void;
  children: ReactNode;
}) {
  const layer = useRef<HTMLElement>(null);
  const dismiss = useRef(onDismiss);
  dismiss.current = onDismiss;

  useEffect(() => {
    const inside = (target: EventTarget | null) => target instanceof Node && Boolean(layer.current?.contains(target) || trigger.current?.contains(target));
    const pointer = (event: PointerEvent) => { if (!inside(event.target)) dismiss.current(false); };
    const key = (event: globalThis.KeyboardEvent) => { if (event.key === "Escape") dismiss.current(true); };
    window.addEventListener("pointerdown", pointer);
    window.addEventListener("keydown", key);
    return () => { window.removeEventListener("pointerdown", pointer); window.removeEventListener("keydown", key); };
  }, [trigger]);

  return (
    <section
      ref={layer}
      id={id}
      className={className}
      role={role}
      aria-label={label}
      onBlur={(event) => {
        const next = event.relatedTarget;
        if (next instanceof Node && !layer.current?.contains(next) && !trigger.current?.contains(next)) dismiss.current(false);
      }}
    >
      {children}
    </section>
  );
}

function Launcher({ apps, onLaunch }: { apps: DesktopApp[]; onLaunch: (app: DesktopApp) => void }) {
  // Follow the icon order the user arranged on the desktop.
  const { order } = useDesktopOrder(apps.map((app) => app.id));
  const [query, setQuery] = useState("");
  const search = useRef<HTMLInputElement>(null);
  const grid = useRef<HTMLDivElement>(null);
  const appsByID = new Map(apps.map((app) => [app.id, app]));
  const terms = query.trim().toLocaleLowerCase("zh-CN").split(/\s+/).filter(Boolean);
  const matches = order
    .map((id) => appsByID.get(id))
    .filter((app): app is DesktopApp => Boolean(app))
    .filter((app) => terms.every((term) => `${app.label} ${app.id} ${app.keywords ?? ""}`.toLocaleLowerCase("zh-CN").includes(term)));
  const available = matches.filter((app) => !app.disabled);
  const planned = matches.filter((app) => app.disabled);

  useEffect(() => { search.current?.focus(); }, []);

  const tiles = () => Array.from(grid.current?.querySelectorAll<HTMLButtonElement>(".launcher-tile") ?? []);
  const columns = () => grid.current ? Math.max(1, getComputedStyle(grid.current).gridTemplateColumns.split(" ").filter(Boolean).length) : 4;
  const searchKey = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === "Enter" && available[0]) { event.preventDefault(); onLaunch(available[0]); }
    else if (event.key === "ArrowDown") { event.preventDefault(); tiles()[0]?.focus(); }
  };
  const gridKey = (event: KeyboardEvent<HTMLDivElement>) => {
    const all = tiles();
    const index = all.indexOf(document.activeElement as HTMLButtonElement);
    if (index < 0) return;
    const step = { ArrowLeft: -1, ArrowRight: 1, ArrowUp: -columns(), ArrowDown: columns(), Home: -index, End: all.length - 1 - index }[event.key];
    if (step === undefined) return;
    event.preventDefault();
    const target = index + step;
    if (target < 0) search.current?.focus();
    else all[Math.min(all.length - 1, target)]?.focus();
  };

  return <>
    <label className="launcher-search">
      <Search aria-hidden="true" />
      <input
        ref={search}
        type="search"
        aria-label="搜索应用"
        placeholder="搜索应用"
        autoComplete="off"
        spellCheck={false}
        value={query}
        onChange={(event) => setQuery(event.target.value)}
        onKeyDown={searchKey}
      />
    </label>
    <div className="launcher-body">
      {available.length > 0 && <>
        <h2 className="launcher-heading">{terms.length ? "搜索结果" : "应用"}<small>{available.length}</small></h2>
        <div ref={grid} className="launcher-grid" onKeyDown={gridKey}>
          {available.map((app, index) => (
            <button
              key={app.id}
              className={`launcher-tile ${app.active ? "running" : ""} ${terms.length && index === 0 ? "suggested" : ""}`}
              aria-label={app.ariaLabel}
              onClick={() => onLaunch(app)}
            >
              <span className={`launcher-icon icon-${app.tone}`} aria-hidden="true">{app.icon}</span>
              <span className="launcher-label">{app.label}</span>
            </button>
          ))}
        </div>
      </>}
      {planned.length > 0 && <>
        <h2 className="launcher-heading">规划中</h2>
        <ul className="launcher-planned" aria-label="规划中的应用">
          {planned.map((app) => (
            <li key={app.id}><span className={`launcher-icon icon-${app.tone}`} aria-hidden="true">{app.icon}</span>{app.label}</li>
          ))}
        </ul>
      </>}
      {!matches.length && <p className="launcher-empty" role="status">没有名为“{query.trim()}”的应用</p>}
    </div>
    <footer className="launcher-footer" aria-hidden="true">
      <span><kbd>↑</kbd><kbd>↓</kbd><kbd>←</kbd><kbd>→</kbd>选择</span>
      <span><kbd>Enter</kbd>打开</span>
      <span><kbd>Esc</kbd>关闭</span>
    </footer>
  </>;
}

function AccountMenu({ username, roleLabel, session, onAccount, onLogout }: {
  username: string;
  roleLabel: string;
  session: SessionDescription;
  onAccount: () => void;
  onLogout: () => void;
}) {
  const menu = useRef<HTMLDivElement>(null);
  useEffect(() => { menu.current?.querySelector<HTMLButtonElement>("[role=menuitem]")?.focus(); }, []);
  const moveFocus = (event: KeyboardEvent<HTMLDivElement>) => {
    const items = Array.from(menu.current?.querySelectorAll<HTMLButtonElement>("[role=menuitem]") ?? []);
    const index = items.indexOf(document.activeElement as HTMLButtonElement);
    const next = { ArrowDown: index + 1, ArrowUp: index - 1, Home: 0, End: items.length - 1 }[event.key];
    if (next === undefined) return;
    event.preventDefault();
    items[(next + items.length) % items.length]?.focus();
  };

  return <>
    <div className="account-card">
      <span className={`account-face ${avatarTone(username)}`} aria-hidden="true">{initial(username)}</span>
      <div className="account-who">
        <strong>{username}</strong>
        <span className="account-role">{roleLabel}</span>
      </div>
    </div>
    <p className="account-session">{session.localConsole ? <Monitor aria-hidden="true" /> : <Clock3 aria-hidden="true" />}{session.text}</p>
    <div ref={menu} className="account-menu" role="menu" aria-label="账号操作" onKeyDown={moveFocus}>
      <button role="menuitem" className="account-item" onClick={onAccount}><UserRound aria-hidden="true" />我的账号<small>密码与 SMB</small></button>
      <div className="account-separator" role="separator" />
      <button role="menuitem" className="account-item danger" onClick={onLogout}><LogOut aria-hidden="true" />退出登录</button>
    </div>
  </>;
}

function initial(username: string): string {
  return [...username][0]?.toUpperCase() ?? "?";
}

interface SessionDescription { localConsole: boolean; text: string }

// Local console sessions are stored with a far-future expiry and last until
// sign-out; browser sessions expire after 12 hours.
export function describeSession(expiresAt: string, now = new Date()): SessionDescription {
  const expiry = new Date(expiresAt);
  if (Number.isNaN(expiry.getTime())) return { localConsole: false, text: "浏览器会话" };
  if (expiry.getUTCFullYear() >= 9999) return { localConsole: true, text: "本机屏幕 · 退出前保持登录" };
  const day = expiry.toDateString() === now.toDateString() ? "今天" : `${expiry.getMonth() + 1}月${expiry.getDate()}日`;
  const time = new Intl.DateTimeFormat("zh-CN", { hour: "2-digit", minute: "2-digit", hourCycle: "h23" }).format(expiry);
  return { localConsole: false, text: `浏览器登录 · 有效至 ${day} ${time}` };
}
