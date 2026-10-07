import { ArrowDown, ArrowUp } from "lucide-react";
import { useEffect, useId, useState } from "react";

import { HostState } from "./api";
import { HostStateView } from "./useHostState";
import { useHostMetrics } from "./useHostMetrics";

// Gauge geometry: a 210° speedometer arc centred at (60, 56) in a 120×84 box;
// the open bottom keeps the needle clear of the numeric readout.
const center = { x: 60, y: 56 };
const radius = 44;
const sweep = 210;
const startAngle = -sweep / 2;

export function StatusBar({ host }: { host: HostStateView }) {
  const { metrics, disconnected: metricsDisconnected } = useHostMetrics();
  const disconnected = host.disconnected || metricsDisconnected;
  const connection = disconnected ? "offline" : host.snapshot ? "online" : "pending";
  const connectionLabel = { offline: "连接中断", online: "设备在线", pending: "正在连接" }[connection];
  const memory = metrics?.memory;

  return (
    <header className={`status-bar ${metricsDisconnected ? "stale" : ""}`} aria-label="设备状态">
      <div className="status-gauges">
        <Gauge
          label="CPU"
          name="CPU 占用"
          percent={metrics?.cpu.usagePercent}
          detail={metrics ? `${metrics.cpu.logicalCores} 核` : "—"}
        />
        <Gauge
          label="内存"
          name="内存占用"
          percent={memory ? (memory.usedBytes / memory.totalBytes) * 100 : undefined}
          detail={memory ? `${formatGiB(memory.usedBytes)} / ${formatGiB(memory.totalBytes)} GiB` : "—"}
        />
      </div>
      <span className="status-divider" aria-hidden="true" />
      <div className="net-rates" role="group" aria-label="网络速率">
        <NetworkRate direction="down" label="下行" bytesPerSecond={metrics?.network.receiveBytesPerSecond} />
        <NetworkRate direction="up" label="上行" bytesPerSecond={metrics?.network.transmitBytesPerSecond} />
      </div>
      <span className="status-divider" aria-hidden="true" />
      <div className="status-clock">
        <Clock />
        <div className="status-meta">
          <span className={`status-dot ${connection}`} title={connectionLabel}><span className="sr-only">{connectionLabel}</span></span>
          {disconnected ? <span className="source-badge offline">连接中断</span> : <SourceBadge state={host.snapshot} />}
        </div>
      </div>
    </header>
  );
}

export function SourceBadge({ state }: { state?: HostState }) {
  if (!state) return <span className="source-badge pending">等待数据</span>;
  return <span className={`source-badge ${state.dataSource}`}>{state.dataSource === "live" ? "实时主机" : "模拟数据"}</span>;
}

function Gauge({ label, name, percent, detail }: { label: string; name: string; percent?: number; detail: string }) {
  const gradientID = `gauge-${useId().replace(/[^\w-]/g, "")}`;
  const value = percent === undefined ? 0 : Math.min(100, Math.max(0, percent));
  const level = percent === undefined ? "idle" : value >= 85 ? "high" : value >= 60 ? "warn" : "ok";
  const start = pointAt(startAngle, radius);
  const end = pointAt(-startAngle, radius);
  const arc = `M ${start.x} ${start.y} A ${radius} ${radius} 0 1 1 ${end.x} ${end.y}`;

  return (
    <div
      className={`gauge gauge-${level}`}
      role="meter"
      aria-label={name}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={percent === undefined ? undefined : Math.round(value)}
      aria-valuetext={percent === undefined ? "暂无数据" : `${Math.round(value)}%`}
    >
      <svg viewBox="0 0 120 84" aria-hidden="true">
        <defs>
          <linearGradient id={gradientID} x1="0" y1="0" x2="1" y2="0">
            <stop offset="0%" stopColor="#4ade80" />
            <stop offset="55%" stopColor="#facc15" />
            <stop offset="100%" stopColor="#fb7185" />
          </linearGradient>
        </defs>
        <path className="gauge-track" d={arc} pathLength={100} />
        <path className="gauge-value" d={arc} pathLength={100} stroke={`url(#${gradientID})`} strokeDasharray={`${value} 100`} />
        {Array.from({ length: 11 }, (_, index) => {
          const angle = startAngle + (sweep / 10) * index;
          const major = index % 5 === 0;
          const outer = pointAt(angle, radius - 9);
          const inner = pointAt(angle, radius - (major ? 15 : 12));
          return <line key={index} className={`gauge-tick ${major ? "major" : ""}`} x1={inner.x} y1={inner.y} x2={outer.x} y2={outer.y} />;
        })}
        <g className="gauge-needle" style={{ transform: `rotate(${startAngle + (sweep * value) / 100}deg)` }}>
          <line x1={center.x} y1={center.y + 6} x2={center.x} y2={center.y - radius + 17} />
        </g>
        <circle className="gauge-hub" cx={center.x} cy={center.y} r={4.5} />
        <text className="gauge-readout" x={center.x} y={82}>
          {percent === undefined ? "—" : Math.round(value)}
          {percent !== undefined && <tspan className="gauge-unit">%</tspan>}
        </text>
      </svg>
      <div className="gauge-caption"><span>{label}</span><small>{detail}</small></div>
    </div>
  );
}

function NetworkRate({ direction, label, bytesPerSecond }: { direction: "up" | "down"; label: string; bytesPerSecond?: number }) {
  const [value, unit] = bytesPerSecond === undefined ? ["—", ""] : formatRate(bytesPerSecond);
  const Icon = direction === "down" ? ArrowDown : ArrowUp;
  return (
    <div className={`net-row ${direction}`}>
      <Icon aria-hidden="true" />
      <span className="sr-only">{label}</span>
      <span className="net-value">{value}</span>
      <span className="net-unit">{unit}</span>
    </div>
  );
}

function Clock() {
  const now = useNow();
  const time = new Intl.DateTimeFormat("zh-CN", { hour: "2-digit", minute: "2-digit", hour12: false }).format(now);
  const date = `${now.getMonth() + 1}月${now.getDate()}日 周${"日一二三四五六"[now.getDay()]}`;
  return <time className="status-time" dateTime={now.toISOString()}><strong>{time}</strong><span>{date}</span></time>;
}

// Re-renders on each minute boundary so the clock never drifts behind.
function useNow(): Date {
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    let timer: number;
    const schedule = () => {
      timer = window.setTimeout(() => {
        setNow(new Date());
        schedule();
      }, 60_000 - (Date.now() % 60_000) + 50);
    };
    schedule();
    return () => window.clearTimeout(timer);
  }, []);
  return now;
}

function pointAt(angleDegrees: number, distance: number) {
  const radians = (angleDegrees * Math.PI) / 180;
  return { x: round(center.x + distance * Math.sin(radians)), y: round(center.y - distance * Math.cos(radians)) };
}

function round(value: number): number {
  return Math.round(value * 100) / 100;
}

export function formatRate(bytesPerSecond: number): [string, string] {
  const units = ["B/s", "KB/s", "MB/s", "GB/s"];
  let value = bytesPerSecond;
  let unit = 0;
  while (value >= 1000 && unit < units.length - 1) {
    value /= 1000;
    unit += 1;
  }
  const digits = unit === 0 || value >= 100 ? 0 : 1;
  return [value.toFixed(digits), units[unit]];
}

function formatGiB(bytes: number): string {
  return (bytes / 1024 ** 3).toFixed(1);
}
