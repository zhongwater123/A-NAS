import {
  Activity,
  ChevronRight,
  Globe,
  HardDrive,
  LayoutDashboard,
  type LucideIcon,
  Palette,
  ScrollText,
  Search,
  SquareTerminal,
  UsersRound,
  X,
} from "lucide-react";
import {
  type KeyboardEvent,
  type ReactNode,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";

import type { Session, User } from "../api.generated";
import type { HostStateView } from "../desktop/useHostState";
import type { PinyinMatch } from "../music/library";
import { loadPinyin } from "../music/pinyin";
import { displayName } from "../people/people";
import { useToasts } from "../ui/feedback";
import { AccountSection } from "./AccountSection";
import { roleLabel } from "./labels";
import { type LogsPreset, LogsSection } from "./logs/LogsSection";
import { OverviewSection } from "./OverviewSection";
import { PersonalizationSection } from "./PersonalizationSection";
import { Avatar, type SettingsKit, SettingsKitContext } from "./parts";
import { StorageSection, useVolumes } from "./StorageSection";
import { type SearchEntry, searchEntries, searchSettings } from "./search";
// After the sections' stylesheets, which these rules follow.
import styles from "./settings.module.css";
import type { StoragePlanFlow } from "./storagePlan";
import { TerminalSection } from "./TerminalSection";
import { TaskManagerSection } from "./taskmanager/TaskManagerSection";
import type { SessionStatus } from "./terminal";
import { UsersSection } from "./UsersSection";
import { WebSearchSection } from "./WebSearchSection";

export { type StoragePlanFlow, useStoragePlan } from "./storagePlan";

export type SettingsSection =
  | "account"
  | "overview"
  | "storage"
  | "personalization"
  | "users"
  | "logs"
  | "terminal"
  | "task-manager"
  | "web-search";

interface SectionLink {
  id: SettingsSection;
  label: string;
  icon: LucideIcon;
  adminOnly?: boolean;
}

// The sidebar's sections, in groups: the device, the household, diagnostics.
const sectionGroups: SectionLink[][] = [
  [
    { id: "overview", label: "概览", icon: LayoutDashboard },
    { id: "storage", label: "存储", icon: HardDrive },
    { id: "task-manager", label: "任务管理器", icon: Activity },
  ],
  [
    { id: "users", label: "用户与权限", icon: UsersRound, adminOnly: true },
    { id: "personalization", label: "个性化", icon: Palette, adminOnly: true },
    { id: "web-search", label: "网页搜索", icon: Globe, adminOnly: true },
  ],
  [
    { id: "logs", label: "日志", icon: ScrollText },
    { id: "terminal", label: "终端", icon: SquareTerminal, adminOnly: true },
  ],
];

const sectionLinks = sectionGroups.flat();
const sectionLabel = (section: SettingsSection) =>
  section === "account" ? "我的账号" : (sectionLinks.find((link) => link.id === section)?.label ?? "");

// A row the search led to: found again after the section has rendered it.
interface Target {
  anchor: string;
  id: number;
}

export function SettingsPanel({
  session,
  host,
  section,
  onSectionChange,
  storagePlan,
  onUserChange,
  onLogout,
}: {
  session: Session;
  host: HostStateView;
  section: SettingsSection;
  onSectionChange: (section: SettingsSection) => void;
  storagePlan: StoragePlanFlow;
  // onUserChange receives the account after its owner changed the nickname or avatar.
  onUserChange: (user: User) => void;
  onLogout: () => void;
}) {
  const { user } = session;
  const isAdmin = user.role === "admin";
  const current = !isAdmin && sectionLinks.some((link) => link.id === section && link.adminOnly) ? "overview" : section;
  // 用户与权限 can open My account with its password dialog showing; the
  // request is used up once the dialog is shown.
  const [passwordRequested, setPasswordRequested] = useState(false);
  // "登录记录" in My account opens the logs on sign-ins.
  const [logsPreset, setLogsPreset] = useState<LogsPreset>();
  const volumes = useVolumes(storagePlan.plan?.state);
  // Once opened, the terminal stays mounted while other sections are shown,
  // so its shell runs until the settings window closes.
  const [terminalOpened, setTerminalOpened] = useState(current === "terminal");
  const [terminalStatus, setTerminalStatus] = useState<SessionStatus>();
  if (current === "terminal" && !terminalOpened) setTerminalOpened(true);

  // A narrow window shows one pane at a time: the section list, or a section
  // with a way back. A section opened from outside shows the section.
  const [pane, setPane] = useState<"sections" | "page">("page");
  const [seenSection, setSeenSection] = useState(section);
  if (section !== seenSection) {
    setSeenSection(section);
    setPane("page");
  }
  const go = (next: SettingsSection) => {
    onSectionChange(next);
    setPane("page");
  };

  const shell = useRef<HTMLDivElement>(null);
  const main = useRef<HTMLDivElement>(null);
  const { toast, element: toasts } = useToasts();
  const kit = useMemo<SettingsKit>(
    () => ({ container: shell, toast, showSections: () => setPane("sections") }),
    [toast],
  );

  // Each section starts at its top.
  useEffect(() => {
    if (!main.current) return;
    main.current.scrollTop = 0;
    main.current.removeAttribute("data-scrolled");
  }, [current]);

  // The search leads to a row: wait for the section to render it, bring it
  // into view and mark it for a moment.
  const [target, setTarget] = useState<Target>();
  useEffect(() => {
    if (!target) return;
    let tries = 0;
    let marked: Element | null = null;
    let clear = 0;
    const find = window.setInterval(() => {
      const found = main.current?.querySelector(`[data-setting="${target.anchor}"]`);
      if (!found && ++tries < 20) return;
      window.clearInterval(find);
      if (!found) return;
      marked = found;
      // Scrolls the page alone: scrollIntoView would also move the desktop behind the window.
      const view = main.current?.getBoundingClientRect();
      const box = found.getBoundingClientRect();
      if (view)
        main.current?.scrollBy({ top: box.top - view.top - (view.height - box.height) / 2, behavior: "smooth" });
      found.setAttribute("data-found", "");
      clear = window.setTimeout(() => found.removeAttribute("data-found"), 2000);
    }, 80);
    return () => {
      window.clearInterval(find);
      window.clearTimeout(clear);
      marked?.removeAttribute("data-found");
    };
  }, [target]);

  const open = (entry: SearchEntry) => {
    go(entry.section);
    if (entry.anchor) setTarget({ anchor: entry.anchor, id: Date.now() });
  };

  const links = sectionGroups
    .map((group) => group.filter((link) => isAdmin || !link.adminOnly))
    .filter((group) => group.length > 0);

  return (
    <SettingsKitContext value={kit}>
      <div className={styles.setShell} ref={shell} data-pane={pane}>
        <div className={styles.setSide}>
          <SettingsSearch isAdmin={isAdmin} onOpen={open}>
            <SectionNav
              user={user}
              groups={links}
              current={current}
              terminalRunning={current !== "terminal" && terminalStatus?.state === "connected"}
              onGo={go}
            />
          </SettingsSearch>
        </div>
        <div
          className={`${styles.setMain} ${current === "terminal" ? styles.fill : ""}`}
          data-settings-main=""
          ref={main}
          onScroll={(event) => event.currentTarget.toggleAttribute("data-scrolled", event.currentTarget.scrollTop > 6)}
        >
          {current !== "terminal" && (
            <div
              key={current}
              className={`${styles.setPage} ${current === "task-manager" ? styles.taskManagerPage : current === "logs" ? styles.wide : ""}`}
            >
              {current === "account" && (
                <AccountSection
                  session={session}
                  openPassword={passwordRequested}
                  onPasswordShown={() => setPasswordRequested(false)}
                  onUserChange={onUserChange}
                  onLogout={onLogout}
                  onShowSignIns={() => {
                    setLogsPreset({ category: "session", id: Date.now() });
                    go("logs");
                  }}
                />
              )}
              {current === "overview" && (
                <OverviewSection host={host} volumes={volumes} isAdmin={isAdmin} onOpenStorage={() => go("storage")} />
              )}
              {current === "storage" && (
                <StorageSection host={host} volumes={volumes} isAdmin={isAdmin} flow={storagePlan} />
              )}
              {current === "personalization" && <PersonalizationSection />}
              {current === "web-search" && <WebSearchSection />}
              {current === "users" && (
                <UsersSection
                  currentUser={user}
                  onChangeOwnPassword={() => {
                    setPasswordRequested(true);
                    go("account");
                  }}
                />
              )}
              {current === "task-manager" && <TaskManagerSection />}
              {current === "logs" && <LogsSection session={session} preset={logsPreset} />}
            </div>
          )}
          {isAdmin && terminalOpened && (
            <TerminalSection
              username={user.username}
              hostname={host.snapshot?.system.hostname}
              active={current === "terminal"}
              onStatus={setTerminalStatus}
            />
          )}
        </div>
        {toasts}
      </div>
    </SettingsKitContext>
  );
}

// SectionNav is the sidebar's list: the account, then the sections in groups.
// One raised pill marks the current section and glides to the next one; it is
// placed from the current button's own box, so it follows whatever the list
// holds for this role.
function SectionNav({
  user,
  groups,
  current,
  terminalRunning,
  onGo,
}: {
  user: User;
  groups: SectionLink[][];
  current: SettingsSection;
  terminalRunning: boolean;
  onGo: (section: SettingsSection) => void;
}) {
  const nav = useRef<HTMLElement>(null);
  const [pill, setPill] = useState<{ top: number; height: number }>();
  const count = groups.reduce((sum, group) => sum + group.length, 0);
  useLayoutEffect(() => {
    const active = nav.current?.querySelector<HTMLElement>('[aria-current="page"]');
    setPill(active ? { top: active.offsetTop, height: active.offsetHeight } : undefined);
  }, [current, count]);
  return (
    <nav className={styles.setNav} aria-label="设置分区" ref={nav}>
      {pill && (
        <span
          className={styles.setNavPill}
          style={{ height: pill.height, transform: `translateY(${pill.top}px)` }}
          aria-hidden="true"
        />
      )}
      <button
        className={`${styles.setNavAccount} ${current === "account" ? styles.active : ""}`}
        aria-label="我的账号"
        aria-current={current === "account" ? "page" : undefined}
        onClick={() => onGo("account")}
      >
        <Avatar who={user} size="lg" />
        <span>
          <strong>{displayName(user)}</strong>
          <small>{roleLabel(user.role)}</small>
        </span>
        <ChevronRight className={styles.setNavGo} aria-hidden="true" />
      </button>
      {groups.map((group) => (
        <div key={group[0].id} className={styles.setNavGroup}>
          {group.map(({ id, label, icon: Icon }) => (
            <button
              key={id}
              className={current === id ? styles.active : ""}
              aria-current={current === id ? "page" : undefined}
              onClick={() => onGo(id)}
            >
              <Icon className={styles.setNavIcon} aria-hidden="true" />
              <span>{label}</span>
              {id === "terminal" && terminalRunning && (
                <span className={styles.setNavLive} title="Shell 正在运行" aria-hidden="true" />
              )}
              <ChevronRight className={styles.setNavGo} aria-hidden="true" />
            </button>
          ))}
        </div>
      ))}
    </nav>
  );
}

// SettingsSearch is the field at the top of the sidebar. While it holds a
// query its results take the place of the section list (its children); the
// arrow keys move through them and Enter opens one.
function SettingsSearch({
  isAdmin,
  onOpen,
  children,
}: {
  isAdmin: boolean;
  onOpen: (entry: SearchEntry) => void;
  children: ReactNode;
}) {
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  // Pinyin matching loads the first time the field is used.
  const [pinyin, setPinyin] = useState<PinyinMatch>();
  const [wanted, setWanted] = useState(false);
  useEffect(() => {
    if (!wanted || pinyin) return;
    let live = true;
    loadPinyin().then(
      (match) => {
        if (live) setPinyin(() => match);
      },
      () => undefined,
    );
    return () => {
      live = false;
    };
  }, [wanted, pinyin]);
  const entries = useMemo(() => searchEntries.filter((entry) => isAdmin || !entry.adminOnly), [isAdmin]);
  const results = useMemo(() => searchSettings(entries, query, sectionLabel, pinyin), [entries, query, pinyin]);
  const listID = useId();
  const searching = query.trim() !== "";
  const choose = (entry: SearchEntry) => {
    setQuery("");
    setActive(0);
    onOpen(entry);
  };
  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === "Escape" && query) {
      event.stopPropagation();
      setQuery("");
    } else if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      if (results.length)
        setActive((value) => (value + (event.key === "ArrowDown" ? 1 : results.length - 1)) % results.length);
    } else if (event.key === "Enter" && results[active]) {
      event.preventDefault();
      choose(results[active]);
    }
  };
  return (
    <>
      <div className={styles.setSearch}>
        <Search aria-hidden="true" />
        <input
          type="text"
          role="combobox"
          aria-label="搜索设置"
          aria-expanded={searching}
          aria-controls={listID}
          aria-autocomplete="list"
          aria-activedescendant={searching && results[active] ? `${listID}-${active}` : undefined}
          placeholder="搜索"
          value={query}
          spellCheck={false}
          onFocus={() => setWanted(true)}
          onChange={(event) => {
            setQuery(event.target.value);
            setActive(0);
          }}
          onKeyDown={onKeyDown}
        />
        {query && (
          <button aria-label="清除搜索" onClick={() => setQuery("")}>
            <X />
          </button>
        )}
      </div>
      {searching ? (
        <div className={styles.setResults} id={listID} role="listbox" aria-label="搜索结果">
          {results.map((entry, index) => (
            <button
              key={`${entry.section}:${entry.title}`}
              id={`${listID}-${index}`}
              role="option"
              aria-selected={index === active}
              className={index === active ? styles.active : ""}
              onPointerMove={() => setActive(index)}
              onClick={() => choose(entry)}
            >
              <span>{entry.title}</span>
              <small>{sectionLabel(entry.section)}</small>
            </button>
          ))}
          {results.length === 0 && <p>没有找到“{query.trim()}”</p>}
        </div>
      ) : (
        children
      )}
    </>
  );
}
