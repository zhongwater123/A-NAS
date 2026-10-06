import { act, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import App from "./App";

const healthyState = {
  dataSource: "simulated",
  productVersion: "test-version",
  observedAt: "2026-10-06T00:00:00Z",
  system: {
    id: "host:fake-01",
    hostname: "anas-fake",
    operatingSystem: { name: "Debian", version: "13" },
    architecture: "amd64",
    uptimeSeconds: 3661,
    health: "healthy",
  },
  disks: [
    {
      id: "disk:fake-system-01",
      model: "A-NAS Fake SSD",
      transport: "nvme",
      capacityBytes: 125000000000,
      rotational: false,
      role: "system",
      health: "healthy",
      temperatureCelsius: 36,
    },
  ],
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("A-NAS desktop", () => {
  it("shows a loading state while the first host observation is pending", async () => {
    let finishRequest: ((response: Response) => void) | undefined;
    vi.stubGlobal("fetch", vi.fn().mockImplementation(() => new Promise<Response>((resolve) => { finishRequest = resolve; })));
    const user = userEvent.setup();

    render(<App />);
    await user.click(screen.getByRole("button", { name: "打开资源管理" }));

    expect(screen.getByText("正在读取设备状态…")).toBeTruthy();
    await act(async () => finishRequest?.(okResponse(healthyState)));
    expect(await screen.findByText("anas-fake")).toBeTruthy();
  });

  it("shows the observed simulated host and disk", async () => {
    stubFetch(healthyState);
    const user = userEvent.setup();

    const { container } = render(<App />);

    expect(screen.queryByRole("dialog")).toBeNull();
    await user.click(screen.getByRole("button", { name: "打开资源管理" }));
    expect(await screen.findByText("anas-fake")).toBeTruthy();
    expect(screen.getAllByText("模拟数据")).toHaveLength(2);
    expect(screen.getByText("A-NAS Fake SSD")).toBeTruthy();
    expect(screen.getByText("116.4 GiB")).toBeTruthy();
    expect(container.querySelector(".capacity-track")).toBeNull();
  });

  it("never presents unknown health as healthy", async () => {
    stubFetch({
      ...healthyState,
      dataSource: "live",
      system: { ...healthyState.system, health: "unknown" },
      disks: [{ ...healthyState.disks[0], health: "unknown", temperatureCelsius: undefined }],
    });

    const user = userEvent.setup();
    render(<App />);
    await user.click(screen.getByRole("button", { name: "打开资源管理" }));

    expect(await screen.findAllByText("实时主机")).toHaveLength(2);
    expect(screen.getAllByText("未知")).toHaveLength(2);
    expect(screen.queryByText("健康")).toBeNull();
  });

  it("keeps the last successful state when refresh loses connection", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(okResponse(healthyState))
      .mockResolvedValueOnce(errorResponse(503));
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();

    render(<App />);
    await user.click(screen.getByRole("button", { name: "打开资源管理" }));
    expect(await screen.findByText("anas-fake")).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "刷新设备状态" }));

    expect(await screen.findByText("连接中断，正在显示上次成功读取的数据")).toBeTruthy();
    expect(screen.getByText("anas-fake")).toBeTruthy();
  });

  it("retries after an initial 503 response", async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce(errorResponse(503)).mockResolvedValueOnce(okResponse(healthyState));
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();

    render(<App />);
    await user.click(screen.getByRole("button", { name: "打开资源管理" }));
    expect(await screen.findByText("暂时无法读取设备状态")).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "重新连接" }));
    expect(await screen.findByText("anas-fake")).toBeTruthy();
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("shows an explicit empty disk inventory", async () => {
    stubFetch({ ...healthyState, disks: [] });
    const user = userEvent.setup();

    render(<App />);
    await user.click(screen.getByRole("button", { name: "打开资源管理" }));

    expect(await screen.findByText("未检测到磁盘")).toBeTruthy();
  });

  it("opens, minimizes, and restores system settings", async () => {
    stubFetch(healthyState);
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText("模拟数据");

    const desktop = screen.getByRole("region", { name: "桌面应用" });
    await user.click(within(desktop).getByRole("button", { name: "打开系统设置" }));
    const settings = screen.getByRole("dialog", { name: "系统设置" });
    expect(settings).toBeTruthy();
    expect(within(settings).getByText("1 小时 1 分钟")).toBeTruthy();

    await user.click(within(settings).getByRole("button", { name: "最小化系统设置" }));
    expect(screen.queryByRole("dialog", { name: "系统设置" })).toBeNull();

    await user.click(screen.getByRole("button", { name: "恢复系统设置" }));
    expect(screen.getByRole("dialog", { name: "系统设置" })).toBeTruthy();

    await user.click(within(screen.getByRole("dialog", { name: "系统设置" })).getByRole("button", { name: "最大化系统设置" }));
    expect(within(screen.getByRole("dialog", { name: "系统设置" })).getByRole("button", { name: "还原系统设置" })).toBeTruthy();

    await user.click(within(screen.getByRole("dialog", { name: "系统设置" })).getByRole("button", { name: "关闭系统设置" }));
    expect(screen.queryByRole("dialog", { name: "系统设置" })).toBeNull();
  });

  it("keeps planned desktop applications visible but disabled", async () => {
    stubFetch(healthyState);
    render(<App />);

    expect(await screen.findByText("模拟数据")).toBeTruthy();
    const desktop = screen.getByRole("region", { name: "桌面应用" });
    expect(within(desktop).getByRole("button", { name: "文件管理，规划中" }).hasAttribute("disabled")).toBe(true);
    expect(within(desktop).getByRole("button", { name: "Docker，规划中" }).hasAttribute("disabled")).toBe(true);
    expect(within(desktop).getByRole("button", { name: "AI 助手，规划中" }).hasAttribute("disabled")).toBe(true);
    const notifications = screen.getByRole("button", { name: "通知，规划中" });
    expect(within(notifications).queryByText("2")).toBeNull();
  });
});

function stubFetch(state: unknown) {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(okResponse(state)));
}

function okResponse(state: unknown) {
  return {
    ok: true,
    status: 200,
    json: async () => state,
  } as Response;
}

function errorResponse(status: number) {
  return {
    ok: false,
    status,
    json: async () => ({ error: { code: "state_unavailable", message: "host state is unavailable" } }),
  } as Response;
}
