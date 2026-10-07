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
    { id: "b".repeat(64), name: "homeassistant", image: "ghcr.io/home-assistant/home-assistant:stable", state: "exited", status: "Exited (0) 5 hours ago", createdAt: "2026-09-26T00:00:00Z", ports: [] },
  ],
  images: [{ id: "sha256:" + "c".repeat(64), tags: ["jellyfin/jellyfin:10.11"], sizeBytes: 1243000000, createdAt: "2026-09-06T00:00:00Z" }],
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
  mounts: [{ hostPath: "/srv/a-nas/data/apps/memos/memos", containerPath: "/var/opt/memos", kind: "appdata", readOnly: false }],
  digest: "d".repeat(64),
  compose: "name: a-nas-memos\n",
};

function appList(memos: Record<string, unknown>) {
  const base = { tagline: "", description: "", version: "1", author: "", website: "", scheme: "http", path: "/", state: "available", running: 0, total: 0 };
  return {
    dataSource: "simulated",
    apps: [
      { ...base, id: "memos", title: "Memos", tagline: "轻量笔记", category: "Productivity", webPort: 5230, ...memos },
      { ...base, id: "navidrome", title: "Navidrome", tagline: "音乐流媒体", category: "Media", webPort: 4533 },
    ],
  };
}

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

  it("enables photos, trash, snapshots, accounts, and storage", async () => {
    installAPI(); const user = userEvent.setup(); render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    expect(within(desktop).getByRole("button", { name: "打开回收站" }).hasAttribute("disabled")).toBe(false);
    expect(within(desktop).getByRole("button", { name: "打开文件快照" }).hasAttribute("disabled")).toBe(false);
    expect(within(desktop).getByRole("button", { name: "打开账号管理" })).toBeTruthy();
    expect(within(desktop).getByRole("button", { name: "打开存储初始化" })).toBeTruthy();
    await user.click(within(desktop).getByRole("button", { name: "打开相册" }));
    expect(await screen.findByRole("dialog", { name: "相册" })).toBeTruthy();
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
    const dragPreview = document.querySelector(".desktop-drag-preview");
    expect(dragPreview).toBeTruthy();
    expect(desktop.contains(dragPreview)).toBe(false);
    fireEvent.pointerMove(window, { pointerId: 1, clientX: 40, clientY: 50 });
    fireEvent.pointerUp(window, { pointerId: 1, clientX: 40, clientY: 50 });
    expect(document.querySelector(".desktop-drag-preview")).toBeNull();
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

  it("manages containers from the Docker app", async () => {
    const scrollIntoView = vi.fn(() => Promise.resolve());
    Element.prototype.scrollIntoView = scrollIntoView as unknown as Element["scrollIntoView"];
    const actions: Array<{ id: string; body: unknown }> = [];
    routeFetch({
      hostState: vi.fn().mockResolvedValue(okResponse(healthyState)),
      containers: async (url, init) => {
        if (init?.method === "POST") {
          actions.push({ id: url.split("/")[4], body: JSON.parse(String(init.body)) });
          return { ok: true, status: 204, json: async () => undefined } as Response;
        }
        if (url.endsWith("/logs?tail=200")) {
          return okResponse({ lines: [{ stream: "stdout", time: "2026-10-06T00:00:01Z", text: "server ready" }, { stream: "stderr", text: "warning: low disk" }] });
        }
        return okResponse(containerSnapshot);
      },
    });
    const user = userEvent.setup();
    render(<App />);
    await user.click(within(await screen.findByRole("region", { name: "桌面应用" })).getByRole("button", { name: "打开 Docker" }));

    const docker = screen.getByRole("dialog", { name: "Docker" });
    const jellyfin = await within(docker).findByRole("article", { name: "容器 jellyfin" });
    expect(within(jellyfin).getByText("运行中")).toBeTruthy();
    expect(within(jellyfin).getByText("8096→8096/tcp")).toBeTruthy();
    expect(within(jellyfin).getByText("12.4%")).toBeTruthy();

    await user.click(within(jellyfin).getByRole("button", { name: "停止jellyfin" }));
    expect(actions).toHaveLength(0);
    await user.click(within(jellyfin).getByRole("button", { name: "确认停止" }));
    expect(actions).toEqual([{ id: containerSnapshot.containers[0].id, body: { action: "stop" } }]);

    const stopped = within(docker).getByRole("article", { name: "容器 homeassistant" });
    await user.click(within(stopped).getByRole("button", { name: "启动homeassistant" }));
    expect(actions[1]).toEqual({ id: containerSnapshot.containers[1].id, body: { action: "start" } });

    await user.click(within(jellyfin).getByRole("button", { name: "查看jellyfin日志" }));
    const log = await within(docker).findByRole("log", { name: "jellyfin 日志" });
    expect(within(log).getByText("server ready")).toBeTruthy();
    expect(within(log).getByText("warning: low disk").parentElement?.className).toContain("stderr");

    // Chromium's scrollIntoView now returns a Promise; leaving the log view must not crash the desktop.
    expect(scrollIntoView).toHaveBeenCalled();
    await user.click(within(docker).getByRole("button", { name: "返回容器列表" }));
    await user.click(within(docker).getByRole("tab", { name: /镜像/ }));
    expect(within(docker).getByText("jellyfin/jellyfin:10.11")).toBeTruthy();
  });

  it("explains when Docker management is disabled", async () => {
    routeFetch({
      hostState: vi.fn().mockResolvedValue(okResponse(healthyState)),
      containers: async () => ({ ok: false, status: 503, json: async () => ({ error: { code: "containers_disabled", message: "disabled" } }) }) as Response,
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
          expect((init.headers as Record<string, string>)["X-CSRF-Token"]).toBe(session.csrfToken);
          installed = url.endsWith("/install");
          return { ok: true, status: 202, json: async () => ({ appId: "memos", action: "install", state: "running", startedAt: "2026-10-07T00:00:00Z", output: [] }) } as Response;
        }
        if (url.endsWith("/plan")) return okResponse(memosPlan);
        return okResponse(appList(installed ? { state: "installed", running: 1, total: 1, job: { appId: "memos", action: "install", state: "succeeded", startedAt: "2026-10-07T00:00:00Z", output: ["Container memos  Started"] } } : {}));
      },
    });
    const user = userEvent.setup();
    render(<App />);
    await user.click(within(await screen.findByRole("region", { name: "桌面应用" })).getByRole("button", { name: "打开应用中心" }));

    const store = screen.getByRole("dialog", { name: "应用中心" });
    await user.type(await within(store).findByRole("textbox", { name: "搜索应用" }), "memo");
    expect(within(store).queryByRole("button", { name: /Navidrome/ })).toBeNull();
    await user.click(within(store).getByRole("button", { name: "Memos，可安装" }));
    await user.click(within(store).getByRole("button", { name: "安装" }));

    const plan = await within(store).findByRole("region", { name: "安装计划" });
    expect(within(plan).getByText("neosmemo/memos:0.28.0")).toBeTruthy();
    expect(within(plan).getByText("5230/tcp")).toBeTruthy();
    expect(within(plan).getByText("/srv/a-nas/data/apps/memos/memos")).toBeTruthy();
    expect(within(plan).getByText("app-memos")).toBeTruthy();
    expect(within(plan).getByText(/只访问自己的应用数据/)).toBeTruthy();
    expect(posts).toHaveLength(0);

    await user.click(within(plan).getByRole("button", { name: "确认安装" }));
    expect(posts).toEqual([{ url: "/api/v1/apps/memos/install", body: { digest: memosPlan.digest } }]);
    expect(await within(store).findByText(/已安装 · 1\/1 个容器运行中/)).toBeTruthy();
    expect(within(store).getByText(/Container memos\s+Started/)).toBeTruthy();

    await user.click(within(store).getByRole("button", { name: "卸载" }));
    await user.click(within(store).getByRole("button", { name: "确认卸载" }));
    expect(posts[1]).toEqual({ url: "/api/v1/apps/memos/uninstall", body: {} });
  });

  it("explains install conflicts and a disabled App Center", async () => {
    routeFetch({
      hostState: vi.fn().mockResolvedValue(okResponse(healthyState)),
      apps: async (url, init) => {
        if (init?.method === "POST") {
          return { ok: false, status: 409, json: async () => ({ error: { code: "port_in_use", message: "a published port is already in use: 5230 is used by other" } }) } as Response;
        }
        if (url.endsWith("/plan")) return okResponse(memosPlan);
        return okResponse(appList({}));
      },
    });
    const user = userEvent.setup();
    const { unmount } = render(<App />);
    await user.click(await screen.findByRole("button", { name: "打开应用中心" }));
    const store = screen.getByRole("dialog", { name: "应用中心" });
    await user.click(await within(store).findByRole("button", { name: "Memos，可安装" }));
    await user.click(within(store).getByRole("button", { name: "安装" }));
    await user.click(await within(store).findByRole("button", { name: "确认安装" }));
    expect((await within(store).findByRole("alert")).textContent).toContain("端口已被占用（5230 is used by other）");
    unmount();

    routeFetch({
      hostState: vi.fn().mockResolvedValue(okResponse(healthyState)),
      apps: async () => ({ ok: false, status: 503, json: async () => ({ error: { code: "apps_disabled", message: "disabled" } }) }) as Response,
    });
    render(<App />);
    await user.click(await screen.findByRole("button", { name: "打开应用中心" }));
    expect(await screen.findByText("应用中心未启用")).toBeTruthy();
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

function routeFetch(routes: {
  hostState: () => Promise<Response>;
  metrics?: () => Promise<Response>;
  terminalEnabled?: boolean;
  containers?: (url: string, init?: RequestInit) => Promise<Response>;
  apps?: (url: string, init?: RequestInit) => Promise<Response>;
}) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input).startsWith("/api/v1/containers") && routes.containers) return routes.containers(String(input), init);
      if (String(input).startsWith("/api/v1/apps") && routes.apps) return routes.apps(String(input), init);
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
    expect(JSON.parse(String(call?.[1]?.body))).toEqual({ password: "correct horse battery staple", reason: "Alice 请求找回文件", scope: "space" });
  });

  it("lets an administrator view a member's private photo library as its own grant", async () => {
    const alice = { id: "user:alice", username: "alice", role: "member", status: "active", createdAt: "2026-10-07T10:00:00Z" };
    const fetchMock = installAPI({ users: [session.user, alice] });
    const user = userEvent.setup();
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    await user.click(within(desktop).getByRole("button", { name: "打开账号管理" }));
    await user.click(await screen.findByRole("button", { name: "查看私有图库" }));
    const form = screen.getByRole("form", { name: "查看 alice 的私有图库" });
    expect(within(form).getByText(/查看他人私有图库会写入审计/)).toBeTruthy();
    await user.type(within(form).getByLabelText("查看原因"), "找婚礼照片");
    await user.type(within(form).getByLabelText("你的密码"), "correct horse battery staple");
    await user.click(within(form).getByRole("button", { name: "开始只读查看" }));
    expect(await screen.findByText(/已开启对 alice 私有图库的只读查看，可在相册中选择/)).toBeTruthy();
    const call = fetchMock.mock.calls.find(([path]) => path === "/api/v1/users/user%3Aalice/viewing");
    expect(JSON.parse(String(call?.[1]?.body))).toEqual({ password: "correct horse battery staple", reason: "找婚礼照片", scope: "library" });
  });

  it("tells the owner when an administrator viewed the private photo library", async () => {
    installAPI({ notifications: [{
      id: "notification:2", kind: "admin_library_viewing", actorUsername: "owner", reason: "找婚礼照片",
      expiresAt: "2026-10-08T10:00:00Z", createdAt: "2026-10-07T10:00:00Z",
    }] });
    render(<App />);
    const notice = await screen.findByRole("alertdialog", { name: "账号通知" });
    expect(within(notice).getByText(/管理员 owner .*开启了对你私有图库的只读查看.*原因：找婚礼照片/)).toBeTruthy();
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

describe("ADR 0008 application access", () => {
  it("shows Docker and the App Center to administrators only", async () => {
    installAPI({ session: { ...session, user: { ...session.user, username: "alice", role: "member" } } });
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    expect(within(desktop).queryByRole("button", { name: "打开 Docker" })).toBeNull();
    expect(within(desktop).queryByRole("button", { name: "打开应用中心" })).toBeNull();
  });
});
