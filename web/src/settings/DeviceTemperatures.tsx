import { CircuitBoard, Cpu, HardDrive, type LucideIcon } from "lucide-react";

import type { Temperature } from "../api.generated";
import type { HostState } from "../desktop/api";
import { Group, IconTile, Pill } from "./parts";
import styles from "./settings.module.css";
import { TemperatureGauge, temperatureLevel } from "./Temperature";

type Disk = HostState["disks"][number];

const diskRole: Record<Disk["role"], string> = { system: "系统盘", data: "数据盘", unassigned: "未使用" };

// diskTemperature is a disk's reading with the limit the system health
// applies to its kind, when it has one.
export function diskTemperature(disk: Disk): Temperature | undefined {
  return disk.temperatureCelsius === undefined || disk.temperatureHotCelsius === undefined
    ? undefined
    : { celsius: disk.temperatureCelsius, hotCelsius: disk.temperatureHotCelsius };
}

// DeviceTemperatures lists what the CPU, GPU and disk sensors read, in the
// overview (docs/specs/device-temperatures.md).
export function DeviceTemperatures({ snapshot }: { snapshot: HostState }) {
  const thermal = snapshot.system.thermal;
  const disks = snapshot.disks.filter((disk) => disk.model);
  const readings = [
    thermal?.cpu,
    ...(thermal?.gpus ?? []).map((gpu) => gpu.temperature),
    ...disks.map(diskTemperature),
  ].filter((reading) => reading !== undefined);
  const hot = readings.filter((reading) => temperatureLevel(reading) === "hot").length;
  const cpuHot = thermal?.cpu !== undefined && temperatureLevel(thermal.cpu) === "hot";
  return (
    <Group
      title="设备温度"
      aside={hot ? `${hot} 项偏高` : undefined}
      footnote="NVMe 固态硬盘的温度随时读取；其他硬盘的温度来自 SMART，每 10 分钟读取一次，不会唤醒待机的硬盘。"
    >
      <TemperatureRow
        icon={Cpu}
        tone="cpu"
        title="处理器"
        detail={cpuHot ? "接近降频的温度，请检查散热器和机箱风扇" : "处理器封装"}
        temperature={thermal?.cpu}
        missing={thermal ? "未检测到温度传感器" : "暂时无法读取"}
      />
      {thermal?.gpus.map((gpu) =>
        gpu.integrated ? (
          <TemperatureRow
            key={gpu.id}
            icon={CircuitBoard}
            tone="gpu"
            title="核显"
            detail="集成在处理器中，没有单独的温度传感器"
            missing="同处理器"
          />
        ) : (
          <TemperatureRow
            key={gpu.id}
            icon={CircuitBoard}
            tone="gpu"
            title={gpu.name}
            detail="独立显卡"
            temperature={gpu.temperature}
            missing="驱动不提供温度"
          />
        ),
      )}
      {disks.map((disk) => (
        <TemperatureRow
          key={disk.id}
          icon={HardDrive}
          tone={disk.rotational ? "hdd" : "ssd"}
          title={disk.model}
          detail={`${diskRole[disk.role]} · ${disk.rotational ? "机械硬盘" : disk.transport === "nvme" ? "NVMe 固态硬盘" : "固态硬盘"}`}
          temperature={diskTemperature(disk)}
          missing="暂无读数"
        />
      ))}
    </Group>
  );
}

function TemperatureRow({
  icon,
  tone,
  title,
  detail,
  temperature,
  missing,
}: {
  icon: LucideIcon;
  tone: string;
  title: string;
  detail: string;
  temperature?: Temperature;
  missing: string;
}) {
  return (
    <div className={`${styles.setRow} ${styles.tempRow}`}>
      <IconTile tone={tone} icon={icon} size="lg" />
      <div className={styles.setRowText}>
        <span>
          <strong>{title}</strong>
          {temperature && temperatureLevel(temperature) === "hot" && <Pill tone="warning">偏高</Pill>}
        </span>
        <small>{detail}</small>
      </div>
      {temperature ? (
        <TemperatureGauge temperature={temperature} label={title} />
      ) : (
        <span className={styles.tempMissing}>{missing}</span>
      )}
    </div>
  );
}
