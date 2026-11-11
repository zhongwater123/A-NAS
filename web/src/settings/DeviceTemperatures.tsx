import { CircuitBoard, Cpu, Fan, HardDrive, type LucideIcon } from "lucide-react";
import type { ReactNode } from "react";

import type { Temperature } from "../api.generated";
import type { HostState } from "../desktop/api";
import { Group, Pill } from "./parts";
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
  // SATA disks' readings come from the SMART cache: at most every 10 minutes,
  // and a disk in standby is not woken for one.
  return (
    <Group title="温度与风扇" aside={attention || undefined} anchor="thermal">
      <div className={styles.tempGrid}>
        <TemperatureRow
          icon={Cpu}
          tone="cpu"
          title="处理器"
          detail={cpuHot ? "温度偏高，请检查散热器和风扇" : undefined}
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
              detail="集成显卡"
              missing="与处理器相同"
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
            detail={fan.stopped ? "风扇停了，请检查风扇和接线" : undefined}
            flag={fan.stopped ? "已停转" : undefined}
            end={<FanSpeed fan={fan} />}
          />
        ))}
      </div>
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
  detail?: string;
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

// Row is one sensor: the sensors sit two to a line where the card is wide enough.
function Row({
  icon: Icon,
  tone,
  title,
  detail,
  flag,
  end,
}: {
  icon: LucideIcon;
  tone: string;
  title: string;
  detail?: string;
  flag?: string;
  end: ReactNode;
}) {
  return (
    <div className={`${styles.tempRow} ${styles[tone] ?? ""}`}>
      <Icon className={styles.tempIcon} aria-hidden="true" />
      <div className={styles.setRowText}>
        <span>
          <strong>{title}</strong>
          {flag && <Pill tone="warning">{flag}</Pill>}
        </span>
        {detail && <small>{detail}</small>}
      </div>
      {end}
    </div>
  );
}
