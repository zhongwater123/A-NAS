import { act, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import App from "./App";
import type { SessionStatus } from "./terminal";

const xterm = vi.hoisted(() => ({ mounts: 0, onStatus: undefined as ((status: SessionStatus) => void) | undefined }));

vi.mock("./XtermSession", async () => {
  const { useEffect } = await import("react");
  return {
    default: function MockXtermSession({ onStatus }: { onStatus: (status: SessionStatus) => void }) {
      useEffect(() => {
        xterm.mounts += 1;
        xterm.onStatus = onStatus;
        onStatus({ state: "connected" });
      }, []);
      return <div data-testid="xterm-session" />;
    },
  };
});

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

beforeEach(() => {
  xterm.mounts = 0;
  xterm.onStatus = undefined;
});

const metricsState = {
  dataSource: "simulated",
  observedAt: "2026-10-06T00:00:00Z",
  cpu: { usagePercent: 23.5, logicalCores: 4 },
  memory: { totalBytes: 8589934592, usedBytes: 3435973837 },
  network: { receiveBytesPerSecond: 2516582, transmitBytesPerSecond: 327680 },
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("A-NAS desktop", () => {
  it("shows a loading state while the first host observation is pending", async () => {
    let finishRequest: ((response: Response) => void) | undefined;
    routeFetch({ hostState: vi.fn(() => new Promise<Response>((resolve) => { finishRequest = resolve; })) });
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
    routeFetch({ hostState: vi.fn().mockResolvedValueOnce(okResponse(healthyState)).mockResolvedValueOnce(errorResponse(503)) });
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
    routeFetch({ hostState: fetchMock });
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

  it("shows utilisation gauges, network rates and the clock in the status bar", async () => {
    stubFetch(healthyState);
    render(<App />);

    const statusBar = screen.getByRole("banner", { name: "设备状态" });
    await within(statusBar).findByText("4 核");
    expect(within(statusBar).getByRole("meter", { name: "CPU 占用" }).getAttribute("aria-valuenow")).toBe("24");
    expect(within(statusBar).getByRole("meter", { name: "内存占用" }).getAttribute("aria-valuenow")).toBe("40");
    expect(within(statusBar).getByText("3.2 / 8.0 GiB")).toBeTruthy();
    const network = within(statusBar).getByRole("group", { name: "网络速率" });
    expect(network.textContent).toContain("下行2.5MB/s");
    expect(network.textContent).toContain("上行328KB/s");
    expect(within(statusBar).getByText(/^\d{2}:\d{2}$/)).toBeTruthy();
    expect(within(statusBar).getByText("设备在线")).toBeTruthy();
  });

  it("marks the status bar disconnected when metrics cannot be read", async () => {
    routeFetch({ hostState: vi.fn().mockResolvedValue(okResponse(healthyState)), metrics: vi.fn().mockResolvedValue(errorResponse(503)) });
    render(<App />);

    const statusBar = screen.getByRole("banner", { name: "设备状态" });
    expect(await within(statusBar).findByText("连接中断", { selector: ".source-badge" })).toBeTruthy();
    expect(within(statusBar).getByRole("meter", { name: "CPU 占用" }).getAttribute("aria-valuetext")).toBe("暂无数据");
  });

  it("opens a terminal session from the desktop icon", async () => {
    stubDesktopFetch(healthyState, true);
    const user = userEvent.setup();
    render(<App />);

    const desktop = screen.getByRole("region", { name: "桌面应用" });
    await user.click(within(desktop).getByRole("button", { name: "打开终端" }));

    const terminal = screen.getByRole("dialog", { name: "终端" });
    expect(await within(terminal).findByTestId("xterm-session")).toBeTruthy();
    expect(xterm.mounts).toBe(1);
  });

  it("keeps the terminal session alive while minimized", async () => {
    stubDesktopFetch(healthyState, true);
    const user = userEvent.setup();
    render(<App />);
    await user.click(screen.getByRole("button", { name: "打开终端" }));
    await screen.findByTestId("xterm-session");

    await user.click(within(screen.getByRole("dialog", { name: "终端" })).getByRole("button", { name: "最小化终端" }));
    expect(screen.queryByRole("dialog", { name: "终端" })).toBeNull();
    await user.click(screen.getByRole("button", { name: "恢复终端" }));

    expect(screen.getByRole("dialog", { name: "终端" })).toBeTruthy();
    expect(xterm.mounts).toBe(1);
  });

  it("offers a new session after the shell exits", async () => {
    stubDesktopFetch(healthyState, true);
    const user = userEvent.setup();
    render(<App />);
    await user.click(screen.getByRole("button", { name: "打开终端" }));
    await screen.findByTestId("xterm-session");

    act(() => xterm.onStatus?.({ state: "ended", exitCode: 0 }));
    expect(await screen.findByText("Shell 已退出（代码 0）")).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "新建会话" }));
    expect(screen.queryByText("Shell 已退出（代码 0）")).toBeNull();
    expect(xterm.mounts).toBe(2);
  });

  it("explains when the terminal is disabled", async () => {
    stubDesktopFetch(healthyState, false);
    const user = userEvent.setup();
    render(<App />);
    await user.click(screen.getByRole("button", { name: "打开终端" }));

    expect(await screen.findByText("终端未启用")).toBeTruthy();
    expect(screen.queryByTestId("xterm-session")).toBeNull();
  });
});

function routeFetch(routes: { hostState: () => Promise<Response>; metrics?: () => Promise<Response>; terminalEnabled?: boolean }) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      switch (String(input)) {
        case "/api/v1/host-state":
          return routes.hostState();
        case "/api/v1/metrics":
          return routes.metrics ? routes.metrics() : okResponse(metricsState);
        case "/api/v1/terminal":
          return okResponse({ enabled: routes.terminalEnabled ?? true });
        default:
          throw new Error(`unexpected request ${String(input)}`);
      }
    }),
  );
}

function stubFetch(state: unknown) {
  routeFetch({ hostState: vi.fn().mockResolvedValue(okResponse(state)) });
}

function stubDesktopFetch(state: unknown, terminalEnabled: boolean) {
  routeFetch({ hostState: vi.fn().mockResolvedValue(okResponse(state)), terminalEnabled });
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
