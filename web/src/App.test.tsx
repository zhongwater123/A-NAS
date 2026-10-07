import { act, fireEvent, render, screen, within } from "@testing-library/react";
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

const session = {
  csrfToken: "csrf-test", expiresAt: "2026-10-07T22:00:00Z",
  user: { id: "user:owner", username: "owner", role: "admin", status: "active", createdAt: "2026-10-07T10:00:00Z" },
};

const healthyState = {
  dataSource: "simulated", productVersion: "v1.0.1-rc.1", observedAt: "2026-10-07T10:00:00Z",
  system: { id: "host:fake-01", hostname: "anas-fake", operatingSystem: { name: "Debian", version: "13" }, architecture: "amd64", uptimeSeconds: 3661, health: "healthy" },
  disks: [{
    id: "disk:fake-data-01", model: "A-NAS Fake HDD", transport: "sata", capacityBytes: 512000000000,
    rotational: true, removable: false, inUse: false, filesystems: [], role: "unassigned", health: "healthy",
    smartStatus: "healthy", eligibleForDataVolume: true, ineligibleReasons: [], temperatureCelsius: 31,
  }],
};

beforeEach(() => {
  localStorage.clear();
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

afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); });

describe("A-NAS v1.0.1 desktop", () => {
  it("activates the local device with only an administrator account and password", async () => {
    const fetchMock = installAPI({ setupRequired: true });
    const user = userEvent.setup();
    render(<App />);
    expect(await screen.findByRole("heading", { name: "启用 A-NAS" })).toBeTruthy();
    expect(screen.queryByLabelText("一次性初始化码")).toBeNull();
    await user.type(screen.getByLabelText("账号"), "owner");
    await user.type(screen.getByLabelText("密码"), "correct horse battery staple");
    await user.click(screen.getByRole("button", { name: "创建管理员并启用" }));
    expect(await screen.findByRole("region", { name: "桌面应用" })).toBeTruthy();
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/setup/admin", expect.objectContaining({ method: "POST" }));
    const setupCall = fetchMock.mock.calls.find(([path]) => path === "/api/v1/setup/admin");
    expect(JSON.parse(String(setupCall?.[1]?.body))).toEqual({ username: "owner", password: "correct horse battery staple" });
  });

  it("shows a loading state while the first host observation is pending", async () => {
    let finishRequest: ((response: Response) => void) | undefined;
    routeFetch({ hostState: vi.fn(() => new Promise<Response>((resolve) => { finishRequest = resolve; })) });
    const user = userEvent.setup();
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    await user.click(within(desktop).getByRole("button", { name: "打开资源管理" }));
    expect(screen.getByText("正在读取设备状态…")).toBeTruthy();
    await act(async () => finishRequest?.(okResponse(healthyState)));
    expect(await screen.findByText("anas-fake")).toBeTruthy();
  });

  it("shows authenticated host health and keeps the clock advancing", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.setSystemTime(new Date("2026-10-07T10:00:00Z"));
    installAPI();
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    await user.click(within(desktop).getByRole("button", { name: "打开资源管理" }));
    expect(await screen.findByText("anas-fake")).toBeTruthy();
    const before = screen.getByText(/\d{2}:\d{2}/, { selector: ".status-time strong" }).textContent;
    act(() => vi.advanceTimersByTime(60_000));
    expect(screen.getByText(/\d{2}:\d{2}/, { selector: ".status-time strong" }).textContent).not.toBe(before);
  });

  it("enables file management and lists only the signed-in user's spaces", async () => {
    installAPI({
      spaces: [{ id: "space:owner", kind: "private", name: "owner", ownerUserId: "user:owner", createdAt: "2026-10-07T10:00:00Z" }, { id: "space:shared", kind: "shared", name: "Shared", createdAt: "2026-10-07T10:00:00Z" }],
      entries: [{ id: "file:1", spaceId: "space:owner", name: "家庭", kind: "directory", sizeBytes: 0, modifiedAt: "2026-10-07T10:00:00Z" }],
    });
    const user = userEvent.setup(); render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    const fileButton = within(desktop).getByRole("button", { name: "打开文件管理" });
    expect(fileButton.hasAttribute("disabled")).toBe(false);
    await user.click(fileButton);
    expect(await screen.findByRole("dialog", { name: "文件管理" })).toBeTruthy();
    expect(await screen.findByText("家庭")).toBeTruthy();
    expect(screen.getByRole("option", { name: "个人空间 · owner" })).toBeTruthy();
    expect(screen.getByRole("option", { name: "共享空间" })).toBeTruthy();
  });

  it("keeps photos planned while enabling trash, snapshots, accounts, and storage", async () => {
    installAPI(); render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    expect(within(desktop).getByRole("button", { name: "打开回收站" }).hasAttribute("disabled")).toBe(false);
    expect(within(desktop).getByRole("button", { name: "打开文件快照" }).hasAttribute("disabled")).toBe(false);
    expect(within(desktop).getByRole("button", { name: "打开账号管理" })).toBeTruthy();
    expect(within(desktop).getByRole("button", { name: "打开存储初始化" })).toBeTruthy();
    expect(within(desktop).getByRole("button", { name: "相册，规划中" }).hasAttribute("disabled")).toBe(true);
  });

  it("keeps the desktop usable when a blank-disk plan contains legacy null arrays", async () => {
    installAPI({
      storagePlan: {
        id: "plan:blank", diskId: "disk:fake-data-01", diskModel: "A-NAS Fake HDD", capacityBytes: 512000000000,
        fingerprint: "blank-disk-fingerprint", signatures: null,
        confirmationPhrase: "ERASE data-01", actions: null, state: "planned", expiresAt: "2026-10-07T10:10:00Z",
      },
    });
    const user = userEvent.setup(); render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    await user.click(within(desktop).getByRole("button", { name: "打开存储初始化" }));
    await user.click(await screen.findByRole("button", { name: "生成格式化计划" }));
    expect(await screen.findByRole("heading", { name: "破坏性操作计划" })).toBeTruthy();
    expect(screen.getByText("将清除的已知签名：未检测到文件系统签名")).toBeTruthy();
    expect(screen.getByRole("region", { name: "桌面应用" })).toBeTruthy();
  });

  it("keeps the last successful state when refresh loses connection", async () => {
    routeFetch({ hostState: vi.fn().mockResolvedValueOnce(okResponse(healthyState)).mockResolvedValueOnce(errorResponse(503)) });
    const user = userEvent.setup();

    render(<App />);
    await user.click(await screen.findByRole("button", { name: "打开资源管理" }));
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
    await user.click(await screen.findByRole("button", { name: "打开资源管理" }));
    expect(await screen.findByText("暂时无法读取设备状态")).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "重新连接" }));
    expect(await screen.findByText("anas-fake")).toBeTruthy();
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("shows an explicit empty disk inventory", async () => {
    stubFetch({ ...healthyState, disks: [] });
    const user = userEvent.setup();

    render(<App />);
    await user.click(await screen.findByRole("button", { name: "打开资源管理" }));

    expect(await screen.findByText("未检测到磁盘")).toBeTruthy();
  });

  it("opens, minimizes, and restores system settings", async () => {
    installAPI(); const user = userEvent.setup(); render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    await user.click(within(desktop).getByRole("button", { name: "打开系统设置" }));
    const settings = screen.getByRole("dialog", { name: "系统设置" });
    await user.click(within(settings).getByRole("button", { name: "最小化系统设置" }));
    await user.click(screen.getByRole("button", { name: "恢复系统设置" }));
    expect(screen.getByRole("dialog", { name: "系统设置" })).toBeTruthy();
  });

  it("shows utilisation gauges, network rates and the clock in the status bar", async () => {
    stubFetch(healthyState);
    render(<App />);

    const statusBar = await screen.findByRole("banner", { name: "设备状态" });
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

    const statusBar = await screen.findByRole("banner", { name: "设备状态" });
    expect(await within(statusBar).findByText("连接中断", { selector: ".source-badge" })).toBeTruthy();
    expect(within(statusBar).getByRole("meter", { name: "CPU 占用" }).getAttribute("aria-valuetext")).toBe("暂无数据");
  });

  it("reorders desktop icons by dragging without opening the app", async () => {
    stubFetch(healthyState);
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    layOutGrid(desktop);
    expect(desktopOrder(desktop).slice(0, 5)).toEqual(["文件管理", "回收站", "系统设置", "资源管理", "终端"]);

    const terminal = within(desktop).getByRole("button", { name: "打开终端" }).parentElement!;
    fireEvent.pointerDown(terminal, { button: 0, pointerId: 1, pointerType: "mouse", clientX: 150, clientY: 160 });
    fireEvent.pointerMove(window, { pointerId: 1, clientX: 120, clientY: 150 });
    fireEvent.pointerMove(window, { pointerId: 1, clientX: 40, clientY: 50 });
    fireEvent.pointerUp(window, { pointerId: 1, clientX: 40, clientY: 50 });
    fireEvent.click(within(desktop).getByRole("button", { name: "打开终端" }));

    expect(desktopOrder(desktop).slice(0, 5)).toEqual(["终端", "文件管理", "回收站", "系统设置", "资源管理"]);
    expect(JSON.parse(localStorage.getItem("a-nas.desktop-order.v1")!).slice(0, 2)).toEqual(["terminal", "files"]);
    expect(screen.queryByRole("dialog", { name: "终端" })).toBeNull();
    expect(screen.getByText("已将终端移动到第 1 位")).toBeTruthy();
  });

  it("restores the original order when a drag is cancelled with Escape", async () => {
    stubFetch(healthyState);
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    layOutGrid(desktop);

    const settings = within(desktop).getByRole("button", { name: "打开系统设置" }).parentElement!;
    fireEvent.pointerDown(settings, { button: 0, pointerId: 2, pointerType: "mouse", clientX: 250, clientY: 50 });
    fireEvent.pointerMove(window, { pointerId: 2, clientX: 40, clientY: 50 });
    expect(desktopOrder(desktop)[0]).toBe("系统设置");
    fireEvent.keyDown(window, { key: "Escape" });

    expect(desktopOrder(desktop).slice(0, 3)).toEqual(["文件管理", "回收站", "系统设置"]);
    expect(localStorage.getItem("a-nas.desktop-order.v1")).toBeNull();
  });

  it("moves a focused icon with Alt and the arrow keys and restores the saved order", async () => {
    stubFetch(healthyState);
    const user = userEvent.setup();
    const { unmount } = render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });

    within(desktop).getByRole("button", { name: "打开资源管理" }).focus();
    await user.keyboard("{Alt>}{ArrowLeft}{/Alt}");
    expect(desktopOrder(desktop).slice(0, 4)).toEqual(["文件管理", "回收站", "资源管理", "系统设置"]);
    expect(document.activeElement).toBe(within(desktop).getByRole("button", { name: "打开资源管理" }));
    unmount();

    render(<App />);
    expect(desktopOrder(await screen.findByRole("region", { name: "桌面应用" })).slice(0, 4)).toEqual(["文件管理", "回收站", "资源管理", "系统设置"]);
  });

  it("switches focus between open apps from an icon-only dock", async () => {
    stubDesktopFetch(healthyState, true);
    const user = userEvent.setup();
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    await user.click(within(desktop).getByRole("button", { name: "打开系统设置" }));
    await user.click(within(desktop).getByRole("button", { name: "打开终端" }));

    const dock = screen.getByRole("navigation", { name: "已打开窗口" });
    expect(dock.textContent).toBe("");
    expect(within(dock).getByRole("button", { name: "最小化终端" }).getAttribute("aria-current")).toBe("true");

    await user.click(within(dock).getByRole("button", { name: "切换到系统设置" }));
    expect(within(dock).getByRole("button", { name: "最小化系统设置" }).getAttribute("aria-current")).toBe("true");
    expect(within(dock).getByRole("button", { name: "切换到终端" })).toBeTruthy();

    await user.click(within(dock).getByRole("button", { name: "最小化系统设置" }));
    expect(screen.queryByRole("dialog", { name: "系统设置" })).toBeNull();
    expect(within(dock).getByRole("button", { name: "最小化终端" }).getAttribute("aria-current")).toBe("true");
    expect(within(dock).getByRole("button", { name: "恢复系统设置" })).toBeTruthy();
  });

  it("lists dock icons in launch order and drops closed apps immediately from the accessibility tree", async () => {
    stubDesktopFetch(healthyState, true);
    const user = userEvent.setup();
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    await user.click(within(desktop).getByRole("button", { name: "打开终端" }));
    await user.click(within(desktop).getByRole("button", { name: "打开资源管理" }));

    const dock = screen.getByRole("navigation", { name: "已打开窗口" });
    expect(within(dock).getAllByRole("button").map((button) => button.getAttribute("aria-label"))).toEqual(["切换到终端", "最小化资源管理"]);

    await user.click(within(screen.getByRole("dialog", { name: "资源管理" })).getByRole("button", { name: "关闭资源管理" }));
    expect(within(dock).getAllByRole("button").map((button) => button.getAttribute("aria-label"))).toEqual(["最小化终端"]);
  });

  it("opens a terminal session from the desktop icon", async () => {
    installAPI({ terminalEnabled: true });
    const user = userEvent.setup();
    render(<App />);

    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    await user.click(within(desktop).getByRole("button", { name: "打开终端" }));

    const terminal = screen.getByRole("dialog", { name: "终端" });
    expect(await within(terminal).findByTestId("xterm-session")).toBeTruthy();
    expect(xterm.mounts).toBe(1);
  });

  it("keeps the terminal session alive while minimized", async () => {
    installAPI({ terminalEnabled: true });
    const user = userEvent.setup();
    render(<App />);
    await user.click(await screen.findByRole("button", { name: "打开终端" }));
    await screen.findByTestId("xterm-session");

    await user.click(within(screen.getByRole("dialog", { name: "终端" })).getByRole("button", { name: "最小化终端" }));
    expect(screen.queryByRole("dialog", { name: "终端" })).toBeNull();
    await user.click(screen.getByRole("button", { name: "恢复终端" }));

    expect(screen.getByRole("dialog", { name: "终端" })).toBeTruthy();
    expect(xterm.mounts).toBe(1);
  });

  it("offers a new session after the shell exits", async () => {
    installAPI({ terminalEnabled: true });
    const user = userEvent.setup();
    render(<App />);
    await user.click(await screen.findByRole("button", { name: "打开终端" }));
    await screen.findByTestId("xterm-session");

    act(() => xterm.onStatus?.({ state: "ended", exitCode: 0 }));
    expect(await screen.findByText("Shell 已退出（代码 0）")).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "新建会话" }));
    expect(screen.queryByText("Shell 已退出（代码 0）")).toBeNull();
    expect(xterm.mounts).toBe(2);
  });

  it("explains when the terminal is disabled", async () => {
    installAPI({ terminalEnabled: false });
    const user = userEvent.setup();
    render(<App />);
    await user.click(await screen.findByRole("button", { name: "打开终端" }));

    expect(await screen.findByText("终端未启用")).toBeTruthy();
    expect(screen.queryByTestId("xterm-session")).toBeNull();
  });
});

function installAPI(options: { setupRequired?: boolean; spaces?: unknown[]; entries?: unknown[]; storagePlan?: unknown; terminalEnabled?: boolean; session?: unknown; notifications?: unknown[]; users?: unknown[] } = {}) {
  const mock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input);
    if (path === "/api/v1/setup/status") return ok({ setupRequired: options.setupRequired ?? false });
    if (path === "/api/v1/setup/admin" && init?.method === "POST") return ok(session, 201);
    if (path === "/api/v1/session") return ok(options.session ?? session);
    if (path === "/api/v1/notifications") return ok({ items: options.notifications ?? [] });
    if (path === "/api/v1/host-state") return ok(healthyState);
    if (path === "/api/v1/metrics") return ok(metricsState);
    if (path === "/api/v1/terminal") return ok({ enabled: options.terminalEnabled ?? false });
    if (path === "/api/v1/spaces") return ok({ items: options.spaces ?? [] });
    if (path.startsWith("/api/v1/spaces/") && path.includes("/entries")) return ok({ items: options.entries ?? [] });
    if (path === "/api/v1/volumes") return ok({ items: [] });
	if (path === "/api/v1/storage/plans" && init?.method === "POST") return ok(options.storagePlan ?? {}, 201);
    if (path === "/api/v1/users") return ok({ items: options.users ?? [session.user] });
    if (path === "/api/v1/trash") return ok({ items: [] });
    return ok({ items: [] });
  });
  vi.stubGlobal("fetch", mock);
  return mock;
}

function ok(body: unknown, status = 200) { return { ok: true, status, json: async () => body } as Response; }

function desktopOrder(desktop: HTMLElement): string[] {
  return within(desktop).getAllByRole("button").map((button) => button.textContent!.replace("规划中", ""));
}

// jsdom has no layout; give each slot the geometry of a 3-column, 100px grid.
function layOutGrid(desktop: HTMLElement) {
  const geometry = (slot: Element) => {
    const index = Array.from(slot.parentElement!.children).filter((child) => child.classList.contains("desktop-slot")).indexOf(slot);
    return { left: (index % 3) * 100, top: Math.floor(index / 3) * 100 };
  };
  desktop.querySelectorAll(".desktop-slot").forEach((slot) => {
    Object.defineProperties(slot, {
      offsetLeft: { configurable: true, get: () => geometry(slot).left },
      offsetTop: { configurable: true, get: () => geometry(slot).top },
      offsetWidth: { configurable: true, get: () => 100 },
      offsetHeight: { configurable: true, get: () => 100 },
    });
    slot.getBoundingClientRect = () => {
      const { left, top } = geometry(slot);
      return { left, top, right: left + 100, bottom: top + 100, width: 100, height: 100, x: left, y: top, toJSON: () => ({}) };
    };
  });
}

function routeFetch(routes: { hostState: () => Promise<Response>; metrics?: () => Promise<Response>; terminalEnabled?: boolean }) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      switch (String(input)) {
        case "/api/v1/setup/status":
          return okResponse({ setupRequired: false });
        case "/api/v1/session":
          return okResponse(session);
        case "/api/v1/host-state":
          return routes.hostState();
        case "/api/v1/metrics":
          return routes.metrics ? routes.metrics() : okResponse(metricsState);
        case "/api/v1/terminal":
          return okResponse({ enabled: routes.terminalEnabled ?? true });
        default:
          return okResponse({ items: [] });
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

describe("ADR 0008 account safeguards", () => {
  it("requires a member whose password was reset to choose a new one first", async () => {
    const fetchMock = installAPI({ session: { ...session, user: { ...session.user, username: "alice", role: "member", mustChangePassword: true } } });
    const user = userEvent.setup();
    render(<App />);
    expect(await screen.findByRole("heading", { name: "设置新密码" })).toBeTruthy();
    expect(screen.queryByRole("region", { name: "桌面应用" })).toBeNull();
    await user.type(screen.getByLabelText("当前密码"), "temporary password 1");
    await user.type(screen.getByLabelText("新密码"), "alice chosen password");
    await user.type(screen.getByLabelText("确认新密码"), "alice chosen password");
    await user.click(screen.getByRole("button", { name: "保存新密码" }));
    expect(await screen.findByRole("region", { name: "桌面应用" })).toBeTruthy();
    const call = fetchMock.mock.calls.find(([path]) => path === "/api/v1/session/password");
    expect(JSON.parse(String(call?.[1]?.body))).toEqual({ currentPassword: "temporary password 1", newPassword: "alice chosen password" });
  });

  it("tells the owner when an administrator viewed the private space", async () => {
    const fetchMock = installAPI({ notifications: [{
      id: "notification:1", kind: "admin_viewing", actorUsername: "owner", reason: "找回照片",
      expiresAt: "2026-10-08T10:00:00Z", createdAt: "2026-10-07T10:00:00Z",
    }] });
    const user = userEvent.setup();
    render(<App />);
    const notice = await screen.findByRole("alertdialog", { name: "账号通知" });
    expect(within(notice).getByText(/管理员 owner .*只读查看.*原因：找回照片/)).toBeTruthy();
    await user.click(within(notice).getByRole("button", { name: "知道了" }));
    expect(screen.queryByRole("alertdialog", { name: "账号通知" })).toBeNull();
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/notifications/notification%3A1/acknowledge", expect.objectContaining({ method: "POST" }));
  });

  it("lets an administrator start read-only viewing with a reason and password", async () => {
    const alice = { id: "user:alice", username: "alice", role: "member", status: "active", createdAt: "2026-10-07T10:00:00Z" };
    const fetchMock = installAPI({ users: [session.user, alice] });
    const user = userEvent.setup();
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    await user.click(within(desktop).getByRole("button", { name: "打开账号管理" }));
    await user.click(await screen.findByRole("button", { name: "查看个人空间" }));
    const form = screen.getByRole("form", { name: "查看 alice 的个人空间" });
    await user.type(within(form).getByLabelText("查看原因"), "Alice 请求找回文件");
    await user.type(within(form).getByLabelText("你的密码"), "correct horse battery staple");
    await user.click(within(form).getByRole("button", { name: "开始只读查看" }));
    expect(await screen.findByText(/已开启对 alice 个人空间的只读查看/)).toBeTruthy();
    const call = fetchMock.mock.calls.find(([path]) => path === "/api/v1/users/user%3Aalice/viewing");
    expect(JSON.parse(String(call?.[1]?.body))).toEqual({ password: "correct horse battery staple", reason: "Alice 请求找回文件" });
  });

  it("shows a viewed private space read-only and can end the viewing", async () => {
    const fetchMock = installAPI({
      spaces: [{ id: "space:alice", kind: "private", name: "alice", ownerUserId: "user:alice", createdAt: "2026-10-07T10:00:00Z", viewing: { grantId: "viewing:1", expiresAt: "2026-10-08T10:00:00Z" } }],
      entries: [{ id: "file:1", spaceId: "space:alice", name: "photo.jpg", kind: "file", sizeBytes: 10, modifiedAt: "2026-10-07T10:00:00Z" }],
    });
    const user = userEvent.setup();
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    await user.click(within(desktop).getByRole("button", { name: "打开文件管理" }));
    expect(await screen.findByText("photo.jpg")).toBeTruthy();
    expect(screen.getByRole("status").textContent).toContain("只读查看他人个人空间");
    expect(screen.queryByRole("button", { name: "新建目录" })).toBeNull();
    expect(screen.queryByRole("button", { name: "删除" })).toBeNull();
    await user.click(screen.getByRole("button", { name: "结束查看" }));
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/viewing/viewing%3A1", expect.objectContaining({ method: "DELETE" }));
  });
});
