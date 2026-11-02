import { render, screen, within } from "@testing-library/react";
import { expect, it } from "vitest";

import type { HostState } from "../desktop/api";
import { healthyState } from "../test/fixtures";
import { DeviceTemperatures } from "./DeviceTemperatures";
import { temperatureLevel } from "./Temperature";

function state(change: (state: HostState) => void): HostState {
  const copy = structuredClone(healthyState);
  change(copy);
  return copy;
}

function row(title: string) {
  return screen.getByText(title, { selector: "strong" }).closest("div[class]")!.parentElement!;
}

it("lists the CPU, the GPU and every disk with its reading", () => {
  render(<DeviceTemperatures snapshot={healthyState} />);
  expect(screen.getByRole("meter", { name: "处理器温度" }).getAttribute("aria-valuenow")).toBe("48");
  expect(within(row("核显")).getByText("与处理器相同")).toBeTruthy();
  const disk = row("A-NAS Fake HDD");
  expect(within(disk).getByText("未使用 · 机械硬盘")).toBeTruthy();
  expect(within(disk).getByRole("meter").getAttribute("aria-valuetext")).toBe("31 °C");
  expect(screen.queryByText("偏高")).toBeNull();
  expect(row("风扇 1").textContent).toContain("1,912RPM");
  expect(screen.getByText("机械硬盘和 SATA 固态硬盘的温度每 10 分钟更新一次，硬盘待机时不会被唤醒。")).toBeTruthy();
});

it("says when a fan that turned has stopped", () => {
  render(
    <DeviceTemperatures
      snapshot={state((copy) => {
        copy.system.thermal!.fans = [
          { id: "fan:1", number: 1, rpm: 0, stopped: true },
          { id: "fan:2", number: 2, label: "CPU_FAN", rpm: 1450, stopped: false },
        ];
      })}
    />,
  );
  expect(screen.getByText("1 个风扇停转")).toBeTruthy();
  expect(within(row("风扇 1")).getByText("已停转")).toBeTruthy();
  expect(within(row("风扇 1")).getByText("风扇停了，请检查风扇和接线")).toBeTruthy();
  expect(row("CPU_FAN").textContent).toContain("1,450RPM");
});

it("marks what runs hot, with the limit the system health uses", () => {
  render(
    <DeviceTemperatures
      snapshot={state((copy) => {
        copy.system.thermal = { cpu: { celsius: 92, hotCelsius: 90 }, gpus: [], fans: [] };
        copy.disks[0].temperatureCelsius = 57;
      })}
    />,
  );
  expect(screen.getByText("2 项偏高")).toBeTruthy();
  expect(within(row("处理器")).getByText("偏高")).toBeTruthy();
  expect(within(row("处理器")).getByText("温度偏高，请检查散热器和风扇")).toBeTruthy();
  expect(screen.getByRole("meter", { name: "处理器温度" }).getAttribute("aria-valuetext")).toBe("92 °C，偏高");
  expect(within(row("A-NAS Fake HDD")).getByText("偏高")).toBeTruthy();
});

it("says why a reading is missing instead of showing zero", () => {
  const { rerender } = render(
    <DeviceTemperatures
      snapshot={state((copy) => {
        copy.system.thermal = {
          gpus: [{ id: "gpu:discrete", name: "GPU 1 (10de:2204)", integrated: false }],
          fans: [],
        };
        delete copy.disks[0].temperatureCelsius;
        delete copy.disks[0].temperatureHotCelsius;
      })}
    />,
  );
  expect(within(row("处理器")).getByText("未检测到温度传感器")).toBeTruthy();
  expect(within(row("GPU 1 (10de:2204)")).getByText("驱动不提供温度")).toBeTruthy();
  expect(within(row("A-NAS Fake HDD")).getByText("暂无读数")).toBeTruthy();
  expect(screen.queryByRole("meter")).toBeNull();

  // An older Host Agent reports no thermal state at all.
  rerender(<DeviceTemperatures snapshot={state((copy) => delete copy.system.thermal)} />);
  expect(within(row("处理器")).getByText("暂时无法读取")).toBeTruthy();
});

it("turns warm within 10 °C of the limit and hot at it", () => {
  expect(temperatureLevel({ celsius: 79, hotCelsius: 90 })).toBe("normal");
  expect(temperatureLevel({ celsius: 80, hotCelsius: 90 })).toBe("warm");
  expect(temperatureLevel({ celsius: 90, hotCelsius: 90 })).toBe("hot");
});
