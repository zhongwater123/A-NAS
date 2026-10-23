import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { useState } from "react";
import { SettingsPanel, type SettingsSection, type StoragePlanFlow } from "../SettingsPanel";
import { onSessionEnded, type Session } from "../api";
import { device, tasks } from "./testData";

vi.mock("../settings/AvatarDialog", () => ({ AvatarDialog: () => null }));
vi.mock("../settings/TerminalSection", () => ({ TerminalSection: () => null }));

const session: Session = {
  csrfToken: "test",
  expiresAt: "2026-10-20T00:00:00Z",
  user: { id: "user:alice", username: "alice", role: "member", status: "active", createdAt: device.observedAt },
};
function Panel() {
  const [section, setSection] = useState<SettingsSection>("overview");
  return (
    <SettingsPanel
      session={session}
      section={section}
      onSectionChange={setSection}
      host={{ loading: true, refreshing: false, disconnected: false, refresh: vi.fn() }}
      storagePlan={{ phrase: "", error: "" } as StoragePlanFlow}
      onUserChange={vi.fn()}
      onLogout={vi.fn()}
    />
  );
}
function api(options: { device?: unknown; tasks?: unknown; status?: number } = {}) {
  const fetcher = vi.fn(async (input: RequestInfo | URL) => {
    const path = String(input);
    const status = path.includes("task-manager") ? (options.status ?? 200) : 200;
    return {
      ok: status === 200,
      status,
      json: async () =>
        path.endsWith("/device")
          ? (options.device ?? device)
          : path.endsWith("/tasks")
            ? (options.tasks ?? tasks)
            : { items: [] },
    } as Response;
  });
  vi.stubGlobal("fetch", fetcher);
  return fetcher;
}
afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

it("lets a member navigate every device page and view services and processes", async () => {
  api();
  render(<Panel />);
  fireEvent.click(screen.getByRole("button", { name: "任务管理器" }));
  expect(await screen.findByRole("img", { name: /CPU 使用率，最近/ })).toBeTruthy();
  for (const tab of ["CPU", "GPU", "NPU", "内存", "网络", "硬盘", "总览"]) {
    fireEvent.click(screen.getByRole("button", { name: tab }));
    expect(screen.getByRole("button", { name: tab }).getAttribute("aria-pressed")).toBe("true");
  }
  fireEvent.click(screen.getByRole("button", { name: "GPU" }));
  expect(screen.getByText("Test GPU")).toBeTruthy();
  expect(screen.getAllByText("当前驱动不提供此指标").length).toBeGreaterThan(0);
  fireEvent.click(screen.getByRole("button", { name: "NPU" }));
  expect(screen.getByText("未检测到设备")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "服务与进程" }));
  expect(await screen.findByText("a-nas.service")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "进程" }));
  const rows = () =>
    within(screen.getByRole("table"))
      .getAllByRole("row")
      .slice(1)
      .map((row) => within(row).getAllByRole("cell")[0].textContent);
  expect(rows()).toEqual(["ten", "two", "unknown"]);
  fireEvent.click(screen.getByRole("button", { name: "CPU 占用" }));
  expect(rows()).toEqual(["two", "ten", "unknown"]);
  fireEvent.click(screen.getByRole("button", { name: "内存占用" }));
  expect(rows()).toEqual(["two", "ten", "unknown"]);
  fireEvent.click(screen.getByRole("button", { name: "内存占用" }));
  expect(rows()).toEqual(["ten", "two", "unknown"]);
  for (const column of ["硬盘读取速度", "硬盘写入速度"]) {
    fireEvent.click(screen.getByRole("button", { name: column }));
    expect(rows()).toEqual(column === "硬盘读取速度" ? ["two", "ten", "unknown"] : ["ten", "two", "unknown"]);
    fireEvent.click(screen.getByRole("button", { name: column }));
    expect(rows()).toEqual(column === "硬盘读取速度" ? ["ten", "two", "unknown"] : ["two", "ten", "unknown"]);
  }
  fireEvent.change(screen.getByRole("searchbox"), { target: { value: "10" } });
  expect(rows()).toEqual(["ten"]);
  fireEvent.change(screen.getByRole("searchbox"), { target: { value: "missing" } });
  expect(screen.getByText("没有匹配的进程")).toBeTruthy();
});

it("keeps a failed service observation distinct from an empty running list", async () => {
  api({ tasks: { ...tasks, services: { status: "unavailable", items: [] } } });
  render(<Panel />);
  fireEvent.click(screen.getByRole("button", { name: "任务管理器" }));
  fireEvent.click(screen.getByRole("button", { name: "服务与进程" }));
  expect(await screen.findByText("暂时无法读取")).toBeTruthy();
  expect(screen.queryByText("没有运行中的服务")).toBeNull();
});

it("offers first-load retry and uses the existing expired-session handler", async () => {
  const ended = vi.fn();
  const unsubscribe = onSessionEnded(ended);
  api({ status: 401 });
  render(<Panel />);
  fireEvent.click(screen.getByRole("button", { name: "任务管理器" }));
  expect(await screen.findByRole("button", { name: "重试" })).toBeTruthy();
  expect(ended).toHaveBeenCalledOnce();
  api();
  fireEvent.click(screen.getByRole("button", { name: "重试" }));
  expect(await screen.findByRole("img", { name: /CPU 使用率，最近/ })).toBeTruthy();
  unsubscribe();
});

it("keeps last observed device data and an error after disconnect", async () => {
  vi.useFakeTimers();
  const fetcher = api();
  render(<Panel />);
  fireEvent.click(screen.getByRole("button", { name: "任务管理器" }));
  await act(async () => {});
  expect(screen.queryByText("模拟数据")).toBeNull();
  fetcher.mockRejectedValue(new Error("offline"));
  await act(async () => {
    await vi.advanceTimersByTimeAsync(2000);
  });
  expect(screen.getByRole("alert").textContent).toContain("连接中断");
  expect(screen.getAllByText("25.0%")).toHaveLength(2);
  expect(screen.queryByText(/观测于/)).toBeNull();
});
it("shows empty hardware groups explicitly and labels partial network totals", async () => {
  const options = {
    device: {
      ...device,
      network: { status: "partial", items: device.network.items },
      disks: { status: "available", items: [] },
    },
  };
  api(options);
  render(<Panel />);
  fireEvent.click(screen.getByRole("button", { name: "任务管理器" }));
  await screen.findByRole("img", { name: /CPU 使用率，最近/ });
  expect(screen.getAllByText("数据不完整")).toHaveLength(1);
  fireEvent.click(screen.getByRole("button", { name: "硬盘" }));
  expect(screen.getByText("未检测到硬盘")).toBeTruthy();
});

it("leaves a graph gap when observations resume after an interruption", async () => {
  vi.useFakeTimers();
  const options = { device };
  api(options);
  render(<Panel />);
  fireEvent.click(screen.getByRole("button", { name: "任务管理器" }));
  await act(async () => {});
  options.device = { ...device, observedAt: "2026-10-15T10:00:10Z" };
  await act(async () => {
    await vi.advanceTimersByTimeAsync(2000);
  });
  const path = screen
    .getByRole("img", { name: /CPU 使用率，最近/ })
    .querySelector(".tm-trend")
    ?.getAttribute("d");
  expect(path?.match(/M/g)).toHaveLength(2);
  expect(path).not.toContain("L");
});
it.each([
  ["unsupported", "当前驱动不提供此指标"],
  ["unavailable", "暂时无法读取"],
])("explains a missing GPU metric while other metrics remain available (%s)", async (usageStatus, reason) => {
  api({
    device: {
      ...device,
      gpu: {
        status: "partial",
        items: [
          {
            ...device.gpu.items[0],
            status: "partial",
            usageStatus,
            memoryUsedStatus: "available",
            memoryTotalStatus: "available",
            memoryUsedBytes: 1024,
            memoryTotalBytes: 4096,
          },
        ],
      },
    },
  });
  render(<Panel />);
  fireEvent.click(screen.getByRole("button", { name: "任务管理器" }));
  await screen.findByRole("img", { name: /CPU 使用率，最近/ });
  fireEvent.click(screen.getByRole("button", { name: "GPU" }));
  expect(screen.getByText(reason)).toBeTruthy();
  expect(screen.queryByText("暂无采样数据")).toBeNull();
});

it.each(["live", "simulated"])("omits the data source and update row for %s observations", async (dataSource) => {
  api({ device: { ...device, dataSource } });
  render(<Panel />);
  fireEvent.click(screen.getByRole("button", { name: "任务管理器" }));
  await screen.findByRole("img", { name: /CPU 使用率，最近/ });
  expect(screen.queryByText(/模拟数据|实时数据|观测于|每 2 秒/)).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "服务与进程" }));
  await screen.findByText("a-nas.service");
  expect(screen.queryByText(/模拟数据|实时数据|观测于|每 2 秒/)).toBeNull();
});
