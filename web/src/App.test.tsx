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
  networks: [] as string[],
  addressPools: [] as string[],
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
      expect(JSON.parse(String(setupCall?.[1]?.body))).toEqual({ username: "owner", password: "correct horse battery staple", localConsole: true });
    } finally {
      window.history.replaceState(null, "", "/");
    }
  });

  it("shows a loading state while the first host observation is pending", async () => {
    let finishRequest: ((response: Response) => void) | undefined;
    routeFetch({ hostState: vi.fn(() => new Promise<Response>((resolve) => { finishRequest = resolve; })) });
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

  it("enables file management and lists only the signed-in user's spaces", async () => {
    const fetchMock = installAPI({
      spaces: [{ id: "space:owner", kind: "private", name: "owner", ownerUserId: "user:owner", createdAt: "2026-10-07T10:00:00Z" }, { id: "space:shared", kind: "shared", name: "Shared", createdAt: "2026-10-07T10:00:00Z" }],
      entries: [
        { id: "file:1", spaceId: "space:owner", name: "家庭", kind: "directory", sizeBytes: 0, modifiedAt: "2026-10-07T10:00:00Z" },
        { id: "file:2", spaceId: "space:owner", name: "A.txt", kind: "file", sizeBytes: 12, modifiedAt: "2026-10-08T10:00:00Z" },
      ],
      volumes: [{ id: "volume:data", diskId: "disk:data", capacityBytes: 480 * 1024 ** 3, availableBytes: 278 * 1024 ** 3, state: "available" }],
    });
    const user = userEvent.setup(); render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    const fileButton = within(desktop).getByRole("button", { name: "打开文件管理" });
    expect(fileButton.hasAttribute("disabled")).toBe(false);
    await user.click(fileButton);
    const dialog = await screen.findByRole("dialog", { name: "文件管理" });
    expect(await screen.findByText("家庭")).toBeTruthy();
    const managerStyle = getComputedStyle(dialog.querySelector<HTMLElement>(".file-manager")!);
    const commandStyle = getComputedStyle(within(dialog).getByRole("button", { name: "新建" }));
    expect(getComputedStyle(dialog.querySelector<HTMLElement>(".file-action-bar")!).overflowX).not.toBe("visible");
    expect(managerStyle.getPropertyValue("--file-font-command").trim()).toBe("12px");
    expect(commandStyle.fontSize).toBe("var(--file-font-command)");
    expect(commandStyle.lineHeight).toBe("16px");
    const commandIcon = within(dialog).getByRole("button", { name: "新建" }).querySelector("svg")!;
    const commandLabel = within(dialog).getByRole("button", { name: "新建" }).querySelector("span")!;
    expect(getComputedStyle(commandIcon).display).toBe("block");
    expect(getComputedStyle(commandLabel).alignItems).toBe("center");
    expect(getComputedStyle(commandLabel).height).toBe("16px");
    expect(within(dialog).getByRole("button", { name: "个人空间owner" })).toBeTruthy();
    expect(within(dialog).getByRole("button", { name: "共享空间共享" })).toBeTruthy();
    expect(within(dialog).getAllByRole("separator")).toHaveLength(2);
    expect(within(dialog).getByText("202 GB")).toBeTruthy();
    const rows = within(dialog).getAllByRole("row");
    expect(rows[1].textContent).toContain("家庭");
    expect(rows[1].querySelector(".file-type-cell .directory")).toBeTruthy();
    await user.click(rows.find((row) => row.textContent?.includes("A.txt"))!);
    await user.click(within(dialog).getByRole("button", { name: "复制" }));
    await user.click(within(dialog).getByRole("button", { name: "粘贴" }));
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/files/file%3A2/copies", expect.objectContaining({
      method: "POST", body: JSON.stringify({ parentId: "", name: "A - 副本.txt" }),
    }));
    const inspector = within(dialog).getByRole("complementary", { name: "详细信息" });
    expect(within(inspector).queryByRole("button")).toBeNull();
    await user.dblClick(within(dialog).getAllByRole("row").find((row) => row.textContent?.includes("图库"))!);
    expect(await screen.findByRole("dialog", { name: "相册" })).toBeTruthy();
  });

  it("downloads files only on double click and exposes the file edit context menu", async () => {
    installAPI({
      spaces: [{ id: "space:owner", kind: "private", name: "owner", ownerUserId: "user:owner", createdAt: "2026-10-07T10:00:00Z" }],
      entries: [{ id: "file:note", spaceId: "space:owner", name: "notes.txt", kind: "file", sizeBytes: 12, modifiedAt: "2026-10-08T10:00:00Z" }],
      textPreview: "hello",
    });
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    const user = userEvent.setup(); render(<App />);
    await user.click(await screen.findByRole("button", { name: "打开文件管理" }));
    const dialog = await screen.findByRole("dialog", { name: "文件管理" });
    const row = (await within(dialog).findAllByRole("row")).find((item) => item.textContent?.includes("notes.txt"))!;

    await user.click(row);
    expect(click).not.toHaveBeenCalled();
    expect(await within(dialog).findByText("hello")).toBeTruthy();
    await user.dblClick(row);
    expect(click).toHaveBeenCalledTimes(1);
    const manager = dialog.querySelector<HTMLElement>(".file-manager")!;
    manager.getBoundingClientRect = () => ({ left: 50, top: 70, right: 950, bottom: 690, width: 900, height: 620, x: 50, y: 70, toJSON: () => ({}) });
    fireEvent.contextMenu(row, { clientX: 120, clientY: 160 });
    const menu = await screen.findByRole("menu", { name: "文件操作" });
    expect(menu.getAttribute("style")).toContain("left: 70px");
    expect(menu.getAttribute("style")).toContain("top: 90px");
    expect(within(menu).getByRole("menuitem", { name: "下载" })).toBeTruthy();
    expect(within(menu).getByRole("menuitem", { name: /重命名/ })).toBeTruthy();
    expect(within(menu).getByRole("menuitem", { name: /移到回收站/ })).toBeTruthy();
  });

  it("selects multiple files with a pointer selection box", async () => {
    installAPI({
      spaces: [{ id: "space:shared", kind: "shared", name: "Shared", createdAt: "2026-10-07T10:00:00Z" }],
      entries: [
        { id: "file:a", spaceId: "space:shared", name: "A.txt", kind: "file", sizeBytes: 12, modifiedAt: "2026-10-08T10:00:00Z" },
        { id: "file:b", spaceId: "space:shared", name: "B.txt", kind: "file", sizeBytes: 18, modifiedAt: "2026-10-08T10:00:00Z" },
      ],
      textPreview: "hello",
    });
    const user = userEvent.setup(); render(<App />);
    await user.click(await screen.findByRole("button", { name: "打开文件管理" }));
    const dialog = await screen.findByRole("dialog", { name: "文件管理" });
    const shell = dialog.querySelector<HTMLElement>(".file-list-shell")!;
    shell.getBoundingClientRect = () => ({ left: 0, top: 0, right: 500, bottom: 500, width: 500, height: 500, x: 0, y: 0, toJSON: () => ({}) });
    Array.from(shell.querySelectorAll<HTMLElement>("[data-file-item]")).forEach((row, index) => {
      row.getBoundingClientRect = () => ({ left: 0, top: 40 + index * 50, right: 500, bottom: 88 + index * 50, width: 500, height: 48, x: 0, y: 40 + index * 50, toJSON: () => ({}) });
    });

    fireEvent.pointerDown(shell, { button: 0, clientX: 480, clientY: 25 });
    fireEvent.pointerMove(window, { clientX: 10, clientY: 150 });
    expect(await within(dialog).findByText("已选择 2 个项目")).toBeTruthy();
    fireEvent.pointerUp(window, { clientX: 10, clientY: 150 });
  });

  it("previews an image file in the details pane", async () => {
    installAPI({
      spaces: [{ id: "space:owner", kind: "private", name: "owner", ownerUserId: "user:owner", createdAt: "2026-10-07T10:00:00Z" }],
      entries: [{ id: "file:photo", spaceId: "space:owner", name: "family-photo.jpg", kind: "file", sizeBytes: 2048, modifiedAt: "2026-10-08T10:00:00Z" }],
    });
    const user = userEvent.setup(); render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    await user.click(within(desktop).getByRole("button", { name: "打开文件管理" }));
    const dialog = await screen.findByRole("dialog", { name: "文件管理" });
    const row = (await within(dialog).findAllByRole("row")).find((item) => item.textContent?.includes("family-photo.jpg"));
    const thumbnail = row?.querySelector(".file-type-icon img");
    expect(thumbnail?.getAttribute("src")).toBe("/api/v1/files/file%3Aphoto/content?disposition=inline");
    await user.click(row!);

    const inspector = within(dialog).getByRole("complementary", { name: "详细信息" });
    const preview = within(inspector).getByRole("img", { name: "family-photo.jpg 的预览" });
    expect(preview.getAttribute("src")).toBe("/api/v1/files/file%3Aphoto/content?disposition=inline");
  });

  it("does not report the whole data volume as used when usage is unavailable", async () => {
    installAPI({
      spaces: [{ id: "space:owner", kind: "private", name: "owner", ownerUserId: "user:owner", createdAt: "2026-10-07T10:00:00Z" }],
      volumes: [{ id: "volume:data", diskId: "disk:data", capacityBytes: 500_107_862_016, state: "available" }],
    });
    const user = userEvent.setup(); render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    await user.click(within(desktop).getByRole("button", { name: "打开文件管理" }));
    const dialog = await screen.findByRole("dialog", { name: "文件管理" });
    const values = dialog.querySelectorAll(".file-volume-values strong");

    expect(values[0]?.textContent).toBe("—");
    expect(values[1]?.textContent).toBe("466 GB");
  });

  it("enables photos, trash, snapshots, and one settings entry", async () => {
    installAPI(); const user = userEvent.setup(); render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    expect(within(desktop).getByRole("button", { name: "打开回收站" }).hasAttribute("disabled")).toBe(false);
    expect(within(desktop).getByRole("button", { name: "打开文件快照" }).hasAttribute("disabled")).toBe(false);
    expect(within(desktop).getByRole("button", { name: "打开设置" })).toBeTruthy();
    for (const retired of ["打开系统设置", "打开资源管理", "打开存储初始化", "打开账号管理"]) {
      expect(within(desktop).queryByRole("button", { name: retired })).toBeNull();
    }
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
    await openSettingsSection(user, desktop, "存储");
    await user.click(await screen.findByRole("button", { name: "初始化数据卷…" }));
    await user.click(screen.getByRole("button", { name: "生成格式化计划" }));
    expect(await screen.findByRole("heading", { name: "破坏性操作计划" })).toBeTruthy();
    expect(screen.getByText("未检测到文件系统签名")).toBeTruthy();
    expect(screen.getByRole("region", { name: "桌面应用" })).toBeTruthy();
  });

  it("keeps the last successful state when refresh loses connection", async () => {
    routeFetch({ hostState: vi.fn().mockResolvedValueOnce(okResponse(healthyState)).mockResolvedValueOnce(errorResponse(503)) });
    const user = userEvent.setup();

    render(<App />);
    await user.click(within(await screen.findByRole("region", { name: "桌面应用" })).getByRole("button", { name: "打开设置" }));
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
    await user.click(within(await screen.findByRole("region", { name: "桌面应用" })).getByRole("button", { name: "打开设置" }));
    expect(await screen.findByText("暂时无法读取设备状态")).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "重新连接" }));
    expect(await screen.findByText("anas-fake")).toBeTruthy();
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("shows an explicit empty disk inventory", async () => {
    stubFetch({ ...healthyState, disks: [] });
    const user = userEvent.setup();

    render(<App />);
    await openSettingsSection(user, await screen.findByRole("region", { name: "桌面应用" }), "存储");

    expect(await screen.findByText("未检测到磁盘")).toBeTruthy();
  });

  it("opens, minimizes, and restores settings", async () => {
    installAPI(); const user = userEvent.setup(); render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    await user.click(within(desktop).getByRole("button", { name: "打开设置" }));
    const settings = screen.getByRole("dialog", { name: "设置" });
    await user.click(within(settings).getByRole("button", { name: "最小化设置" }));
    await user.click(screen.getByRole("button", { name: "恢复设置" }));
    expect(screen.getByRole("dialog", { name: "设置" })).toBeTruthy();
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

  it("returns to the login screen when the session expires instead of reporting a lost connection", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let expired = false;
    const poll = (state: unknown) => vi.fn(async () => (expired ? sessionExpiredResponse() : okResponse(state)));
    routeFetch({ hostState: poll(healthyState), metrics: poll(metricsState) });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    render(<App />);
    await screen.findByRole("region", { name: "桌面应用" });

    expired = true;
    await act(async () => { vi.advanceTimersByTime(2_000); });
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
    expect(desktopOrder(desktop).slice(0, 5)).toEqual(["文件管理", "回收站", "设置", "终端", "应用中心"]);

    const terminal = within(desktop).getByRole("button", { name: "打开终端" }).parentElement!;
    fireEvent.pointerDown(terminal, { button: 0, pointerId: 1, pointerType: "mouse", clientX: 50, clientY: 160 });
    fireEvent.pointerMove(window, { pointerId: 1, clientX: 40, clientY: 140 });
    const dragPreview = document.querySelector(".desktop-drag-preview");
    expect(dragPreview).toBeTruthy();
    expect(desktop.contains(dragPreview)).toBe(false);
    fireEvent.pointerMove(window, { pointerId: 1, clientX: 40, clientY: 50 });
    fireEvent.pointerUp(window, { pointerId: 1, clientX: 40, clientY: 50 });
    expect(document.querySelector(".desktop-drag-preview")).toBeNull();
    fireEvent.click(within(desktop).getByRole("button", { name: "打开终端" }));

    expect(desktopOrder(desktop).slice(0, 5)).toEqual(["终端", "文件管理", "回收站", "设置", "应用中心"]);
    expect(JSON.parse(localStorage.getItem("a-nas.desktop-order.v1")!).slice(0, 2)).toEqual(["terminal", "files"]);
    expect(screen.queryByRole("dialog", { name: "终端" })).toBeNull();
    expect(screen.getByText("已将终端移动到第 1 位")).toBeTruthy();
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

    within(desktop).getByRole("button", { name: "打开终端" }).focus();
    await user.keyboard("{Alt>}{ArrowLeft}{/Alt}");
    expect(desktopOrder(desktop).slice(0, 4)).toEqual(["文件管理", "回收站", "终端", "设置"]);
    expect(document.activeElement).toBe(within(desktop).getByRole("button", { name: "打开终端" }));
    unmount();

    render(<App />);
    expect(desktopOrder(await screen.findByRole("region", { name: "桌面应用" })).slice(0, 4)).toEqual(["文件管理", "回收站", "终端", "设置"]);
  });

  it("switches focus between open apps from an icon-only dock", async () => {
    stubDesktopFetch(healthyState, true);
    const user = userEvent.setup();
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    await user.click(within(desktop).getByRole("button", { name: "打开设置" }));
    await user.click(within(desktop).getByRole("button", { name: "打开终端" }));

    const dock = screen.getByRole("navigation", { name: "已打开窗口" });
    expect(dock.textContent).toBe("");
    expect(within(dock).getByRole("button", { name: "最小化终端" }).getAttribute("aria-current")).toBe("true");

    await user.click(within(dock).getByRole("button", { name: "切换到设置" }));
    expect(within(dock).getByRole("button", { name: "最小化设置" }).getAttribute("aria-current")).toBe("true");
    expect(within(dock).getByRole("button", { name: "切换到终端" })).toBeTruthy();

    await user.click(within(dock).getByRole("button", { name: "最小化设置" }));
    expect(screen.queryByRole("dialog", { name: "设置" })).toBeNull();
    expect(within(dock).getByRole("button", { name: "最小化终端" }).getAttribute("aria-current")).toBe("true");
    expect(within(dock).getByRole("button", { name: "恢复设置" })).toBeTruthy();
  });

  it("lists dock icons in launch order and drops closed apps immediately from the accessibility tree", async () => {
    stubDesktopFetch(healthyState, true);
    const user = userEvent.setup();
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    await user.click(within(desktop).getByRole("button", { name: "打开终端" }));
    await user.click(within(desktop).getByRole("button", { name: "打开设置" }));

    const dock = screen.getByRole("navigation", { name: "已打开窗口" });
    expect(within(dock).getAllByRole("button").map((button) => button.getAttribute("aria-label"))).toEqual(["切换到终端", "最小化设置"]);

    await user.click(within(screen.getByRole("dialog", { name: "设置" })).getByRole("button", { name: "关闭设置" }));
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
    expect(within(plan).getByText("使用 Docker 默认网桥，不新建网络")).toBeTruthy();
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

  it("shows the Docker networks an app creates and refuses them without an address pool", async () => {
    let poolConfigured = true;
    routeFetch({
      hostState: vi.fn().mockResolvedValue(okResponse(healthyState)),
      apps: async (url) => {
        if (url.endsWith("/plan")) {
          return poolConfigured
            ? okResponse({ ...memosPlan, networks: ["default"], addressPools: ["10.96.64.0/19"] })
            : { ok: false, status: 409, json: async () => ({ error: { code: "address_pool_missing", message: "Docker has no address pool for app networks" } }) } as Response;
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
    const plan = await within(store).findByRole("region", { name: "安装计划" });
    expect(within(plan).getByText("default")).toBeTruthy();
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
          return { ok: false, status: 423, json: async () => ({ error: { code: "volume_unavailable", message: "the data volume is not available" } }) } as Response;
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

function installAPI(options: { setupRequired?: boolean; spaces?: unknown[]; entries?: unknown[]; volumes?: unknown[]; storagePlan?: unknown; terminalEnabled?: boolean; session?: unknown; notifications?: unknown[]; users?: unknown[]; textPreview?: string } = {}) {
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
    if (path.startsWith("/api/v1/files/") && path.includes("/content")) return { ok: true, status: 200, text: async () => options.textPreview ?? "" } as Response;
    if (path === "/api/v1/volumes") return ok({ items: options.volumes ?? [] });
	if (path === "/api/v1/storage/plans" && init?.method === "POST") return ok(options.storagePlan ?? {}, 201);
    if (path === "/api/v1/users") return ok({ items: options.users ?? [session.user] });
    if (path === "/api/v1/trash") return ok({ items: [] });
    return ok({ items: [] });
  });
  vi.stubGlobal("fetch", mock);
  return mock;
}

function ok(body: unknown, status = 200) { return { ok: true, status, json: async () => body } as Response; }

async function openSettingsSection(user: ReturnType<typeof userEvent.setup>, desktop: HTMLElement, section: string) {
  await user.click(within(desktop).getByRole("button", { name: "打开设置" }));
  const sections = within(screen.getByRole("dialog", { name: "设置" })).getByRole("navigation", { name: "设置分区" });
  await user.click(within(sections).getByRole("button", { name: section }));
}

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

function sessionExpiredResponse() {
  return {
    ok: false,
    status: 401,
    json: async () => ({ error: { code: "authentication_required", message: "authentication is required" } }),
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
  it("lets the signed-in administrator change their own password and rebuild SMB credentials", async () => {
    const fetchMock = installAPI({ users: [session.user] });
    const user = userEvent.setup();
    render(<App />);
    await user.click(await screen.findByRole("button", { name: "我的账号（owner）" }));
    const settings = screen.getByRole("dialog", { name: "设置" });
    expect(within(settings).getByRole("button", { name: "我的账号" }).getAttribute("aria-current")).toBe("page");
    await user.click(within(settings).getByRole("button", { name: "修改密码…" }));
    const form = within(settings).getByRole("form", { name: "修改密码" });
    await user.type(within(form).getByLabelText("当前密码"), "correct horse battery staple");
    await user.type(within(form).getByLabelText("新密码"), "administrator chosen password");
    await user.type(within(form).getByLabelText("确认新密码"), "administrator chosen password");
    await user.click(within(form).getByRole("button", { name: "保存新密码" }));
    expect(await within(settings).findByText("密码已更新，SMB 凭据已同步。")).toBeTruthy();
    expect(within(settings).queryByRole("form", { name: "修改密码" })).toBeNull();
    const call = fetchMock.mock.calls.find(([path]) => path === "/api/v1/session/password");
    expect(JSON.parse(String(call?.[1]?.body))).toEqual({
      currentPassword: "correct horse battery staple",
      newPassword: "administrator chosen password",
    });
  });

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
    await openSettingsSection(user, desktop, "用户与权限");
    await user.click(await screen.findByRole("button", { name: "alice 的更多操作" }));
    await user.click(within(screen.getByRole("menu", { name: "alice 的更多操作" })).getByRole("menuitem", { name: "查看个人空间…" }));
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
    await openSettingsSection(user, desktop, "用户与权限");
    await user.click(await screen.findByRole("button", { name: "alice 的更多操作" }));
    await user.click(within(screen.getByRole("menu", { name: "alice 的更多操作" })).getByRole("menuitem", { name: "查看私有图库…" }));
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
    expect(screen.getByRole("button", { name: "新建" }).hasAttribute("disabled")).toBe(true);
    expect(screen.getByRole("button", { name: "删除" }).hasAttribute("disabled")).toBe(true);
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

describe("Settings", () => {
  const alice = { id: "user:alice", username: "alice", role: "member", status: "active", createdAt: "2026-10-07T10:00:00Z" };

  it("shows members their account, the overview and read-only storage", async () => {
    const fetchMock = installAPI({ session: { ...session, user: alice } });
    const user = userEvent.setup();
    render(<App />);
    await openSettingsSection(user, await screen.findByRole("region", { name: "桌面应用" }), "存储");
    const settings = screen.getByRole("dialog", { name: "设置" });
    const sections = within(settings).getByRole("navigation", { name: "设置分区" });
    const enabled = within(sections).getAllByRole("button").filter((button) => !button.hasAttribute("disabled"));
    expect(enabled.map((button) => button.getAttribute("aria-label") ?? button.textContent)).toEqual(["我的账号", "概览", "存储"]);
    expect(await within(settings).findByRole("heading", { name: "尚未创建数据卷" })).toBeTruthy();
    expect(within(settings).getByText(/请联系管理员初始化/)).toBeTruthy();
    expect(within(settings).getByText("A-NAS Fake HDD")).toBeTruthy();
    expect(within(settings).queryByRole("button", { name: "初始化数据卷…" })).toBeNull();
    expect(within(settings).queryByRole("region", { name: "初始化数据卷" })).toBeNull();
    expect(fetchMock.mock.calls.some(([path]) => path === "/api/v1/users")).toBe(false);
  });

  it("lets a member change their own password", async () => {
    const fetchMock = installAPI({ session: { ...session, user: alice } });
    const user = userEvent.setup();
    render(<App />);
    await openSettingsSection(user, await screen.findByRole("region", { name: "桌面应用" }), "我的账号");
    await user.click(screen.getByRole("button", { name: "修改密码…" }));
    const form = screen.getByRole("form", { name: "修改密码" });
    await user.type(within(form).getByLabelText("当前密码"), "alice old password");
    await user.type(within(form).getByLabelText("新密码"), "alice new password");
    await user.type(within(form).getByLabelText("确认新密码"), "alice another password");
    await user.click(within(form).getByRole("button", { name: "保存新密码" }));
    expect(within(form).getByText("两次输入的新密码不一致")).toBeTruthy();
    expect(fetchMock.mock.calls.some(([path]) => path === "/api/v1/session/password")).toBe(false);

    await user.clear(within(form).getByLabelText("确认新密码"));
    await user.type(within(form).getByLabelText("确认新密码"), "alice new password");
    await user.click(within(form).getByRole("button", { name: "保存新密码" }));
    expect(await screen.findByText("密码已更新，SMB 凭据已同步。")).toBeTruthy();
    const call = fetchMock.mock.calls.find(([path]) => path === "/api/v1/session/password");
    expect(JSON.parse(String(call?.[1]?.body))).toEqual({ currentPassword: "alice old password", newPassword: "alice new password" });
  });

  it("keeps a pending storage plan across sections and when settings closes", async () => {
    installAPI({
      storagePlan: {
        id: "plan:pending", diskId: "disk:fake-data-01", diskModel: "A-NAS Fake HDD", capacityBytes: 512000000000,
        fingerprint: "pending-fingerprint", signatures: [], confirmationPhrase: "ERASE data-01", actions: [], state: "planned", expiresAt: "2099-01-01T00:00:00Z",
      },
    });
    const user = userEvent.setup();
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    await openSettingsSection(user, desktop, "存储");
    await user.click(await screen.findByRole("button", { name: "初始化数据卷…" }));
    await user.click(screen.getByRole("button", { name: "生成格式化计划" }));
    await user.type(await screen.findByLabelText(/输入确认短语/), "ERASE");

    let settings = screen.getByRole("dialog", { name: "设置" });
    await user.click(within(settings).getByRole("button", { name: "概览" }));
    expect(within(settings).queryByRole("heading", { name: "破坏性操作计划" })).toBeNull();
    await user.click(within(settings).getByRole("button", { name: "关闭设置" }));
    await user.click(within(desktop).getByRole("button", { name: "打开设置" }));
    settings = screen.getByRole("dialog", { name: "设置" });
    expect(within(settings).getByRole("button", { name: "概览" }).getAttribute("aria-current")).toBe("page");

    await user.click(within(settings).getByRole("button", { name: "存储" }));
    expect(within(settings).getByRole("heading", { name: "破坏性操作计划" })).toBeTruthy();
    expect((within(settings).getByLabelText(/输入确认短语/) as HTMLInputElement).value).toBe("ERASE");
    expect(within(settings).getByRole("button", { name: "确认计划" }).hasAttribute("disabled")).toBe(true);
    expect(within(settings).queryByRole("button", { name: "生成格式化计划" })).toBeNull();
  });

  it("reports unknown data volume usage instead of a full volume", async () => {
    installAPI({ volumes: [{ id: "volume:data", diskId: "disk:data", capacityBytes: 500_107_862_016, state: "available", filesystemUuid: "09e275fe-794a" }] });
    const user = userEvent.setup();
    render(<App />);
    await openSettingsSection(user, await screen.findByRole("region", { name: "桌面应用" }), "存储");
    const settings = screen.getByRole("dialog", { name: "设置" });
    expect(await within(settings).findByText("已用空间未知 · 共 465.8 GiB")).toBeTruthy();
    expect(within(settings).getByText("在线")).toBeTruthy();
    expect(within(settings).queryByRole("meter", { name: "数据卷已用空间" })).toBeNull();
    expect(within(settings).queryByRole("region", { name: "初始化数据卷" })).toBeNull();
  });

  it("resets a member password from a form inside the window", async () => {
    const prompt = vi.spyOn(window, "prompt");
    const fetchMock = installAPI({ users: [session.user, alice] });
    const user = userEvent.setup();
    render(<App />);
    await openSettingsSection(user, await screen.findByRole("region", { name: "桌面应用" }), "用户与权限");
    const settings = screen.getByRole("dialog", { name: "设置" });
    expect(await within(settings).findByText("成员 · 正常")).toBeTruthy();
    expect(within(settings).getByText("管理员 · 正常")).toBeTruthy();
    expect(within(settings).getByText("你")).toBeTruthy();

    await user.click(within(settings).getByRole("button", { name: "alice 的更多操作" }));
    await user.click(within(settings).getByRole("menuitem", { name: "重置密码…" }));
    const form = within(settings).getByRole("form", { name: "重置 alice 的密码" });
    await user.type(within(form).getByLabelText("新密码"), "temporary password 1");
    await user.click(within(form).getByRole("button", { name: "确认重置" }));
    expect(await within(settings).findByText(/已重置 alice 的密码/)).toBeTruthy();
    expect(prompt).not.toHaveBeenCalled();
    const call = fetchMock.mock.calls.find(([path, init]) => path === "/api/v1/users/user%3Aalice/credential" && init?.method === "PATCH");
    expect(JSON.parse(String(call?.[1]?.body))).toEqual({ password: "temporary password 1" });

    await user.click(within(settings).getByRole("button", { name: "修改我的密码" }));
    expect(within(settings).getByRole("button", { name: "我的账号" }).getAttribute("aria-current")).toBe("page");
    expect(within(settings).getByRole("form", { name: "修改密码" })).toBeTruthy();
    prompt.mockRestore();
  });

  it("asks for confirmation before disabling a member", async () => {
    const fetchMock = installAPI({ users: [session.user, alice] });
    const user = userEvent.setup();
    render(<App />);
    await openSettingsSection(user, await screen.findByRole("region", { name: "桌面应用" }), "用户与权限");
    const settings = screen.getByRole("dialog", { name: "设置" });
    await user.click(await within(settings).findByRole("button", { name: "alice 的更多操作" }));
    await user.click(within(settings).getByRole("menuitem", { name: "禁用账号…" }));
    const confirm = within(settings).getByRole("form", { name: "禁用 alice" });
    expect(fetchMock.mock.calls.some(([, init]) => init?.method === "DELETE")).toBe(false);

    await user.click(within(confirm).getByRole("button", { name: "取消" }));
    expect(within(settings).queryByRole("form", { name: "禁用 alice" })).toBeNull();
    await user.click(within(settings).getByRole("button", { name: "alice 的更多操作" }));
    await user.click(within(settings).getByRole("menuitem", { name: "禁用账号…" }));
    await user.click(within(within(settings).getByRole("form", { name: "禁用 alice" })).getByRole("button", { name: "禁用账号" }));
    expect(await within(settings).findByText("已禁用 alice。")).toBeTruthy();
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/users/user%3Aalice", expect.objectContaining({ method: "DELETE" }));
  });
});
