import type { Temperature } from "../api.generated";
import styles from "./settings.module.css";

// Temperatures of the CPU, GPUs and disks (docs/specs/device-temperatures.md).
// Each reading carries the temperature from which it counts as hot, so the
// pages and the system health agree on it.

export type TemperatureLevel = "normal" | "warm" | "hot";

// warmBand is how far below its hot limit a reading starts to show warm.
const warmBand = 10;
// The gauge runs from room temperature to a little past the hot limit.
const gaugeFloor = 20;
const gaugeHeadroom = 15;

export function temperatureLevel({ celsius, hotCelsius }: Temperature): TemperatureLevel {
  if (celsius >= hotCelsius) return "hot";
  return celsius >= hotCelsius - warmBand ? "warm" : "normal";
}

const levelClass: Record<TemperatureLevel, string | undefined> = {
  normal: styles.tempNormal,
  warm: styles.tempWarm,
  hot: styles.tempHot,
};

const rpm = new Intl.NumberFormat("zh-CN");

// fanName is the chip's label for a fan input, else its number.
export function fanName(fan: { number: number; label?: string }): string {
  return fan.label || `风扇 ${fan.number}`;
}

// FanSpeed shows a fan's speed; one that turned before and reads 0 now has
// stopped (docs/specs/device-temperatures.md).
export function FanSpeed({ fan }: { fan: { rpm: number; stopped: boolean } }) {
  return (
    <span className={`${styles.fanSpeed} ${fan.stopped ? styles.fanStopped : ""}`}>
      <strong>{rpm.format(fan.rpm)}</strong>
      <small>RPM</small>
    </span>
  );
}

// TemperatureGauge shows a reading with a bar from 20 °C to just past its
// hot limit, the limit marked on the track.
export function TemperatureGauge({ temperature, label }: { temperature: Temperature; label: string }) {
  const { celsius, hotCelsius } = temperature;
  const ceiling = hotCelsius + gaugeHeadroom;
  const position = (value: number) => Math.min(100, Math.max(0, ((value - gaugeFloor) / (ceiling - gaugeFloor)) * 100));
  const level = temperatureLevel(temperature);
  return (
    <span
      className={`${styles.tempGauge} ${levelClass[level] ?? ""}`}
      role="meter"
      aria-label={`${label}温度`}
      aria-valuenow={celsius}
      aria-valuemin={gaugeFloor}
      aria-valuemax={ceiling}
      aria-valuetext={`${celsius} °C${level === "hot" ? "，偏高" : ""}`}
      title={`${hotCelsius} °C 以上偏高`}
    >
      <strong>
        {celsius}
        <small>°C</small>
      </strong>
      <span className={styles.tempTrack} aria-hidden="true">
        <i style={{ width: `${position(celsius)}%` }} />
        <b style={{ left: `${position(hotCelsius)}%` }} />
      </span>
    </span>
  );
}
