import { ChevronRight, CircleFadingArrowUp, Copy } from "lucide-react";
import { useId } from "react";

import type { Disk, Health, SystemUpdateStatus } from "../api.generated";
import type { HostState } from "../desktop/api";
import type { HostStateView } from "../desktop/useHostState";
import { formatBytes } from "../lib/bytes";
import { hasNewVersion } from "../systemupdate/status";
import { versionLabel } from "../systemupdate/texts";
import { CompanionCredit } from "./CompanionCredit";
import { DeviceTemperatures, diskTemperature } from "./DeviceTemperatures";
import { describeFinding } from "./health";
import { HostStateBanner, HostStatePending } from "./hostState";
import { Group, PageHeader, Pill, RefreshButton, Row, type Tone, useSettingsKit } from "./parts";
import { type VolumesView, volumeState, volumeUsage } from "./StorageSection";
import styles from "./settings.module.css";
import { temperatureLevel } from "./Temperature";

// OverviewSection is Settings → 概览. The device heads the page, drawn with a
// bay for each disk, and says whether it is all right; under it are how much
// room is left, the warmest part and the time running, then what needs doing
// and the details (docs/specs/settings.md).
export function OverviewSection({
  host,
  volumes,
  isAdmin,
  update,
  onOpenStorage,
  onOpenUpdate,
}: {
  host: HostStateView;
  volumes: VolumesView;
  isAdmin: boolean;
  // update is the system update's state, read for administrators alone.
  update?: SystemUpdateStatus;
  onOpenStorage: () => void;
  onOpenUpdate: () => void;
}) {
  const { snapshot } = host;
  const { toast } = useSettingsKit();
  const header = (
    <PageHeader
      title="概览"
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
        <CompanionCredit />
      </>
    );
  const { system } = snapshot;
  const health = healthPresentation(system.health);
  const systemStorage = system.storage;
  // The clipboard exists only on HTTPS and on the device's own screen.
  const clipboard = navigator.clipboard as Clipboard | undefined;
  const copyID = () =>
    void clipboard?.writeText(system.id).then(
      () => toast({ text: "已复制设备标识。" }),
      () => toast({ text: "无法复制，请手动选择文字。", tone: "error" }),
    );
  return (
    <>
      {header}
      <HostStateBanner host={host} snapshot={snapshot} />
      <section className={`${styles.ovHero} ${styles[health.tone] ?? ""}`} aria-label="设备状态" data-setting="health">
        <DeviceArt disks={snapshot.disks} />
        <h3>
          {system.hostname}
          {snapshot.dataSource !== "live" && <Pill>模拟数据</Pill>}
        </h3>
        <p className={styles.ovStatus}>
          <i aria-hidden="true" />
          {health.label}
        </p>
      </section>
      <div className={styles.ovFacts}>
        <StorageSummary
          volumes={volumes}
          diskCount={snapshot.disks.length}
          isAdmin={isAdmin}
          onOpenStorage={onOpenStorage}
        />
        <Warmest snapshot={snapshot} />
        <Uptime seconds={system.uptimeSeconds} observedAt={snapshot.observedAt} />
      </div>
      <HealthFindings
        snapshot={snapshot}
        update={hasNewVersion(update) ? update : undefined}
        onOpenStorage={onOpenStorage}
        onOpenUpdate={onOpenUpdate}
      />
      <DeviceTemperatures snapshot={snapshot} />
      <Group title="关于本机" anchor="about">
        <Row label="产品版本" value={snapshot.productVersion} />
        <Row label="操作系统" value={`${system.operatingSystem.name} ${system.operatingSystem.version}`} />
        <Row label="系统架构" value={system.architecture} />
        <Row
          label="系统盘空间"
          value={
            systemStorage
              ? `可用 ${formatBytes(systemStorage.availableBytes)}，共 ${formatBytes(systemStorage.totalBytes)}`
              : "暂时无法读取"
          }
        />
        <Row label="设备标识" value={<span className={styles.setMono}>{system.id}</span>}>
          {clipboard && (
            <button className={styles.setIconBtn} aria-label="复制设备标识" title="复制" onClick={copyID}>
              <Copy />
            </button>
          )}
        </Row>
        <Row label="状态更新于" value={formatTime(snapshot.observedAt)} />
      </Group>
      <CompanionCredit />
    </>
  );
}

// StorageSummary is the data volume in one figure; the whole card opens 存储.
function StorageSummary({
  volumes,
  diskCount,
  isAdmin,
  onOpenStorage,
}: {
  volumes: VolumesView;
  diskCount: number;
  isAdmin: boolean;
  onOpenStorage: () => void;
}) {
  const volume = volumes.items?.[0];
  const usage = volume ? volumeUsage(volume) : undefined;
  const disks = diskCount ? `${diskCount} 块硬盘` : "未检测到硬盘";
  const [amount, unit] = usage?.known ? formatBytes(usage.available).split(" ") : [];
  const words = `${styles.ovFactFigure} ${styles.words}`;
  return (
    <button className={styles.ovFact} onClick={onOpenStorage}>
      <span className={styles.ovFactHead}>
        存储空间
        <ChevronRight aria-hidden="true" />
      </span>
      {volumes.items === undefined ? (
        <>
          <span className={words}>—</span>
          <span className={styles.ovFactNote}>正在读取</span>
        </>
      ) : !volume ? (
        <>
          <span className={words}>尚未创建数据卷</span>
          <span className={styles.ovFactNote}>{isAdmin ? `${disks}，前往存储完成初始化` : "等待管理员初始化"}</span>
        </>
      ) : usage?.known ? (
        <>
          <span className={styles.ovFactFigure}>
            {amount}
            <small> {unit} 可用</small>
          </span>
          <span className={`${styles.ovStorageBar} ${usage.percent >= 85 ? styles.high : ""}`} aria-hidden="true">
            <i style={{ width: `${Math.max(usage.percent, 1)}%` }} />
          </span>
          <span className={styles.ovFactNote}>
            已用 {formatBytes(usage.used)}，共 {formatBytes(usage.capacity)}
          </span>
        </>
      ) : (
        <>
          <span className={words}>{volumeState(volume.state).label}</span>
          <span className={styles.ovFactNote}>已用空间未知</span>
        </>
      )}
    </button>
  );
}

// Warmest is the highest reading among the processor, the graphics cards and
// the disks, with the part it comes from.
function Warmest({ snapshot }: { snapshot: HostState }) {
  const thermal = snapshot.system.thermal;
  const readings = [
    { name: "处理器", reading: thermal?.cpu },
    ...(thermal?.gpus ?? []).map((gpu) => ({ name: gpu.name, reading: gpu.temperature })),
    ...snapshot.disks.map((disk) => ({ name: disk.model || "硬盘", reading: diskTemperature(disk) })),
  ].flatMap(({ name, reading }) => (reading ? [{ name, reading }] : []));
  const warmest = readings.reduce<(typeof readings)[number] | undefined>(
    (top, item) => (!top || item.reading.celsius > top.reading.celsius ? item : top),
    undefined,
  );
  const hot = warmest !== undefined && temperatureLevel(warmest.reading) === "hot";
  return (
    <div className={styles.ovFact}>
      <span className={styles.ovFactHead}>最高温度</span>
      {warmest ? (
        <>
          <span className={styles.ovFactFigure}>
            {Math.round(warmest.reading.celsius)}
            <small> °C</small>
          </span>
          <span className={`${styles.ovFactNote} ${hot ? styles.hot : ""}`}>
            {warmest.name}
            {hot && "，偏高"}
          </span>
        </>
      ) : (
        <>
          <span className={`${styles.ovFactFigure} ${styles.words}`}>—</span>
          <span className={styles.ovFactNote}>{thermal ? "未检测到温度传感器" : "暂时无法读取"}</span>
        </>
      )}
    </div>
  );
}

const startedAt = new Intl.DateTimeFormat("zh-CN", {
  month: "long",
  day: "numeric",
  hour: "2-digit",
  minute: "2-digit",
});

// Uptime is how long the device has been running and when it started.
function Uptime({ seconds, observedAt }: { seconds: number; observedAt: string }) {
  const days = Math.floor(seconds / 86400);
  const hours = Math.floor((seconds % 86400) / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  const parts: [number, string][] =
    days > 0
      ? [
          [days, "天"],
          [hours, "小时"],
        ]
      : hours > 0
        ? [
            [hours, "小时"],
            [minutes, "分钟"],
          ]
        : [[minutes, "分钟"]];
  const observed = new Date(observedAt).getTime();
  return (
    <div className={styles.ovFact}>
      <span className={styles.ovFactHead}>已运行</span>
      <span className={styles.ovFactFigure}>
        {parts.map(([value, unit]) => (
          <span key={unit}>
            {value}
            <small> {unit} </small>
          </span>
        ))}
      </span>
      {!Number.isNaN(observed) && (
        <span className={styles.ovFactNote}>{startedAt.format(new Date(observed - seconds * 1000))} 开机</span>
      )}
    </div>
  );
}

// HealthFindings lists what makes the system rated as it is, each with what
// to do about it (docs/specs/system-health.md), and a new version of A-NAS
// for administrators (docs/specs/system-update.md). A healthy device shows
// no findings: the status line above already says so.
function HealthFindings({
  snapshot,
  update,
  onOpenStorage,
  onOpenUpdate,
}: {
  snapshot: HostState;
  update?: SystemUpdateStatus;
  onOpenStorage: () => void;
  onOpenUpdate: () => void;
}) {
  const findings = snapshot.system.health === "unknown" ? [] : snapshot.system.healthFindings;
  const count = findings.length + (update ? 1 : 0);
  if (count === 0) return null;
  return (
    <Group title="需要处理" aside={`${count} 项`}>
      {update?.candidate && (
        <Row
          icon={<CircleFadingArrowUp className={`${styles.healthIcon} ${styles.accent}`} aria-hidden="true" />}
          label={<strong>有新版本 {versionLabel(update.candidate)}</strong>}
          detail={update.phase === "ready" ? "已下载，可以安装" : "可以下载"}
          anchor="new-version"
          onClick={onOpenUpdate}
        />
      )}
      {findings.map((finding, index) => {
        const described = describeFinding(finding, snapshot.disks);
        const critical = finding.severity === "critical";
        return (
          <div
            key={`${finding.code}:${finding.diskId ?? ""}:${index}`}
            className={`${styles.setRow} ${styles.healthRow}`}
          >
            <described.icon
              className={`${styles.healthIcon} ${critical ? styles.danger : styles.warning}`}
              aria-hidden="true"
            />
            <div className={styles.setRowText}>
              <span>
                <strong>{described.title}</strong>
                <Pill tone={critical ? "danger" : "warning"}>{critical ? "严重" : "注意"}</Pill>
              </span>
              <small>{described.detail}</small>
            </div>
          </div>
        );
      })}
      {findings.length > 0 && (
        <button className={`${styles.setRow} ${styles.setRowCommand}`} onClick={onOpenStorage}>
          在存储中查看磁盘与数据卷
        </button>
      )}
    </Group>
  );
}

// DeviceArt draws the device from the front with a bay for each disk that is
// not the system disk; a bay's light follows its disk's health.
function DeviceArt({ disks }: { disks: Disk[] }) {
  const id = useId().replace(/[^\w-]/g, "");
  const drives = disks.filter((disk) => disk.role !== "system").slice(0, 6);
  const bays = Math.max(drives.length, 2);
  const gap = 7;
  const width = (148 - gap * (bays - 1)) / bays;
  return (
    <svg className={styles.ovArt} viewBox="0 0 232 166" aria-hidden="true">
      <defs>
        <linearGradient id={`${id}-body`} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0" stopColor="#465263" />
          <stop offset="0.5" stopColor="#252d3a" />
          <stop offset="1" stopColor="#151b24" />
        </linearGradient>
        <linearGradient id={`${id}-bay`} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0" stopColor="#5d697b" />
          <stop offset="1" stopColor="#343e4d" />
        </linearGradient>
        <linearGradient id={`${id}-sheen`} x1="0" y1="0" x2="0.9" y2="1">
          <stop offset="0" stopColor="#fff" stopOpacity="0.18" />
          <stop offset="0.45" stopColor="#fff" stopOpacity="0" />
        </linearGradient>
        <radialGradient id={`${id}-floor`}>
          <stop offset="0" stopColor="#0f172a" stopOpacity="0.3" />
          <stop offset="1" stopColor="#0f172a" stopOpacity="0" />
        </radialGradient>
      </defs>
      <ellipse cx="116" cy="150" rx="104" ry="10" fill={`url(#${id}-floor)`} />
      <rect x="54" y="142" width="22" height="5" rx="2" fill="#0c1016" />
      <rect x="156" y="142" width="22" height="5" rx="2" fill="#0c1016" />
      <rect x="26" y="10" width="180" height="134" rx="22" fill={`url(#${id}-body)`} />
      <rect x="26" y="10" width="180" height="134" rx="22" fill={`url(#${id}-sheen)`} />
      <rect x="26.5" y="10.5" width="179" height="133" rx="21.5" fill="none" stroke="rgba(255,255,255,.16)" />
      {Array.from({ length: bays }, (_, index) => {
        const disk = drives.at(index);
        const x = 42 + index * (width + gap);
        const middle = x + width / 2;
        const tone = disk ? (styles[disk.health] ?? "") : "";
        return (
          <g key={disk?.id ?? index}>
            <rect x={x} y="26" width={width} height="86" rx="8" fill={disk ? `url(#${id}-bay)` : "#141a23"} />
            <rect
              x={x + 0.5}
              y="26.5"
              width={width - 1}
              height="85"
              rx="7.5"
              fill="none"
              stroke={disk ? "rgba(255,255,255,.14)" : "rgba(255,255,255,.06)"}
            />
            {disk && (
              <>
                <rect x={middle - 9} y="34" width="18" height="4" rx="2" fill="rgba(255,255,255,.24)" />
                <path
                  d={[52, 58, 64, 70, 76].map((y) => `M${x + 9} ${y}H${x + width - 9}`).join("")}
                  stroke="rgba(10,14,20,.34)"
                  strokeWidth="1.6"
                  strokeLinecap="round"
                />
                <circle className={`${styles.ovLed} ${styles.ovLedGlow} ${tone}`} cx={middle} cy="98" r="6.5" />
                <circle className={`${styles.ovLed} ${tone}`} cx={middle} cy="98" r="2.4" />
              </>
            )}
          </g>
        );
      })}
      <g className={styles.ovPower}>
        <circle cx="48" cy="128" r="6.5" fill="#60a5fa" opacity="0.3" />
        <circle cx="48" cy="128" r="2.4" fill="#8cc2ff" />
      </g>
      <text
        x="190"
        y="131"
        textAnchor="end"
        fontSize="8.5"
        fontWeight="700"
        letterSpacing="1.8"
        fill="rgba(255,255,255,.36)"
      >
        A-NAS
      </text>
    </svg>
  );
}

// The health covers disk health, temperatures and the data volume; it stays
// unknown until the disks have been read (docs/specs/system-health.md).
function healthPresentation(health: Health): { label: string; tone: Tone } {
  return {
    healthy: { label: "运行正常", tone: "success" as Tone },
    warning: { label: "需要注意", tone: "warning" as Tone },
    critical: { label: "严重故障", tone: "danger" as Tone },
    unknown: { label: "健康未评估", tone: "neutral" as Tone },
  }[health];
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
