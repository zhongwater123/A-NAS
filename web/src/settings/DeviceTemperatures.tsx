import { CircuitBoard, Cpu, Fan, HardDrive, type LucideIcon } from "lucide-react";
import type { ReactNode } from "react";

import type { Temperature } from "../api.generated";
import type { HostState } from "../desktop/api";
import { Group, IconTile, Pill } from "./parts";
import styles from "./settings.module.css";
import { FanSpeed, fanName, TemperatureGauge, temperatureLevel } from "./Temperature";

type Disk = HostState["disks"][number];

const diskRole: Record<Disk["role"], string> = { system: "系统盘", data: "数据盘", unassigned: "未使用" };

// diskTemperature is a disk's reading with the limit the system health
// applies to its kind, when it has one.
export function diskTemperature(disk: Disk): Temperature | undefined {
  return disk.temperatureCelsius === undefined || disk.temperatureHotCelsius === undefined
    ? undefined
    : { celsius: disk.temperatureCelsius, hotCelsius: disk.temperatureHotCelsius };
}

// DeviceTemperatures lists what the CPU, GPU and disk sensors read and how
// fast the fans turn, in the overview (docs/specs/device-temperatures.md).
export function DeviceTemperatures({ snapshot }: { snapshot: HostState }) {
  const thermal = snapshot.system.thermal;
  const disks = snapshot.disks.filter((disk) => disk.model);
  const fans = thermal?.fans ?? [];
  const readings = [
    thermal?.cpu,
    ...(thermal?.gpus ?? []).map((gpu) => gpu.temperature),
    ...disks.map(diskTemperature),
  ].filter((reading) => reading !== undefined);
  const hot = readings.filter((reading) => temperatureLevel(reading) === "hot").length;
  const stopped = fans.filter((fan) => fan.stopped).length;
  const attention = [hot ? `${hot} 项偏高` : "", stopped ? `${stopped} 个风扇停转` : ""].filter(Boolean).join("，");
  const cpuHot = thermal?.cpu !== undefined && temperatureLevel(thermal.cpu) === "hot";
  return (
    <Group
      title="温度与风扇"
      aside={attention || undefined}
      footnote={`NVMe 固态硬盘的温度随时读取；其他硬盘的温度来自 SMART，每 10 分钟读取一次，不会唤醒待机的硬盘。${fans.length ? "风扇转速来自主板的传感器芯片，没接风扇的接口不显示。" : ""}`}
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
      {fans.map((fan) => (
        <Row
          key={fan.id}
          icon={Fan}
          tone="fan"
          title={fanName(fan)}
          detail={fan.stopped ? "之前在转，现在读数为 0，请检查风扇和接线" : "主板风扇接口"}
          flag={fan.stopped ? "已停转" : undefined}
          end={<FanSpeed fan={fan} />}
        />
      ))}
    </Group>
  );
}

function TemperatureRow({
  temperature,
  missing,
  ...row
}: {
  icon: LucideIcon;
  tone: string;
  title: string;
  detail: string;
  temperature?: Temperature;
  missing: string;
}) {
  return (
    <Row
      {...row}
      flag={temperature && temperatureLevel(temperature) === "hot" ? "偏高" : undefined}
      end={
        temperature ? (
          <TemperatureGauge temperature={temperature} label={row.title} />
        ) : (
          <span className={styles.tempMissing}>{missing}</span>
        )
      }
    />
  );
}

function Row({
  icon,
  tone,
  title,
  detail,
  flag,
  end,
}: {
  icon: LucideIcon;
  tone: string;
  title: string;
  detail: string;
  flag?: string;
  end: ReactNode;
}) {
  return (
    <div className={`${styles.setRow} ${styles.tempRow}`}>
      <IconTile tone={tone} icon={icon} size="lg" />
      <div className={styles.setRowText}>
        <span>
          <strong>{title}</strong>
          {flag && <Pill tone="warning">{flag}</Pill>}
        </span>
        <small>{detail}</small>
      </div>
      {end}
    </div>
  );
}
