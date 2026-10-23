import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import App from "./App";
import type { AppListResponse as AppList, AppStatus } from "./api.generated";
import { apiError, json } from "./test/api";
import { app } from "./test/fixtures";
import {
  errorResponse,
  healthyState,
  installAPI,
  metricsState,
  okResponse,
  resetDesktop,
  routeFetch,
  session,
  sessionExpiredResponse,
  stubDesktopFetch,
  stubFetch,
} from "./test/desktop";

// The task manager has its own tests with full device samples; here only the
// way into it matters.
vi.mock("./taskmanager/TaskManagerSection", () => ({
  TaskManagerSection: () => <div data-testid="task-manager" />,
}));

beforeEach(() => {
  localStorage.clear();
});

afterEach(resetDesktop);

const containerSnapshot = {
  dataSource: "simulated",
  observedAt: "2026-10-06T00:00:00Z",
  engine: { version: "29.1.3", apiVersion: "1.52" },
  containers: [
    {
      id: "a".repeat(64),
      name: "jellyfin",
      image: "jellyfin/jellyfin:10.11",
      state: "running",
      status: "Up 3 hours",
      createdAt: "2026-10-03T00:00:00Z",
      project: "media",
      ports: [{ hostIp: "0.0.0.0", hostPort: 8096, containerPort: 8096, protocol: "tcp" }],
      usage: { cpuPercent: 12.4, memoryBytes: 641728512, memoryLimitBytes: 8589934592 },
    },
    {
      id: "b".repeat(64),
      name: "homeassistant",
      image: "ghcr.io/home-assistant/home-assistant:stable",
      state: "exited",
      status: "Exited (0) 5 hours ago",
      createdAt: "2026-09-26T00:00:00Z",
      ports: [],
    },
  ],
  images: [
    {
      id: "sha256:" + "c".repeat(64),
      tags: ["jellyfin/jellyfin:10.11"],
      sizeBytes: 1243000000,
      createdAt: "2026-09-06T00:00:00Z",
    },
  ],
};

const memosPlan = {
  appId: "memos",
  title: "Memos",
  version: "0.28.0",
  project: "a-nas-memos",
  identity: { username: "app-memos", uid: 30000, gid: 30000 },
  images: ["neosmemo/memos:0.28.0"],
  containers: ["memos"],
  ports: [{ hostPort: 5230, containerPort: 5230, protocol: "tcp", purpose: "WebUI 端口" }],
  mounts: [
    { hostPath: "/srv/a-nas/data/apps/memos/memos", containerPath: "/var/opt/memos", kind: "appdata", readOnly: false },
  ],
  networks: [] as string[],
  addressPools: [] as string[],
  digest: "d".repeat(64),
  compose: "name: a-nas-memos\n",
};

function appList(memos: Partial<AppStatus>): AppList {
  return {
    dataSource: "simulated",
    apps: [
      app("memos", { title: "Memos", tagline: "轻量笔记", category: "notes", webPort: 5230, ...memos }),
      app("navidrome", { title: "Navidrome", tagline: "音乐流媒体", category: "media", webPort: 4533 }),
    ],
  };
}

describe("A-NAS v1.0.1 desktop", () => {
  it("sends a LAN browser to the device's own screen to set it up", async () => {
    const fetchMock = installAPI({ setupRequired: true, setupAllowed: false });
    render(<App />);
    expect(await screen.findByRole("heading", { name: "请在 NAS 本机启用" })).toBeTruthy();
    expect(screen.getByText(/SSH 隧道/)).toBeTruthy();
    expect(screen.queryByLabelText("账号")).toBeNull();
    expect(fetchMock.mock.calls.some(([path]) => path === "/api/v1/setup/admin")).toBe(false);
  });

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
    expect(JSON.parse(String(setupCall?.[1]?.body))).toEqual({
      username: "owner",
      password: "correct horse battery staple",
    });
  });

  it("asks for a session that lasts until sign-out on the local console", async () => {
    window.history.replaceState(null, "", "/?local-console=1");
    try {
      const fetchMock = installAPI({ setupRequired: true });
      const user = userEvent.setup();
      render(<App />);
      await user.type(await screen.findByLabelText("账号"), "owner");
      await user.type(screen.getByLabelText("密码"), "correct horse battery staple");
      await user.click(screen.getByRole("button", { name: "创建管理员并启用" }));
      expect(await screen.findByRole("region", { name: "桌面应用" })).toBeTruthy();
      const setupCall = fetchMock.mock.calls.find(([path]) => path === "/api/v1/setup/admin");
      expect(JSON.parse(String(setupCall?.[1]?.body))).toEqual({
        username: "owner",
        password: "correct horse battery staple",
        localConsole: true,
      });
    } finally {
      window.history.replaceState(null, "", "/");
    }
  });

  it("shows a loading state while the first host observation is pending", async () => {
    let finishRequest: ((response: Response) => void) | undefined;
    routeFetch({
      hostState: vi.fn(
        () =>
          new Promise<Response>((resolve) => {
            finishRequest = resolve;
          }),
      ),
    });
    const user = userEvent.setup();
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    await user.click(within(desktop).getByRole("button", { name: "打开设置" }));
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
    await user.click(within(desktop).getByRole("button", { name: "打开设置" }));
    expect(await screen.findByText("anas-fake")).toBeTruthy();
    const before = screen.getByText(/\d{2}:\d{2}/, { selector: ".status-time strong" }).textContent;
    act(() => vi.advanceTimersByTime(60_000));
    expect(screen.getByText(/\d{2}:\d{2}/, { selector: ".status-time strong" }).textContent).not.toBe(before);
  });

  it("enables photos, trash, the backup center, and one settings entry", async () => {
    installAPI();
    const user = userEvent.setup();
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    expect(within(desktop).getByRole("button", { name: "打开回收站" }).hasAttribute("disabled")).toBe(false);
    expect(within(desktop).getByRole("button", { name: "打开备份中心" }).hasAttribute("disabled")).toBe(false);
    expect(within(desktop).getByRole("button", { name: "打开设置" })).toBeTruthy();
    // 文件快照 and the planned 备份 entry became the one 备份中心; the planned 日志 moved into 设置.
    // The terminal moved into 设置 → 终端.
    for (const retired of [
      "打开系统设置",
      "打开资源管理",
      "打开存储初始化",
      "打开账号管理",
      "打开文件快照",
      "备份，规划中",
      "日志，规划中",
      "打开终端",
    ]) {
      expect(within(desktop).queryByRole("button", { name: retired })).toBeNull();
    }
    await user.click(within(desktop).getByRole("button", { name: "打开相册" }));
    expect(await screen.findByRole("dialog", { name: "相册" })).toBeTruthy();
  });

  it("keeps the last successful state when refresh loses connection", async () => {
    routeFetch({
      hostState: vi.fn().mockResolvedValueOnce(okResponse(healthyState)).mockResolvedValueOnce(errorResponse(503)),
    });
    const user = userEvent.setup();

    render(<App />);
    await user.click(
      within(await screen.findByRole("region", { name: "桌面应用" })).getByRole("button", { name: "打开设置" }),
    );
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
    await user.click(
      within(await screen.findByRole("region", { name: "桌面应用" })).getByRole("button", { name: "打开设置" }),
    );
    expect(await screen.findByText("暂时无法读取设备状态")).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "重新连接" }));
    expect(await screen.findByText("anas-fake")).toBeTruthy();
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("opens, minimizes, and restores settings", async () => {
    installAPI();
    const user = userEvent.setup();
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    await user.click(within(desktop).getByRole("button", { name: "打开设置" }));
    const settings = screen.getByRole("dialog", { name: "设置" });
    await user.click(within(settings).getByRole("button", { name: "最小化设置" }));
    await user.click(screen.getByRole("button", { name: "恢复设置" }));
    expect(screen.getByRole("dialog", { name: "设置" })).toBeTruthy();
  });

  it("shows utilisation gauges, network rates and the clock in the status bar, and opens transfers from the rates", async () => {
    stubFetch(healthyState);
    const user = userEvent.setup();
    render(<App />);

    const statusBar = await screen.findByRole("banner", { name: "设备状态" });
    await within(statusBar).findByText("4 核");
    expect(within(statusBar).getByRole("meter", { name: "CPU 占用" }).getAttribute("aria-valuenow")).toBe("24");
    expect(within(statusBar).getByRole("meter", { name: "内存占用" }).getAttribute("aria-valuenow")).toBe("40");
    expect(within(statusBar).getByText("3.2 / 8.0 GiB")).toBeTruthy();
    const network = within(statusBar).getByRole("button", { name: "网络速率，打开传输中心" });
    expect(network.textContent).toContain("下行2.5MB/s");
    expect(network.textContent).toContain("上行328KB/s");
    expect(within(statusBar).getByText(/^\d{2}:\d{2}$/)).toBeTruthy();
    expect(within(statusBar).getByText("设备在线")).toBeTruthy();
    await user.click(network);
    expect(await screen.findByRole("dialog", { name: "传输中心" })).toBeTruthy();
  });

  it("opens the task manager from the utilisation gauges, also when settings shows another section", async () => {
    stubFetch(healthyState);
    const user = userEvent.setup();
    render(<App />);

    const statusBar = await screen.findByRole("banner", { name: "设备状态" });
    const gauges = within(statusBar).getByRole("button", { name: "CPU 与内存占用，打开任务管理器" });
    // Screen readers do not read a button's content, so the meters must stay outside it.
    for (const name of ["CPU 占用", "内存占用"]) {
      expect(gauges.contains(within(statusBar).getByRole("meter", { name }))).toBe(false);
    }
    await user.click(gauges);
    const settings = await screen.findByRole("dialog", { name: "设置" });
    const section = (name: string) => within(settings).getByRole("button", { name });
    expect(section("任务管理器").getAttribute("aria-current")).toBe("page");
    expect(within(settings).getByTestId("task-manager")).toBeTruthy();

    await user.click(section("概览"));
    expect(within(settings).queryByTestId("task-manager")).toBeNull();
    await user.click(gauges);
    expect(section("任务管理器").getAttribute("aria-current")).toBe("page");
    expect(within(settings).getByTestId("task-manager")).toBeTruthy();
  });

  it("restores minimized settings on the task manager when the gauges are activated from the keyboard", async () => {
    stubFetch(healthyState);
    const user = userEvent.setup();
    render(<App />);
    const statusBar = await screen.findByRole("banner", { name: "设备状态" });
    const gauges = within(statusBar).getByRole("button", { name: "CPU 与内存占用，打开任务管理器" });

    for (const key of ["{Enter}", " "]) {
      gauges.focus();
      await user.keyboard(key);
      expect(screen.queryByRole("button", { name: "恢复设置" })).toBeNull();
      const settings = await screen.findByRole("dialog", { name: "设置" });
      expect(within(settings).getByRole("button", { name: "任务管理器" }).getAttribute("aria-current")).toBe("page");
      await user.click(within(settings).getByRole("button", { name: "概览" }));
      await user.click(within(settings).getByRole("button", { name: "最小化设置" }));
      expect(screen.getByRole("button", { name: "恢复设置" })).toBeTruthy();
    }
  });

  it("closes a half-filled password form when the gauges switch settings to the task manager", async () => {
    installAPI({ users: [session.user] });
    const user = userEvent.setup();
    render(<App />);
    const openAccount = async () => {
      await user.click(await screen.findByRole("button", { name: "账号（owner）" }));
      await user.click(screen.getByRole("menuitem", { name: /^我的账号/ }));
      return screen.getByRole("dialog", { name: "设置" });
    };
    let settings = await openAccount();
    await user.click(within(settings).getByRole("button", { name: "修改密码…" }));
    await user.type(within(settings).getByLabelText("当前密码"), "correct horse battery staple");

    const statusBar = screen.getByRole("banner", { name: "设备状态" });
    await user.click(within(statusBar).getByRole("button", { name: "CPU 与内存占用，打开任务管理器" }));
    expect(within(settings).getByTestId("task-manager")).toBeTruthy();

    settings = await openAccount();
    expect(within(settings).queryByRole("form", { name: "修改密码" })).toBeNull();
    expect(within(settings).getByRole("button", { name: "修改密码…" })).toBeTruthy();
  });

  it("marks the status bar disconnected when metrics cannot be read", async () => {
    routeFetch({
      hostState: vi.fn().mockResolvedValue(okResponse(healthyState)),
      metrics: vi.fn().mockResolvedValue(errorResponse(503)),
    });
    render(<App />);

    const statusBar = await screen.findByRole("banner", { name: "设备状态" });
    expect(await within(statusBar).findByText("连接中断", { selector: ".source-badge" })).toBeTruthy();
    expect(within(statusBar).getByRole("meter", { name: "CPU 占用" }).getAttribute("aria-valuetext")).toBe("暂无数据");
  });

  it("returns to the login screen when the session expires instead of reporting a lost connection", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let expired = false;
    const poll = (state: unknown) => vi.fn(async () => (expired ? sessionExpiredResponse() : okResponse(state)));
    routeFetch({ hostState: poll(healthyState), metrics: poll(metricsState) });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    render(<App />);
    await screen.findByRole("region", { name: "桌面应用" });

    expired = true;
    await act(async () => {
      vi.advanceTimersByTime(2_000);
    });
    expect(await screen.findByRole("heading", { name: "登录 A-NAS" })).toBeTruthy();
    expect(screen.getByText("登录已过期，请重新登录")).toBeTruthy();
    expect(screen.queryByText(/连接中断/)).toBeNull();

    expired = false;
    await user.type(screen.getByLabelText("账号"), "owner");
    await user.type(screen.getByLabelText("密码"), "correct horse battery staple");
    await user.click(screen.getByRole("button", { name: "登录" }));
    expect(await screen.findByRole("region", { name: "桌面应用" })).toBeTruthy();
    expect(screen.queryByText("登录已过期，请重新登录")).toBeNull();
  });

  it("reorders desktop icons by dragging without opening the app", async () => {
    stubFetch(healthyState);
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    layOutGrid(desktop);
    expect(desktopOrder(desktop).slice(0, 5)).toEqual(["文件管理", "回收站", "设置", "应用中心", "影视中心"]);

    const store = within(desktop).getByRole("button", { name: "打开应用中心" }).parentElement!;
    fireEvent.pointerDown(store, { button: 0, pointerId: 1, pointerType: "mouse", clientX: 50, clientY: 160 });
    fireEvent.pointerMove(window, { pointerId: 1, clientX: 40, clientY: 140 });
    const dragPreview = document.querySelector(".desktop-drag-preview");
    expect(dragPreview).toBeTruthy();
    expect(desktop.contains(dragPreview)).toBe(false);
    fireEvent.pointerMove(window, { pointerId: 1, clientX: 40, clientY: 50 });
    fireEvent.pointerUp(window, { pointerId: 1, clientX: 40, clientY: 50 });
    expect(document.querySelector(".desktop-drag-preview")).toBeNull();
    fireEvent.click(within(desktop).getByRole("button", { name: "打开应用中心" }));

    expect(desktopOrder(desktop).slice(0, 5)).toEqual(["应用中心", "文件管理", "回收站", "设置", "影视中心"]);
    expect(JSON.parse(localStorage.getItem("a-nas.desktop-order.v1")!).slice(0, 2)).toEqual(["store", "files"]);
    expect(screen.queryByRole("dialog", { name: "应用中心" })).toBeNull();
    expect(screen.getByText("已将应用中心移动到第 1 位")).toBeTruthy();
  });

  it("restores the original order when a drag is cancelled with Escape", async () => {
    stubFetch(healthyState);
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    layOutGrid(desktop);

    const settings = within(desktop).getByRole("button", { name: "打开设置" }).parentElement!;
    fireEvent.pointerDown(settings, { button: 0, pointerId: 2, pointerType: "mouse", clientX: 250, clientY: 50 });
    fireEvent.pointerMove(window, { pointerId: 2, clientX: 40, clientY: 50 });
    expect(desktopOrder(desktop)[0]).toBe("设置");
    fireEvent.keyDown(window, { key: "Escape" });

    expect(desktopOrder(desktop).slice(0, 3)).toEqual(["文件管理", "回收站", "设置"]);
    expect(localStorage.getItem("a-nas.desktop-order.v1")).toBeNull();
  });

  it("moves a focused icon with Alt and the arrow keys and restores the saved order", async () => {
    stubFetch(healthyState);
    const user = userEvent.setup();
    const { unmount } = render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });

    within(desktop).getByRole("button", { name: "打开应用中心" }).focus();
    await user.keyboard("{Alt>}{ArrowLeft}{/Alt}");
    expect(desktopOrder(desktop).slice(0, 4)).toEqual(["文件管理", "回收站", "应用中心", "设置"]);
    expect(document.activeElement).toBe(within(desktop).getByRole("button", { name: "打开应用中心" }));
    unmount();

    render(<App />);
    expect(desktopOrder(await screen.findByRole("region", { name: "桌面应用" })).slice(0, 4)).toEqual([
      "文件管理",
      "回收站",
      "应用中心",
      "设置",
    ]);
  });

  it("switches focus between open apps from an icon-only dock", async () => {
    stubDesktopFetch(healthyState, true);
    const user = userEvent.setup();
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    await user.click(within(desktop).getByRole("button", { name: "打开设置" }));
    await user.click(within(desktop).getByRole("button", { name: "打开回收站" }));

    const dock = screen.getByRole("navigation", { name: "已打开窗口" });
    expect(dock.textContent).toBe("");
    expect(within(dock).getByRole("button", { name: "最小化回收站" }).getAttribute("aria-current")).toBe("true");

    await user.click(within(dock).getByRole("button", { name: "切换到设置" }));
    expect(within(dock).getByRole("button", { name: "最小化设置" }).getAttribute("aria-current")).toBe("true");
    expect(within(dock).getByRole("button", { name: "切换到回收站" })).toBeTruthy();

    await user.click(within(dock).getByRole("button", { name: "最小化设置" }));
    expect(screen.queryByRole("dialog", { name: "设置" })).toBeNull();
    expect(within(dock).getByRole("button", { name: "最小化回收站" }).getAttribute("aria-current")).toBe("true");
    expect(within(dock).getByRole("button", { name: "恢复设置" })).toBeTruthy();
  });

  it("previews a dock app's window on hover, shows or closes it from there, and hides the preview on click", async () => {
    stubDesktopFetch(healthyState, true);
    const user = userEvent.setup();
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    await user.click(within(desktop).getByRole("button", { name: "打开设置" }));
    await user.click(within(desktop).getByRole("button", { name: "打开回收站" }));
    const dock = screen.getByRole("navigation", { name: "已打开窗口" });

    await user.hover(within(dock).getByRole("button", { name: "切换到设置" }));
    const settings = await screen.findByRole("group", { name: "设置窗口预览" });
    expect(within(settings).queryByText("已最小化")).toBeNull();
    await user.click(within(settings).getByRole("button", { name: "显示设置窗口" }));
    expect(within(dock).getByRole("button", { name: "最小化设置" }).getAttribute("aria-current")).toBe("true");
    await waitFor(() => expect(screen.queryByRole("group", { name: "设置窗口预览" })).toBeNull());

    await user.hover(within(dock).getByRole("button", { name: "切换到回收站" }));
    await user.click(
      within(await screen.findByRole("group", { name: "回收站窗口预览" })).getByRole("button", { name: "关闭回收站" }),
    );
    expect(screen.queryByRole("dialog", { name: "回收站" })).toBeNull();
    expect(
      within(dock)
        .getAllByRole("button")
        .map((button) => button.getAttribute("aria-label")),
    ).toEqual(["最小化设置"]);

    await user.hover(within(dock).getByRole("button", { name: "最小化设置" }));
    await screen.findByRole("group", { name: "设置窗口预览" });
    await user.click(within(dock).getByRole("button", { name: "最小化设置" }));
    await new Promise((resolve) => setTimeout(resolve, 600));
    expect(screen.queryByRole("group", { name: "设置窗口预览" })).toBeNull();

    await user.unhover(within(dock).getByRole("button", { name: "恢复设置" }));
    await user.hover(within(dock).getByRole("button", { name: "恢复设置" }));
    expect(within(await screen.findByRole("group", { name: "设置窗口预览" })).getByText("已最小化")).toBeTruthy();
  });

  it("lists dock icons in launch order and drops closed apps immediately from the accessibility tree", async () => {
    stubDesktopFetch(healthyState, true);
    const user = userEvent.setup();
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    await user.click(within(desktop).getByRole("button", { name: "打开回收站" }));
    await user.click(within(desktop).getByRole("button", { name: "打开设置" }));

    const dock = screen.getByRole("navigation", { name: "已打开窗口" });
    expect(
      within(dock)
        .getAllByRole("button")
        .map((button) => button.getAttribute("aria-label")),
    ).toEqual(["切换到回收站", "最小化设置"]);

    await user.click(within(screen.getByRole("dialog", { name: "设置" })).getByRole("button", { name: "关闭设置" }));
    expect(
      within(dock)
        .getAllByRole("button")
        .map((button) => button.getAttribute("aria-label")),
    ).toEqual(["最小化回收站"]);
  });

  it("keeps the system rail to working shortcuts", async () => {
    stubDesktopFetch(healthyState, true);
    render(<App />);
    const rail = await screen.findByRole("complementary", { name: "系统快捷栏" });
    const buttons = within(rail).getAllByRole("button");
    expect(buttons.map((button) => button.getAttribute("aria-label"))).toEqual([
      "显示桌面",
      "全部应用",
      "打开分享",
      "打开设置",
      "账号（owner）",
    ]);
    expect(buttons.some((button) => button.hasAttribute("disabled"))).toBe(false);
    expect(within(rail).getByRole("button", { name: "显示桌面" }).getAttribute("aria-pressed")).toBe("true");
  });

  it("hides the open windows from the rail and brings back only those", async () => {
    stubDesktopFetch(healthyState, true);
    const user = userEvent.setup();
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    await user.click(within(desktop).getByRole("button", { name: "打开文件管理" }));
    await user.click(within(desktop).getByRole("button", { name: "打开设置" }));
    await user.click(within(desktop).getByRole("button", { name: "打开回收站" }));
    const dock = screen.getByRole("navigation", { name: "已打开窗口" });
    await user.click(within(dock).getByRole("button", { name: "切换到文件管理" }));
    await user.click(within(dock).getByRole("button", { name: "最小化文件管理" }));

    const showDesktop = within(screen.getByRole("complementary", { name: "系统快捷栏" })).getByRole("button", {
      name: "显示桌面",
    });
    expect(showDesktop.getAttribute("aria-pressed")).toBe("false");
    await user.click(showDesktop);
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(showDesktop.getAttribute("aria-pressed")).toBe("true");

    await user.click(showDesktop);
    expect(screen.getAllByRole("dialog").map((dialog) => dialog.getAttribute("aria-label"))).toEqual([
      "回收站",
      "设置",
    ]);
    expect(within(dock).getByRole("button", { name: "最小化回收站" }).getAttribute("aria-current")).toBe("true");
    expect(within(dock).getByRole("button", { name: "恢复文件管理" })).toBeTruthy();
    expect(showDesktop.getAttribute("aria-pressed")).toBe("false");
  });

  it("puts the windows away when the bare desktop is clicked", async () => {
    stubDesktopFetch(healthyState, true);
    const user = userEvent.setup();
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    const wallpaper = document.querySelector<HTMLElement>(".desktop-shell")!;
    const rail = screen.getByRole("complementary", { name: "系统快捷栏" });
    const showDesktop = within(rail).getByRole("button", { name: "显示桌面" });
    const openWindows = () => screen.queryAllByRole("dialog").map((dialog) => dialog.getAttribute("aria-label"));
    await user.click(within(desktop).getByRole("button", { name: "打开设置" }));
    await user.click(within(desktop).getByRole("button", { name: "打开回收站" }));

    // A press inside a window keeps the windows.
    await user.click(screen.getByRole("dialog", { name: "设置" }));
    expect(openWindows()).toEqual(["回收站", "设置"]);
    // So does a window drag released over the wallpaper, and a press on the wallpaper that moves away.
    const titleBar = screen.getByRole("dialog", { name: "回收站" }).querySelector<HTMLElement>(".window-titlebar")!;
    await user.pointer([
      { keys: "[MouseLeft>]", target: titleBar, coords: { clientX: 400, clientY: 90 } },
      { target: wallpaper, coords: { clientX: 40, clientY: 600 } },
      { keys: "[/MouseLeft]" },
    ]);
    await user.pointer([
      { keys: "[MouseLeft>]", target: wallpaper, coords: { clientX: 1200, clientY: 600 } },
      { coords: { clientX: 1100, clientY: 500 } },
      { keys: "[/MouseLeft]" },
    ]);
    expect(openWindows()).toEqual(["回收站", "设置"]);
    // The click that closes the launcher only closes it.
    await user.click(within(rail).getByRole("button", { name: "全部应用" }));
    await user.click(wallpaper);
    expect(screen.queryByRole("dialog", { name: "全部应用" })).toBeNull();
    expect(openWindows()).toEqual(["回收站", "设置"]);

    await user.click(wallpaper);
    expect(openWindows()).toEqual([]);
    expect(showDesktop.getAttribute("aria-pressed")).toBe("true");
    // Clicking it again leaves them away; 显示桌面 brings them back with the focus where it was.
    await user.click(desktop);
    await user.click(showDesktop);
    expect(openWindows()).toEqual(["回收站", "设置"]);
    expect(
      within(screen.getByRole("navigation", { name: "已打开窗口" }))
        .getByRole("button", { name: "最小化回收站" })
        .getAttribute("aria-current"),
    ).toBe("true");

    // The gaps between desktop icons count as the desktop, and a single window goes away too.
    await user.click(within(screen.getByRole("dialog", { name: "设置" })).getByRole("button", { name: "关闭设置" }));
    await user.click(desktop);
    expect(openWindows()).toEqual([]);
  });

  it("finds an app in the launcher by what it does and opens it with Enter", async () => {
    stubDesktopFetch(healthyState, true);
    const user = userEvent.setup();
    render(<App />);
    const rail = await screen.findByRole("complementary", { name: "系统快捷栏" });
    await user.click(within(rail).getByRole("button", { name: "全部应用" }));
    const launcher = screen.getByRole("dialog", { name: "全部应用" });
    const search = within(launcher).getByRole("searchbox", { name: "搜索应用" });
    expect(document.activeElement).toBe(search);
    expect(
      within(launcher)
        .getAllByRole("button")
        .map((button) => button.getAttribute("aria-label")),
    ).toContain("打开相册");
    expect(within(launcher).getByRole("list", { name: "规划中的应用" }).textContent).toContain("VNAS");

    expect(within(launcher).getByRole("list", { name: "规划中的应用" }).textContent).not.toContain("日志");
    await user.type(search, "日志");
    expect(
      within(launcher)
        .getAllByRole("button")
        .map((button) => button.getAttribute("aria-label")),
    ).toEqual(["打开设置"]);
    await user.clear(search);
    await user.type(search, "命令行");
    expect(
      within(launcher)
        .getAllByRole("button")
        .map((button) => button.getAttribute("aria-label")),
    ).toEqual(["打开设置"]);
    await user.keyboard("{Enter}");
    expect(screen.queryByRole("dialog", { name: "全部应用" })).toBeNull();
    expect(screen.getByRole("dialog", { name: "设置" })).toBeTruthy();
  });

  it("closes the launcher with Escape and returns focus to the rail", async () => {
    stubDesktopFetch(healthyState, true);
    const user = userEvent.setup();
    render(<App />);
    const trigger = within(await screen.findByRole("complementary", { name: "系统快捷栏" })).getByRole("button", {
      name: "全部应用",
    });
    await user.click(trigger);
    expect(trigger.getAttribute("aria-expanded")).toBe("true");
    const launcher = screen.getByRole("dialog", { name: "全部应用" });
    await user.type(within(launcher).getByRole("searchbox", { name: "搜索应用" }), "传真");
    expect(within(launcher).getByRole("status").textContent).toBe("没有名为“传真”的应用");
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog", { name: "全部应用" })).toBeNull();
    expect(trigger.getAttribute("aria-expanded")).toBe("false");
    expect(document.activeElement).toBe(trigger);
  });

  it("signs out only from the account menu", async () => {
    const fetchMock = installAPI();
    const user = userEvent.setup();
    render(<App />);
    await user.click(await screen.findByRole("button", { name: "账号（owner）" }));
    const account = screen.getByRole("region", { name: "账号" });
    expect(account.textContent).toContain("管理员");
    expect(account.textContent).toContain("浏览器登录 · 有效至");
    expect(document.activeElement).toBe(within(account).getByRole("menuitem", { name: /^我的账号/ }));
    await user.keyboard("{ArrowDown}");
    expect(document.activeElement).toBe(within(account).getByRole("menuitem", { name: "退出登录" }));
    expect(fetchMock.mock.calls.some(([, init]) => init?.method === "DELETE")).toBe(false);
    await user.keyboard("{Enter}");
    expect(await screen.findByRole("button", { name: "登录" })).toBeTruthy();
    expect(fetchMock.mock.calls.some(([path, init]) => path === "/api/v1/session" && init?.method === "DELETE")).toBe(
      true,
    );
  });

  it("manages containers from the Docker app", async () => {
    const actions: Array<{ id: string; body: unknown }> = [];
    routeFetch({
      hostState: vi.fn().mockResolvedValue(okResponse(healthyState)),
      containers: async (url, init) => {
        if (init?.method === "POST") {
          actions.push({ id: url.split("/")[4], body: JSON.parse(String(init.body)) });
          return json(undefined, 204);
        }
        if (url.endsWith("/logs?tail=200")) {
          return okResponse({
            lines: [
              { stream: "stdout", time: "2026-10-06T00:00:01Z", text: "server ready" },
              { stream: "stderr", text: "warning: low disk" },
            ],
          });
        }
        return okResponse(containerSnapshot);
      },
    });
    const user = userEvent.setup();
    render(<App />);
    await user.click(
      within(await screen.findByRole("region", { name: "桌面应用" })).getByRole("button", { name: "打开 Docker" }),
    );

    const docker = screen.getByRole("dialog", { name: "Docker" });
    const jellyfin = await within(docker).findByRole("article", { name: "容器 jellyfin" });
    expect(within(jellyfin).getByText("运行中")).toBeTruthy();
    expect(within(jellyfin).getByText("已运行 3 小时")).toBeTruthy();
    expect(within(jellyfin).getByRole("link", { name: "打开 jellyfin 的端口 8096" }).getAttribute("href")).toMatch(
      /:8096\/$/,
    );
    expect(within(jellyfin).getByText("12.4%")).toBeTruthy();

    await user.click(within(jellyfin).getByRole("button", { name: "停止 jellyfin" }));
    expect(actions).toHaveLength(0);
    await user.click(
      within(within(docker).getByRole("dialog", { name: "确认停止 jellyfin" })).getByRole("button", {
        name: "确认停止",
      }),
    );
    expect(actions).toEqual([{ id: containerSnapshot.containers[0].id, body: { action: "stop" } }]);

    const stopped = within(docker).getByRole("article", { name: "容器 homeassistant" });
    await user.click(within(stopped).getByRole("button", { name: "启动 homeassistant" }));
    expect(actions[1]).toEqual({ id: containerSnapshot.containers[1].id, body: { action: "start" } });

    await user.click(within(jellyfin).getByRole("button", { name: "查看 jellyfin 的日志" }));
    const log = await within(docker).findByRole("log", { name: "jellyfin 日志" });
    expect(within(log).getByText("server ready")).toBeTruthy();
    expect(within(log).getByText("warning: low disk").parentElement?.className).toContain("stderr");
    await user.type(within(docker).getByRole("textbox", { name: "筛选日志" }), "disk");
    expect(within(log).queryByText("server ready")).toBeNull();

    // Leaving the log view must not crash the desktop (it once did through scrollIntoView's Promise).
    const nav = within(docker).getByRole("navigation", { name: "Docker 导航" });
    await user.click(within(nav).getByRole("button", { name: /镜像/ }));
    expect(within(within(docker).getByRole("table", { name: "镜像列表" })).getByText("jellyfin/jellyfin")).toBeTruthy();
  });

  it("explains when Docker management is disabled", async () => {
    routeFetch({
      hostState: vi.fn().mockResolvedValue(okResponse(healthyState)),
      containers: async () => apiError(503, "containers_disabled", "disabled"),
    });
    const user = userEvent.setup();
    render(<App />);
    await user.click(await screen.findByRole("button", { name: "打开 Docker" }));

    expect(await within(screen.getByRole("dialog", { name: "Docker" })).findByText("Docker 未启用")).toBeTruthy();
  });

  it("installs an app from the App Center after confirming its plan", async () => {
    const posts: Array<{ url: string; body: unknown }> = [];
    let installed = false;
    routeFetch({
      hostState: vi.fn().mockResolvedValue(okResponse(healthyState)),
      apps: async (url, init) => {
        if (init?.method === "POST") {
          posts.push({ url, body: JSON.parse(String(init.body)) });
          expect(new Headers(init.headers).get("X-CSRF-Token")).toBe(session.csrfToken);
          installed = url.endsWith("/install");
          return json(
            { appId: "memos", action: "install", state: "running", startedAt: "2026-10-07T00:00:00Z", output: [] },
            202,
          );
        }
        if (url.endsWith("/plan")) return okResponse(memosPlan);
        return okResponse(
          appList(
            installed
              ? {
                  state: "installed",
                  running: 1,
                  total: 1,
                  job: {
                    appId: "memos",
                    action: "install",
                    state: "succeeded",
                    startedAt: "2026-10-07T00:00:00Z",
                    output: ["Container memos  Started"],
                  },
                }
              : {},
          ),
        );
      },
    });
    const user = userEvent.setup();
    render(<App />);
    await user.click(
      within(await screen.findByRole("region", { name: "桌面应用" })).getByRole("button", { name: "打开应用中心" }),
    );

    const store = screen.getByRole("dialog", { name: "应用中心" });
    await user.click(await within(store).findByRole("button", { name: "安装 Memos" }));

    const plan = await within(store).findByRole("dialog", { name: "安装计划" });
    expect(await within(plan).findByText("neosmemo/memos:0.28.0")).toBeTruthy();
    expect(within(plan).getByText("5230/tcp")).toBeTruthy();
    expect(within(plan).getByText("/srv/a-nas/data/apps/memos/memos")).toBeTruthy();
    expect(within(plan).getByText("app-memos")).toBeTruthy();
    expect(within(plan).getByText(/只访问自己的应用数据/)).toBeTruthy();
    expect(within(plan).getByText("使用 Docker 默认网桥，不新建网络")).toBeTruthy();
    expect(posts).toHaveLength(0);

    await user.click(within(plan).getByRole("button", { name: "确认安装" }));
    expect(posts).toEqual([{ url: "/api/v1/apps/memos/install", body: { digest: memosPlan.digest } }]);
    expect(await within(store).findByText("Memos 已安装")).toBeTruthy();
    expect(within(store).getByText(/已安装 · 1\/1 个容器运行中/)).toBeTruthy();
    expect(within(store).getByText(/Container memos\s+Started/)).toBeTruthy();
  });

  it("explains install conflicts and a disabled App Center", async () => {
    routeFetch({
      hostState: vi.fn().mockResolvedValue(okResponse(healthyState)),
      apps: async (url, init) => {
        if (init?.method === "POST") {
          return apiError(409, "port_in_use", "a published port is already in use: 5230 is used by other");
        }
        if (url.endsWith("/plan")) return okResponse(memosPlan);
        return okResponse(appList({}));
      },
    });
    const user = userEvent.setup();
    const { unmount } = render(<App />);
    await user.click(await screen.findByRole("button", { name: "打开应用中心" }));
    const store = screen.getByRole("dialog", { name: "应用中心" });
    await user.click(await within(store).findByRole("button", { name: "安装 Memos" }));
    await user.click(await within(store).findByRole("button", { name: "确认安装" }));
    expect((await within(store).findByRole("alert")).textContent).toContain("端口已被占用（5230 is used by other）");
    unmount();

    routeFetch({
      hostState: vi.fn().mockResolvedValue(okResponse(healthyState)),
      apps: async () => apiError(503, "apps_disabled", "disabled"),
    });
    render(<App />);
    await user.click(await screen.findByRole("button", { name: "打开应用中心" }));
    expect(await screen.findByText("应用中心未启用")).toBeTruthy();
  });

  it("shows the Docker networks an app creates and refuses them without an address pool", async () => {
    let poolConfigured = true;
    routeFetch({
      hostState: vi.fn().mockResolvedValue(okResponse(healthyState)),
      apps: async (url) => {
        if (url.endsWith("/plan")) {
          return poolConfigured
            ? okResponse({ ...memosPlan, networks: ["default"], addressPools: ["10.96.64.0/19"] })
            : apiError(409, "address_pool_missing", "Docker has no address pool for app networks");
        }
        return okResponse(appList({}));
      },
    });
    const user = userEvent.setup();
    render(<App />);
    await user.click(await screen.findByRole("button", { name: "打开应用中心" }));
    const store = screen.getByRole("dialog", { name: "应用中心" });
    await user.click(await within(store).findByRole("button", { name: "Memos，可安装" }));
    await user.click(within(store).getByRole("button", { name: "安装" }));
    const plan = await within(store).findByRole("dialog", { name: "安装计划" });
    expect(await within(plan).findByText("default")).toBeTruthy();
    expect(within(plan).getByText(/地址只从 10\.96\.64\.0\/19 分配/)).toBeTruthy();

    await user.click(within(plan).getByRole("button", { name: "取消" }));
    poolConfigured = false;
    await user.click(within(store).getByRole("button", { name: "安装" }));
    expect((await within(store).findByRole("alert")).textContent).toContain("Docker 尚未配置 A-NAS 网络地址池");
  });

  it("explains when app installation is blocked by an unavailable data volume", async () => {
    routeFetch({
      hostState: vi.fn().mockResolvedValue(okResponse(healthyState)),
      apps: async (url, init) => {
        if (init?.method === "POST") {
          return apiError(423, "volume_unavailable", "the data volume is not available");
        }
        if (url.endsWith("/plan")) return okResponse(memosPlan);
        return okResponse(appList({}));
      },
    });
    const user = userEvent.setup();
    render(<App />);
    await user.click(await screen.findByRole("button", { name: "打开应用中心" }));
    const store = screen.getByRole("dialog", { name: "应用中心" });
    await user.click(await within(store).findByRole("button", { name: "Memos，可安装" }));
    await user.click(within(store).getByRole("button", { name: "安装" }));
    await user.click(await within(store).findByRole("button", { name: "确认安装" }));
    expect((await within(store).findByRole("alert")).textContent).toContain("数据卷当前不可用");
  });
});

function desktopOrder(desktop: HTMLElement): string[] {
  return within(desktop)
    .getAllByRole("button")
    .map((button) => button.textContent!.replace("规划中", ""));
}

// jsdom has no layout; give each slot the geometry of a 3-column, 100px grid.
function layOutGrid(desktop: HTMLElement) {
  const geometry = (slot: Element) => {
    const index = Array.from(slot.parentElement!.children)
      .filter((child) => child.classList.contains("desktop-slot"))
      .indexOf(slot);
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
      return {
        left,
        top,
        right: left + 100,
        bottom: top + 100,
        width: 100,
        height: 100,
        x: left,
        y: top,
        toJSON: () => ({}),
      };
    };
  });
}

describe("ADR 0008 account safeguards", () => {
  it("requires a member whose password was reset to choose a new one first", async () => {
    const fetchMock = installAPI({
      session: { ...session, user: { ...session.user, username: "alice", role: "member", mustChangePassword: true } },
    });
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
    expect(JSON.parse(String(call?.[1]?.body))).toEqual({
      currentPassword: "temporary password 1",
      newPassword: "alice chosen password",
    });
  });

  it("tells the owner when an administrator viewed the private space", async () => {
    const fetchMock = installAPI({
      notifications: [
        {
          id: "notification:1",
          kind: "admin_viewing",
          actorUsername: "owner",
          reason: "找回照片",
          expiresAt: "2026-10-08T10:00:00Z",
          createdAt: "2026-10-07T10:00:00Z",
        },
      ],
    });
    const user = userEvent.setup();
    render(<App />);
    const notice = await screen.findByRole("alertdialog", { name: "账号通知" });
    expect(within(notice).getByText(/管理员 owner .*只读查看.*原因：找回照片/)).toBeTruthy();
    await user.click(within(notice).getByRole("button", { name: "知道了" }));
    expect(screen.queryByRole("alertdialog", { name: "账号通知" })).toBeNull();
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/notifications/notification%3A1/acknowledge",
      expect.objectContaining({ method: "POST" }),
    );
  });

  it("tells the owner when an administrator viewed the private photo library", async () => {
    installAPI({
      notifications: [
        {
          id: "notification:2",
          kind: "admin_library_viewing",
          actorUsername: "owner",
          reason: "找婚礼照片",
          expiresAt: "2026-10-08T10:00:00Z",
          createdAt: "2026-10-07T10:00:00Z",
        },
      ],
    });
    render(<App />);
    const notice = await screen.findByRole("alertdialog", { name: "账号通知" });
    expect(within(notice).getByText(/管理员 owner .*开启了对你私有图库的只读查看.*原因：找婚礼照片/)).toBeTruthy();
  });
});

describe("ADR 0008 application access", () => {
  it("shows Docker and the App Center to administrators only", async () => {
    installAPI({ session: { ...session, user: { ...session.user, username: "alice", role: "member" } } });
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    expect(within(desktop).queryByRole("button", { name: "打开 Docker" })).toBeNull();
    expect(within(desktop).queryByRole("button", { name: "打开应用中心" })).toBeNull();
  });
});

describe("Installed apps on the desktop", () => {
  const immich = { id: "immich", title: "Immich", webPort: 2283, scheme: "http", path: "/", running: 4, total: 4 };
  const navidrome = {
    id: "navidrome",
    title: "Navidrome",
    webPort: 4533,
    scheme: "http",
    path: "/",
    running: 0,
    total: 1,
  };

  it("opens an installed app's web page from its desktop icon and from the launcher", async () => {
    installAPI({ shortcuts: [immich, navidrome] });
    const open = vi.spyOn(window, "open").mockReturnValue(null);
    const user = userEvent.setup();
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    const icon = await within(desktop).findByRole("button", { name: "打开 Immich（新窗口）" });
    expect(icon.querySelector("img")?.getAttribute("src")).toBe("/api/v1/app-shortcuts/immich/icon");
    await user.click(icon);
    expect(open).toHaveBeenLastCalledWith(
      expect.stringMatching(/^http:\/\/[^/]+:2283\/$/),
      "_blank",
      "noopener,noreferrer",
    );

    await user.click(
      within(screen.getByRole("complementary", { name: "系统快捷栏" })).getByRole("button", { name: "全部应用" }),
    );
    const launcher = screen.getByRole("dialog", { name: "全部应用" });
    await user.type(within(launcher).getByRole("searchbox", { name: "搜索应用" }), "immich");
    await user.keyboard("{Enter}");
    expect(open).toHaveBeenCalledTimes(2);
  });

  it("marks a stopped app and shows administrators its containers in Docker", async () => {
    installAPI({ shortcuts: [navidrome] });
    const open = vi.spyOn(window, "open").mockReturnValue(null);
    const user = userEvent.setup();
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    const stopped = await within(desktop).findByRole("button", { name: "Navidrome，没有在运行，在 Docker 中查看" });
    expect(stopped.textContent).toContain("未运行");
    await user.click(stopped);
    expect(open).not.toHaveBeenCalled();
    expect(await screen.findByRole("dialog", { name: "Docker" })).toBeTruthy();
  });

  it("shows installed apps to members, who cannot open Docker", async () => {
    installAPI({
      shortcuts: [immich, navidrome],
      session: { ...session, user: { ...session.user, username: "alice", role: "member" } },
    });
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    expect(await within(desktop).findByRole("button", { name: "打开 Immich（新窗口）" })).toBeTruthy();
    const stopped = within(desktop).getByRole("button", { name: "Navidrome，没有在运行" });
    expect(stopped.getAttribute("aria-disabled")).toBe("true");
  });
});
