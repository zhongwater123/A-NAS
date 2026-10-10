import {
  ChevronRight, CircleAlert, CircleArrowUp, CircleCheck, Clock3, Database, Ellipsis, FolderLock, Globe, HardDrive, Images, KeyRound,
  LayoutDashboard, LogOut, Monitor, Network, RefreshCw, ShieldCheck, TriangleAlert, UserPlus, UsersRound, UserX, X, type LucideIcon,
} from "lucide-react";
import { FormEvent, ReactNode, useCallback, useEffect, useId, useRef, useState } from "react";

import {
  Health, HostState, Session, StoragePlan, User, ViewingScope, Volume,
  changePassword, confirmStoragePlan, createMember, createStoragePlan, disableMember, executeStoragePlan, listUsers, listVolumes, resetMember, startViewing,
} from "./api";
import { isLocalConsole } from "./LocalConsoleScreenSaver";
import type { HostStateView } from "./useHostState";

export type SettingsSection = "account" | "overview" | "storage" | "users";

type PlanStep = "plan" | "confirm" | "execute";
type Tone = "neutral" | "accent" | "success" | "warning" | "danger";

export interface StoragePlanFlow {
  plan?: StoragePlan;
  phrase: string;
  busy?: PlanStep;
  error: string;
  setPhrase: (value: string) => void;
  generate: (diskID: string) => Promise<void>;
  confirm: () => Promise<void>;
  execute: () => Promise<void>;
  dismiss: () => void;
}

const systemSections: Array<{ id: SettingsSection; label: string; icon: LucideIcon; tone: string; adminOnly?: boolean }> = [
  { id: "overview", label: "概览", icon: LayoutDashboard, tone: "overview" },
  { id: "storage", label: "存储", icon: HardDrive, tone: "storage" },
  { id: "users", label: "用户与权限", icon: UsersRound, tone: "users", adminOnly: true },
];

const plannedSections: Array<{ label: string; icon: LucideIcon; tone: string }> = [
  { label: "网络与访问", icon: Network, tone: "network" },
  { label: "安全与审计", icon: ShieldCheck, tone: "security" },
  { label: "系统更新", icon: CircleArrowUp, tone: "update" },
];

// useStoragePlan belongs to the desktop rather than the storage section: the
// service refuses a new plan while one is pending and cannot read one back, so
// closing settings or switching sections must not forget it.
export function useStoragePlan(): StoragePlanFlow {
  const [plan, setPlan] = useState<StoragePlan>();
  const [phrase, setPhrase] = useState("");
  const [busy, setBusy] = useState<PlanStep>();
  const [error, setError] = useState("");
  const run = async (step: PlanStep, action: () => Promise<StoragePlan>) => {
    setBusy(step); setError("");
    try { setPlan(await action()); }
    catch (caught) { setError(messageOf(caught)); }
    finally { setBusy(undefined); }
  };
  return {
    plan, phrase, busy, error, setPhrase,
    generate: async (diskID) => { setPhrase(""); await run("plan", () => createStoragePlan(diskID)); },
    confirm: async () => { if (plan) await run("confirm", () => confirmStoragePlan(plan.id, phrase)); },
    execute: async () => { if (plan) await run("execute", () => executeStoragePlan(plan.id)); },
    dismiss: () => { setPlan(undefined); setPhrase(""); setError(""); },
  };
}

export function SettingsPanel({ session, host, section, onSectionChange, storagePlan, onLogout }: {
  session: Session;
  host: HostStateView;
  section: SettingsSection;
  onSectionChange: (section: SettingsSection) => void;
  storagePlan: StoragePlanFlow;
  onLogout: () => void;
}) {
  const { user } = session;
  const isAdmin = user.role === "admin";
  const current = section === "users" && !isAdmin ? "overview" : section;
  const [editingPassword, setEditingPassword] = useState(false);
  const volumes = useVolumes(storagePlan.plan?.state);
  const navigate = (next: SettingsSection) => { setEditingPassword(false); onSectionChange(next); };
  return (
    <div className="set-shell">
      <nav className="set-nav" aria-label="设置分区">
        <button className={`set-nav-account ${current === "account" ? "active" : ""}`} aria-label="我的账号" aria-current={current === "account" ? "page" : undefined} onClick={() => navigate("account")}>
          <Avatar name={user.username} />
          <span><strong>{user.username}</strong><small>{roleLabel(user.role)} · 我的账号</small></span>
        </button>
        {systemSections.filter((link) => isAdmin || !link.adminOnly).map(({ id, label, icon, tone }) => (
          <button key={id} className={current === id ? "active" : ""} aria-current={current === id ? "page" : undefined} onClick={() => navigate(id)}>
            <IconTile tone={tone} icon={icon} />{label}
          </button>
        ))}
        <p className="set-nav-label">规划中</p>
        {plannedSections.map(({ label, icon, tone }) => <button key={label} aria-label={`${label}，规划中`} disabled><IconTile tone={tone} icon={icon} />{label}</button>)}
      </nav>
      <div className="set-main">
        <div className="set-page">
          {current === "account" && <AccountSection session={session} editing={editingPassword} onEditingChange={setEditingPassword} onLogout={onLogout} />}
          {current === "overview" && <OverviewSection host={host} volumes={volumes} isAdmin={isAdmin} onOpenStorage={() => navigate("storage")} />}
          {current === "storage" && <StorageSection host={host} volumes={volumes} isAdmin={isAdmin} flow={storagePlan} />}
          {current === "users" && <UsersSection currentUser={user} onChangeOwnPassword={() => { onSectionChange("account"); setEditingPassword(true); }} />}
        </div>
      </div>
    </div>
  );
}

interface VolumesView { items?: Volume[]; error: string; refresh: () => Promise<void> }

// A finished storage plan changes the volume list, so it is read again then.
function useVolumes(planState?: string): VolumesView {
  const [items, setItems] = useState<Volume[]>();
  const [error, setError] = useState("");
  const refresh = useCallback(async () => {
    try { setItems(await listVolumes()); setError(""); }
    catch (caught) { setError(messageOf(caught)); }
  }, []);
  useEffect(() => { void refresh(); }, [refresh, planState]);
  return { items, error, refresh };
}

// ---------------------------------------------------------------- My account

function AccountSection({ session, editing, onEditingChange, onLogout }: {
  session: Session;
  editing: boolean;
  onEditingChange: (editing: boolean) => void;
  onLogout: () => void;
}) {
  const { user } = session;
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [saving, setSaving] = useState(false);
  const open = () => { setError(""); setNotice(""); onEditingChange(true); };
  const cancel = () => { setError(""); onEditingChange(false); };
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    const next = String(data.get("newPassword"));
    if (next !== String(data.get("confirmPassword"))) { setError("两次输入的新密码不一致"); return; }
    setSaving(true);
    try {
      await changePassword(String(data.get("currentPassword")), next);
      setError(""); setNotice("密码已更新，SMB 凭据已同步。"); onEditingChange(false);
    } catch (caught) { setError(messageOf(caught)); }
    finally { setSaving(false); }
  };
  const localConsole = isLocalConsole();
  return (
    <>
      <PageHeader title="我的账号" subtitle="你的登录身份、密码与当前会话" />
      <div className="set-card acct-hero">
        <Avatar name={user.username} size="lg" />
        <div>
          <h3>{user.username}</h3>
          <p><Pill tone={user.role === "admin" ? "accent" : "neutral"}>{roleLabel(user.role)}</Pill><StatusText status={user.status} /></p>
        </div>
        <button className="set-btn" onClick={onLogout}><LogOut />退出登录</button>
      </div>
      <Group title="登录与安全">
        <Row icon={<IconTile tone="key" icon={KeyRound} />} label="密码" detail="网页登录和 SMB 共享使用同一个密码">
          {!editing && <button className="set-btn" onClick={open}>修改密码…</button>}
        </Row>
        {editing && (
          <form className="acct-form" aria-label="修改密码" autoComplete="off" onSubmit={(event) => void submit(event)}>
            <Field label="当前密码"><input className="set-input" name="currentPassword" type="password" required autoComplete="current-password" autoFocus /></Field>
            <Field label="新密码" hint="至少 12 位"><input className="set-input" name="newPassword" type="password" minLength={12} required autoComplete="new-password" /></Field>
            <Field label="确认新密码"><input className="set-input" name="confirmPassword" type="password" minLength={12} required autoComplete="new-password" /></Field>
            {error && <Banner tone="error">{error}</Banner>}
            <div className="acct-form-actions">
              <button type="button" className="set-btn" onClick={cancel}>取消</button>
              <button className="set-btn primary" disabled={saving}>{saving ? "正在保存…" : "保存新密码"}</button>
            </div>
          </form>
        )}
        {notice && <div className="set-inline-note" role="status"><CircleCheck />{notice}</div>}
        <Row
          icon={<IconTile tone="session" icon={localConsole ? Monitor : Globe} />}
          label="本次登录"
          detail={localConsole ? "设备本机屏幕 · 退出登录前一直保持登录" : `浏览器 · ${formatDateTime(session.expiresAt)} 前有效，之后需要重新登录`}
        />
      </Group>
    </>
  );
}

// ------------------------------------------------------------------ Overview

function OverviewSection({ host, volumes, isAdmin, onOpenStorage }: { host: HostStateView; volumes: VolumesView; isAdmin: boolean; onOpenStorage: () => void }) {
  const { snapshot } = host;
  const header = <PageHeader title="概览" subtitle="这台 A-NAS 的运行状态" action={snapshot && <RefreshButton label="刷新设备状态" busy={host.refreshing} onClick={() => void host.refresh()} />} />;
  if (!snapshot) return <>{header}<HostStatePending host={host} /></>;
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
            <Pill tone={health.tone}><span className="set-dot" />{health.label}</Pill>
            <Pill tone={snapshot.dataSource === "live" ? "success" : "warning"}>{snapshot.dataSource === "live" ? "实时主机" : "模拟数据"}</Pill>
          </div>
        </div>
      </div>
      <div className="ov-stats">
        <div className="ov-stat"><span><Clock3 />运行时间</span><strong>{formatUptime(snapshot.system.uptimeSeconds)}</strong><small>观测于 {formatTime(snapshot.observedAt)}</small></div>
        <div className="ov-stat"><span><HardDrive />磁盘</span><strong>{snapshot.disks.length} 块</strong><small>{snapshot.disks.length ? `总容量 ${formatCapacity(totalCapacity)}` : "尚未发现块设备"}</small></div>
        <button className="ov-stat" onClick={onOpenStorage} aria-label="查看存储">
          <span><Database />数据卷<ChevronRight className="ov-stat-go" /></span>
          {volumes.items === undefined ? <><strong>—</strong><small>正在读取</small></>
            : !volume ? <><strong>未初始化</strong><small>{isAdmin ? "前往存储完成初始化" : "等待管理员初始化"}</small></>
              : usage?.known ? <><strong>已用 {usage.percent}%</strong><small>可用 {formatCapacity(usage.available)}</small><span className="ov-stat-bar"><i style={{ width: `${usage.percent}%` }} /></span></>
                : <><strong>{volumeState(volume.state).label}</strong><small>已用空间未知</small></>}
        </button>
      </div>
      <Group title="关于本机">
        <InfoRow label="产品版本" value={snapshot.productVersion} />
        <InfoRow label="操作系统" value={`${snapshot.system.operatingSystem.name} ${snapshot.system.operatingSystem.version}`} />
        <InfoRow label="系统架构" value={snapshot.system.architecture} />
        <InfoRow label="设备标识" value={<span className="set-mono">{snapshot.system.id}</span>} />
        <InfoRow label="最近观测" value={formatTime(snapshot.observedAt)} />
      </Group>
    </>
  );
}

function DeviceArt() {
  const id = useId().replace(/[^\w-]/g, "");
  return (
    <svg className="ov-art" viewBox="0 0 80 80" aria-hidden="true">
      <defs>
        <linearGradient id={`${id}-body`} x1="0" y1="0" x2="0" y2="1"><stop offset="0" stopColor="#465468" /><stop offset="1" stopColor="#141a24" /></linearGradient>
        <linearGradient id={`${id}-bay`} x1="0" y1="0" x2="0" y2="1"><stop offset="0" stopColor="#5f6c80" /><stop offset="1" stopColor="#2a3240" /></linearGradient>
      </defs>
      <ellipse cx="40" cy="74" rx="24" ry="3" fill="rgba(20,30,45,.18)" />
      <rect x="15" y="7" width="50" height="64" rx="10" fill={`url(#${id}-body)`} />
      <rect x="15.5" y="7.5" width="49" height="63" rx="9.5" fill="none" stroke="rgba(255,255,255,.16)" />
      {[15, 36].map((y) => (
        <g key={y}>
          <rect x="22" y={y} width="36" height="17" rx="4" fill={`url(#${id}-bay)`} />
          <path d={`M27 ${y + 6}h18M27 ${y + 11}h18`} stroke="rgba(255,255,255,.22)" strokeWidth="1.6" strokeLinecap="round" />
          <circle cx="52" cy={y + 8.5} r="2" fill="#4ade80" />
        </g>
      ))}
      <circle cx="40" cy="62" r="2.4" fill="#60a5fa" />
    </svg>
  );
}

// ------------------------------------------------------------------- Storage

function StorageSection({ host, volumes, isAdmin, flow }: { host: HostStateView; volumes: VolumesView; isAdmin: boolean; flow: StoragePlanFlow }) {
  const { snapshot, refresh: refreshHost } = host;
  const [setupOpen, setSetupOpen] = useState(false);
  const planState = flow.plan?.state;
  useEffect(() => { if (planState === "succeeded") void refreshHost(); }, [refreshHost, planState]);
  const disks = snapshot?.disks ?? [];
  const noVolume = volumes.items?.length === 0;
  const showSetup = isAdmin && (Boolean(flow.plan) || (noVolume && setupOpen));
  return (
    <>
      <PageHeader title="存储" subtitle="数据卷承载全部文件、相册与共享" action={<RefreshButton label="刷新存储状态" busy={host.refreshing} onClick={() => { void volumes.refresh(); void refreshHost(); }} />} />
      {volumes.error && <Banner tone="error">{volumes.error}</Banner>}
      {showSetup ? <VolumeSetup disks={disks} flow={flow} onClose={() => setSetupOpen(false)} />
        : volumes.items === undefined ? !volumes.error && <div className="set-card set-state compact"><span className="loader" /><p>正在读取数据卷…</p></div>
          : noVolume ? (
            <div className="set-card set-empty">
              <span className="set-empty-art"><Database /></span>
              <h3>尚未创建数据卷</h3>
              <p>{isAdmin ? "选择一块空闲磁盘初始化为数据卷后，就可以开始存放文件、照片和共享内容。" : "文件、照片和共享内容都保存在数据卷中。尚未创建数据卷，请联系管理员初始化。"}</p>
              {isAdmin && <button className="set-btn primary" onClick={() => setSetupOpen(true)}>初始化数据卷…</button>}
            </div>
          ) : volumes.items.map((volume) => <VolumeCard key={volume.id} volume={volume} />)}
      <Group title="磁盘" aside={snapshot ? `${disks.length} 块` : undefined}>
        {!snapshot ? <HostStatePending host={host} inline /> : (
          <>
            {host.disconnected || isStale(snapshot) ? <div className="set-card-banner"><HostStateBanner host={host} snapshot={snapshot} /></div> : null}
            {disks.length === 0
              ? <div className="set-state compact"><HardDrive /><h4>未检测到磁盘</h4><p>设备重新出现后会自动更新。</p></div>
              : disks.map((disk) => <DiskRow key={disk.id} disk={disk} />)}
          </>
        )}
      </Group>
    </>
  );
}

function VolumeCard({ volume }: { volume: Volume }) {
  const usage = volumeUsage(volume);
  const state = volumeState(volume.state);
  return (
    <div className="set-card vol-card">
      <div className="vol-head">
        <IconTile tone="storage" icon={Database} size="lg" />
        <div><h3>数据卷</h3><p>Btrfs · 单盘</p></div>
        <Pill tone={state.tone}><span className="set-dot" />{state.label}</Pill>
      </div>
      {usage.known ? (
        <div>
          <div className="vol-numbers"><strong>{formatCapacity(usage.used)}</strong><span>已用，共 {formatCapacity(usage.capacity)}</span><em>{usage.percent}%</em></div>
          <div className={`vol-bar ${usage.percent >= 85 ? "high" : ""}`} role="meter" aria-label="数据卷已用空间" aria-valuemin={0} aria-valuemax={100} aria-valuenow={usage.percent}><span style={{ width: `${usage.percent}%` }} /></div>
          <div className="vol-legend"><span><i className="used" />已用 {formatCapacity(usage.used)}</span><span><i className="free" />可用 {formatCapacity(usage.available)}</span></div>
        </div>
      ) : (
        // Missing usage is unknown, never "full".
        <p className="vol-unknown">{usage.capacity > 0 ? `已用空间未知 · 共 ${formatCapacity(usage.capacity)}` : "容量未知"}</p>
      )}
      <div className="vol-meta"><span>文件系统 UUID</span><code>{volume.filesystemUuid ?? "等待 UUID"}</code></div>
    </div>
  );
}

function DiskRow({ disk }: { disk: HostState["disks"][number] }) {
  const health = diskHealth(disk.health);
  const role = { system: { label: "系统盘", tone: "neutral" as Tone }, data: { label: "数据盘", tone: "accent" as Tone }, unassigned: { label: "未分配", tone: "neutral" as Tone } }[disk.role];
  return (
    <div className="set-row">
      <IconTile tone={disk.rotational ? "hdd" : "ssd"} icon={HardDrive} size="lg" />
      <div className="set-row-text">
        <span><strong>{disk.model}</strong><Pill tone={role.tone} outline={disk.role === "unassigned"}>{role.label}</Pill></span>
        <small>{disk.transport.toUpperCase()} · {disk.rotational ? "机械硬盘" : "固态存储"} · <span className="set-mono">{shortID(disk.id)}</span></small>
      </div>
      <div className="disk-end">
        <strong>{formatCapacity(disk.capacityBytes)}</strong>
        <small className={`tone-${health.tone}`}><span className="set-dot" />{health.label}{disk.temperatureCelsius === undefined ? "" : ` · ${disk.temperatureCelsius}°C`}</small>
      </div>
    </div>
  );
}

function VolumeSetup({ disks, flow, onClose }: { disks: HostState["disks"]; flow: StoragePlanFlow; onClose: () => void }) {
  const { plan, busy } = flow;
  const candidates = disks.filter((disk) => disk.role === "unassigned");
  const eligibleIDs = candidates.filter((disk) => disk.eligibleForDataVolume).map((disk) => disk.id);
  const [selected, setSelected] = useState("");
  // With a single eligible disk there is nothing to choose.
  useEffect(() => {
    if (!selected && eligibleIDs.length === 1) setSelected(eligibleIDs[0]);
  }, [selected, eligibleIDs.join("\n")]);
  const pending = plan?.state === "planned" || plan?.state === "confirmed";
  const expired = Boolean(plan && pending && Date.parse(plan.expiresAt) <= Date.now());
  const running = busy === "execute" || plan?.state === "running";
  const state = !plan ? "select" : running ? "running" : expired ? "expired" : plan.state;
  const steps: Record<string, number> = { select: 0, planned: 1, confirmed: 2, running: 2, succeeded: 3 };
  const stepIndex = steps[state] ?? 1;
  const minutesLeft = plan ? Math.max(0, Math.ceil((Date.parse(plan.expiresAt) - Date.now()) / 60_000)) : 0;
  return (
    <section className="wiz" aria-label="初始化数据卷">
      <div className="wiz-head">
        <IconTile tone="danger" icon={TriangleAlert} size="lg" />
        <div><h3>初始化数据卷</h3><p>把一块空闲磁盘格式化为 A-NAS 数据卷</p></div>
        {plan ? <Pill tone={planTone(state)}>{planStateLabel(state)}</Pill> : <button className="set-icon-btn" aria-label="关闭初始化" onClick={onClose}><X /></button>}
      </div>
      <ol className="wiz-steps">
        {["选择磁盘", "核对计划", "执行"].map((label, index) => (
          <li key={label} className={index < stepIndex ? "done" : index === stepIndex ? "current" : ""}><b>{index < stepIndex ? <CircleCheck /> : index + 1}</b>{label}</li>
        ))}
      </ol>
      <div className="wiz-body">
        {flow.error && <Banner tone="error">{flow.error}</Banner>}
        {!plan ? (
          <>
            <p className="wiz-lead">只能使用非系统、非 USB、未被占用且身份稳定的 SATA 或 NVMe 磁盘。</p>
            {candidates.length ? (
              <div className="set-card" role="radiogroup" aria-label="候选磁盘">
                {candidates.map((disk) => (
                  <label key={disk.id} className={`wiz-disk ${disk.eligibleForDataVolume ? "" : "disabled"} ${selected === disk.id ? "selected" : ""}`}>
                    <input type="radio" name="volume-disk" value={disk.id} disabled={!disk.eligibleForDataVolume || Boolean(busy)} checked={selected === disk.id} onChange={() => setSelected(disk.id)} />
                    <IconTile tone={disk.rotational ? "hdd" : "ssd"} icon={HardDrive} />
                    <span className="set-row-text">
                      <span><strong>{disk.model}</strong></span>
                      <small>{disk.transport.toUpperCase()} · <span className="set-mono">{shortID(disk.id)}</span></small>
                      {!disk.eligibleForDataVolume && <small className="wiz-reason">{disk.ineligibleReasons.join("；")}</small>}
                    </span>
                    <strong className="wiz-disk-size">{formatCapacity(disk.capacityBytes)}</strong>
                  </label>
                ))}
              </div>
            ) : <div className="set-card set-state compact"><HardDrive /><h4>没有可初始化的磁盘</h4><p>请接入一块空闲的 SATA 或 NVMe 磁盘。</p></div>}
          </>
        ) : (
          <>
            <h4 className="wiz-title">破坏性操作计划</h4>
            <dl className="wiz-facts">
              <dt>目标磁盘</dt><dd>{plan.diskModel} · {formatCapacity(plan.capacityBytes)}</dd>
              <dt>身份指纹</dt><dd><code>{plan.fingerprint}</code></dd>
              <dt>将清除的签名</dt><dd>{plan.signatures?.length ? plan.signatures.join("、") : "未检测到文件系统签名"}</dd>
              <dt>计划有效期</dt><dd>{new Date(plan.expiresAt).toLocaleString("zh-CN")}{pending && !expired ? `（还剩约 ${minutesLeft} 分钟）` : ""}</dd>
            </dl>
            {(plan.actions?.length ?? 0) > 0 && <ol className="wiz-actions">{plan.actions!.map((action) => <li key={action.kind}>{action.description}</li>)}</ol>}
            {state === "planned" && (
              <label className="wiz-confirm">
                <span>输入确认短语 <code>{plan.confirmationPhrase}</code> 以确认这是要清除的磁盘</span>
                <input className="set-input" value={flow.phrase} onChange={(event) => flow.setPhrase(event.target.value)} autoComplete="off" spellCheck={false} />
              </label>
            )}
            {state === "confirmed" && <Callout tone="danger" title="最后一步：执行后无法撤销">{plan.diskModel} 上的全部分区和数据将被永久清除，然后创建新的数据卷。</Callout>}
            {state === "running" && (
              <div className="wiz-running" role="status"><div className="wiz-progress"><span /></div><p>正在创建数据卷，请勿关闭设备或断电…</p></div>
            )}
            {state === "succeeded" && <Callout tone="success" title="数据卷已创建">现在可以在文件管理和相册中存放内容了。</Callout>}
            {state === "expired" && <Callout tone="warning" title="计划已过期">为保证磁盘身份没有变化，计划只在十分钟内有效。请重新开始。</Callout>}
            {(state === "failed" || state === "needs_attention") && (
              <Callout tone="danger" title={state === "failed" ? "初始化失败" : "需要人工处理"}>
                {plan.failure ? `${plan.failure}。` : ""}不要立即生成新计划，先按运行手册导出日志并核对磁盘状态。
              </Callout>
            )}
          </>
        )}
      </div>
      <div className="wiz-foot">
        {!plan && (
          <>
            <p className="wiz-warn"><TriangleAlert />所选磁盘上的全部数据都会被清除</p>
            <button className="set-btn" onClick={onClose}>取消</button>
            <button className="set-btn primary" disabled={!selected || Boolean(busy)} onClick={() => void flow.generate(selected)}>{busy === "plan" ? "正在生成…" : "生成格式化计划"}</button>
          </>
        )}
        {state === "planned" && <button className="set-btn primary" disabled={flow.phrase !== plan?.confirmationPhrase || Boolean(busy)} onClick={() => void flow.confirm()}>{busy === "confirm" ? "正在确认…" : "确认计划"}</button>}
        {state === "confirmed" && <button className="set-btn danger" disabled={Boolean(busy)} onClick={() => void flow.execute()}>清除磁盘并创建数据卷</button>}
        {state === "succeeded" && <button className="set-btn primary" onClick={flow.dismiss}>完成</button>}
        {state === "expired" && <button className="set-btn primary" onClick={flow.dismiss}>重新开始</button>}
        {(state === "failed" || state === "needs_attention") && <button className="set-btn" onClick={flow.dismiss}>关闭</button>}
      </div>
    </section>
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
  const refresh = useCallback(async () => {
    try { setUsers(await listUsers()); setError(""); }
    catch (caught) { setError(messageOf(caught)); }
  }, []);
  useEffect(() => { void refresh(); }, [refresh]);
  const open = (next: UserDialog) => { setNotice(""); setDialog(next); };
  const finish = async (message: string) => { setDialog(undefined); setNotice(message); await refresh(); };
  return (
    <>
      <PageHeader title="用户与权限" subtitle="管理家庭成员的账号" action={<button className="set-btn primary" onClick={() => open({ kind: "create" })}><UserPlus />添加成员</button>} />
      {error && <Banner tone="error">{error}</Banner>}
      {notice && <Banner tone="success" onClose={() => setNotice("")}>{notice}</Banner>}
      <Group
        title="账号"
        aside={users ? `${users.length} 个` : undefined}
        overflowVisible
        footnote="管理员默认看不到成员的个人空间和私有图库。需要找回内容时可以开启只读查看：必须填写原因，会写入审计并通知对方，24 小时后自动结束。"
      >
        {users === undefined ? <div className="set-state compact"><span className="loader" /><p>正在读取账号…</p></div> : sortUsers(users, currentUser.id).map((user) => {
          const self = user.id === currentUser.id;
          return (
            <div className="set-row" key={user.id}>
              <Avatar name={user.username} />
              <div className="set-row-text">
                <span><strong>{user.username}</strong>{self && <Pill tone="accent">你</Pill>}</span>
                <small><span className={`set-dot status-${user.status}`} />{roleLabel(user.role)} · {statusLabel(user.status)}</small>
              </div>
              <div className="set-row-end">
                {self ? <button className="set-btn" onClick={onChangeOwnPassword}>修改我的密码</button> : (
                  <RowMenu label={`${user.username} 的更多操作`} items={[
                    { label: "重置密码…", icon: KeyRound, onSelect: () => open({ kind: "reset", user }) },
                    { label: "查看个人空间…", icon: FolderLock, onSelect: () => open({ kind: "viewing", user, scope: "space" }) },
                    { label: "查看私有图库…", icon: Images, onSelect: () => open({ kind: "viewing", user, scope: "library" }) },
                    "separator",
                    { label: user.status === "disabled" ? "账号已禁用" : "禁用账号…", icon: UserX, danger: true, disabled: user.status === "disabled", onSelect: () => open({ kind: "disable", user }) },
                  ]} />
                )}
              </div>
            </div>
          );
        })}
      </Group>
      {dialog && <UserDialogView dialog={dialog} onClose={() => setDialog(undefined)} onDone={(message) => void finish(message)} />}
    </>
  );
}

function UserDialogView({ dialog, onClose, onDone }: { dialog: UserDialog; onClose: () => void; onDone: (message: string) => void }) {
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const run = async (action: () => Promise<string>) => {
    setBusy(true); setError("");
    try { onDone(await action()); }
    catch (caught) { setError(messageOf(caught)); setBusy(false); }
  };
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    const value = (name: string) => String(data.get(name));
    if (dialog.kind === "create") {
      void run(async () => { await createMember(value("username"), value("password")); return `已添加成员 ${value("username")}。`; });
    } else if (dialog.kind === "reset") {
      void run(async () => {
        await resetMember(dialog.user.id, value("password"));
        return `已重置 ${dialog.user.username} 的密码。对方下次登录时必须先设置新密码，SMB 共享在此之前保持停用。`;
      });
    } else if (dialog.kind === "viewing") {
      const { user, scope } = dialog;
      void run(async () => {
        await startViewing(user.id, value("password"), value("reason"), scope);
        return scope === "library"
          ? `已开启对 ${user.username} 私有图库的只读查看，可在相册中选择“只读查看 · ${user.username}”。`
          : `已开启对 ${user.username} 个人空间的只读查看，可在文件管理中选择“只读查看 · ${user.username}”。`;
      });
    } else {
      void run(async () => { await disableMember(dialog.user.id); return `已禁用 ${dialog.user.username}。`; });
    }
  };
  const what = dialog.kind === "viewing" && dialog.scope === "library" ? "私有图库" : "个人空间";
  const content = {
    create: { title: "添加成员", label: "添加成员", icon: UserPlus, tone: "accent", description: "成员拥有自己的个人空间和私有图库，并可使用 Shared 共享空间。", submit: "创建成员", danger: false },
    reset: dialog.kind === "reset" && { title: `重置 ${dialog.user.username} 的密码`, label: `重置 ${dialog.user.username} 的密码`, icon: KeyRound, tone: "warning", description: `重置后 ${dialog.user.username} 下次登录必须先设置只有自己知道的新密码，在此之前 SMB 共享保持停用。`, submit: "确认重置", danger: false },
    viewing: dialog.kind === "viewing" && { title: `只读查看 ${dialog.user.username} 的${what}`, label: `查看 ${dialog.user.username} 的${what}`, icon: what === "私有图库" ? Images : FolderLock, tone: "warning", description: `查看他人${what}会写入审计，并在 ${dialog.user.username} 下次登录时通知对方。访问为只读，24 小时后自动结束。`, submit: "开始只读查看", danger: false },
    disable: dialog.kind === "disable" && { title: `禁用 ${dialog.user.username}？`, label: `禁用 ${dialog.user.username}`, icon: UserX, tone: "danger", description: `${dialog.user.username} 将无法登录网页和 SMB 共享，已建立的 SMB 连接会立即断开；个人空间中的文件不会被删除。账号禁用后暂时无法重新启用。`, submit: "禁用账号", danger: true },
  }[dialog.kind] as { title: string; label: string; icon: LucideIcon; tone: string; description: string; submit: string; danger: boolean };
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
            <Field label="账号"><input className="set-input" name="username" required autoFocus /></Field>
            <Field label="初始密码" hint="至少 12 位；成员可在“我的账号”中自行修改"><input className="set-input" name="password" type="password" minLength={12} autoComplete="new-password" required /></Field>
          </>
        )}
        {dialog.kind === "reset" && <Field label="新密码" hint="至少 12 位"><input className="set-input" name="password" type="password" minLength={12} autoComplete="new-password" required autoFocus /></Field>}
        {dialog.kind === "viewing" && (
          <>
            <Field label="查看原因"><input className="set-input" name="reason" maxLength={500} required autoFocus placeholder="例如：帮助找回误删的照片" /></Field>
            <Field label="你的密码" hint="为确认是你本人操作"><input className="set-input" name="password" type="password" autoComplete="current-password" required /></Field>
          </>
        )}
        {error && <Banner tone="error">{error}</Banner>}
        <footer>
          <button type="button" className="set-btn" onClick={onClose} disabled={busy}>取消</button>
          <button className={`set-btn ${content.danger ? "danger" : "primary"}`} disabled={busy}>{busy ? "正在处理…" : content.submit}</button>
        </footer>
      </form>
    </Dialog>
  );
}

type MenuItem = { label: string; icon: LucideIcon; onSelect: () => void; danger?: boolean; disabled?: boolean } | "separator";

function RowMenu({ label, items }: { label: string; items: MenuItem[] }) {
  const [open, setOpen] = useState(false);
  const anchor = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const closeOutside = (event: PointerEvent) => { if (!anchor.current?.contains(event.target as Node)) setOpen(false); };
    const closeOnEscape = (event: KeyboardEvent) => { if (event.key === "Escape") setOpen(false); };
    window.addEventListener("pointerdown", closeOutside);
    window.addEventListener("keydown", closeOnEscape);
    return () => { window.removeEventListener("pointerdown", closeOutside); window.removeEventListener("keydown", closeOnEscape); };
  }, [open]);
  return (
    <div className="set-menu-anchor" ref={anchor}>
      <button className={`set-icon-btn bordered ${open ? "open" : ""}`} aria-label={label} aria-haspopup="menu" aria-expanded={open} onClick={() => setOpen((value) => !value)}><Ellipsis /></button>
      {open && (
        <div className="set-menu" role="menu" aria-label={label}>
          {items.map((item, index) => item === "separator" ? <hr key={index} /> : (
            <button key={item.label} role="menuitem" className={item.danger ? "danger" : ""} disabled={item.disabled} onClick={() => { setOpen(false); item.onSelect(); }}>
              <item.icon />{item.label}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

function Dialog({ title, onClose, children }: { title: string; onClose?: () => void; children: ReactNode }) {
  useEffect(() => {
    if (!onClose) return;
    const closeOnEscape = (event: KeyboardEvent) => { if (event.key === "Escape") onClose(); };
    window.addEventListener("keydown", closeOnEscape);
    return () => window.removeEventListener("keydown", closeOnEscape);
  }, [onClose]);
  return (
    <div className="set-dialog-layer" onPointerDown={(event) => { if (event.target === event.currentTarget) onClose?.(); }}>
      <div className="set-dialog" role="dialog" aria-modal="true" aria-label={title}>{children}</div>
    </div>
  );
}

// -------------------------------------------------------------- Shared parts

function PageHeader({ title, subtitle, action }: { title: string; subtitle: string; action?: ReactNode }) {
  return <header className="set-header"><div><h2>{title}</h2><p>{subtitle}</p></div>{action}</header>;
}

function Group({ title, aside, footnote, overflowVisible, children }: { title: string; aside?: string; footnote?: string; overflowVisible?: boolean; children: ReactNode }) {
  return (
    <div className="set-group">
      <div className="set-group-head"><h3>{title}</h3>{aside && <span>{aside}</span>}</div>
      <div className={`set-card ${overflowVisible ? "overflow-visible" : ""}`}>{children}</div>
      {footnote && <p className="set-footnote">{footnote}</p>}
    </div>
  );
}

function Row({ icon, label, detail, children }: { icon?: ReactNode; label: string; detail?: string; children?: ReactNode }) {
  return (
    <div className="set-row">
      {icon}
      <div className="set-row-text"><span>{label}</span>{detail && <small>{detail}</small>}</div>
      {children && <div className="set-row-end">{children}</div>}
    </div>
  );
}

function InfoRow({ label, value }: { label: string; value: ReactNode }) {
  return <div className="set-row info"><span>{label}</span><span className="set-value">{value}</span></div>;
}

function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return <div className="set-field"><label><span>{label}</span>{children}</label>{hint && <small>{hint}</small>}</div>;
}

function Pill({ tone = "neutral", outline, children }: { tone?: Tone; outline?: boolean; children: ReactNode }) {
  return <span className={`set-pill ${tone} ${outline ? "outline" : ""}`}>{children}</span>;
}

function Banner({ tone, onClose, children }: { tone: "error" | "warning" | "success"; onClose?: () => void; children: ReactNode }) {
  const Icon = tone === "success" ? CircleCheck : CircleAlert;
  return (
    <div className={`set-banner ${tone}`} role={tone === "error" ? "alert" : "status"}>
      <Icon /><span>{children}</span>
      {onClose && <button className="set-icon-btn" aria-label="关闭提示" onClick={onClose}><X /></button>}
    </div>
  );
}

function Callout({ tone, title, children }: { tone: "danger" | "success" | "warning"; title: string; children: ReactNode }) {
  const Icon = tone === "success" ? CircleCheck : TriangleAlert;
  return <div className={`wiz-callout ${tone}`}><Icon /><div><strong>{title}</strong><p>{children}</p></div></div>;
}

function IconTile({ tone, icon: Icon, size }: { tone: string; icon: LucideIcon; size?: "lg" }) {
  return <span className={`set-tile ${tone} ${size ?? ""}`}><Icon /></span>;
}

function Avatar({ name, size }: { name: string; size?: "lg" }) {
  return <span className={`set-avatar ${avatarTone(name)} ${size ?? ""}`} aria-hidden="true">{name.slice(0, 1).toUpperCase()}</span>;
}

// avatarTone gives a person the same colour wherever their initial appears.
export function avatarTone(name: string): string {
  return `avatar-c${[...name].reduce((sum, character) => sum + character.charCodeAt(0), 0) % 6}`;
}

function StatusText({ status }: { status: User["status"] }) {
  return <span className="set-status"><span className={`set-dot status-${status}`} />{statusLabel(status)}</span>;
}

function RefreshButton({ label, busy, onClick }: { label: string; busy: boolean; onClick: () => void }) {
  return <button className="set-icon-btn bordered" aria-label={label} disabled={busy} onClick={onClick}><RefreshCw className={busy ? "spinning" : ""} /></button>;
}

function HostStatePending({ host, inline }: { host: HostStateView; inline?: boolean }) {
  const className = inline ? "set-state" : "set-card set-state";
  if (host.loading) return <div className={className}><span className="loader" /><h4>正在读取设备状态…</h4><p>正在连接 A-NAS 产品服务</p></div>;
  return <div className={className}><CircleAlert /><h4>暂时无法读取设备状态</h4><p>请确认产品服务和 Host Agent 正在运行。</p><button className="set-btn primary" onClick={() => void host.refresh()}>重新连接</button></div>;
}

function HostStateBanner({ host, snapshot }: { host: HostStateView; snapshot: HostState }) {
  if (host.disconnected) return <Banner tone="error">连接中断，正在显示上次成功读取的数据</Banner>;
  if (isStale(snapshot)) return <Banner tone="warning">实时观测时间较早，正在等待新数据</Banner>;
  return null;
}

// ------------------------------------------------------------------ Helpers

function messageOf(error: unknown) { return error instanceof Error ? error.message : "请求失败"; }

function volumeUsage(volume: Volume) {
  const capacity = volume.capacityBytes ?? 0;
  const known = capacity > 0 && volume.availableBytes !== undefined;
  const available = known ? Math.min(capacity, Math.max(0, volume.availableBytes!)) : 0;
  const used = capacity - available;
  return { known, capacity, available, used, percent: known ? Math.min(100, Math.round((used / capacity) * 100)) : 0 };
}

function volumeState(state: Volume["state"]): { label: string; tone: Tone } {
  return {
    creating: { label: "创建中", tone: "accent" as Tone },
    available: { label: "在线", tone: "success" as Tone },
    unavailable: { label: "离线", tone: "danger" as Tone },
    read_only: { label: "只读", tone: "warning" as Tone },
  }[state];
}

function healthPresentation(health: Health): { label: string; tone: Tone } {
  return {
    healthy: { label: "运行正常", tone: "success" as Tone },
    warning: { label: "需要注意", tone: "warning" as Tone },
    critical: { label: "严重故障", tone: "danger" as Tone },
    unknown: { label: "健康未评估", tone: "neutral" as Tone },
  }[health];
}

function diskHealth(health: Health): { label: string; tone: Tone } {
  return {
    healthy: { label: "健康", tone: "success" as Tone },
    warning: { label: "注意", tone: "warning" as Tone },
    critical: { label: "严重", tone: "danger" as Tone },
    unknown: { label: "健康未知", tone: "neutral" as Tone },
  }[health];
}

// The signed-in account comes first, then administrators, then members by name.
function sortUsers(users: User[], selfID: string): User[] {
  const rank = (user: User) => (user.id === selfID ? 0 : user.role === "admin" ? 1 : 2);
  return [...users].sort((left, right) => rank(left) - rank(right) || left.username.localeCompare(right.username, "zh-CN"));
}

function roleLabel(role: User["role"]): string {
  return role === "admin" ? "管理员" : "成员";
}

function statusLabel(status: User["status"]): string {
  return { active: "正常", pending: "创建中", disabled: "已禁用", error: "凭据异常" }[status];
}

// Raw states stay visible next to the label because the runbooks name them.
function planStateLabel(state: string): string {
  const labels: Record<string, string> = { planned: "待确认", confirmed: "已确认", running: "执行中", succeeded: "已完成", failed: "失败", needs_attention: "需要人工处理", expired: "已过期" };
  return labels[state] ? `${labels[state]} · ${state}` : state;
}

function planTone(state: string): Tone {
  if (state === "succeeded") return "success";
  if (state === "failed" || state === "needs_attention") return "danger";
  if (state === "expired") return "warning";
  return "accent";
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

function formatDateTime(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "—" : new Intl.DateTimeFormat("zh-CN", { month: "long", day: "numeric", hour: "2-digit", minute: "2-digit" }).format(date);
}

function shortID(value: string): string {
  return value.length > 24 ? `${value.slice(0, 14)}…${value.slice(-6)}` : value;
}

function isStale(state: HostState): boolean {
  return state.dataSource === "live" && Date.now() - new Date(state.observedAt).getTime() > 30_000;
}
