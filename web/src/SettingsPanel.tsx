import {
  Activity,
  AtSign,
  Camera,
  ChevronRight,
  CircleArrowUp,
  CircleCheck,
  Clock3,
  Database,
  Ellipsis,
  FolderLock,
  Globe,
  HardDrive,
  History,
  Images,
  KeyRound,
  LayoutDashboard,
  LogOut,
  Monitor,
  Network,
  Palette,
  PenLine,
  ScrollText,
  ShieldCheck,
  SquareTerminal,
  TriangleAlert,
  UserPlus,
  UsersRound,
  UserX,
  type LucideIcon,
} from "lucide-react";
import { FormEvent, ReactNode, useCallback, useEffect, useId, useRef, useState } from "react";

import {
  Health,
  HostState,
  Session,
  User,
  ViewingScope,
  changePassword,
  createMember,
  disableMember,
  listUsers,
  removeAvatar,
  resetMember,
  startViewing,
  updateNickname,
} from "./api";
import {
  nicknameProblem,
  nicknameRuleText,
  passwordProblem,
  passwordRuleText,
  usernameProblem,
  usernameRuleText,
} from "./accountRules";
import { formatBytes } from "./bytes";
import { isLocalConsole } from "./LocalConsoleScreenSaver";
import { LogsSection, type LogsPreset } from "./logs/LogsSection";
import { displayName, fullName, usePeople } from "./people";
import { AvatarDialog } from "./settings/AvatarDialog";
import { HostStateBanner, HostStatePending } from "./settings/hostState";
import { describeFinding } from "./settings/health";
import {
  Avatar,
  Banner,
  Group,
  IconTile,
  PageHeader,
  Pill,
  RefreshButton,
  messageOf,
  type Tone,
} from "./settings/parts";
import { PersonalizationSection } from "./settings/PersonalizationSection";
import {
  StorageSection,
  useVolumes,
  volumeState,
  volumeUsage,
  type StoragePlanFlow,
  type VolumesView,
} from "./settings/StorageSection";
import { TerminalSection } from "./settings/TerminalSection";
import { TaskManagerSection } from "./taskmanager/TaskManagerSection";
import type { SessionStatus } from "./terminal";
import type { HostStateView } from "./useHostState";

export { useStoragePlan, type StoragePlanFlow } from "./settings/StorageSection";

export type SettingsSection =
  | "account"
  | "overview"
  | "storage"
  | "personalization"
  | "users"
  | "logs"
  | "terminal"
  | "task-manager";

const systemSections: Array<{
  id: SettingsSection;
  label: string;
  icon: LucideIcon;
  tone: string;
  adminOnly?: boolean;
}> = [
  { id: "overview", label: "概览", icon: LayoutDashboard, tone: "overview" },
  { id: "task-manager", label: "任务管理器", icon: Activity, tone: "security" },
  { id: "storage", label: "存储", icon: HardDrive, tone: "storage" },
  { id: "personalization", label: "个性化", icon: Palette, tone: "personalize", adminOnly: true },
  { id: "users", label: "用户与权限", icon: UsersRound, tone: "users", adminOnly: true },
  { id: "logs", label: "日志", icon: ScrollText, tone: "logs" },
  { id: "terminal", label: "终端", icon: SquareTerminal, tone: "terminal", adminOnly: true },
];

const plannedSections: Array<{ label: string; icon: LucideIcon; tone: string }> = [
  { label: "网络与访问", icon: Network, tone: "network" },
  { label: "安全", icon: ShieldCheck, tone: "security" },
  { label: "系统更新", icon: CircleArrowUp, tone: "update" },
];

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
  const current =
    (section === "users" || section === "terminal" || section === "personalization") && !isAdmin ? "overview" : section;
  const [editingPassword, setEditingPassword] = useState(false);
  // "登录记录" in My account opens the logs on sign-ins.
  const [logsPreset, setLogsPreset] = useState<LogsPreset>();
  const volumes = useVolumes(storagePlan.plan?.state);
  // Once opened, the terminal stays mounted while other sections are shown,
  // so its shell runs until the settings window closes.
  const [terminalOpened, setTerminalOpened] = useState(current === "terminal");
  const [terminalStatus, setTerminalStatus] = useState<SessionStatus>();
  if (current === "terminal" && !terminalOpened) setTerminalOpened(true);
  const navigate = (next: SettingsSection) => {
    setEditingPassword(false);
    onSectionChange(next);
  };
  return (
    <div className="set-shell">
      <nav className="set-nav" aria-label="设置分区">
        <button
          className={`set-nav-account ${current === "account" ? "active" : ""}`}
          aria-label="我的账号"
          aria-current={current === "account" ? "page" : undefined}
          onClick={() => navigate("account")}
        >
          <Avatar who={user} />
          <span>
            <strong>{displayName(user)}</strong>
            <small>{roleLabel(user.role)} · 我的账号</small>
          </span>
        </button>
        {systemSections
          .filter((link) => isAdmin || !link.adminOnly)
          .map(({ id, label, icon, tone }) => (
            <button
              key={id}
              className={current === id ? "active" : ""}
              aria-current={current === id ? "page" : undefined}
              onClick={() => navigate(id)}
            >
              <IconTile tone={tone} icon={icon} />
              {label}
              {id === "terminal" && current !== "terminal" && terminalStatus?.state === "connected" && (
                <span className="set-nav-live" title="Shell 正在运行" aria-hidden="true" />
              )}
            </button>
          ))}
        <p className="set-nav-label">规划中</p>
        {plannedSections.map(({ label, icon, tone }) => (
          <button key={label} aria-label={`${label}，规划中`} disabled>
            <IconTile tone={tone} icon={icon} />
            {label}
          </button>
        ))}
      </nav>
      <div className={`set-main ${current === "terminal" ? "fill" : ""}`}>
        {current !== "terminal" && (
          <div
            className={`set-page ${current === "task-manager" ? "task-manager-page" : current === "logs" ? "wide" : ""}`}
          >
            {current === "account" && (
              <AccountSection
                session={session}
                editing={editingPassword}
                onEditingChange={setEditingPassword}
                onUserChange={onUserChange}
                onLogout={onLogout}
                onShowSignIns={() => {
                  setLogsPreset({ category: "session", id: Date.now() });
                  navigate("logs");
                }}
              />
            )}
            {current === "overview" && (
              <OverviewSection
                host={host}
                volumes={volumes}
                isAdmin={isAdmin}
                onOpenStorage={() => navigate("storage")}
              />
            )}
            {current === "storage" && (
              <StorageSection host={host} volumes={volumes} isAdmin={isAdmin} flow={storagePlan} />
            )}
            {current === "personalization" && <PersonalizationSection />}
            {current === "users" && (
              <UsersSection
                currentUser={user}
                onChangeOwnPassword={() => {
                  onSectionChange("account");
                  setEditingPassword(true);
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
    </div>
  );
}

// ---------------------------------------------------------------- My account

function AccountSection({
  session,
  editing,
  onEditingChange,
  onUserChange,
  onLogout,
  onShowSignIns,
}: {
  session: Session;
  editing: boolean;
  onEditingChange: (editing: boolean) => void;
  onUserChange: (user: User) => void;
  onLogout: () => void;
  onShowSignIns: () => void;
}) {
  const { user } = session;
  const people = usePeople();
  const [choosingAvatar, setChoosingAvatar] = useState(false);
  const [profileNotice, setProfileNotice] = useState("");
  const [profileError, setProfileError] = useState("");
  const [removing, setRemoving] = useState(false);
  const profileSaved = (next: User, message: string) => {
    onUserChange(next);
    setProfileError("");
    setProfileNotice(message);
  };
  const dropAvatar = async () => {
    setRemoving(true);
    setProfileNotice("");
    try {
      profileSaved(await removeAvatar(), "已移除头像，现在显示名字的第一个字。");
    } catch (caught) {
      setProfileError(messageOf(caught));
    } finally {
      setRemoving(false);
    }
  };
  const resetBy = session.credentialReset ? people.byUsername(session.credentialReset.actorUsername) : undefined;
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [saving, setSaving] = useState(false);
  const open = () => {
    setError("");
    setNotice("");
    onEditingChange(true);
  };
  const cancel = () => {
    setError("");
    onEditingChange(false);
  };
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    const next = String(data.get("newPassword"));
    const problem = passwordProblem(next);
    if (problem) {
      setError(problem);
      return;
    }
    if (next !== String(data.get("confirmPassword"))) {
      setError("两次输入的新密码不一致");
      return;
    }
    setSaving(true);
    try {
      await changePassword(String(data.get("currentPassword")), next);
      setError("");
      setNotice("密码已更新，SMB 凭据已同步。");
      onEditingChange(false);
    } catch (caught) {
      setError(messageOf(caught));
    } finally {
      setSaving(false);
    }
  };
  const localConsole = isLocalConsole();
  return (
    <>
      <PageHeader title="我的账号" subtitle="你的头像、昵称、密码与当前会话" />
      <div className="set-card acct-hero">
        <button className="acct-avatar" aria-label="更换头像" title="更换头像" onClick={() => setChoosingAvatar(true)}>
          <Avatar who={user} size="xl" />
          <span className="acct-avatar-badge" aria-hidden="true">
            <Camera />
          </span>
        </button>
        <div className="acct-hero-text">
          <h3>{displayName(user)}</h3>
          <p>
            <Pill tone={user.role === "admin" ? "accent" : "neutral"}>{roleLabel(user.role)}</Pill>
            <StatusText status={user.status} />
            {user.nickname && <span className="acct-handle">账号名 {user.username}</span>}
          </p>
        </div>
        <button className="set-btn" onClick={onLogout}>
          <LogOut />
          退出登录
        </button>
      </div>
      {profileError && (
        <Banner tone="error" onClose={() => setProfileError("")}>
          {profileError}
        </Banner>
      )}
      {profileNotice && (
        <Banner tone="success" onClose={() => setProfileNotice("")}>
          {profileNotice}
        </Banner>
      )}
      <Group
        title="个人资料"
        footnote="昵称和头像会显示在桌面、日志、问题反馈和相册中，家里的每个账号都能看到。登录和 SMB 共享仍使用账号名。"
      >
        <Row
          icon={<Avatar who={user} />}
          label="头像"
          detail={user.avatarVersion ? "保存时已裁成圆形并压缩为小图" : "未设置，显示名字的第一个字"}
        >
          {user.avatarVersion && (
            <button className="set-btn" disabled={removing} onClick={() => void dropAvatar()}>
              {removing ? "正在移除…" : "移除"}
            </button>
          )}
          <button
            className="set-btn"
            onClick={() => {
              setProfileNotice("");
              setChoosingAvatar(true);
            }}
          >
            {user.avatarVersion ? "更换…" : "设置头像…"}
          </button>
        </Row>
        <NicknameRow
          user={user}
          onEditing={() => setProfileNotice("")}
          onSaved={(next) =>
            profileSaved(
              next,
              next.nickname ? `昵称已改为“${next.nickname}”。` : `已清除昵称，现在显示账号名 ${next.username}。`,
            )
          }
        />
        <Row
          icon={<IconTile tone="handle" icon={AtSign} />}
          label="账号名"
          detail="登录、SMB 共享和设备上的 Linux 账号使用，不能修改"
        >
          <span className="acct-username">{user.username}</span>
        </Row>
      </Group>
      {choosingAvatar && (
        <AvatarDialog
          onClose={() => setChoosingAvatar(false)}
          onSaved={(next) => {
            setChoosingAvatar(false);
            profileSaved(next, "头像已更新。");
          }}
        />
      )}
      <Group title="登录与安全">
        <Row icon={<IconTile tone="key" icon={KeyRound} />} label="密码" detail="网页登录和 SMB 共享使用同一个密码">
          {!editing && (
            <button className="set-btn" onClick={open}>
              修改密码…
            </button>
          )}
        </Row>
        {session.credentialReset && (
          <Row
            icon={<IconTile tone="warning" icon={TriangleAlert} />}
            label="管理员重置过密码"
            detail={`${session.credentialReset.actorUsername ? fullName(resetBy ?? { username: session.credentialReset.actorUsername }) : "管理员"} 于 ${new Date(session.credentialReset.at).toLocaleString("zh-CN")} 重置了这个账号的密码。此后的密码若不是你自己设置的，请当面确认。`}
          />
        )}
        {editing && (
          <form className="acct-form" aria-label="修改密码" autoComplete="off" onSubmit={(event) => void submit(event)}>
            <Field label="当前密码">
              <input
                className="set-input"
                name="currentPassword"
                type="password"
                required
                autoComplete="current-password"
                autoFocus
              />
            </Field>
            <Field label="新密码" hint={passwordRuleText}>
              <input
                className="set-input"
                name="newPassword"
                type="password"
                minLength={12}
                required
                autoComplete="new-password"
              />
            </Field>
            <Field label="确认新密码">
              <input
                className="set-input"
                name="confirmPassword"
                type="password"
                minLength={12}
                required
                autoComplete="new-password"
              />
            </Field>
            {error && <Banner tone="error">{error}</Banner>}
            <div className="acct-form-actions">
              <button type="button" className="set-btn" onClick={cancel}>
                取消
              </button>
              <button className="set-btn primary" disabled={saving}>
                {saving ? "正在保存…" : "保存新密码"}
              </button>
            </div>
          </form>
        )}
        {notice && (
          <div className="set-inline-note" role="status">
            <CircleCheck />
            {notice}
          </div>
        )}
        <Row
          icon={<IconTile tone="session" icon={localConsole ? Monitor : Globe} />}
          label="本次登录"
          detail={
            localConsole
              ? "设备本机屏幕 · 退出登录前一直保持登录"
              : `浏览器 · ${formatDateTime(session.expiresAt)} 前有效，之后需要重新登录`
          }
        />
        <Row
          icon={<IconTile tone="logs" icon={History} />}
          label="登录记录"
          detail="这个账号在哪里登录过、是否有人输错过密码"
        >
          <button className="set-btn" onClick={onShowSignIns}>
            查看
            <ChevronRight />
          </button>
        </Row>
      </Group>
    </>
  );
}

function NicknameRow({
  user,
  onEditing,
  onSaved,
}: {
  user: User;
  onEditing: () => void;
  onSaved: (user: User) => void;
}) {
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState("");
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const open = () => {
    setValue(user.nickname ?? "");
    setError("");
    setEditing(true);
    onEditing();
  };
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const problem = nicknameProblem(value);
    if (problem) {
      setError(problem);
      return;
    }
    setSaving(true);
    try {
      const next = await updateNickname(value.trim());
      setEditing(false);
      setError("");
      onSaved(next);
    } catch (caught) {
      setError(messageOf(caught));
    } finally {
      setSaving(false);
    }
  };
  return (
    <>
      <Row
        icon={<IconTile tone="nickname" icon={PenLine} />}
        label="昵称"
        detail={user.nickname || `未设置，显示账号名 ${user.username}`}
      >
        {!editing && (
          <button className="set-btn" onClick={open}>
            {user.nickname ? "修改…" : "设置昵称…"}
          </button>
        )}
      </Row>
      {editing && (
        <form className="acct-form" aria-label="修改昵称" autoComplete="off" onSubmit={(event) => void submit(event)}>
          <Field label="昵称" hint={`${nicknameRuleText}。留空则显示账号名。`}>
            <input
              className="set-input"
              name="nickname"
              value={value}
              onChange={(event) => setValue(event.target.value)}
              placeholder={user.username}
              autoFocus
              spellCheck={false}
            />
          </Field>
          {error && <Banner tone="error">{error}</Banner>}
          <div className="acct-form-actions">
            <button
              type="button"
              className="set-btn"
              onClick={() => {
                setEditing(false);
                setError("");
              }}
            >
              取消
            </button>
            <button className="set-btn primary" disabled={saving}>
              {saving ? "正在保存…" : "保存昵称"}
            </button>
          </div>
        </form>
      )}
    </>
  );
}

// ------------------------------------------------------------------ Overview

function OverviewSection({
  host,
  volumes,
  isAdmin,
  onOpenStorage,
}: {
  host: HostStateView;
  volumes: VolumesView;
  isAdmin: boolean;
  onOpenStorage: () => void;
}) {
  const { snapshot } = host;
  const header = (
    <PageHeader
      title="概览"
      subtitle="这台 A-NAS 的运行状态"
      action={
        snapshot && <RefreshButton label="刷新设备状态" busy={host.refreshing} onClick={() => void host.refresh()} />
      }
    />
  );
  if (!snapshot)
    return (
      <>
        {header}
        <HostStatePending host={host} />
      </>
    );
  const totalCapacity = snapshot.disks.reduce((sum, disk) => sum + disk.capacityBytes, 0);
  const volume = volumes.items?.[0];
  const usage = volume ? volumeUsage(volume) : undefined;
  const health = healthPresentation(snapshot.system.health);
  return (
    <>
      {header}
      <HostStateBanner host={host} snapshot={snapshot} />
      <div className="ov-hero">
        <DeviceArt />
        <div>
          <h3>{snapshot.system.hostname}</h3>
          <p>A-NAS 家庭存储</p>
          <div className="ov-hero-pills">
            <Pill tone={health.tone}>
              <span className="set-dot" />
              {health.label}
            </Pill>
            <Pill tone={snapshot.dataSource === "live" ? "success" : "warning"}>
              {snapshot.dataSource === "live" ? "实时主机" : "模拟数据"}
            </Pill>
          </div>
        </div>
      </div>
      <div className="ov-stats">
        <div className="ov-stat">
          <span>
            <Clock3 />
            运行时间
          </span>
          <strong>{formatUptime(snapshot.system.uptimeSeconds)}</strong>
          <small>观测于 {formatTime(snapshot.observedAt)}</small>
        </div>
        <div className="ov-stat">
          <span>
            <HardDrive />
            磁盘
          </span>
          <strong>{snapshot.disks.length} 块</strong>
          <small>{snapshot.disks.length ? `总容量 ${formatBytes(totalCapacity)}` : "尚未发现块设备"}</small>
        </div>
        <button className="ov-stat" onClick={onOpenStorage} aria-label="查看存储">
          <span>
            <Database />
            数据卷
            <ChevronRight className="ov-stat-go" />
          </span>
          {volumes.items === undefined ? (
            <>
              <strong>—</strong>
              <small>正在读取</small>
            </>
          ) : !volume ? (
            <>
              <strong>未初始化</strong>
              <small>{isAdmin ? "前往存储完成初始化" : "等待管理员初始化"}</small>
            </>
          ) : usage?.known ? (
            <>
              <strong>已用 {usage.percent}%</strong>
              <small>可用 {formatBytes(usage.available)}</small>
              <span className="ov-stat-bar">
                <i style={{ width: `${usage.percent}%` }} />
              </span>
            </>
          ) : (
            <>
              <strong>{volumeState(volume.state).label}</strong>
              <small>已用空间未知</small>
            </>
          )}
        </button>
      </div>
      <HealthChecks snapshot={snapshot} onOpenStorage={onOpenStorage} />
      <Group title="关于本机">
        <InfoRow label="产品版本" value={snapshot.productVersion} />
        <InfoRow
          label="操作系统"
          value={`${snapshot.system.operatingSystem.name} ${snapshot.system.operatingSystem.version}`}
        />
        <InfoRow label="系统架构" value={snapshot.system.architecture} />
        <InfoRow label="设备标识" value={<span className="set-mono">{snapshot.system.id}</span>} />
        <InfoRow label="最近观测" value={formatTime(snapshot.observedAt)} />
      </Group>
    </>
  );
}

// HealthChecks says why the system is rated as it is: each finding with what
// to do about it, or what was checked when nothing is wrong
// (docs/specs/system-health.md).
function HealthChecks({ snapshot, onOpenStorage }: { snapshot: HostState; onOpenStorage: () => void }) {
  const findings = snapshot.system.healthFindings;
  if (snapshot.system.health === "unknown") {
    return (
      <Group title="健康检查">
        <div className="set-row health-row">
          <IconTile tone="overview" icon={ShieldCheck} size="lg" />
          <div className="set-row-text">
            <span>
              <strong>还没有可以检查的磁盘</strong>
            </span>
            <small>读到磁盘后会检查硬盘健康、温度和数据卷。</small>
          </div>
        </div>
      </Group>
    );
  }
  return (
    <Group title="健康检查" aside={findings.length ? `${findings.length} 项需要处理` : undefined}>
      {findings.length === 0 ? (
        <div className="set-row health-row">
          <IconTile tone="security" icon={ShieldCheck} size="lg" />
          <div className="set-row-text">
            <span>
              <strong>未发现问题</strong>
            </span>
            <small>已检查硬盘健康（SMART）与温度，以及数据卷的挂载、读写、剩余空间和错误记录。</small>
          </div>
        </div>
      ) : (
        <>
          {findings.map((finding, index) => {
            const described = describeFinding(finding, snapshot.disks);
            const critical = finding.severity === "critical";
            return (
              <div key={`${finding.code}:${finding.diskId ?? ""}:${index}`} className="set-row health-row">
                <IconTile tone={critical ? "danger" : "warning"} icon={described.icon} size="lg" />
                <div className="set-row-text">
                  <span>
                    <strong>{described.title}</strong>
                    <Pill tone={critical ? "danger" : "warning"}>{critical ? "严重" : "注意"}</Pill>
                  </span>
                  <small>{described.detail}</small>
                </div>
              </div>
            );
          })}
          <button className="set-row health-go" onClick={onOpenStorage}>
            在存储中查看磁盘与数据卷
            <ChevronRight />
          </button>
        </>
      )}
    </Group>
  );
}

function DeviceArt() {
  const id = useId().replace(/[^\w-]/g, "");
  return (
    <svg className="ov-art" viewBox="0 0 80 80" aria-hidden="true">
      <defs>
        <linearGradient id={`${id}-body`} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0" stopColor="#465468" />
          <stop offset="1" stopColor="#141a24" />
        </linearGradient>
        <linearGradient id={`${id}-bay`} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0" stopColor="#5f6c80" />
          <stop offset="1" stopColor="#2a3240" />
        </linearGradient>
      </defs>
      <ellipse cx="40" cy="74" rx="24" ry="3" fill="rgba(20,30,45,.18)" />
      <rect x="15" y="7" width="50" height="64" rx="10" fill={`url(#${id}-body)`} />
      <rect x="15.5" y="7.5" width="49" height="63" rx="9.5" fill="none" stroke="rgba(255,255,255,.16)" />
      {[15, 36].map((y) => (
        <g key={y}>
          <rect x="22" y={y} width="36" height="17" rx="4" fill={`url(#${id}-bay)`} />
          <path
            d={`M27 ${y + 6}h18M27 ${y + 11}h18`}
            stroke="rgba(255,255,255,.22)"
            strokeWidth="1.6"
            strokeLinecap="round"
          />
          <circle cx="52" cy={y + 8.5} r="2" fill="#4ade80" />
        </g>
      ))}
      <circle cx="40" cy="62" r="2.4" fill="#60a5fa" />
    </svg>
  );
}

// --------------------------------------------------------- Users & permissions

type UserDialog =
  | { kind: "create" }
  | { kind: "reset"; user: User }
  | { kind: "viewing"; user: User; scope: ViewingScope }
  | { kind: "disable"; user: User };

function UsersSection({ currentUser, onChangeOwnPassword }: { currentUser: User; onChangeOwnPassword: () => void }) {
  const [users, setUsers] = useState<User[]>();
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [dialog, setDialog] = useState<UserDialog>();
  const refresh = useCallback(
    () =>
      listUsers()
        .then((items) => {
          setUsers(items);
          setError("");
        })
        .catch((caught: unknown) => {
          setError(messageOf(caught));
        }),
    [],
  );
  useEffect(() => {
    void refresh();
  }, [refresh]);
  const open = (next: UserDialog) => {
    setNotice("");
    setDialog(next);
  };
  const finish = async (message: string) => {
    setDialog(undefined);
    setNotice(message);
    await refresh();
  };
  return (
    <>
      <PageHeader
        title="用户与权限"
        subtitle="管理家庭成员的账号"
        action={
          <button className="set-btn primary" onClick={() => open({ kind: "create" })}>
            <UserPlus />
            添加成员
          </button>
        }
      />
      {error && <Banner tone="error">{error}</Banner>}
      {notice && (
        <Banner tone="success" onClose={() => setNotice("")}>
          {notice}
        </Banner>
      )}
      <Group
        title="账号"
        aside={users ? `${users.length} 个` : undefined}
        overflowVisible
        footnote="管理员默认看不到成员的个人空间和私有图库。需要找回内容时可以开启只读查看：必须填写原因，会写入审计并通知对方，24 小时后自动结束。"
      >
        {users === undefined ? (
          <div className="set-state compact">
            <span className="loader" />
            <p>正在读取账号…</p>
          </div>
        ) : (
          sortUsers(users, currentUser.id).map((user) => {
            const self = user.id === currentUser.id;
            return (
              <div className="set-row" key={user.id}>
                <Avatar who={user} />
                <div className="set-row-text">
                  <span>
                    <strong>{displayName(user)}</strong>
                    {user.nickname && <span className="set-handle">{user.username}</span>}
                    {self && <Pill tone="accent">你</Pill>}
                  </span>
                  <small>
                    <span className={`set-dot status-${user.status}`} />
                    {roleLabel(user.role)} · {statusLabel(user.status)}
                  </small>
                </div>
                <div className="set-row-end">
                  {self ? (
                    <button className="set-btn" onClick={onChangeOwnPassword}>
                      修改我的密码
                    </button>
                  ) : (
                    <RowMenu
                      label={`${fullName(user)} 的更多操作`}
                      items={[
                        { label: "重置密码…", icon: KeyRound, onSelect: () => open({ kind: "reset", user }) },
                        {
                          label: "查看个人空间…",
                          icon: FolderLock,
                          onSelect: () => open({ kind: "viewing", user, scope: "space" }),
                        },
                        {
                          label: "查看私有图库…",
                          icon: Images,
                          onSelect: () => open({ kind: "viewing", user, scope: "library" }),
                        },
                        "separator",
                        {
                          label: user.status === "disabled" ? "账号已禁用" : "禁用账号…",
                          icon: UserX,
                          danger: true,
                          disabled: user.status === "disabled",
                          onSelect: () => open({ kind: "disable", user }),
                        },
                      ]}
                    />
                  )}
                </div>
              </div>
            );
          })
        )}
      </Group>
      {dialog && (
        <UserDialogView
          dialog={dialog}
          onClose={() => setDialog(undefined)}
          onDone={(message) => void finish(message)}
        />
      )}
    </>
  );
}

function UserDialogView({
  dialog,
  onClose,
  onDone,
}: {
  dialog: UserDialog;
  onClose: () => void;
  onDone: (message: string) => void;
}) {
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const run = async (action: () => Promise<string>) => {
    setBusy(true);
    setError("");
    try {
      onDone(await action());
    } catch (caught) {
      setError(messageOf(caught));
      setBusy(false);
    }
  };
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    const value = (name: string) => String(data.get(name));
    const problem =
      dialog.kind === "create"
        ? (usernameProblem(value("username")) ?? passwordProblem(value("password")))
        : dialog.kind === "reset"
          ? passwordProblem(value("password"))
          : undefined;
    if (problem) {
      setError(problem);
      return;
    }
    if (dialog.kind === "create") {
      void run(async () => {
        const member = await createMember(value("username"), value("password"));
        return `已添加成员 ${member.username}。`;
      });
    } else if (dialog.kind === "reset") {
      void run(async () => {
        await resetMember(dialog.user.id, value("password"));
        return `已重置 ${fullName(dialog.user)} 的密码。对方下次登录时必须先设置新密码，SMB 共享在此之前保持停用。`;
      });
    } else if (dialog.kind === "viewing") {
      const { user, scope } = dialog;
      void run(async () => {
        await startViewing(user.id, value("password"), value("reason"), scope);
        return scope === "library"
          ? `已开启对 ${fullName(user)} 私有图库的只读查看，可在相册中选择“只读查看 · ${displayName(user)}”。`
          : `已开启对 ${fullName(user)} 个人空间的只读查看，可在文件管理中选择“只读查看 · ${displayName(user)}”。`;
      });
    } else {
      void run(async () => {
        await disableMember(dialog.user.id);
        return `已禁用 ${fullName(dialog.user)}。`;
      });
    }
  };
  const what = dialog.kind === "viewing" && dialog.scope === "library" ? "私有图库" : "个人空间";
  const content = {
    create: {
      title: "添加成员",
      label: "添加成员",
      icon: UserPlus,
      tone: "accent",
      description: "成员拥有自己的个人空间和私有图库，并可使用 Shared 共享空间。",
      submit: "创建成员",
      danger: false,
    },
    reset: dialog.kind === "reset" && {
      title: `重置 ${fullName(dialog.user)} 的密码`,
      label: `重置 ${fullName(dialog.user)} 的密码`,
      icon: KeyRound,
      tone: "warning",
      description: `重置后 ${displayName(dialog.user)} 下次登录必须先设置新密码，在此之前 SMB 共享保持停用。重置会写入审计，并一直显示在对方的“我的账号”中。`,
      submit: "确认重置",
      danger: false,
    },
    viewing: dialog.kind === "viewing" && {
      title: `只读查看 ${fullName(dialog.user)} 的${what}`,
      label: `查看 ${fullName(dialog.user)} 的${what}`,
      icon: what === "私有图库" ? Images : FolderLock,
      tone: "warning",
      description: `查看他人${what}会写入审计，并在 ${displayName(dialog.user)} 下次登录时通知对方。访问为只读，24 小时后自动结束。`,
      submit: "开始只读查看",
      danger: false,
    },
    disable: dialog.kind === "disable" && {
      title: `禁用 ${fullName(dialog.user)}？`,
      label: `禁用 ${fullName(dialog.user)}`,
      icon: UserX,
      tone: "danger",
      description: `${displayName(dialog.user)} 将无法登录网页和 SMB 共享，已建立的 SMB 连接会立即断开；个人空间中的文件不会被删除。账号禁用后暂时无法重新启用。`,
      submit: "禁用账号",
      danger: true,
    },
  }[dialog.kind] as {
    title: string;
    label: string;
    icon: LucideIcon;
    tone: string;
    description: string;
    submit: string;
    danger: boolean;
  };
  return (
    <Dialog title={content.title} onClose={busy ? undefined : onClose}>
      <form className="set-dialog-form" aria-label={content.label} autoComplete="off" onSubmit={submit}>
        <header>
          <IconTile tone={content.tone} icon={content.icon} size="lg" />
          <h3>{content.title}</h3>
          <p>{content.description}</p>
        </header>
        {dialog.kind === "create" && (
          <>
            <Field label="账号" hint={usernameRuleText}>
              <input
                className="set-input"
                name="username"
                required
                autoFocus
                autoCapitalize="none"
                spellCheck={false}
              />
            </Field>
            <Field label="初始密码" hint={`${passwordRuleText}；成员可在“我的账号”中自行修改`}>
              <input
                className="set-input"
                name="password"
                type="password"
                minLength={12}
                autoComplete="new-password"
                required
              />
            </Field>
          </>
        )}
        {dialog.kind === "reset" && (
          <Field label="新密码" hint={passwordRuleText}>
            <input
              className="set-input"
              name="password"
              type="password"
              minLength={12}
              autoComplete="new-password"
              required
              autoFocus
            />
          </Field>
        )}
        {dialog.kind === "viewing" && (
          <>
            <Field label="查看原因">
              <input
                className="set-input"
                name="reason"
                maxLength={500}
                required
                autoFocus
                placeholder="例如：帮助找回误删的照片"
              />
            </Field>
            <Field label="你的密码" hint="为确认是你本人操作">
              <input className="set-input" name="password" type="password" autoComplete="current-password" required />
            </Field>
          </>
        )}
        {error && <Banner tone="error">{error}</Banner>}
        <footer>
          <button type="button" className="set-btn" onClick={onClose} disabled={busy}>
            取消
          </button>
          <button className={`set-btn ${content.danger ? "danger" : "primary"}`} disabled={busy}>
            {busy ? "正在处理…" : content.submit}
          </button>
        </footer>
      </form>
    </Dialog>
  );
}

type MenuItem =
  | { label: string; icon: LucideIcon; onSelect: () => void; danger?: boolean; disabled?: boolean }
  | "separator";

function RowMenu({ label, items }: { label: string; items: MenuItem[] }) {
  const [open, setOpen] = useState(false);
  const anchor = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const closeOutside = (event: PointerEvent) => {
      if (!anchor.current?.contains(event.target as Node)) setOpen(false);
    };
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape") setOpen(false);
    };
    window.addEventListener("pointerdown", closeOutside);
    window.addEventListener("keydown", closeOnEscape);
    return () => {
      window.removeEventListener("pointerdown", closeOutside);
      window.removeEventListener("keydown", closeOnEscape);
    };
  }, [open]);
  return (
    <div className="set-menu-anchor" ref={anchor}>
      <button
        className={`set-icon-btn bordered ${open ? "open" : ""}`}
        aria-label={label}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
      >
        <Ellipsis />
      </button>
      {open && (
        <div className="set-menu" role="menu" aria-label={label}>
          {items.map((item, index) =>
            item === "separator" ? (
              <hr key={index} />
            ) : (
              <button
                key={item.label}
                role="menuitem"
                className={item.danger ? "danger" : ""}
                disabled={item.disabled}
                onClick={() => {
                  setOpen(false);
                  item.onSelect();
                }}
              >
                <item.icon />
                {item.label}
              </button>
            ),
          )}
        </div>
      )}
    </div>
  );
}

function Dialog({ title, onClose, children }: { title: string; onClose?: () => void; children: ReactNode }) {
  useEffect(() => {
    if (!onClose) return;
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
    };
    window.addEventListener("keydown", closeOnEscape);
    return () => window.removeEventListener("keydown", closeOnEscape);
  }, [onClose]);
  return (
    <div
      className="set-dialog-layer"
      onPointerDown={(event) => {
        if (event.target === event.currentTarget) onClose?.();
      }}
    >
      <div className="set-dialog" role="dialog" aria-modal="true" aria-label={title}>
        {children}
      </div>
    </div>
  );
}

// -------------------------------------------------------------- Shared parts

function Row({
  icon,
  label,
  detail,
  children,
}: {
  icon?: ReactNode;
  label: string;
  detail?: string;
  children?: ReactNode;
}) {
  return (
    <div className="set-row">
      {icon}
      <div className="set-row-text">
        <span>{label}</span>
        {detail && <small>{detail}</small>}
      </div>
      {children && <div className="set-row-end">{children}</div>}
    </div>
  );
}

function InfoRow({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div className="set-row info">
      <span>{label}</span>
      <span className="set-value">{value}</span>
    </div>
  );
}

function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <div className="set-field">
      <label>
        <span>{label}</span>
        {children}
      </label>
      {hint && <small>{hint}</small>}
    </div>
  );
}

function StatusText({ status }: { status: User["status"] }) {
  return (
    <span className="set-status">
      <span className={`set-dot status-${status}`} />
      {statusLabel(status)}
    </span>
  );
}

// ------------------------------------------------------------------ Helpers

function healthPresentation(health: Health): { label: string; tone: Tone } {
  return {
    healthy: { label: "运行正常", tone: "success" as Tone },
    warning: { label: "需要注意", tone: "warning" as Tone },
    critical: { label: "严重故障", tone: "danger" as Tone },
    unknown: { label: "健康未评估", tone: "neutral" as Tone },
  }[health];
}

// The signed-in account comes first, then administrators, then members by name.
function sortUsers(users: User[], selfID: string): User[] {
  const rank = (user: User) => (user.id === selfID ? 0 : user.role === "admin" ? 1 : 2);
  return [...users].sort(
    (left, right) => rank(left) - rank(right) || displayName(left).localeCompare(displayName(right), "zh-CN"),
  );
}

function roleLabel(role: User["role"]): string {
  return role === "admin" ? "管理员" : "成员";
}

function statusLabel(status: User["status"]): string {
  return { active: "正常", pending: "创建中", disabled: "已禁用", error: "凭据异常" }[status];
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
  return Number.isNaN(date.getTime())
    ? "—"
    : new Intl.DateTimeFormat("zh-CN", {
        month: "2-digit",
        day: "2-digit",
        hour: "2-digit",
        minute: "2-digit",
        second: "2-digit",
      }).format(date);
}

function formatDateTime(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime())
    ? "—"
    : new Intl.DateTimeFormat("zh-CN", { month: "long", day: "numeric", hour: "2-digit", minute: "2-digit" }).format(
        date,
      );
}
