import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { HttpResponse } from "msw";
import { useState } from "react";
import { afterEach, expect, it, vi } from "vitest";

import { onSessionEnded } from "../api/client";
import type { Session, TaskManagerDeviceResponse, TaskManagerMetricStatus } from "../api.generated";
import { SettingsPanel, type SettingsSection, type StoragePlanFlow } from "../settings/SettingsPanel";
import { taskManagerDevice as device, taskManagerTasks as tasks } from "../test/fixtures";
import { type TaskManagerSamples, taskManagerHandlers } from "../test/handlers/taskmanager";
import { apiError, http, server } from "../test/server";

vi.mock("../settings/AvatarDialog", () => ({ AvatarDialog: () => null }));
vi.mock("../settings/TerminalSection", () => ({ TerminalSection: () => null }));

const session: Session = {
  csrfToken: "test",
  expiresAt: "2026-10-20T00:00:00Z",
  user: {
    id: "user:alice",
    username: "alice",
    role: "member",
    status: "active",
    createdAt: device.observedAt,
    mustChangePassword: false,
  },
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
// serve answers the task manager's samples, and the settings window's own read
// of the data volumes, which it shows on 概览 before 任务管理器 is opened.
function serve(samples: TaskManagerSamples = {}) {
  server.use(
    http.get("/api/v1/volumes", ({ response }) => response(200).json({ items: [] })),
    ...taskManagerHandlers(samples),
  );
}
afterEach(() => {
  vi.useRealTimers();
});

it("lets a member navigate every device page and view services and processes", async () => {
  serve();
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
  serve({ tasks: { ...tasks, services: { status: "unavailable", items: [] } } });
  render(<Panel />);
  fireEvent.click(screen.getByRole("button", { name: "任务管理器" }));
  fireEvent.click(screen.getByRole("button", { name: "服务与进程" }));
  expect(await screen.findByText("暂时无法读取")).toBeTruthy();
  expect(screen.queryByText("没有运行中的服务")).toBeNull();
});

it("offers first-load retry and uses the existing expired-session handler", async () => {
  const ended = vi.fn();
  const unsubscribe = onSessionEnded(ended);
  serve();
  server.use(
    http.get("/api/v1/task-manager/device", ({ response }) => response(401).json(apiError("authentication_required"))),
    http.get("/api/v1/task-manager/tasks", ({ response }) => response(401).json(apiError("authentication_required"))),
  );
  render(<Panel />);
  fireEvent.click(screen.getByRole("button", { name: "任务管理器" }));
  expect(await screen.findByRole("button", { name: "重试" })).toBeTruthy();
  expect(ended).toHaveBeenCalledOnce();
  server.use(...taskManagerHandlers());
  fireEvent.click(screen.getByRole("button", { name: "重试" }));
  expect(await screen.findByRole("img", { name: /CPU 使用率，最近/ })).toBeTruthy();
  unsubscribe();
});

it("keeps last observed device data and an error after disconnect", async () => {
  // MSW answers on the real clock; the fake one only drives the polling.
  vi.useFakeTimers({ shouldAdvanceTime: true });
  serve();
  render(<Panel />);
  fireEvent.click(screen.getByRole("button", { name: "任务管理器" }));
  await screen.findByRole("img", { name: /CPU 使用率，最近/ });
  expect(screen.queryByText("模拟数据")).toBeNull();
  // The connection drops: the next samples get no answer at all.
  server.use(
    http.get("/api/v1/task-manager/device", () => HttpResponse.error()),
    http.get("/api/v1/task-manager/tasks", () => HttpResponse.error()),
  );
  await act(async () => {
    await vi.advanceTimersByTimeAsync(2000);
  });
  expect((await screen.findByRole("alert")).textContent).toContain("连接中断");
  expect(screen.getAllByText("25.0%")).toHaveLength(2);
  expect(screen.queryByText(/观测于/)).toBeNull();
});
it("shows empty hardware groups explicitly and labels partial network totals", async () => {
  serve({
    device: {
      ...device,
      network: { status: "partial", items: device.network.items },
      disks: { status: "available", items: [] },
    },
  });
  render(<Panel />);
  fireEvent.click(screen.getByRole("button", { name: "任务管理器" }));
  await screen.findByRole("img", { name: /CPU 使用率，最近/ });
  expect(screen.getAllByText("数据不完整")).toHaveLength(1);
  fireEvent.click(screen.getByRole("button", { name: "硬盘" }));
  expect(screen.getByText("未检测到硬盘")).toBeTruthy();
});

it("leaves a graph gap when observations resume after an interruption", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  const samples: TaskManagerSamples = { device };
  serve(samples);
  render(<Panel />);
  fireEvent.click(screen.getByRole("button", { name: "任务管理器" }));
  await screen.findByRole("img", { name: /CPU 使用率，最近 1 个/ });
  samples.device = { ...device, observedAt: "2026-10-15T10:00:10Z" };
  await act(async () => {
    await vi.advanceTimersByTimeAsync(2000);
  });
  // The graph draws its grid, then the area under the line, then the line.
  const graph = await screen.findByRole("img", { name: /CPU 使用率，最近 2 个/ });
  const path = graph.querySelectorAll("path")[2]?.getAttribute("d");
  expect(path?.match(/M/g)).toHaveLength(2);
  expect(path).not.toContain("L");
});
it.each<[TaskManagerMetricStatus, string]>([
  ["unsupported", "当前驱动不提供此指标"],
  ["unavailable", "暂时无法读取"],
])("explains a missing GPU metric while other metrics remain available (%s)", async (usageStatus, reason) => {
  serve({
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

it.each<TaskManagerDeviceResponse["dataSource"]>(["live", "simulated"])(
  "omits the data source and update row for %s observations",
  async (dataSource) => {
    serve({ device: { ...device, dataSource } });
    render(<Panel />);
    fireEvent.click(screen.getByRole("button", { name: "任务管理器" }));
    await screen.findByRole("img", { name: /CPU 使用率，最近/ });
    expect(screen.queryByText(/模拟数据|实时数据|观测于|每 2 秒/)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "服务与进程" }));
    await screen.findByText("a-nas.service");
    expect(screen.queryByText(/模拟数据|实时数据|观测于|每 2 秒/)).toBeNull();
  },
);
