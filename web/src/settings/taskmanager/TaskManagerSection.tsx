import {
  Activity,
  ArrowDown,
  ArrowUp,
  CircuitBoard,
  Cpu,
  HardDrive,
  Layers,
  MemoryStick,
  Network as NetworkIcon,
  Search,
  Thermometer,
} from "lucide-react";
import { type CSSProperties, type ReactNode, useId, useState } from "react";

import { formatBytes, formatRate } from "../../lib/bytes";
import { Banner } from "../parts";
import styles from "../settings.module.css";
import { FanSpeed, fanName, TemperatureGauge } from "../Temperature";
import {
  type Availability,
  type DeviceSnapshot,
  type Disk,
  type Group,
  readDevice,
  readTasks,
  type Task,
  type Temperature,
} from "./api";
import { TaskIcon } from "./TaskIcon";
import { type SnapshotView, useSnapshot } from "./useSnapshot";

const deviceTabs = ["总览", "CPU", "GPU", "NPU", "内存", "网络", "硬盘"] as const;
type DeviceTab = (typeof deviceTabs)[number];
const percent = (value: number | null) => (value === null ? "—" : `${value.toFixed(1)}%`);
const size = (value: number | null) => (value === null ? "—" : formatBytes(value));
const rate = (value: number | null) => (value === null ? "—" : formatRate(value));
const celsius = (value: { celsius: number } | null) => (value === null ? "—" : `${value.celsius} °C`);
const diskKind = (disk: Disk) => (disk.rotational ? "机械硬盘" : "固态硬盘");
const memoryPercent = (data: DeviceSnapshot) =>
  data.memory.totalBytes > 0 ? (data.memory.usedBytes / data.memory.totalBytes) * 100 : null;
const availabilityText: Record<Exclude<Availability, "available">, string> = {
  partial: "部分数据暂时不可用",
  unavailable: "暂时无法读取",
  unsupported: "当前系统不支持采集",
  absent: "未检测到设备",
};

export function TaskManagerSection() {
  const [section, setSection] = useState("设备运行状态");
  return (
    <>
      <header className={styles.setHeader}>
        <h2>任务管理器</h2>
      </header>
      <Choices label="任务管理器板块" items={["设备运行状态", "服务与进程"]} value={section} onChange={setSection} />
      {section === "设备运行状态" ? <DevicePanel /> : <TasksPanel />}
    </>
  );
}

function Choices<T extends string>({
  label,
  items,
  value,
  onChange,
}: {
  label: string;
  items: readonly T[];
  value: T;
  onChange: (next: T) => void;
}) {
  return (
    <nav className={styles.tmChoices} aria-label={label}>
      {items.map((item) => (
        <button key={item} aria-pressed={item === value} onClick={() => onChange(item)}>
          {item}
        </button>
      ))}
    </nav>
  );
}
function Pending({ error, retry }: { error: string; retry: () => void }) {
  return (
    <div className={`${styles.setCard} ${styles.setState}`} role="status">
      {error ? (
        <>
          <Activity />
          <h4>暂时无法读取</h4>
          <p>{error}</p>
          <button className={`${styles.setBtn} primary`} onClick={retry}>
            重试
          </button>
        </>
      ) : (
        <>
          <span className="loader" />
          <p>正在读取运行状态…</p>
        </>
      )}
    </div>
  );
}
function ReadError({ error, retry }: { error: string; retry: () => void }) {
  return error ? (
    <Banner tone="error">
      连接中断，正在显示上次成功读取的数据{" "}
      <button className={styles.setBtn} onClick={retry}>
        重试
      </button>
    </Banner>
  ) : null;
}
function AvailabilityNote({ status }: { status: Availability }) {
  return status === "available" ? null : (
    <p className={styles.tmAvailability} role="status">
      {availabilityText[status]}
    </p>
  );
}
function DevicePanel() {
  const view = useSnapshot(readDevice);
  const [tab, setTab] = useState<DeviceTab>("总览");
  return (
    <section className={styles.tmPanel} aria-label="设备运行状态">
      <Choices label="设备资源" items={deviceTabs} value={tab} onChange={setTab} />
      {!view.data ? (
        <Pending {...view} />
      ) : (
        <>
          <ReadError error={view.error} retry={view.retry} />
          <DeviceDetails tab={tab} view={view as SnapshotView<DeviceSnapshot> & { data: DeviceSnapshot }} />
        </>
      )}
    </section>
  );
}

function aggregate(data: DeviceSnapshot, kind: "receive" | "transmit" | "read" | "write"): number | null {
  const group = kind === "read" || kind === "write" ? data.disks : data.network;
  if (group.status === "unavailable" || group.status === "unsupported" || !group.items.length) return null;
  if (kind === "read" || kind === "write") {
    const values = data.disks.items.map((item) =>
      kind === "read" ? item.readBytesPerSecond : item.writeBytesPerSecond,
    );
    return values.some((value) => value === null) ? null : values.reduce<number>((sum, value) => sum + value!, 0);
  }
  const physical = data.network.items.filter((item) => item.physical);
  const values = (physical.length ? physical : data.network.items).map((item) =>
    kind === "receive" ? item.receiveBytesPerSecond : item.transmitBytesPerSecond,
  );
  return values.some((value) => value === null) ? null : values.reduce<number>((sum, value) => sum + value!, 0);
}
function DeviceDetails({
  tab,
  view: { data, history },
}: {
  tab: DeviceTab;
  view: SnapshotView<DeviceSnapshot> & { data: DeviceSnapshot };
}) {
  const cpu = (
    <Metric
      title="CPU 使用率"
      value={percent(data.cpu.usagePercent)}
      times={history.map((point) => Date.parse(point.observedAt))}
      values={history.map((point) => point.cpu.usagePercent)}
      max={100}
      icon={<Cpu />}
      detail={`${data.cpu.logicalCores} 个逻辑核心`}
    />
  );
  const memory = (
    <Metric
      title="内存使用率"
      tone="violet"
      value={percent(memoryPercent(data))}
      times={history.map((point) => Date.parse(point.observedAt))}
      values={history.map(memoryPercent)}
      max={100}
      icon={<MemoryStick />}
      detail={`${size(data.memory.usedBytes)} / ${size(data.memory.totalBytes)}`}
    />
  );
  if (tab === "总览")
    return (
      <>
        <div className={`${styles.tmGrid} tm-overview-grid`}>
          {cpu}
          {memory}
          <Metric
            title="网络"
            value={rate(aggregate(data, "receive"))}
            valueLabel="接收"
            secondary={{
              label: "发送",
              value: rate(aggregate(data, "transmit")),
              values: history.map((point) => aggregate(point, "transmit")),
            }}
            times={history.map((point) => Date.parse(point.observedAt))}
            values={history.map((point) => aggregate(point, "receive"))}
            icon={<NetworkIcon />}
            tone="teal"
            detail={data.network.status === "partial" ? "数据不完整" : undefined}
          />
          <Metric
            title="硬盘"
            value={rate(aggregate(data, "read"))}
            valueLabel="读取"
            secondary={{
              label: "写入",
              value: rate(aggregate(data, "write")),
              values: history.map((point) => aggregate(point, "write")),
            }}
            times={history.map((point) => Date.parse(point.observedAt))}
            values={history.map((point) => aggregate(point, "read"))}
            icon={<HardDrive />}
            tone="amber"
            detail={data.disks.status === "partial" ? "数据不完整" : undefined}
          />
        </div>
        <Temperatures data={data} />
        <div className={styles.tmHardwareStrip}>
          {(["gpu", "npu"] as const).map((kind) => (
            <div key={kind} className={styles.tmHardware}>
              <CircuitBoard />
              <div>
                <h3>{kind.toUpperCase()}</h3>
                <AvailabilityNote status={data[kind].status} />
                {data[kind].items.map((item) => (
                  <p key={item.id}>
                    <strong>{item.name}</strong> ·{" "}
                    {item.usagePercent === null ? acceleratorMissing(item.usageStatus) : percent(item.usagePercent)}
                  </p>
                ))}
              </div>
            </div>
          ))}
        </div>
        {(data.network.status !== "available" || data.disks.status !== "available") && (
          <div className={styles.tmNotes}>
            {data.network.status !== "available" && (
              <div>
                网络
                <AvailabilityNote status={data.network.status} />
              </div>
            )}
            {data.disks.status !== "available" && (
              <div>
                硬盘
                <AvailabilityNote status={data.disks.status} />
              </div>
            )}
          </div>
        )}
      </>
    );
  if (tab === "CPU")
    return (
      <>
        <ResourceTitle
          title={data.cpu.model || "CPU"}
          detail={`${data.cpu.logicalCores} 个逻辑核心 · 整机总容量为 100%`}
        />
        {cpu}
        <Metric
          title="CPU 温度"
          tone="rose"
          value={celsius(data.cpu.temperature)}
          times={history.map((point) => Date.parse(point.observedAt))}
          values={history.map((point) => point.cpu.temperature?.celsius ?? null)}
          max={100}
          scale="0–100 °C"
          icon={<Thermometer />}
          detail={data.cpu.temperature ? `${data.cpu.temperature.hotCelsius} °C 以上偏高` : "未检测到 CPU 温度传感器"}
        />
      </>
    );
  if (tab === "内存")
    return (
      <>
        {memory}
        <div className={`${styles.setCard} ${styles.tmFacts}`}>
          <Fact label="总内存" value={size(data.memory.totalBytes)} />
          <Fact label="已使用" value={size(data.memory.usedBytes)} />
          <Fact
            label="可用内存"
            value={data.memory.totalBytes > 0 ? size(Math.max(0, data.memory.totalBytes - data.memory.usedBytes)) : "—"}
          />
        </div>
        <p className={styles.setFootnote}>可回收缓存计入可用内存。</p>
      </>
    );
  if (tab === "GPU" || tab === "NPU")
    return <Accelerators kind={tab === "GPU" ? "gpu" : "npu"} data={data} history={history} />;
  if (tab === "网络")
    return (
      <>
        <AvailabilityNote status={data.network.status} />
        {data.network.status === "available" && !data.network.items.length && (
          <p className={styles.tmAvailability}>未检测到网络接口</p>
        )}
        {data.network.items.map((item) => (
          <section className={styles.tmResource} key={item.id}>
            <ResourceTitle title={item.name} detail={item.physical ? "物理接口" : "虚拟接口"} />
            <div className={styles.tmGrid}>
              <Metric
                title="接收速度"
                value={rate(item.receiveBytesPerSecond)}
                times={history.map((point) => Date.parse(point.observedAt))}
                values={history.map(
                  (point) =>
                    point.network.items.find((network) => network.id === item.id)?.receiveBytesPerSecond ?? null,
                )}
                detail={`累计接收 ${size(item.receiveBytes)}`}
              />
              <Metric
                title="发送速度"
                value={rate(item.transmitBytesPerSecond)}
                times={history.map((point) => Date.parse(point.observedAt))}
                values={history.map(
                  (point) =>
                    point.network.items.find((network) => network.id === item.id)?.transmitBytesPerSecond ?? null,
                )}
                detail={`累计发送 ${size(item.transmitBytes)}`}
              />
            </div>
          </section>
        ))}
      </>
    );
  return (
    <>
      <AvailabilityNote status={data.disks.status} />
      {data.disks.status === "available" && !data.disks.items.length && (
        <p className={styles.tmAvailability}>未检测到硬盘</p>
      )}
      {data.disks.items.map((item) => (
        <section className={styles.tmResource} key={item.id}>
          <ResourceTitle
            title={item.model || "硬盘"}
            detail={`${size(item.capacityBytes)} · ${diskKind(item)} · ${item.id}`}
            aside={
              item.temperature ? (
                <TemperatureGauge temperature={item.temperature} label={item.model || "硬盘"} />
              ) : (
                <span className={styles.tempMissing}>暂无温度读数</span>
              )
            }
          />
          <div className={styles.tmGrid}>
            <Metric
              title="读取速度"
              value={rate(item.readBytesPerSecond)}
              times={history.map((point) => Date.parse(point.observedAt))}
              values={history.map(
                (point) => point.disks.items.find((disk) => disk.id === item.id)?.readBytesPerSecond ?? null,
              )}
            />
            <Metric
              title="写入速度"
              value={rate(item.writeBytesPerSecond)}
              times={history.map((point) => Date.parse(point.observedAt))}
              values={history.map(
                (point) => point.disks.items.find((disk) => disk.id === item.id)?.writeBytesPerSecond ?? null,
              )}
            />
          </div>
        </section>
      ))}
    </>
  );
}
function ResourceTitle({ title, detail, aside }: { title: string; detail: string; aside?: ReactNode }) {
  return (
    <header className={styles.tmResourceTitle}>
      <div>
        <h3>{title}</h3>
        <p>{detail}</p>
      </div>
      {aside}
    </header>
  );
}
// Temperatures shows every sensor at a glance: the CPU, each GPU, each disk
// and each fan (docs/specs/device-temperatures.md).
function Temperatures({ data }: { data: DeviceSnapshot }) {
  const tiles: Array<{ id: string; name: string; kind: string; temperature: Temperature | null; missing: string }> = [
    {
      id: "cpu",
      name: "CPU",
      kind: "处理器",
      temperature: data.cpu.temperature,
      missing: "未检测到传感器",
    },
    ...data.gpu.items.map((gpu) => ({
      id: gpu.id,
      name: gpu.integrated ? "核显" : gpu.name,
      kind: gpu.integrated ? "集成显卡" : "GPU",
      temperature: gpu.integrated ? null : gpu.temperature,
      missing: gpu.integrated ? "与 CPU 相同" : "驱动不提供",
    })),
    ...data.disks.items.map((disk) => ({
      id: disk.id,
      name: disk.model || "硬盘",
      kind: diskKind(disk),
      temperature: disk.temperature,
      missing: "暂无读数",
    })),
  ];
  const fans = data.fans.items;
  const title = fans.length ? "温度与风扇" : "温度";
  return (
    <section className={styles.tmThermal} aria-label={title}>
      <h3>
        <Thermometer />
        {title}
      </h3>
      <div>
        {tiles.map((tile) => (
          <div key={tile.id} className={styles.tmThermalTile}>
            <span title={tile.name}>{tile.name}</span>
            <small>{tile.kind}</small>
            {tile.temperature ? (
              <TemperatureGauge temperature={tile.temperature} label={tile.name} />
            ) : (
              <span className={styles.tempMissing}>{tile.missing}</span>
            )}
          </div>
        ))}
        {fans.map((fan) => (
          <div key={fan.id} className={styles.tmThermalTile}>
            <span>{fanName(fan)}</span>
            <small className={fan.stopped ? styles.fanStoppedNote : undefined}>{fan.stopped ? "已停转" : "转速"}</small>
            <FanSpeed fan={fan} />
          </div>
        ))}
      </div>
    </section>
  );
}
function Fact({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <span>{label}</span>
      <strong>{value}</strong>
    </div>
  );
}
function acceleratorMissing(status: Availability) {
  return status === "unavailable" ? "暂时无法读取" : status === "unsupported" ? "当前驱动不提供此指标" : "暂无采样数据";
}
function Accelerators({
  kind,
  data,
  history,
}: {
  kind: "gpu" | "npu";
  data: DeviceSnapshot;
  history: DeviceSnapshot[];
}) {
  const group = data[kind];
  return (
    <>
      <AvailabilityNote status={group.status} />
      {group.items.map((item) => (
        <section className={styles.tmResource} key={item.id}>
          <ResourceTitle title={item.name} detail={item.driver ? `驱动 ${item.driver}` : "未识别驱动"} />
          <Metric
            title={`${kind.toUpperCase()} 使用率`}
            value={percent(item.usagePercent)}
            times={history.map((point) => Date.parse(point.observedAt))}
            values={history.map(
              (point) => point[kind].items.find((device) => device.id === item.id)?.usagePercent ?? null,
            )}
            max={100}
            detail={item.usagePercent === null ? acceleratorMissing(item.usageStatus) : undefined}
          />
          <div className={`${styles.setCard} ${styles.tmFacts}`}>
            {kind === "gpu" && (
              <Fact
                label="温度"
                value={
                  item.integrated
                    ? `与 CPU 相同${data.cpu.temperature ? `（${celsius(data.cpu.temperature)}）` : ""}`
                    : item.temperature
                      ? celsius(item.temperature)
                      : "当前驱动不提供温度"
                }
              />
            )}
            <Fact
              label={kind === "gpu" ? "已用显存" : "已用设备内存"}
              value={
                item.memoryUsedBytes === null ? acceleratorMissing(item.memoryUsedStatus) : size(item.memoryUsedBytes)
              }
            />
            <Fact
              label="设备内存总量"
              value={
                item.memoryTotalBytes === null
                  ? acceleratorMissing(item.memoryTotalStatus)
                  : size(item.memoryTotalBytes)
              }
            />
          </div>
        </section>
      ))}
    </>
  );
}

function Metric({
  title,
  value,
  detail,
  values,
  times,
  max,
  icon,
  tone = "blue",
  valueLabel,
  secondary,
  scale = "0–100%",
}: {
  title: string;
  value: string;
  detail?: string;
  values: Array<number | null>;
  times: number[];
  max?: number;
  scale?: string;
  icon?: ReactNode;
  tone?: string;
  valueLabel?: string;
  secondary?: { label: string; value: string; values: Array<number | null> };
}) {
  const gradient = useId();
  const ceiling =
    max ?? Math.max(1, ...values.map((point) => point ?? 0), ...(secondary?.values ?? []).map((point) => point ?? 0));
  const points = values.slice(-60);
  const timestamps = times.slice(-60);
  function paths(series: Array<number | null>) {
    const result: string[] = [];
    let current = "";
    series.slice(-60).forEach((point, index) => {
      if (point === null || !Number.isFinite(point)) {
        if (current) result.push(current);
        current = "";
        return;
      }
      if (index > 0 && timestamps[index] - timestamps[index - 1] > 3000) {
        if (current) result.push(current);
        current = "";
      }
      current += `${current ? " L" : "M"}${(((60 - points.length + index) / 59) * 600).toFixed(2)},${(100 - Math.min(1, Math.max(0, point) / ceiling) * 94).toFixed(2)}`;
    });
    if (current) result.push(current);
    return result;
  }
  const lines = paths(values);
  const fills = lines
    .map((path) => `${path} L${path.match(/([\d.]+),[\d.]+$/)?.[1]},100 L${path.match(/^M([\d.]+)/)?.[1]},100 Z`)
    .join(" ");
  return (
    <article className={`${styles.tmMetric} ${styles[`tm-tone-${tone}`] ?? ""}`}>
      <header>
        <span>
          {icon}
          {title}
        </span>
        {max && <span className={styles.tmChartScale}>{scale}</span>}
      </header>
      <div className={styles.tmValues}>
        <div>
          {valueLabel && (
            <span>
              <i />
              {valueLabel}
            </span>
          )}
          <strong>{value}</strong>
        </div>
        {secondary && (
          <div className={styles.tmSecondaryValue}>
            <span>
              <i />
              {secondary.label}
            </span>
            <strong>{secondary.value}</strong>
          </div>
        )}
      </div>
      {detail && <p>{detail}</p>}
      <svg
        viewBox="0 0 604 106"
        role="img"
        aria-label={`${title}，最近 ${points.length} 个采样的趋势`}
        preserveAspectRatio="none"
      >
        <defs>
          <linearGradient id={gradient} x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stopColor="currentColor" stopOpacity=".13" />
            <stop offset="100%" stopColor="currentColor" stopOpacity=".01" />
          </linearGradient>
        </defs>
        <path className={styles.tmGridline} d="M0 6H604 M0 53H604 M0 100H604" />
        <path d={fills} fill={`url(#${gradient})`} />
        <path className={styles.tmTrend} d={lines.join(" ")} />
        {points.at(-1) != null && (
          <circle
            cx="600"
            cy={100 - Math.min(1, Math.max(0, points.at(-1)!) / ceiling) * 94}
            r="2"
            fill="currentColor"
          />
        )}
        {secondary && (
          <path className={`${styles.tmTrend} ${styles.tmSecondaryTrend}`} d={paths(secondary.values).join(" ")} />
        )}
      </svg>
      <footer>
        <span>
          {points.filter((point) => point !== null).length < 2 ? "等待更多有效采样" : `最近 ${points.length} 次采样`}
        </span>
        <span>现在</span>
      </footer>
    </article>
  );
}

const columns = [
  { key: "cpuPercent", label: "CPU 占用", format: percent },
  { key: "memoryBytes", label: "内存占用", format: size },
  { key: "readBytesPerSecond", label: "硬盘读取速度", format: rate },
  { key: "writeBytesPerSecond", label: "硬盘写入速度", format: rate },
] as const;
type SortKey = (typeof columns)[number]["key"];
function TasksPanel() {
  const view = useSnapshot(readTasks, 1);
  const [kind, setKind] = useState("运行中的服务");
  return (
    <section className={styles.tmPanel} aria-label="服务与进程">
      {!view.data ? (
        <Pending {...view} />
      ) : (
        <>
          <ReadError error={view.error} retry={view.retry} />
          <div className={styles.tmWorkloadSummary}>
            <div className={styles.tmServiceCount}>
              <Activity />
              <div>
                <strong>
                  {view.data.services.status === "available"
                    ? view.data.services.items.length
                    : view.data.services.status === "partial"
                      ? `至少 ${view.data.services.items.length}`
                      : "—"}
                </strong>
                <span>运行中的系统服务</span>
              </div>
            </div>
            <div className={styles.tmServiceCount}>
              <Layers />
              <div>
                <strong>
                  {view.data.processes.status === "available"
                    ? view.data.processes.items.length
                    : view.data.processes.status === "partial"
                      ? `至少 ${view.data.processes.items.length}`
                      : "—"}
                </strong>
                <span>进程</span>
              </div>
            </div>
          </div>
          <TaskTable
            icons={view.data.icons}
            navigation={<Choices label="任务列表" items={["运行中的服务", "进程"]} value={kind} onChange={setKind} />}
            key={kind}
            group={kind === "进程" ? view.data.processes : view.data.services}
            processes={kind === "进程"}
          />
        </>
      )}
    </section>
  );
}
function TaskTable({
  group,
  processes,
  navigation,
  icons,
}: {
  group: Group<Task>;
  processes: boolean;
  navigation: ReactNode;
  icons?: Record<string, string>;
}) {
  const [query, setQuery] = useState("");
  const [sort, setSort] = useState<{ key: SortKey; descending: boolean }>({ key: "cpuPercent", descending: true });
  const needle = query.trim().toLocaleLowerCase();
  const rows = group.items
    .filter(
      (item) => item.name.toLocaleLowerCase().includes(needle) || (processes && String(item.pid).includes(needle)),
    )
    .sort((a, b) => {
      const left = a[sort.key],
        right = b[sort.key];
      if (left === null) return right === null ? a.name.localeCompare(b.name) : 1;
      if (right === null) return -1;
      return (sort.descending ? right - left : left - right) || a.name.localeCompare(b.name);
    });
  const readable = group.status === "available" || group.status === "partial";
  return (
    <>
      <div className={styles.tmToolbar}>
        {navigation}
        <label className={styles.tmSearch}>
          <Search />
          <input
            type="search"
            aria-label={processes ? "搜索进程名称或 PID" : "搜索服务名称"}
            placeholder={processes ? "搜索名称或 PID" : "搜索服务名称"}
            value={query}
            onChange={(event) => setQuery(event.target.value)}
          />
        </label>
        <span>{readable ? `${rows.length} / ${group.items.length} 项` : "数量不可用"}</span>
      </div>
      <AvailabilityNote status={group.status} />
      {(readable || rows.length > 0) && (
        <div
          className={`${styles.setCard} ${styles.tmTableScroll}`}
          tabIndex={0}
          role="region"
          aria-label={processes ? "进程资源用量" : "服务资源用量"}
        >
          <table className={styles.tmTable}>
            <thead>
              <tr>
                <th scope="col">名称</th>
                <th scope="col">{processes ? "PID" : "运行状态"}</th>
                {columns.map((column) => (
                  <th
                    scope="col"
                    key={column.key}
                    aria-sort={sort.key === column.key ? (sort.descending ? "descending" : "ascending") : "none"}
                  >
                    <button
                      title={column.key === "cpuPercent" ? "以整机 CPU 总容量为 100%" : undefined}
                      onClick={() =>
                        setSort((previous) => ({
                          key: column.key,
                          descending: previous.key === column.key ? !previous.descending : true,
                        }))
                      }
                    >
                      {column.label}
                      {sort.key === column.key && (sort.descending ? <ArrowDown /> : <ArrowUp />)}
                    </button>
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map((item) => (
                <tr key={item.id}>
                  <td title={item.name}>
                    <span className={styles.tmTaskName}>
                      <TaskIcon task={item} icons={icons} processes={processes} />
                      {item.name}
                    </span>
                  </td>
                  <td>
                    {processes ? (
                      item.pid
                    ) : (
                      <span className={item.state === "running" ? styles.tmRunning : ""}>
                        {item.state === "running" ? "运行中" : item.state}
                      </span>
                    )}
                  </td>
                  {columns.map((column) => (
                    <td key={column.key}>
                      <span
                        className={column.key === "cpuPercent" ? styles.tmCpuCell : ""}
                        style={
                          column.key === "cpuPercent"
                            ? ({ "--usage": `${Math.min(100, Math.max(0, item.cpuPercent ?? 0))}%` } as CSSProperties)
                            : undefined
                        }
                      >
                        {column.format(item[column.key])}
                      </span>
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
          {rows.length === 0 && (
            <p className={styles.tmEmpty}>
              {needle
                ? processes
                  ? "没有匹配的进程"
                  : "没有匹配的服务"
                : group.status === "partial"
                  ? "当前未获取到列表，数据不完整"
                  : processes
                    ? "没有可显示的进程"
                    : "没有运行中的服务"}
            </p>
          )}
        </div>
      )}
    </>
  );
}
