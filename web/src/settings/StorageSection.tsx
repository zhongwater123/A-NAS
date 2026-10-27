import { errorText } from "../errorMessages";
import { Archive, ChevronLeft, CircleCheck, Database, HardDrive, Layers, TriangleAlert, X } from "lucide-react";
import { useCallback, useEffect, useState, useSyncExternalStore } from "react";

import {
  HostState,
  StoragePlan,
  StorageStep,
  Volume,
  VolumeConversion,
  VolumeLayout,
  cancelStoragePlan,
  confirmStoragePlan,
  continueVolumeConversion,
  createStoragePlan,
  executeStoragePlan,
  getStoragePlan,
  listUnfinishedStoragePlans,
  listVolumes,
} from "../api";
import { formatBytes, formatNominalCapacity } from "../bytes";
import type { HostStateView } from "../useHostState";
import { HostStateBanner, HostStatePending, isStale } from "./hostState";
import {
  conversionPercent,
  fits,
  formatRemaining,
  layoutLabel,
  layouts,
  multiDiskLayouts,
  remainingSeconds,
  usableBytes,
  usedBytes,
  type ProgressSample,
} from "./layouts";
import { Banner, Callout, Group, IconTile, PageHeader, Pill, RefreshButton, messageOf, type Tone } from "./parts";

type Disk = HostState["disks"][number];
type PlanStep = "plan" | "confirm" | "execute" | "cancel";

export interface StoragePlanFlow {
  plan?: StoragePlan;
  phrase: string;
  busy?: PlanStep;
  error: string;
  setPhrase: (value: string) => void;
  generate: (diskIDs: string[], layout?: VolumeLayout) => Promise<void>;
  confirm: () => Promise<void>;
  // execute starts a confirmed plan, or retries one that needs attention.
  execute: () => Promise<void>;
  // cancel gives up a plan that has not run and forgets it; it reports
  // whether the plan is gone, so the wizard can close.
  cancel: () => Promise<boolean>;
  dismiss: () => void;
  // resume shows a plan that ran on the device while this page was away.
  resume: (plan: StoragePlan) => void;
}

// useStoragePlan belongs to the desktop rather than the storage section: the
// service cannot read back a plan that has not run, so closing settings or
// switching sections must not forget it. A running plan is followed until the
// device finishes it.
export function useStoragePlan(): StoragePlanFlow {
  const [plan, setPlan] = useState<StoragePlan>();
  const [phrase, setPhrase] = useState("");
  const [busy, setBusy] = useState<PlanStep>();
  const [error, setError] = useState("");
  // reread is how a plan that stopped is shown: its own failure says why.
  const run = async (step: PlanStep, action: () => Promise<StoragePlan>, reread?: () => Promise<StoragePlan>) => {
    setBusy(step);
    setError("");
    try {
      setPlan(await action());
    } catch (caught) {
      const stopped = reread && (caught as { code?: string } | undefined)?.code === "operation_needs_attention";
      try {
        if (stopped) setPlan(await reread());
        else setError(storageErrorText(caught));
      } catch {
        setError(storageErrorText(caught));
      }
    } finally {
      setBusy(undefined);
    }
  };
  const planID = plan?.id;
  const running = plan?.state === "running";
  useEffect(() => {
    if (!planID || !running) return;
    let current = true;
    const timer = window.setInterval(() => {
      getStoragePlan(planID)
        .then((next) => {
          if (current) setPlan((shown) => (shown?.id === planID ? next : shown));
        })
        // The device runs the plan regardless; the next check may reach it.
        .catch(() => undefined);
    }, 1500);
    return () => {
      current = false;
      window.clearInterval(timer);
    };
  }, [planID, running]);
  const resume = useCallback((found: StoragePlan) => {
    setPlan((shown) => shown ?? found);
    setPhrase("");
    setError("");
  }, []);
  return {
    plan,
    phrase,
    busy,
    error,
    setPhrase,
    resume,
    generate: async (diskIDs, layout) => {
      setPhrase("");
      await run("plan", () => createStoragePlan(diskIDs, layout));
    },
    confirm: async () => {
      if (plan) await run("confirm", () => confirmStoragePlan(plan.id, phrase));
    },
    execute: async () => {
      if (plan)
        await run(
          "execute",
          () => executeStoragePlan(plan.id),
          () => getStoragePlan(plan.id),
        );
    },
    cancel: async () => {
      if (plan?.state !== "planned" && plan?.state !== "confirmed") {
        setPlan(undefined);
        setPhrase("");
        setError("");
        return true;
      }
      setBusy("cancel");
      setError("");
      try {
        await cancelStoragePlan(plan.id);
        setPlan(undefined);
        setPhrase("");
        return true;
      } catch (caught) {
        setError(storageErrorText(caught));
        return false;
      } finally {
        setBusy(undefined);
      }
    },
    dismiss: () => {
      setPlan(undefined);
      setPhrase("");
      setError("");
    },
  };
}

export interface VolumesView {
  items?: Volume[];
  // When items were read, in milliseconds since the epoch.
  readAt: number;
  error: string;
  refresh: () => Promise<void>;
}

// useVolumes reads the data volume again when a storage plan changes state,
// and while the volume converts: every few seconds so the progress moves, or
// every half minute while a stopped conversion waits for the Host Agent.
export function useVolumes(planState?: string): VolumesView {
  const [items, setItems] = useState<Volume[]>();
  const [readAt, setReadAt] = useState(0);
  const [error, setError] = useState("");
  const refresh = useCallback(
    () =>
      listVolumes()
        .then((volumes) => {
          setItems(volumes);
          setReadAt(Date.now());
          setError("");
        })
        .catch((caught: unknown) => {
          setError(messageOf(caught));
        }),
    [],
  );
  useEffect(() => {
    void refresh();
  }, [refresh, planState]);
  const conversion = items?.find((volume) => volume.conversion)?.conversion?.state;
  useEffect(() => {
    if (!conversion) return;
    const timer = window.setInterval(() => void refresh(), conversion === "running" ? 5000 : 30_000);
    return () => window.clearInterval(timer);
  }, [conversion, refresh]);
  return { items, readAt, error, refresh };
}

// useUnfinishedPlan finds a plan that ran on the device while this page was
// away: a running one is shown at once, one that stopped is offered.
function useUnfinishedPlan(isAdmin: boolean, flow: StoragePlanFlow): StoragePlan | undefined {
  const [stopped, setStopped] = useState<StoragePlan>();
  const showing = Boolean(flow.plan);
  const { resume } = flow;
  useEffect(() => {
    if (!isAdmin || showing) return;
    let current = true;
    listUnfinishedStoragePlans()
      .then((items) => {
        if (!current) return;
        const running = items.find((plan) => plan.state === "running");
        if (running) resume(running);
        setStopped(running ? undefined : items[0]);
      })
      .catch(() => {
        if (current) setStopped(undefined);
      });
    return () => {
      current = false;
    };
  }, [isAdmin, showing, resume]);
  return showing ? undefined : stopped;
}

export function StorageSection({
  host,
  volumes,
  isAdmin,
  flow,
}: {
  host: HostStateView;
  volumes: VolumesView;
  isAdmin: boolean;
  flow: StoragePlanFlow;
}) {
  const { snapshot, refresh: refreshHost } = host;
  const [wizardOpen, setWizardOpen] = useState(false);
  const stopped = useUnfinishedPlan(isAdmin, flow);
  const planState = flow.plan?.state;
  useEffect(() => {
    if (planState === "succeeded") void refreshHost();
  }, [refreshHost, planState]);
  const disks = snapshot?.disks ?? [];
  const volume = volumes.items?.[0];
  const noVolume = volumes.items?.length === 0;
  const usable = disks.filter((disk) => disk.role === "unassigned" && disk.eligibleForDataVolume);
  const canAdd = Boolean(volume && volume.state === "available" && !volume.conversion && usable.length > 0);
  const showWizard = isAdmin && (Boolean(flow.plan) || (wizardOpen && (noVolume || canAdd)));
  const mode: WizardMode = flow.plan
    ? flow.plan.kind === "add_disks"
      ? "add"
      : "create"
    : noVolume
      ? "create"
      : "add";
  const finish = () => {
    flow.dismiss();
    setWizardOpen(false);
    void volumes.refresh();
  };
  return (
    <>
      <PageHeader
        title="存储"
        subtitle="数据卷承载全部文件、相册与共享"
        action={
          <RefreshButton
            label="刷新存储状态"
            busy={host.refreshing}
            onClick={() => {
              void volumes.refresh();
              void refreshHost();
            }}
          />
        }
      />
      {volumes.error && <Banner tone="error">{volumes.error}</Banner>}
      {!showWizard && stopped && <StoppedPlan plan={stopped} onOpen={() => flow.resume(stopped)} />}
      {showWizard ? (
        <VolumeWizard
          mode={mode}
          disks={disks}
          volume={volume}
          flow={flow}
          onClose={() => setWizardOpen(false)}
          onFinish={finish}
          onCancel={async () => {
            if (await flow.cancel()) setWizardOpen(false);
          }}
        />
      ) : volumes.items === undefined ? (
        !volumes.error && (
          <div className="set-card set-state compact">
            <span className="loader" />
            <p>正在读取数据卷…</p>
          </div>
        )
      ) : noVolume ? (
        <div className="set-card set-empty">
          <span className="set-empty-art">
            <Database />
          </span>
          <h3>尚未创建数据卷</h3>
          <p>
            {isAdmin
              ? "选择空闲硬盘初始化为数据卷后，就可以开始存放文件、照片和共享内容。"
              : "文件、照片和共享内容都保存在数据卷中。尚未创建数据卷，请联系管理员初始化。"}
          </p>
          {isAdmin && (
            <button className="set-btn primary" onClick={() => setWizardOpen(true)}>
              初始化数据卷…
            </button>
          )}
        </div>
      ) : (
        volume && (
          <>
            <VolumeCard
              volume={volume}
              readAt={volumes.readAt}
              disks={disks}
              isAdmin={isAdmin}
              onRefresh={volumes.refresh}
            />
            {isAdmin && usable.length > 0 && (
              <SpareDisks count={usable.length} volume={volume} canAdd={canAdd} onOpen={() => setWizardOpen(true)} />
            )}
          </>
        )
      )}
      <Group title="磁盘" aside={snapshot ? `${disks.length} 块` : undefined}>
        {!snapshot ? (
          <HostStatePending host={host} inline />
        ) : (
          <>
            {host.disconnected || isStale(snapshot) ? (
              <div className="set-card-banner">
                <HostStateBanner host={host} snapshot={snapshot} />
              </div>
            ) : null}
            {disks.length === 0 ? (
              <div className="set-state compact">
                <HardDrive />
                <h4>未检测到磁盘</h4>
                <p>设备重新出现后会自动更新。</p>
              </div>
            ) : (
              disks.map((disk) => <DiskRow key={disk.id} disk={disk} />)
            )}
          </>
        )}
      </Group>
    </>
  );
}

function StoppedPlan({ plan, onOpen }: { plan: StoragePlan; onOpen: () => void }) {
  return (
    <div className="set-card spare-card">
      <IconTile tone="danger" icon={TriangleAlert} size="lg" />
      <div>
        <h3>上次的存储操作没有完成</h3>
        <p>
          {plan.kind === "add_disks" ? "把硬盘加入数据卷" : "创建数据卷"}
          {stoppedWhere(plan.step)}。可以查看原因并重试，已经完成的步骤不会重做。
        </p>
      </div>
      <button className="set-btn primary" onClick={onOpen}>
        查看并处理
      </button>
    </div>
  );
}

function SpareDisks({
  count,
  volume,
  canAdd,
  onOpen,
}: {
  count: number;
  volume: Volume;
  canAdd: boolean;
  onOpen: () => void;
}) {
  const why = volume.conversion
    ? "数据卷转换完成后才能加入新硬盘。"
    : volume.state !== "available"
      ? "数据卷在线且可写时才能加入新硬盘。"
      : "可以加入数据卷，增加容量，或者让每个文件在两块硬盘上各有一份。";
  return (
    <div className="set-card spare-card">
      <IconTile tone="accent" icon={HardDrive} size="lg" />
      <div>
        <h3>发现 {count} 块未使用的硬盘</h3>
        <p>{why}</p>
      </div>
      <button className="set-btn primary" disabled={!canAdd} onClick={onOpen}>
        设置用途…
      </button>
    </div>
  );
}

function VolumeCard({
  volume,
  readAt,
  disks,
  isAdmin,
  onRefresh,
}: {
  volume: Volume;
  readAt: number;
  disks: Disk[];
  isAdmin: boolean;
  onRefresh: () => Promise<void>;
}) {
  const usage = volumeUsage(volume);
  const state =
    volume.conversion && volume.state === "available"
      ? { label: "转换中", tone: "accent" as Tone }
      : volumeState(volume.state);
  const members = (volume.diskIds ?? []).map(
    (id) => disks.find((disk) => disk.id === id) ?? { id, model: "未检测到的硬盘", capacityBytes: 0 },
  );
  return (
    <div className="set-card vol-card">
      <div className="vol-head">
        <IconTile tone="storage" icon={Database} size="lg" />
        <div>
          <h3>数据卷</h3>
          <p>
            Btrfs · {layoutLabel(volume.layout)} · {members.length} 块硬盘
          </p>
        </div>
        <Pill tone={state.tone}>
          <span className="set-dot" />
          {state.label}
        </Pill>
      </div>
      {usage.known ? (
        <div>
          <div className="vol-numbers">
            <strong>{formatBytes(usage.used)}</strong>
            <span>已用，共 {formatBytes(usage.capacity)}</span>
            <em>{usage.percent}%</em>
          </div>
          <div
            className={`vol-bar ${usage.percent >= 85 ? "high" : ""}`}
            role="meter"
            aria-label="数据卷已用空间"
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={usage.percent}
          >
            <span style={{ width: `${usage.percent}%` }} />
          </div>
          <div className="vol-legend">
            <span>
              <i className="used" />
              已用 {formatBytes(usage.used)}
            </span>
            <span>
              <i className="free" />
              可用 {formatBytes(usage.available)}
            </span>
          </div>
        </div>
      ) : (
        // Missing usage is unknown, never "full".
        <p className="vol-unknown">
          {usage.capacity > 0 ? `已用空间未知 · 共 ${formatBytes(usage.capacity)}` : "容量未知"}
        </p>
      )}
      {volume.conversion && (
        <ConversionProgress
          volume={volume}
          conversion={volume.conversion}
          readAt={readAt}
          isAdmin={isAdmin}
          onRefresh={onRefresh}
        />
      )}
      {members.length > 0 && (
        <ul className="vol-members" aria-label="组成数据卷的硬盘">
          {members.map((disk) => (
            <li key={disk.id}>
              <HardDrive />
              <span>{disk.model}</span>
              <small className="set-mono">{shortID(disk.id)}</small>
              {disk.capacityBytes > 0 && <small>{formatBytes(disk.capacityBytes)}</small>}
            </li>
          ))}
        </ul>
      )}
      <div className="vol-meta">
        <span>文件系统 UUID</span>
        <code>{volume.filesystemUuid ?? "等待 UUID"}</code>
      </div>
    </div>
  );
}

function ConversionProgress({
  volume,
  conversion,
  readAt,
  isAdmin,
  onRefresh,
}: {
  volume: Volume;
  conversion: VolumeConversion;
  readAt: number;
  isAdmin: boolean;
  onRefresh: () => Promise<void>;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  // The speed is measured from the first progress this page saw, by when
  // each progress was read.
  const [first, setFirst] = useState<ProgressSample>();
  const restart = conversion.state !== "running" || !first || conversion.convertedBytes < first.bytes;
  const sample = restart ? { at: readAt, bytes: conversion.convertedBytes } : first;
  if (restart && (first?.at !== sample.at || first?.bytes !== sample.bytes)) setFirst(sample);
  const left = remainingSeconds(sample, readAt, conversion);
  const target = layouts[volume.layout].name;
  const running = conversion.state === "running";
  const percent = conversionPercent(conversion);
  const resume = async () => {
    setBusy(true);
    setError("");
    try {
      await continueVolumeConversion(volume.id);
      await onRefresh();
    } catch (caught) {
      setError(storageErrorText(caught));
    } finally {
      setBusy(false);
    }
  };
  return (
    <section className={`vol-convert ${running ? "" : "waiting"}`} aria-label="转换进度">
      <div className="vol-convert-head">
        <strong>{running ? `正在转换为${target}` : `转换为${target}已暂停`}</strong>
        <em>{percent}%</em>
      </div>
      <div
        className="vol-bar convert"
        role="progressbar"
        aria-label={`转换为${target}`}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={percent}
      >
        <span style={{ width: `${percent}%` }} />
      </div>
      <p>
        已转换 {formatBytes(conversion.convertedBytes)}，共 {formatBytes(conversion.totalBytes)}
        {running && left !== undefined ? ` · 预计还需${formatRemaining(left)}` : ""}
      </p>
      <p className="vol-convert-note">
        {running
          ? `转换期间可以照常使用，关机或重启后会自动继续。${volume.layout === "mirror" ? "完成前坏一块硬盘仍可能丢失文件。" : ""}`
          : "系统每 15 分钟会自动继续一次；停下来的原因记录在“日志”的系统日志中。"}
      </p>
      {!running && isAdmin && (
        <div className="vol-convert-actions">
          <button className="set-btn" disabled={busy} onClick={() => void resume()}>
            {busy ? "正在继续…" : "继续转换"}
          </button>
        </div>
      )}
      {error && <Banner tone="error">{error}</Banner>}
    </section>
  );
}

const reasonText: Record<string, string> = {
  missing_identity: "无法识别硬盘的稳定身份",
  duplicate_identity: "硬盘身份与其他设备重复，不能安全选择",
  missing_model: "设备未提供型号",
  missing_capacity: "未检测到存储介质或容量未知",
  read_only: "设备为只读，不能加入数据卷",
  invalid_device_name: "设备信息无效，不能加入数据卷",
  unsupported_transport: "USB 等外接硬盘不能加入数据卷",
  removable: "可移除设备不能加入数据卷",
  in_use: "正被挂载、磁盘阵列、LVM 或加密卷使用",
};

function ineligibleText(disk: Disk): string {
  return disk.ineligibleReasons
    .map((reason) => reasonText[reason])
    .filter(Boolean)
    .join("；");
}

function DiskRow({ disk }: { disk: Disk }) {
  const health = diskHealth(disk.health);
  const role = {
    system: { label: "系统盘", tone: "neutral" as Tone },
    data: { label: "数据盘", tone: "accent" as Tone },
    unassigned: { label: "未使用", tone: "neutral" as Tone },
  }[disk.role];
  const spare = disk.role === "unassigned";
  const blocked = !disk.eligibleForDataVolume ? ineligibleText(disk) : "";
  return (
    <div className="set-row">
      <IconTile tone={disk.rotational ? "hdd" : "ssd"} icon={HardDrive} size="lg" />
      <div className="set-row-text">
        <span>
          <strong>{disk.model || "未识别设备"}</strong>
          <Pill tone={role.tone} outline={spare}>
            {role.label}
          </Pill>
          {spare && disk.filesystems.length > 0 && (
            <Pill tone="warning" outline>
              有旧数据
            </Pill>
          )}
        </span>
        <small>
          {disk.transport.toUpperCase()} · {disk.rotational ? "机械硬盘" : "固态存储"} ·{" "}
          <span className="set-mono">{shortID(disk.id)}</span>
        </small>
        {blocked && <small className="wiz-reason">{blocked}</small>}
      </div>
      <div className="disk-end">
        <strong
          title={
            disk.capacityBytes > 0
              ? `硬盘标称 ${formatNominalCapacity(disk.capacityBytes)}：厂商按 1000 进制计算，这里按 1024 进制（GiB），所以数字更小`
              : undefined
          }
        >
          {disk.capacityBytes > 0 ? formatBytes(disk.capacityBytes) : "容量未知"}
          {disk.capacityBytes > 0 && (
            <span className="disk-nominal">标称 {formatNominalCapacity(disk.capacityBytes)}</span>
          )}
        </strong>
        <small className={`tone-${health.tone}`}>
          <span className="set-dot" />
          {health.label}
          {disk.temperatureCelsius === undefined ? "" : ` · ${disk.temperatureCelsius}°C`}
        </small>
      </div>
    </div>
  );
}

// ------------------------------------------------------------------ Wizard

type WizardMode = "create" | "add";

// minutesUntil is the whole minutes left before a time, none once it passed.
export const minutesUntil = (until: number, now: number) => Math.max(0, Math.ceil((until - now) / 60_000));

// useMinutesLeft counts the minutes left before a time and changes on its own
// as each one passes, so a plan's expiry shows without anything else
// rendering the page.
export function useMinutesLeft(until: number | undefined): number {
  const subscribe = useCallback(
    (changed: () => void) => {
      let timer = 0;
      const next = () => {
        if (until === undefined) return;
        const left = until - Date.now();
        if (!(left > 0)) return;
        // The count drops when the time left reaches the next whole minute.
        timer = window.setTimeout(
          () => {
            changed();
            next();
          },
          (left % 60_000 || 60_000) + 1,
        );
      };
      next();
      return () => window.clearTimeout(timer);
    },
    [until],
  );
  return useSyncExternalStore(subscribe, () => (until === undefined ? 0 : minutesUntil(until, Date.now())));
}

interface LayoutOption {
  layout: VolumeLayout;
  before?: number;
  after: number;
  fits: boolean;
}

// The wizard can be left at every step but while a plan runs: before a plan
// by closing it, with a plan that has not run by cancelling it, and once a
// plan ended by finishing it.
function VolumeWizard({
  mode,
  disks,
  volume,
  flow,
  onClose,
  onFinish,
  onCancel,
}: {
  mode: WizardMode;
  disks: Disk[];
  volume?: Volume;
  flow: StoragePlanFlow;
  onClose: () => void;
  onFinish: () => void;
  onCancel: () => Promise<void>;
}) {
  const { plan, busy } = flow;
  const candidates = disks.filter((disk) => disk.role === "unassigned");
  const eligible = candidates.filter((disk) => disk.eligibleForDataVolume);
  const [selected, setSelected] = useState<string[]>(() => (eligible.length === 1 ? [eligible[0].id] : []));
  const [stage, setStage] = useState<"disks" | "layout">("disks");
  const [layout, setLayout] = useState<VolumeLayout>("mirror");
  const members = mode === "add" && volume ? disks.filter((disk) => (volume.diskIds ?? []).includes(disk.id)) : [];
  const chosen = disks.filter((disk) => selected.includes(disk.id));
  const options = layoutOptions(mode, volume, members, chosen);
  const choice = options.find((option) => option.layout === layout && option.fits)
    ? layout
    : options.find((option) => option.fits)?.layout;
  const needsLayout = plan
    ? plan.kind === "add_disks" || (plan.disks?.length ?? 0) > 1
    : mode === "add" || selected.length > 1;
  const steps =
    mode === "add"
      ? ["硬盘与用途", "冗余方式", "核对计划", "执行"]
      : needsLayout
        ? ["选择硬盘", "冗余方式", "核对计划", "执行"]
        : ["选择硬盘", "核对计划", "执行"];

  const pending = plan?.state === "planned" || plan?.state === "confirmed";
  const minutesLeft = useMinutesLeft(plan && pending ? Date.parse(plan.expiresAt) : undefined);
  const expired = Boolean(plan && pending && minutesLeft === 0);
  const running = busy === "execute" || plan?.state === "running";
  const state = !plan ? stage : running ? "running" : expired ? "expired" : plan.state;
  const review = steps.indexOf("核对计划");
  const stepIndex =
    state === "disks"
      ? 0
      : state === "layout"
        ? 1
        : state === "confirmed" || state === "running"
          ? review + 1
          : state === "succeeded"
            ? steps.length
            : review;
  const title = mode === "add" ? "设置硬盘用途" : "初始化数据卷";
  const toggle = (id: string) =>
    setSelected((current) => (current.includes(id) ? current.filter((value) => value !== id) : [...current, id]));
  const generate = () => void flow.generate(selected, needsLayout ? choice : undefined);

  return (
    <section className="wiz" aria-label={title}>
      <div className="wiz-head">
        <IconTile
          tone={mode === "add" ? "storage" : "danger"}
          icon={mode === "add" ? Layers : TriangleAlert}
          size="lg"
        />
        <div>
          <h3>{title}</h3>
          <p>{mode === "add" ? "让新硬盘加入数据卷，增加容量或让文件多一份" : "把空闲硬盘格式化为 A-NAS 数据卷"}</p>
        </div>
        <span className="wiz-head-end">
          {plan && <Pill tone={planTone(state)}>{planStateLabel(state)}</Pill>}
          {!running && (
            <button
              className="set-icon-btn"
              aria-label={pending && !expired ? "取消计划并关闭" : `关闭${title}`}
              disabled={Boolean(busy)}
              onClick={() =>
                void (!plan
                  ? onClose()
                  : pending && !expired
                    ? onCancel()
                    : state === "succeeded"
                      ? onFinish()
                      : onCancel())
              }
            >
              <X />
            </button>
          )}
        </span>
      </div>
      <ol className="wiz-steps">
        {steps.map((label, index) => (
          <li key={label} className={index < stepIndex ? "done" : index === stepIndex ? "current" : ""}>
            <b>{index < stepIndex ? <CircleCheck /> : index + 1}</b>
            {label}
          </li>
        ))}
      </ol>
      <div className="wiz-body">
        {flow.error && <Banner tone="error">{flow.error}</Banner>}
        {!plan && stage === "disks" && (
          <>
            {mode === "add" && (
              <div className="wiz-purposes" role="radiogroup" aria-label="用途">
                <label className="wiz-purpose selected">
                  <input type="radio" name="disk-purpose" checked readOnly />
                  <IconTile tone="storage" icon={Layers} />
                  <span>
                    <strong>加入数据卷</strong>
                    <small>增加容量，或者让每个文件在两块硬盘上各有一份。已有文件保持原样。</small>
                  </span>
                </label>
                <label className="wiz-purpose disabled">
                  <input type="radio" name="disk-purpose" disabled />
                  <IconTile tone="overview" icon={Archive} />
                  <span>
                    <strong>
                      用作备份盘<Pill>规划中</Pill>
                    </strong>
                    <small>定期把数据卷的快照备份到这块硬盘，防误删和勒索。</small>
                  </span>
                </label>
              </div>
            )}
            <p className="wiz-lead">
              {mode === "add"
                ? "选择要加入的硬盘："
                : "选择一块或多块硬盘。只能使用非系统、非 USB、没有被使用且身份稳定的 SATA 或 NVMe 硬盘；选两块及以上可以组成镜像。"}
            </p>
            {candidates.length ? (
              <div className="set-card" role="group" aria-label="候选硬盘">
                {candidates.map((disk) => {
                  const blocked = !disk.eligibleForDataVolume;
                  return (
                    <label
                      key={disk.id}
                      className={`wiz-disk ${blocked ? "disabled" : ""} ${selected.includes(disk.id) ? "selected" : ""}`}
                    >
                      <input
                        type="checkbox"
                        value={disk.id}
                        disabled={blocked || Boolean(busy)}
                        checked={selected.includes(disk.id)}
                        onChange={() => toggle(disk.id)}
                      />
                      <IconTile tone={disk.rotational ? "hdd" : "ssd"} icon={HardDrive} />
                      <span className="set-row-text">
                        <span>
                          <strong>{disk.model || "未识别设备"}</strong>
                          {disk.filesystems.length > 0 && (
                            <Pill tone="warning" outline>
                              有旧数据
                            </Pill>
                          )}
                        </span>
                        <small>
                          {disk.transport.toUpperCase()} · <span className="set-mono">{shortID(disk.id)}</span>
                          {disk.filesystems.length > 0 ? ` · ${disk.filesystems.join("、")}` : ""}
                        </small>
                        {blocked && <small className="wiz-reason">{ineligibleText(disk) || "不能使用这块硬盘"}</small>}
                      </span>
                      <strong className="wiz-disk-size">
                        {disk.capacityBytes > 0 ? formatBytes(disk.capacityBytes) : "容量未知"}
                      </strong>
                    </label>
                  );
                })}
              </div>
            ) : (
              <div className="set-card set-state compact">
                <HardDrive />
                <h4>没有可用的硬盘</h4>
                <p>请接入一块空闲的 SATA 或 NVMe 硬盘。</p>
              </div>
            )}
          </>
        )}
        {!plan && stage === "layout" && (
          <>
            <p className="wiz-lead">
              {mode === "add"
                ? `加入后，数据卷由 ${members.length + chosen.length} 块硬盘组成。选择文件在这些硬盘上怎么存放：`
                : `选择文件在这 ${chosen.length} 块硬盘上怎么存放：`}
            </p>
            <div className="wiz-layouts" role="radiogroup" aria-label="冗余方式">
              {options.map((option) => {
                const info = layouts[option.layout];
                return (
                  <label
                    key={option.layout}
                    className={`wiz-layout ${choice === option.layout ? "selected" : ""} ${option.fits ? "" : "disabled"}`}
                  >
                    <input
                      type="radio"
                      name="volume-layout"
                      value={option.layout}
                      disabled={!option.fits}
                      checked={choice === option.layout}
                      onChange={() => setLayout(option.layout)}
                    />
                    <LayoutDiagram layout={option.layout} />
                    <span className="wiz-layout-text">
                      <span className="wiz-layout-name">
                        <strong>{info.name}</strong>
                        <Pill tone="neutral" outline>
                          {info.tag}
                        </Pill>
                        {option.layout === "mirror" && option.fits && <Pill tone="success">推荐</Pill>}
                      </span>
                      <small>{info.summary}</small>
                      <small className={`wiz-layout-loss tone-${info.tone}`}>{info.loss}</small>
                      {!option.fits && (
                        <small className="wiz-reason">
                          已用 {formatBytes(usedBytes(volume) ?? 0)}，放不进加入后的 {formatBytes(option.after)}
                        </small>
                      )}
                    </span>
                    <span className="wiz-layout-size">
                      {option.before !== undefined && <small>{formatBytes(option.before)} →</small>}
                      <strong>{formatBytes(option.after)}</strong>
                      <small>可用</small>
                    </span>
                  </label>
                );
              })}
            </div>
            <p className="wiz-note">冗余防的是硬盘损坏，不能代替备份：误删的文件从回收站和快照中找回。</p>
          </>
        )}
        {plan && (
          <PlanReview
            plan={plan}
            flow={flow}
            state={state}
            pending={pending}
            expired={expired}
            minutesLeft={minutesLeft}
          />
        )}
      </div>
      <div className="wiz-foot">
        {!plan && stage === "disks" && (
          <>
            <p className="wiz-warn">
              <TriangleAlert />
              所选硬盘上的全部数据都会被清除
            </p>
            <button className="set-btn" onClick={onClose}>
              取消
            </button>
            {needsLayout ? (
              <button className="set-btn primary" disabled={!selected.length} onClick={() => setStage("layout")}>
                下一步
              </button>
            ) : (
              <button className="set-btn primary" disabled={!selected.length || Boolean(busy)} onClick={generate}>
                {busy === "plan" ? "正在生成…" : "生成格式化计划"}
              </button>
            )}
          </>
        )}
        {!plan && stage === "layout" && (
          <>
            <button className="set-btn wiz-back" onClick={() => setStage("disks")}>
              <ChevronLeft />
              上一步
            </button>
            <button className="set-btn primary" disabled={!choice || Boolean(busy)} onClick={generate}>
              {busy === "plan" ? "正在生成…" : mode === "add" ? "生成计划" : "生成格式化计划"}
            </button>
          </>
        )}
        {(state === "planned" || state === "confirmed") && (
          <button className="set-btn wiz-back" disabled={Boolean(busy)} onClick={() => void onCancel()}>
            {busy === "cancel" ? "正在取消…" : "取消计划"}
          </button>
        )}
        {state === "planned" && (
          <button
            className="set-btn primary"
            disabled={flow.phrase !== plan?.confirmationPhrase || Boolean(busy)}
            onClick={() => void flow.confirm()}
          >
            {busy === "confirm" ? "正在确认…" : "确认计划"}
          </button>
        )}
        {state === "confirmed" && (
          <button className="set-btn danger" disabled={Boolean(busy)} onClick={() => void flow.execute()}>
            {plan?.kind === "add_disks" ? "清除硬盘并加入数据卷" : "清除硬盘并创建数据卷"}
          </button>
        )}
        {state === "succeeded" && (
          <button className="set-btn primary" onClick={onFinish}>
            完成
          </button>
        )}
        {state === "expired" && (
          <button className="set-btn primary" onClick={flow.dismiss}>
            重新开始
          </button>
        )}
        {(state === "failed" || state === "needs_attention") && (
          <>
            <button className="set-btn wiz-back" disabled={Boolean(busy)} onClick={() => void onCancel()}>
              关闭
            </button>
            <button className="set-btn primary" disabled={Boolean(busy)} onClick={() => void flow.execute()}>
              重试
            </button>
          </>
        )}
      </div>
    </section>
  );
}

function PlanReview({
  plan,
  flow,
  state,
  pending,
  expired,
  minutesLeft,
}: {
  plan: StoragePlan;
  flow: StoragePlanFlow;
  state: string;
  pending: boolean;
  expired: boolean;
  minutesLeft: number;
}) {
  const disks = plan.disks ?? [];
  const adding = plan.kind === "add_disks";
  const leftovers = disks.flatMap((disk) =>
    (disk.signatures ?? []).map((signature) =>
      signature.label ? `${signature.type}（${signature.label}）` : signature.type,
    ),
  );
  const models = disks.map((disk) => disk.model).join("、");
  return (
    <>
      <h4 className="wiz-title">破坏性操作计划</h4>
      <dl className="wiz-facts">
        <dt>操作</dt>
        <dd>{adding ? "把硬盘加入数据卷" : "创建数据卷"}</dd>
        {plan.layout && (
          <>
            <dt>冗余方式</dt>
            <dd>{layoutLabel(plan.layout)}</dd>
          </>
        )}
        {plan.capacityAfterBytes > 0 && (
          <>
            <dt>可用容量</dt>
            <dd>
              {plan.capacityBeforeBytes ? `${formatBytes(plan.capacityBeforeBytes)} → ` : "约 "}
              {formatBytes(plan.capacityAfterBytes)}
            </dd>
          </>
        )}
        <dt>计划有效期</dt>
        <dd>
          {new Date(plan.expiresAt).toLocaleString("zh-CN")}
          {pending && !expired ? `（还剩约 ${minutesLeft} 分钟）` : ""}
        </dd>
      </dl>
      <div className="wiz-targets" aria-label="将清除的硬盘">
        {disks.map((disk) => (
          <div key={disk.id} className="wiz-target">
            <HardDrive />
            <div>
              <strong>
                {disk.model}
                <span>{formatBytes(disk.capacityBytes)}</span>
              </strong>
              <small>
                身份指纹 <code>{disk.fingerprint}</code>
              </small>
              <small>
                {disk.signatures?.length
                  ? `将清除：${disk.signatures.map((signature) => (signature.label ? `${signature.type}（${signature.label}）` : signature.type)).join("、")}`
                  : "未检测到文件系统签名"}
              </small>
            </div>
          </div>
        ))}
      </div>
      {state === "planned" && leftovers.length > 0 && (
        <Callout tone="warning" title="硬盘上有其他系统留下的数据">
          {leftovers.join("、")} 可能来自其他 NAS 或电脑，清除后无法恢复。请确认里面没有需要的文件再继续。
        </Callout>
      )}
      {(plan.actions?.length ?? 0) > 0 && (
        <ol className="wiz-actions">
          {plan.actions!.map((action) => (
            <li key={action.kind}>{action.description}</li>
          ))}
        </ol>
      )}
      {state === "planned" && (
        <label className="wiz-confirm">
          <span>
            输入确认短语 <code>{plan.confirmationPhrase}</code> 以确认这是要清除的硬盘
          </span>
          <input
            className="set-input"
            value={flow.phrase}
            onChange={(event) => flow.setPhrase(event.target.value)}
            autoComplete="off"
            spellCheck={false}
          />
        </label>
      )}
      {state === "confirmed" && (
        <Callout tone="danger" title="最后一步：执行后无法撤销">
          {adding
            ? `${models} 上的全部分区和数据将被永久清除，然后加入数据卷。数据卷上已有的文件不受影响。`
            : `${models} 上的全部分区和数据将被永久清除，然后创建新的数据卷。`}
        </Callout>
      )}
      {state === "running" && (
        <div className="wiz-running" role="status">
          <div className="wiz-progress">
            <span />
          </div>
          <p>{(plan.step && stepTexts[plan.step]) || (adding ? "正在把硬盘加入数据卷…" : "正在创建数据卷…")}</p>
          <p>操作在设备上进行，关闭这个窗口或页面不会打断它，回来时会看到结果。请勿关闭设备或断电。</p>
        </div>
      )}
      {state === "succeeded" &&
        (adding ? (
          <Callout tone="success" title="硬盘已加入数据卷">
            数据卷正在后台转换为{layouts[plan.layout]?.name ?? "新的冗余方式"}
            ，进度显示在数据卷卡片上。转换期间可以照常使用，也可以关闭这个窗口。
          </Callout>
        ) : (
          <Callout tone="success" title="数据卷已创建">
            现在可以在文件管理和相册中存放内容了。
          </Callout>
        ))}
      {state === "expired" && (
        <Callout tone="warning" title="计划已过期">
          为保证硬盘身份没有变化，计划只在十分钟内有效。请重新开始。
        </Callout>
      )}
      {(state === "failed" || state === "needs_attention") && (
        <Callout tone="danger" title={state === "failed" ? "执行失败" : "需要人工处理"}>
          {adding ? "把硬盘加入数据卷" : "创建数据卷"}
          {stoppedWhere(plan.step)}
          {plan.failure ? `：${plan.failure}` : ""}。
          可以重试：设备会再核对一次硬盘身份，已经完成的步骤不会重做。重试仍不成功时，按运行手册导出日志并核对硬盘状态。
        </Callout>
      )}
    </>
  );
}

function layoutOptions(mode: WizardMode, volume: Volume | undefined, members: Disk[], chosen: Disk[]): LayoutOption[] {
  const sizes = chosen.map((disk) => disk.capacityBytes);
  if (mode === "create")
    return multiDiskLayouts.map((layout) => ({ layout, after: usableBytes(layout, sizes), fits: true }));
  const memberSizes = members.map((disk) => disk.capacityBytes);
  const before = usableBytes(volume?.layout ?? "single", memberSizes);
  const used = usedBytes(volume);
  return multiDiskLayouts.map((layout) => {
    const after = usableBytes(layout, [...memberSizes, ...sizes]);
    return { layout, before, after, fits: fits(after, used) };
  });
}

// LayoutDiagram draws two disks and where the blocks of a few files land.
function LayoutDiagram({ layout }: { layout: VolumeLayout }) {
  const rows: Record<VolumeLayout, [string[], string[]]> = {
    single: [["a", "b", "c"], []],
    mirror: [
      ["a", "b", "c"],
      ["a", "b", "c"],
    ],
    span: [
      ["a", "b", "c"],
      ["d", "e", ""],
    ],
    stripe: [
      ["a", "c", "e"],
      ["b", "d", "f"],
    ],
  };
  return (
    <svg className="wiz-diagram" viewBox="0 0 76 44" aria-hidden="true">
      {rows[layout].map((blocks, disk) => (
        <g key={disk} transform={`translate(${disk * 40} 0)`}>
          <rect className="ld-disk" x="1" y="1" width="34" height="42" rx="6" />
          {blocks.map(
            (block, row) =>
              block && <rect key={row} className={`ld-${block}`} x="7" y={8 + row * 11} width="22" height="7" rx="2" />,
          )}
        </g>
      ))}
    </svg>
  );
}

// The steps a plan goes through on the device, as it runs and where it stopped.
const stepTexts: Record<StorageStep, string> = {
  erase: "正在清除硬盘上的分区和签名…",
  format: "正在格式化，固态硬盘可能要几分钟…",
  mount: "正在挂载数据卷…",
  prepare: "正在准备空间与目录…",
  add_devices: "正在把硬盘加入数据卷…",
};
const stepNames: Record<StorageStep, string> = {
  erase: "清除硬盘",
  format: "格式化",
  mount: "挂载",
  prepare: "准备空间",
  add_devices: "加入数据卷",
};

function stoppedWhere(step?: StorageStep): string {
  return step ? `在“${stepNames[step]}”这一步停下了` : "没有完成";
}

// storageErrorText turns the storage API's error codes into what to do.
function storageErrorText(error: unknown): string {
  return errorText(error, "storage");
}

export function volumeUsage(volume: Volume) {
  const capacity = volume.capacityBytes ?? 0;
  const known = capacity > 0 && volume.availableBytes !== undefined;
  const available = known ? Math.min(capacity, Math.max(0, volume.availableBytes!)) : 0;
  const used = capacity - available;
  return { known, capacity, available, used, percent: known ? Math.min(100, Math.round((used / capacity) * 100)) : 0 };
}

export function volumeState(state: Volume["state"]): { label: string; tone: Tone } {
  return {
    creating: { label: "创建中", tone: "accent" as Tone },
    available: { label: "在线", tone: "success" as Tone },
    unavailable: { label: "离线", tone: "danger" as Tone },
    read_only: { label: "只读", tone: "warning" as Tone },
  }[state];
}

function diskHealth(health: Disk["health"]): { label: string; tone: Tone } {
  return {
    healthy: { label: "健康", tone: "success" as Tone },
    warning: { label: "注意", tone: "warning" as Tone },
    critical: { label: "严重", tone: "danger" as Tone },
    unknown: { label: "健康未知", tone: "neutral" as Tone },
  }[health];
}

// Raw states stay visible next to the label because the runbooks name them.
function planStateLabel(state: string): string {
  const labels: Record<string, string> = {
    planned: "待确认",
    confirmed: "已确认",
    running: "执行中",
    succeeded: "已完成",
    failed: "失败",
    needs_attention: "需要人工处理",
    expired: "已过期",
  };
  return labels[state] ? `${labels[state]} · ${state}` : state;
}

function planTone(state: string): Tone {
  if (state === "succeeded") return "success";
  if (state === "failed" || state === "needs_attention") return "danger";
  if (state === "expired") return "warning";
  return "accent";
}

function shortID(value: string): string {
  return value.length > 24 ? `${value.slice(0, 14)}…${value.slice(-6)}` : value;
}
