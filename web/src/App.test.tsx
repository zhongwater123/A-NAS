import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import App from "./App";
import type { SessionStatus } from "./terminal";
import { transferStore } from "./transfers/store";

const xterm = vi.hoisted(() => ({ mounts: 0, onStatus: undefined as ((status: SessionStatus) => void) | undefined }));

// Uploads go through tus-js-client; tests answer its requests themselves.
const tus = vi.hoisted(() => {
  class FakeUpload {
    url: string | null;
    constructor(readonly file: File, readonly options: Record<string, any>) {
      this.url = options.uploadUrl ?? null;
      uploads.push(this);
    }
    start() {}
    abort() { return Promise.resolve(); }
  }
  const uploads: FakeUpload[] = [];
  return { FakeUpload, uploads };
});
vi.mock("tus-js-client", () => ({ Upload: tus.FakeUpload }));

// The cropper needs layout jsdom lacks; this one reports a fixed square.
vi.mock("react-easy-crop", async () => {
  const { useEffect } = await import("react");
  return {
    default: function MockCropper({ image, onCropComplete }: { image: string; onCropComplete: (area: unknown, pixels: unknown) => void }) {
      useEffect(() => { onCropComplete({ x: 10, y: 0, width: 80, height: 100 }, { x: 40, y: 0, width: 320, height: 320 }); }, [image]);
      return <div data-testid="cropper" data-image={image} />;
    },
  };
});

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

describe("member sharing in the file manager", () => {
  it("opens sharing from the rail instead of the desktop and shares read-only files", async () => {
    installAPI({ spaces: [{ id: "shared", kind: "shared", name: "Shared", writable: false }], entries: [{ id: "shared-file", spaceId: "shared", name: "分享计划.txt", kind: "file", sizeBytes: 4 }] });
    const user = userEvent.setup(); render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    expect(within(desktop).queryByRole("button", { name: "打开分享" })).toBeNull();
    const rail = screen.getByRole("complementary", { name: "系统快捷栏" });
    await user.click(within(rail).getByRole("button", { name: "打开分享" }));
    expect(await screen.findByRole("dialog", { name: "分享" })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "关闭分享" }));
    await user.click(within(desktop).getByRole("button", { name: "打开文件管理" }));
    const dialog = await screen.findByRole("dialog", { name: "文件管理" });
    await user.click(await within(dialog).findByRole("row", { name: /分享计划.txt/ }));
    expect(within(dialog).getByRole("button", { name: "分享" }).hasAttribute("disabled")).toBe(false);
    await user.click(within(dialog).getByRole("button", { name: "分享" }));
    expect(await screen.findByRole("dialog", { name: "分享给成员" })).toBeTruthy();
  });

  it("does not share administrative viewing files", async () => {
    installAPI({ spaces: [{ id: "viewed", kind: "private", name: "Alice", ownerUserId: "alice", viewing: { grantId: "grant", expiresAt: "2099-01-01T00:00:00Z" } }], entries: [{ id: "viewed-file", spaceId: "viewed", name: "查看计划.txt", kind: "file", sizeBytes: 4 }] });
    const user = userEvent.setup(); render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    await user.click(within(desktop).getByRole("button", { name: "打开文件管理" }));
    const dialog = await screen.findByRole("dialog", { name: "文件管理" });
    await user.click(await within(dialog).findByRole("row", { name: /查看计划.txt/ }));
    expect(within(dialog).getByRole("button", { name: "分享" }).hasAttribute("disabled")).toBe(true);
  });
});

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
  const base = {
    tagline: "", description: "", notes: "", version: "1", author: "", developer: "", website: "", repo: "", docs: "", updated: "",
    scheme: "http", path: "/", state: "available", running: 0, total: 0,
  };
  return {
    dataSource: "simulated",
    apps: [
      { ...base, id: "memos", title: "Memos", tagline: "轻量笔记", category: "notes", webPort: 5230, ...memos },
      { ...base, id: "navidrome", title: "Navidrome", tagline: "音乐流媒体", category: "media", webPort: 4533 },
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

afterEach(() => { transferStore.reset(); tus.uploads.length = 0; vi.useRealTimers(); vi.unstubAllGlobals(); });

describe("A-NAS v1.0.1 desktop", () => {
  it.each(["clip.mp4", "photo.HEIC", "photo.heif"])("shows a thumbnail for %s in file management", async (name) => {
    installAPI({
      spaces: [{ id: "space:shared", kind: "shared", name: "Shared" }],
      entries: [{ id: "file:preview", spaceId: "space:shared", name, kind: "file", sizeBytes: 1024, modifiedAt: "2026-10-14T00:00:00Z" }],
    });
    const user = userEvent.setup(); render(<App />);
    await user.click(await screen.findByRole("button", { name: "打开文件管理" }));
    const dialog = await screen.findByRole("dialog", { name: "文件管理" });
    await within(dialog).findByText(name);
    const row = dialog.querySelector<HTMLElement>('[data-file-item="file:preview"]')!;
    expect(row.querySelector("img")?.getAttribute("src")).toContain("/api/v1/files/file%3Apreview/preview?size=thumbnail");
    fireEvent.error(row.querySelector("img")!);
    expect(row.querySelector("img")?.hidden).toBe(true);
    await user.click(row);
    const inspector = within(dialog).getByRole("complementary", { name: "详细信息" });
    expect(within(inspector).getByRole("img", { name: name + " 的预览" }).getAttribute("src")).toContain("/preview?size=preview");
    if (name !== "clip.mp4") {
      await user.dblClick(row);
      const viewer = await within(dialog).findByRole("dialog", { name: "预览 " + name });
      expect(within(viewer).getByRole("img", { name }).getAttribute("src")).toContain("/preview?size=preview");
      fireEvent.error(within(viewer).getByRole("img", { name }));
      expect(within(viewer).getByRole("alert").textContent).toContain("无法预览这张图片");
      expect(within(viewer).getAllByRole("button", { name: "下载" }).length).toBeGreaterThan(0);
    }
  });

  it("pins folders and preserves manual ordering across views and reopening without moving files", async () => {
    const fetchMock = installAPI({
      spaces: [{ id: "space:shared", kind: "shared", name: "Shared" }],
      entries: [
        { id: "a", spaceId: "space:shared", name: "A", kind: "directory", sizeBytes: 0, modifiedAt: "2026-10-01" },
        { id: "b", spaceId: "space:shared", name: "B", kind: "directory", sizeBytes: 0, modifiedAt: "2026-10-02" },
        { id: "c", spaceId: "space:shared", name: "C.bin", kind: "file", sizeBytes: 12, modifiedAt: "2026-10-03" },
      ],
    });
    const user = userEvent.setup(); const mounted = render(<App />);
    await user.click(await screen.findByRole("button", { name: "打开文件管理" }));
    let dialog = await screen.findByRole("dialog", { name: "文件管理" });
    const item = (id: string) => dialog.querySelector<HTMLElement>(`[data-file-item="${id}"]`)!;
    const order = () => Array.from(dialog.querySelectorAll<HTMLElement>("[data-file-item]")).map((row) => row.dataset.fileItem);
    await waitFor(() => expect(order()).toEqual(["a", "b", "c"]));
    fireEvent.contextMenu(item("b"));
    await user.click(screen.getByRole("menuitem", { name: "置顶文件夹" }));
    expect(order()).toEqual(["b", "a", "c"]);
    expect(item("b").querySelector('[aria-label="已置顶"]')).toBeTruthy();
    await user.click(within(dialog).getByRole("button", { name: "切换排序方向" }));
    expect(order()[0]).toBe("b");
    await user.selectOptions(within(dialog).getByLabelText("排序方式"), "manual");
    await user.click(item("c"));
    await user.click(within(dialog).getByRole("button", { name: "上移所选项目" }));
    expect(order()).toEqual(["b", "c", "a"]);
    expect(within(dialog).getByRole("button", { name: "上移所选项目" }).hasAttribute("disabled")).toBe(true);
    await user.click(within(dialog).getByRole("button", { name: "查看" }));
    await user.click(within(dialog).getByRole("button", { name: "图标视图" }));
    expect(order()).toEqual(["b", "c", "a"]);
    const dataTransfer = { types: ["text/plain"], setData: vi.fn(), effectAllowed: "", dropEffect: "" };
    fireEvent.dragStart(item("a"), { dataTransfer });
    fireEvent.dragOver(item("c"), { dataTransfer });
    fireEvent.drop(item("c"), { dataTransfer });
    expect(order()).toEqual(["b", "a", "c"]);
    expect(transferStore.getSnapshot().uploads).toHaveLength(0);
    await user.type(within(dialog).getByLabelText("搜索当前目录"), "A");
    expect(item("a").draggable).toBe(false);
    expect(within(dialog).getByRole("button", { name: "下移所选项目" }).hasAttribute("disabled")).toBe(true);
    await user.clear(within(dialog).getByLabelText("搜索当前目录"));
    await user.selectOptions(within(dialog).getByLabelText("排序方式"), "name");
    await user.selectOptions(within(dialog).getByLabelText("排序方式"), "manual");
    expect(order()).toEqual(["b", "a", "c"]);
    mounted.unmount(); render(<App />);
    await user.click(await screen.findByRole("button", { name: "打开文件管理" }));
    dialog = await screen.findByRole("dialog", { name: "文件管理" });
    await waitFor(() => expect(order()).toEqual(["b", "a", "c"]));
    expect((within(dialog).getByLabelText("排序方式") as HTMLSelectElement).value).toBe("manual");
    await user.click(item("b"));
    await user.click(within(dialog).getByRole("button", { name: "取消置顶" }));
    expect(item("b").querySelector('[aria-label="已置顶"]')).toBeNull();
    expect(fetchMock.mock.calls.some(([, init]) => init?.method === "POST")).toBe(false);
  }, 15000);

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

  // jsdom resolves the computed styles this test checks slowly.
  it("enables file management and lists only the signed-in user's spaces", { timeout: 15_000 }, async () => {
    const fetchMock = installAPI({
      spaces: [{ id: "space:owner", kind: "private", name: "owner", ownerUserId: "user:owner", createdAt: "2026-10-07T10:00:00Z" }, { id: "space:shared", kind: "shared", name: "Shared", createdAt: "2026-10-07T10:00:00Z" }],
      entries: [
        { id: "file:1", spaceId: "space:owner", name: "家庭", kind: "directory", sizeBytes: 0, modifiedAt: "2026-10-07T10:00:00Z" },
        { id: "file:2", spaceId: "space:owner", name: "A.txt", kind: "file", sizeBytes: 12, modifiedAt: "2026-10-08T10:00:00Z" },
      ],
      volumes: [{ id: "volume:data", diskIds: ["disk:data"], layout: "single", capacityBytes: 480 * 1024 ** 3, availableBytes: 278 * 1024 ** 3, state: "available" }],
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
    expect(within(dialog).getByText("202 GiB")).toBeTruthy();
    const rows = within(dialog).getAllByRole("row");
    expect(rows[1].textContent).toContain("家庭");
    expect(rows[1].querySelector(".file-type-cell .directory")).toBeTruthy();
    await user.click(rows.find((row) => row.textContent?.includes("A.txt"))!);
    await user.click(within(dialog).getByRole("button", { name: "复制" }));
    await user.click(within(dialog).getByRole("button", { name: "粘贴" }));
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/files/file%3A2/copies", expect.objectContaining({
      method: "POST", body: JSON.stringify({ spaceId: "space:owner", parentId: "", name: "A - 副本.txt" }),
    }));
    const inspector = within(dialog).getByRole("complementary", { name: "详细信息" });
    expect(within(inspector).queryByRole("button")).toBeNull();
    await user.dblClick(within(dialog).getAllByRole("row").find((row) => row.textContent?.includes("图库"))!);
    expect(await screen.findByRole("dialog", { name: "相册" })).toBeTruthy();
  });

  // Switching spaces and pasting re-renders the whole file manager several times.
  it("moves a cut file into the space it is pasted in", { timeout: 15_000 }, async () => {
    const fetchMock = installAPI({
      spaces: [{ id: "space:owner", kind: "private", name: "owner", ownerUserId: "user:owner", createdAt: "2026-10-07T10:00:00Z" }, { id: "space:shared", kind: "shared", name: "Shared", createdAt: "2026-10-07T10:00:00Z" }],
      entries: [{ id: "file:film", spaceId: "space:owner", name: "Heat (1995).mp4", kind: "file", sizeBytes: 12, modifiedAt: "2026-10-08T10:00:00Z" }],
    });
    const user = userEvent.setup(); render(<App />);
    await user.click(await screen.findByRole("button", { name: "打开文件管理" }));
    const dialog = await screen.findByRole("dialog", { name: "文件管理" });
    await user.click((await within(dialog).findAllByRole("row")).find((row) => row.textContent?.includes("Heat (1995).mp4"))!);
    await user.click(within(dialog).getByRole("button", { name: "剪切" }));
    // Pasting where the file already is would do nothing.
    expect(within(dialog).getByRole("button", { name: "粘贴" }).hasAttribute("disabled")).toBe(true);

    await user.click(within(dialog).getByRole("button", { name: "共享空间共享" }));
    await waitFor(() => expect(within(dialog).getByRole("button", { name: "粘贴" }).hasAttribute("disabled")).toBe(false));
    await user.click(within(dialog).getByRole("button", { name: "粘贴" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith("/api/v1/files/file%3Afilm", expect.objectContaining({
      method: "PATCH", body: JSON.stringify({ spaceId: "space:shared", parentId: "", name: "Heat (1995).mp4" }),
    })));
    expect(within(dialog).queryByText(/已剪切/)).toBeNull();
  });

  it("downloads files through the transfer center only on double click and exposes the file edit context menu", async () => {
    const fetchMock = installAPI({
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
    await waitFor(() => expect(click).toHaveBeenCalledTimes(1));
    const created = fetchMock.mock.calls.find(([path]) => path === "/api/v1/transfers/downloads");
    expect(JSON.parse(String(created?.[1]?.body))).toEqual({ entryIds: ["file:note"] });
    expect(click.mock.contexts[0]).toHaveProperty("href", expect.stringContaining("/api/v1/transfers/download-1/content"));
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

  it("previews images and PDFs in place on double click and keeps download a separate action", async () => {
    const fetchMock = installAPI({
      spaces: [{ id: "space:owner", kind: "private", name: "owner", ownerUserId: "user:owner", createdAt: "2026-10-07T10:00:00Z" }],
      entries: [
        { id: "file:beach", spaceId: "space:owner", name: "beach.jpg", kind: "file", sizeBytes: 20 * 1024 ** 2, modifiedAt: "2026-10-08T10:00:00Z" },
        { id: "file:cat", spaceId: "space:owner", name: "cat.png", kind: "file", sizeBytes: 300 * 1024, modifiedAt: "2026-10-08T10:00:00Z" },
        { id: "file:manual", spaceId: "space:owner", name: "manual.pdf", kind: "file", sizeBytes: 90 * 1024, modifiedAt: "2026-10-08T10:00:00Z" },
        { id: "file:archive", spaceId: "space:owner", name: "photos.zip", kind: "file", sizeBytes: 5, modifiedAt: "2026-10-08T10:00:00Z" },
      ],
    });
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    const user = userEvent.setup(); render(<App />);
    await user.click(await screen.findByRole("button", { name: "打开文件管理" }));
    const dialog = await screen.findByRole("dialog", { name: "文件管理" });
    const rows = await within(dialog).findAllByRole("row");
    const row = (name: string) => rows.find((item) => item.textContent?.includes(name))!;
    expect(row("beach.jpg").textContent).toContain("双击预览");
    expect(row("manual.pdf").textContent).toContain("双击预览");
    expect(row("photos.zip").textContent).toContain("双击下载");

    await user.dblClick(row("beach.jpg"));
    const viewer = await within(dialog).findByRole("dialog", { name: "预览 beach.jpg" });
    expect(within(viewer).getByText("20.0 MiB · 1 / 3")).toBeTruthy();
    expect(within(viewer).getByRole("img", { name: "beach.jpg" }).getAttribute("src")).toContain("/api/v1/files/file%3Abeach/content?disposition=inline");
    expect(click).not.toHaveBeenCalled();
    await user.keyboard("{ArrowRight}");
    const cat = await within(dialog).findByRole("dialog", { name: "预览 cat.png" });
    await user.click(within(cat).getByRole("button", { name: "下载" }));
    await waitFor(() => expect(click).toHaveBeenCalledTimes(1));
    const created = fetchMock.mock.calls.find(([path]) => path === "/api/v1/transfers/downloads");
    expect(JSON.parse(String(created?.[1]?.body))).toEqual({ entryIds: ["file:cat"] });
    await user.keyboard("{Escape}");
    expect(within(dialog).queryByRole("dialog", { name: /^预览/ })).toBeNull();
    // The last file viewed stays selected.
    expect(row("cat.png").getAttribute("aria-selected")).toBe("true");

    const manager = dialog.querySelector<HTMLElement>(".file-manager")!;
    manager.getBoundingClientRect = () => ({ left: 50, top: 70, right: 950, bottom: 690, width: 900, height: 620, x: 50, y: 70, toJSON: () => ({}) });
    fireEvent.contextMenu(row("beach.jpg"), { clientX: 120, clientY: 160 });
    const menu = await screen.findByRole("menu", { name: "文件操作" });
    await user.click(within(menu).getByRole("menuitem", { name: "预览" }));
    await user.click(within(await within(dialog).findByRole("dialog", { name: "预览 beach.jpg" })).getByRole("button", { name: "关闭预览" }));
    fireEvent.contextMenu(row("beach.jpg"), { clientX: 120, clientY: 160 });
    await user.click(within(await screen.findByRole("menu", { name: "文件操作" })).getByRole("menuitem", { name: "下载" }));
    await waitFor(() => expect(click).toHaveBeenCalledTimes(2));

    await user.dblClick(row("photos.zip"));
    await waitFor(() => expect(click).toHaveBeenCalledTimes(3));
    expect(within(dialog).queryByRole("dialog", { name: /^预览/ })).toBeNull();
  });

  // Opening the transfer center loads its window code on first use.
  it("uploads picked and dropped files through the transfer center without blocking the folder", { timeout: 15_000 }, async () => {
    const fetchMock = installAPI({
      spaces: [{ id: "space:shared", kind: "shared", name: "Shared", createdAt: "2026-10-07T10:00:00Z" }],
      entries: [{ id: "file:old", spaceId: "space:shared", name: "old.txt", kind: "file", sizeBytes: 3, modifiedAt: "2026-10-08T10:00:00Z" }],
    });
    const user = userEvent.setup(); render(<App />);
    await user.click(await screen.findByRole("button", { name: "打开文件管理" }));
    const dialog = await screen.findByRole("dialog", { name: "文件管理" });
    await within(dialog).findByText("old.txt");
    await user.click(within(dialog).getByRole("button", { name: "上传到当前目录" }));
    expect(within(dialog).getByRole("menuitem", { name: /上传文件夹/ })).toBeTruthy();
    await user.upload(within(dialog).getByLabelText("选择要上传的文件"), [new File(["hello"], "a.txt"), new File(["world!"], "b.txt")]);
    expect(tus.uploads).toHaveLength(2);
    expect(tus.uploads[0].options.metadata).toMatchObject({ filename: "a.txt", spaceId: "space:shared", parentId: "", conflict: "ask" });
    // Other commands stay available while files upload.
    expect(within(dialog).getByRole("button", { name: "新建" }).hasAttribute("disabled")).toBe(false);
    tus.uploads[0].options.onProgress(5, 5);
    expect(await within(dialog).findByRole("button", { name: /正在上传 2 个文件/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: /传输中心：正在上传 .+ 等 2 个任务/ })).toBeTruthy();

    const browser = within(dialog).getByRole("region", { name: "文件浏览器" });
    const dataTransfer = { types: ["Files"], items: [], files: [new File(["dropped"], "c.txt")], dropEffect: "none" };
    fireEvent.dragEnter(browser, { dataTransfer });
    expect(within(dialog).getByText("松开即可上传")).toBeTruthy();
    fireEvent.drop(browser, { dataTransfer });
    await waitFor(() => expect(tus.uploads).toHaveLength(3));
    expect(within(dialog).queryByText("松开即可上传")).toBeNull();

    const listed = () => fetchMock.mock.calls.filter(([path]) => String(path).startsWith("/api/v1/spaces/space%3Ashared/entries")).length;
    const before = listed();
    tus.uploads[0].options.onSuccess();
    await waitFor(() => expect(listed()).toBeGreaterThan(before));
    await user.click(screen.getByRole("button", { name: /传输中心：正在上传 .+ 等 2 个任务/ }));
    const center = await screen.findByRole("dialog", { name: "传输中心" });
    expect(await within(center).findByRole("heading", { name: "正在传输" })).toBeTruthy();
  });

  it("downloads a folder as one archive from the context menu", async () => {
    const fetchMock = installAPI({
      spaces: [{ id: "space:owner", kind: "private", name: "owner", ownerUserId: "user:owner", createdAt: "2026-10-07T10:00:00Z" }],
      entries: [{ id: "file:album", spaceId: "space:owner", name: "相册", kind: "directory", sizeBytes: 0, modifiedAt: "2026-10-08T10:00:00Z" }],
    });
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    const user = userEvent.setup(); render(<App />);
    await user.click(await screen.findByRole("button", { name: "打开文件管理" }));
    const dialog = await screen.findByRole("dialog", { name: "文件管理" });
    const row = (await within(dialog).findAllByRole("row")).find((item) => item.textContent?.includes("相册") && item.textContent?.includes("文件夹"))!;
    const manager = dialog.querySelector<HTMLElement>(".file-manager")!;
    manager.getBoundingClientRect = () => ({ left: 50, top: 70, right: 950, bottom: 690, width: 900, height: 620, x: 50, y: 70, toJSON: () => ({}) });
    fireEvent.contextMenu(row, { clientX: 120, clientY: 160 });
    await user.click(within(await screen.findByRole("menu", { name: "文件操作" })).getByRole("menuitem", { name: /打包下载/ }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([path]) => path === "/api/v1/transfers/downloads")).toBe(true));
    const created = fetchMock.mock.calls.find(([path]) => path === "/api/v1/transfers/downloads");
    expect(JSON.parse(String(created?.[1]?.body))).toEqual({ entryIds: ["file:album"] });
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
      volumes: [{ id: "volume:data", diskIds: ["disk:data"], layout: "single", capacityBytes: 500_107_862_016, state: "available" }],
    });
    const user = userEvent.setup(); render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    await user.click(within(desktop).getByRole("button", { name: "打开文件管理" }));
    const dialog = await screen.findByRole("dialog", { name: "文件管理" });
    const values = dialog.querySelectorAll(".file-volume-values strong");

    expect(values[0]?.textContent).toBe("—");
    expect(values[1]?.textContent).toBe("466 GiB");
  });

  it("enables photos, trash, the backup center, and one settings entry", async () => {
    installAPI(); const user = userEvent.setup(); render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    expect(within(desktop).getByRole("button", { name: "打开回收站" }).hasAttribute("disabled")).toBe(false);
    expect(within(desktop).getByRole("button", { name: "打开备份中心" }).hasAttribute("disabled")).toBe(false);
    expect(within(desktop).getByRole("button", { name: "打开设置" })).toBeTruthy();
    // 文件快照 and the planned 备份 entry became the one 备份中心; the planned 日志 moved into 设置.
    // The terminal moved into 设置 → 终端.
    for (const retired of ["打开系统设置", "打开资源管理", "打开存储初始化", "打开账号管理", "打开文件快照", "备份，规划中", "日志，规划中", "打开终端"]) {
      expect(within(desktop).queryByRole("button", { name: retired })).toBeNull();
    }
    await user.click(within(desktop).getByRole("button", { name: "打开相册" }));
    expect(await screen.findByRole("dialog", { name: "相册" })).toBeTruthy();
  });

  it("keeps the desktop usable when a blank-disk plan contains legacy null arrays", async () => {
    installAPI({
      storagePlan: {
        id: "plan:blank", kind: "create_volume", layout: "single", capacityAfterBytes: 512000000000,
        disks: [{ id: "disk:fake-data-01", model: "A-NAS Fake HDD", capacityBytes: 512000000000, fingerprint: "blank-disk-fingerprint", signatures: null }],
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
    expect(desktopOrder(await screen.findByRole("region", { name: "桌面应用" })).slice(0, 4)).toEqual(["文件管理", "回收站", "应用中心", "设置"]);
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
    await user.click(within(await screen.findByRole("group", { name: "回收站窗口预览" })).getByRole("button", { name: "关闭回收站" }));
    expect(screen.queryByRole("dialog", { name: "回收站" })).toBeNull();
    expect(within(dock).getAllByRole("button").map((button) => button.getAttribute("aria-label"))).toEqual(["最小化设置"]);

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
    expect(within(dock).getAllByRole("button").map((button) => button.getAttribute("aria-label"))).toEqual(["切换到回收站", "最小化设置"]);

    await user.click(within(screen.getByRole("dialog", { name: "设置" })).getByRole("button", { name: "关闭设置" }));
    expect(within(dock).getAllByRole("button").map((button) => button.getAttribute("aria-label"))).toEqual(["最小化回收站"]);
  });

  it("keeps the system rail to working shortcuts", async () => {
    stubDesktopFetch(healthyState, true);
    render(<App />);
    const rail = await screen.findByRole("complementary", { name: "系统快捷栏" });
    const buttons = within(rail).getAllByRole("button");
    expect(buttons.map((button) => button.getAttribute("aria-label"))).toEqual(["显示桌面", "全部应用", "打开分享", "打开设置", "账号（owner）"]);
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

    const showDesktop = within(screen.getByRole("complementary", { name: "系统快捷栏" })).getByRole("button", { name: "显示桌面" });
    expect(showDesktop.getAttribute("aria-pressed")).toBe("false");
    await user.click(showDesktop);
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(showDesktop.getAttribute("aria-pressed")).toBe("true");

    await user.click(showDesktop);
    expect(screen.getAllByRole("dialog").map((dialog) => dialog.getAttribute("aria-label"))).toEqual(["回收站", "设置"]);
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
    await user.pointer([{ keys: "[MouseLeft>]", target: titleBar, coords: { clientX: 400, clientY: 90 } }, { target: wallpaper, coords: { clientX: 40, clientY: 600 } }, { keys: "[/MouseLeft]" }]);
    await user.pointer([{ keys: "[MouseLeft>]", target: wallpaper, coords: { clientX: 1200, clientY: 600 } }, { coords: { clientX: 1100, clientY: 500 } }, { keys: "[/MouseLeft]" }]);
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
    expect(within(screen.getByRole("navigation", { name: "已打开窗口" })).getByRole("button", { name: "最小化回收站" }).getAttribute("aria-current")).toBe("true");

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
    expect(within(launcher).getAllByRole("button").map((button) => button.getAttribute("aria-label"))).toContain("打开相册");
    expect(within(launcher).getByRole("list", { name: "规划中的应用" }).textContent).toContain("VNAS");

    expect(within(launcher).getByRole("list", { name: "规划中的应用" }).textContent).not.toContain("日志");
    await user.type(search, "日志");
    expect(within(launcher).getAllByRole("button").map((button) => button.getAttribute("aria-label"))).toEqual(["打开设置"]);
    await user.clear(search);
    await user.type(search, "命令行");
    expect(within(launcher).getAllByRole("button").map((button) => button.getAttribute("aria-label"))).toEqual(["打开设置"]);
    await user.keyboard("{Enter}");
    expect(screen.queryByRole("dialog", { name: "全部应用" })).toBeNull();
    expect(screen.getByRole("dialog", { name: "设置" })).toBeTruthy();
  });

  it("closes the launcher with Escape and returns focus to the rail", async () => {
    stubDesktopFetch(healthyState, true);
    const user = userEvent.setup();
    render(<App />);
    const trigger = within(await screen.findByRole("complementary", { name: "系统快捷栏" })).getByRole("button", { name: "全部应用" });
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
    expect(fetchMock.mock.calls.some(([path, init]) => path === "/api/v1/session" && init?.method === "DELETE")).toBe(true);
  });

  it("manages containers from the Docker app", async () => {
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
    expect(within(jellyfin).getByText("已运行 3 小时")).toBeTruthy();
    expect(within(jellyfin).getByRole("link", { name: "打开 jellyfin 的端口 8096" }).getAttribute("href")).toMatch(/:8096\/$/);
    expect(within(jellyfin).getByText("12.4%")).toBeTruthy();

    await user.click(within(jellyfin).getByRole("button", { name: "停止 jellyfin" }));
    expect(actions).toHaveLength(0);
    await user.click(within(within(docker).getByRole("dialog", { name: "确认停止 jellyfin" })).getByRole("button", { name: "确认停止" }));
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
    await user.click(await within(store).findByRole("button", { name: "安装 Memos" }));
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

  it("opens the administrator terminal in Settings rather than on the desktop", async () => {
    installAPI({ terminalEnabled: true });
    const user = userEvent.setup();
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    expect(within(desktop).queryByRole("button", { name: "打开终端" })).toBeNull();

    await openSettingsSection(user, desktop, "终端");
    const settings = screen.getByRole("dialog", { name: "设置" });
    expect(await within(settings).findByTestId("xterm-session")).toBeTruthy();
    expect(within(settings).getByRole("heading", { name: "终端" })).toBeTruthy();
    expect(within(settings).getByText("以 owner 的 Linux 账号在 anas-fake 上运行的 Shell")).toBeTruthy();
    expect(await within(settings).findByText("已连接")).toBeTruthy();
    expect(xterm.mounts).toBe(1);
  });

  it("keeps the shell running while another settings section is shown", async () => {
    installAPI({ terminalEnabled: true });
    const user = userEvent.setup();
    render(<App />);
    await openSettingsSection(user, await screen.findByRole("region", { name: "桌面应用" }), "终端");
    const settings = screen.getByRole("dialog", { name: "设置" });
    await within(settings).findByTestId("xterm-session");
    const sections = within(settings).getByRole("navigation", { name: "设置分区" });

    await user.click(within(sections).getByRole("button", { name: "日志" }));
    expect(within(settings).getByRole("heading", { name: "日志" })).toBeTruthy();
    expect(within(settings).getByTestId("xterm-session").closest("[hidden]")).toBeTruthy();
    expect(within(sections).getByRole("button", { name: "终端" }).querySelector(".set-nav-live")).toBeTruthy();

    await user.click(within(sections).getByRole("button", { name: "终端" }));
    expect(within(settings).getByTestId("xterm-session").closest("[hidden]")).toBeNull();
    expect(within(sections).getByRole("button", { name: "终端" }).querySelector(".set-nav-live")).toBeNull();
    expect(xterm.mounts).toBe(1);
  });

  it("keeps the shell while Settings is minimized and ends it when Settings closes", async () => {
    installAPI({ terminalEnabled: true });
    const user = userEvent.setup();
    render(<App />);
    await openSettingsSection(user, await screen.findByRole("region", { name: "桌面应用" }), "终端");
    await screen.findByTestId("xterm-session");

    await user.click(within(screen.getByRole("dialog", { name: "设置" })).getByRole("button", { name: "最小化设置" }));
    expect(screen.queryByRole("dialog", { name: "设置" })).toBeNull();
    await user.click(screen.getByRole("button", { name: "恢复设置" }));
    expect(screen.getByTestId("xterm-session")).toBeTruthy();
    expect(xterm.mounts).toBe(1);

    await user.click(within(screen.getByRole("dialog", { name: "设置" })).getByRole("button", { name: "关闭设置" }));
    expect(screen.queryByTestId("xterm-session")).toBeNull();
    // Settings reopens on the terminal with a new shell.
    await user.click(within(screen.getByRole("region", { name: "桌面应用" })).getByRole("button", { name: "打开设置" }));
    expect(await screen.findByTestId("xterm-session")).toBeTruthy();
    expect(xterm.mounts).toBe(2);
  });

  it("offers a new session after the shell exits", async () => {
    installAPI({ terminalEnabled: true });
    const user = userEvent.setup();
    render(<App />);
    await openSettingsSection(user, await screen.findByRole("region", { name: "桌面应用" }), "终端");
    await screen.findByTestId("xterm-session");

    act(() => xterm.onStatus?.({ state: "ended", exitCode: 0 }));
    const settings = screen.getByRole("dialog", { name: "设置" });
    const notice = within(settings).getByRole("status");
    expect(notice.textContent).toContain("Shell 已退出（代码 0）");
    expect(within(settings).getByText("已结束")).toBeTruthy();

    await user.click(within(notice).getByRole("button", { name: "新建会话" }));
    expect(screen.queryByText("Shell 已退出（代码 0）")).toBeNull();
    expect(xterm.mounts).toBe(2);
  });

  it("explains how the device's root turns the terminal on", async () => {
    const fetchMock = installAPI({ terminalEnabled: false });
    const user = userEvent.setup();
    render(<App />);
    await openSettingsSection(user, await screen.findByRole("region", { name: "桌面应用" }), "终端");

    expect(await screen.findByRole("heading", { name: "终端尚未开启" })).toBeTruthy();
    expect(screen.getByLabelText("开启终端的命令").textContent).toContain("ANAS_TERMINAL=enabled");
    expect(screen.queryByTestId("xterm-session")).toBeNull();
    await user.click(screen.getByRole("button", { name: "重新检查" }));
    await waitFor(() => expect(fetchMock.mock.calls.filter(([path]) => path === "/api/v1/terminal")).toHaveLength(2));
  });
});

function installAPI(options: { setupRequired?: boolean; setupAllowed?: boolean; spaces?: unknown[]; entries?: unknown[]; volumes?: unknown[]; storagePlan?: unknown; terminalEnabled?: boolean; session?: unknown; notifications?: unknown[]; users?: unknown[]; textPreview?: string; profiles?: unknown[]; shortcuts?: unknown[]; hostState?: unknown } = {}) {
  const mock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input);
    if (path === "/api/v1/app-shortcuts") return ok({ items: options.shortcuts ?? [] });
    const signedIn = (options.session ?? session) as { user: Record<string, unknown> };
    if (path === "/api/v1/profiles") return ok({ items: options.profiles ?? [] });
    if (path === "/api/v1/session/profile" && init?.method === "PATCH") {
      const { nickname } = JSON.parse(String(init.body)) as { nickname: string };
      if (nickname === "owner") return failure(409, "nickname_unavailable");
      return ok({ ...signedIn.user, nickname: nickname || undefined });
    }
    if (path === "/api/v1/session/avatar" && init?.method === "PUT") return ok({ ...signedIn.user, avatarVersion: "0123456789abcdef" });
    if (path === "/api/v1/session/avatar" && init?.method === "DELETE") return ok({ ...signedIn.user, avatarVersion: undefined });
    if (path === "/api/v1/setup/status") return ok({ setupRequired: options.setupRequired ?? false, setupAllowed: options.setupAllowed ?? true });
    if (path === "/api/v1/setup/admin" && init?.method === "POST") return ok(session, 201);
    if (path === "/api/v1/session") return ok(options.session ?? session);
    if (path === "/api/v1/notifications") return ok({ items: options.notifications ?? [] });
    if (path === "/api/v1/host-state") return ok(options.hostState ?? healthyState);
    if (path === "/api/v1/metrics") return ok(metricsState);
    if (path === "/api/v1/terminal") return ok({ enabled: options.terminalEnabled ?? false });
    if (path === "/api/v1/spaces") return ok({ items: options.spaces ?? [] });
    if (path.startsWith("/api/v1/spaces/") && path.includes("/entries")) return ok({ items: options.entries ?? [] });
    if (path.startsWith("/api/v1/files/") && path.includes("/content")) return { ok: true, status: 200, text: async () => options.textPreview ?? "" } as Response;
    if (path === "/api/v1/volumes") return ok({ items: options.volumes ?? [] });
	if (path === "/api/v1/storage/plans" && init?.method === "POST") return ok(options.storagePlan ?? {}, 201);
    if (path.startsWith("/api/v1/storage/plans/") && path.endsWith("/confirm")) return ok({ ...(options.storagePlan as object), state: "confirmed" });
    if (path.startsWith("/api/v1/storage/plans/") && path.endsWith("/execute")) return ok({ ...(options.storagePlan as object), state: "succeeded" });
    if (path.startsWith("/api/v1/storage/plans/") && path.endsWith("/cancel")) return ok({ ...(options.storagePlan as object), state: "cancelled" });
    if (path.startsWith("/api/v1/volumes/") && path.endsWith("/conversion")) return { ok: true, status: 202, headers: new Headers({ "Content-Length": "0" }), json: async () => undefined } as Response;
    if (path === "/api/v1/users" && init?.method === "POST") {
      const { username } = JSON.parse(String(init.body)) as { username: string };
      return ok({ id: "user:new", username: username.trim().toLowerCase(), role: "member", status: "active", createdAt: "2026-10-07T10:00:00Z", mustChangePassword: false }, 201);
    }
    if (path === "/api/v1/users") return ok({ items: options.users ?? [session.user] });
    if (path === "/api/v1/trash") return ok({ items: [] });
    if (path === "/api/v1/transfers/downloads" && init?.method === "POST") {
      const { entryIds } = JSON.parse(String(init.body)) as { entryIds: string[] };
      return ok({
        id: "download-1", direction: "download", kind: entryIds.length > 1 ? "archive" : "file", name: entryIds.length > 1 ? "下载.zip" : "notes.txt",
        location: { spaceId: "space:owner", spaceKind: "private", folders: [] }, totalBytes: 12, transferredBytes: 0, bytesPerSecond: 0,
        state: "pending", contentUrl: "/api/v1/transfers/download-1/content", createdAt: "2026-10-12T10:00:00Z", updatedAt: "2026-10-12T10:00:00Z",
      }, 201);
    }
    return ok({ items: [] });
  });
  vi.stubGlobal("fetch", mock);
  return mock;
}

function ok(body: unknown, status = 200) { return { ok: true, status, json: async () => body } as Response; }
function failure(status: number, code: string) { return { ok: false, status, json: async () => ({ error: { code, message: code } }) } as Response; }

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
    await user.click(await screen.findByRole("button", { name: "账号（owner）" }));
    await user.click(screen.getByRole("menuitem", { name: /^我的账号/ }));
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

describe("Installed apps on the desktop", () => {
  const immich = { id: "immich", title: "Immich", webPort: 2283, scheme: "http", path: "/", running: 4, total: 4 };
  const navidrome = { id: "navidrome", title: "Navidrome", webPort: 4533, scheme: "http", path: "/", running: 0, total: 1 };

  it("opens an installed app's web page from its desktop icon and from the launcher", async () => {
    installAPI({ shortcuts: [immich, navidrome] });
    const open = vi.spyOn(window, "open").mockReturnValue(null);
    const user = userEvent.setup();
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    const icon = await within(desktop).findByRole("button", { name: "打开 Immich（新窗口）" });
    expect(icon.querySelector("img")?.getAttribute("src")).toBe("/api/v1/app-shortcuts/immich/icon");
    await user.click(icon);
    expect(open).toHaveBeenLastCalledWith(expect.stringMatching(/^http:\/\/[^/]+:2283\/$/), "_blank", "noopener,noreferrer");

    await user.click(within(screen.getByRole("complementary", { name: "系统快捷栏" })).getByRole("button", { name: "全部应用" }));
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
    installAPI({ shortcuts: [immich, navidrome], session: { ...session, user: { ...session.user, username: "alice", role: "member" } } });
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    expect(await within(desktop).findByRole("button", { name: "打开 Immich（新窗口）" })).toBeTruthy();
    const stopped = within(desktop).getByRole("button", { name: "Navidrome，没有在运行" });
    expect(stopped.getAttribute("aria-disabled")).toBe("true");
  });
});

describe("Settings", () => {
  const alice = { id: "user:alice", username: "alice", role: "member", status: "active", createdAt: "2026-10-07T10:00:00Z" };

  it("shows members their account, task manager, overview and read-only storage", async () => {
    const fetchMock = installAPI({ session: { ...session, user: alice } });
    const user = userEvent.setup();
    render(<App />);
    await openSettingsSection(user, await screen.findByRole("region", { name: "桌面应用" }), "存储");
    const settings = screen.getByRole("dialog", { name: "设置" });
    const sections = within(settings).getByRole("navigation", { name: "设置分区" });
    const enabled = within(sections).getAllByRole("button").filter((button) => !button.hasAttribute("disabled"));
    expect(enabled.map((button) => button.getAttribute("aria-label") ?? button.textContent)).toEqual(["我的账号", "概览", "任务管理器", "存储", "日志"]);
    expect(await within(settings).findByRole("heading", { name: "尚未创建数据卷" })).toBeTruthy();
    expect(within(settings).getByText(/请联系管理员初始化/)).toBeTruthy();
    expect(within(settings).getByText("A-NAS Fake HDD")).toBeTruthy();
    expect(within(settings).queryByRole("button", { name: "初始化数据卷…" })).toBeNull();
    expect(within(settings).queryByRole("region", { name: "初始化数据卷" })).toBeNull();
    expect(fetchMock.mock.calls.some(([path]) => path === "/api/v1/users")).toBe(false);
  });

  it("opens a member's sign-in records from My account", async () => {
    const fetchMock = installAPI({ session: { ...session, user: alice } });
    const user = userEvent.setup();
    render(<App />);
    await openSettingsSection(user, await screen.findByRole("region", { name: "桌面应用" }), "我的账号");
    await user.click(screen.getByRole("button", { name: "查看" }));
    const settings = screen.getByRole("dialog", { name: "设置" });
    expect(within(settings).getByRole("button", { name: "日志" }).getAttribute("aria-current")).toBe("page");
    expect(within(settings).getByRole("heading", { name: "我的活动" })).toBeTruthy();
    expect(within(settings).getByRole("radio", { name: "登录" }).getAttribute("aria-checked")).toBe("true");
    await waitFor(() => expect(fetchMock.mock.calls.some(([path]) => String(path).startsWith("/api/v1/activity?") && String(path).includes("category=session"))).toBe(true));
    expect(within(settings).queryByRole("tab", { name: "系统日志" })).toBeNull();
  });

  it("keeps showing a member who reset their password", async () => {
    installAPI({ session: { ...session, user: alice, credentialReset: { actorUsername: "owner", at: "2026-10-14T09:10:00Z" } } });
    const user = userEvent.setup();
    render(<App />);
    await openSettingsSection(user, await screen.findByRole("region", { name: "桌面应用" }), "我的账号");
    expect(screen.getByText("管理员重置过密码")).toBeTruthy();
    expect(screen.getByText(/owner 于 .* 重置了这个账号的密码/)).toBeTruthy();
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
        id: "plan:pending", kind: "create_volume", layout: "single", capacityAfterBytes: 512000000000,
        disks: [{ id: "disk:fake-data-01", model: "A-NAS Fake HDD", capacityBytes: 512000000000, fingerprint: "pending-fingerprint", signatures: [] }],
        confirmationPhrase: "ERASE data-01", actions: [], state: "planned", expiresAt: "2099-01-01T00:00:00Z",
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

  it("says why the system is rated as it is and leads to storage", async () => {
    installAPI({ hostState: { ...healthyState, system: { ...healthyState.system, health: "critical", healthFindings: [
      { code: "volume_read_only", severity: "critical", diskId: "disk:fake-data-01" },
      { code: "disk_hot", severity: "warning", diskId: "disk:fake-data-01", value: 57 },
    ] } } });
    const user = userEvent.setup();
    render(<App />);
    await openSettingsSection(user, await screen.findByRole("region", { name: "桌面应用" }), "概览");
    const settings = screen.getByRole("dialog", { name: "设置" });
    expect(await within(settings).findByText("严重故障")).toBeTruthy();
    expect(within(settings).getByText("2 项需要处理")).toBeTruthy();
    expect(within(settings).getByText("数据卷变成了只读")).toBeTruthy();
    expect(within(settings).getByText("A-NAS Fake HDD 温度 57 °C，偏高")).toBeTruthy();
    expect(within(settings).queryByText("健康未评估")).toBeNull();
    await user.click(within(settings).getByRole("button", { name: "在存储中查看磁盘与数据卷" }));
    expect(within(settings).getByRole("button", { name: "存储" }).getAttribute("aria-current")).toBe("page");
  });

  it("says what was checked when nothing is wrong", async () => {
    installAPI();
    const user = userEvent.setup();
    render(<App />);
    await openSettingsSection(user, await screen.findByRole("region", { name: "桌面应用" }), "概览");
    const settings = screen.getByRole("dialog", { name: "设置" });
    expect(await within(settings).findByText("运行正常")).toBeTruthy();
    expect(within(settings).getByText("未发现问题")).toBeTruthy();
    expect(within(settings).queryByRole("button", { name: "在存储中查看磁盘与数据卷" })).toBeNull();
  });

  it("reports unknown data volume usage instead of a full volume", async () => {
    installAPI({ volumes: [{ id: "volume:data", diskIds: ["disk:data"], layout: "single", capacityBytes: 500_107_862_016, state: "available", filesystemUuid: "09e275fe-794a" }] });
    const user = userEvent.setup();
    render(<App />);
    await openSettingsSection(user, await screen.findByRole("region", { name: "桌面应用" }), "存储");
    const settings = screen.getByRole("dialog", { name: "设置" });
    expect(await within(settings).findByText("已用空间未知 · 共 466 GiB")).toBeTruthy();
    expect(within(settings).getByText("在线")).toBeTruthy();
    expect(within(settings).queryByRole("meter", { name: "数据卷已用空间" })).toBeNull();
    expect(within(settings).queryByRole("region", { name: "初始化数据卷" })).toBeNull();
  });

  it("adds a new disk to the data volume as a mirror", async () => {
    const dataDisk = { ...healthyState.disks[0], id: "disk:data", model: "ST500DM002-1BD142", capacityBytes: 500_107_862_016, role: "data", inUse: true, filesystems: ["btrfs"], eligibleForDataVolume: false, ineligibleReasons: ["data_volume", "in_use"] };
    const spareDisk = { ...healthyState.disks[0], id: "disk:spare", model: "ST500DM002-1BD142", capacityBytes: 500_107_862_016, filesystems: ["linux_raid_member"] };
    const usbDisk = { ...healthyState.disks[0], id: "disk:usb", model: "USB Stick", transport: "usb", removable: true, eligibleForDataVolume: false, ineligibleReasons: ["unsupported_transport", "removable"] };
    const plan = {
      id: "plan:add", kind: "add_disks", layout: "mirror", capacityBeforeBytes: 500_107_862_016, capacityAfterBytes: 500_107_862_016,
      disks: [{ id: "disk:spare", model: "ST500DM002-1BD142", capacityBytes: 500_107_862_016, fingerprint: "spare-fingerprint", signatures: [{ type: "linux_raid_member", label: "OrangePi3B:0" }] }],
      confirmationPhrase: "ERASE k:spare", actions: [{ kind: "convert_layout", description: "在后台把数据卷从单盘转换为镜像（RAID 1），期间可以照常使用" }],
      state: "planned", expiresAt: "2099-01-01T00:00:00Z",
    };
    const fetchMock = installAPI({
      hostState: { ...healthyState, disks: [dataDisk, spareDisk, usbDisk] },
      volumes: [{ id: "volume:data", diskIds: ["disk:data"], layout: "single", capacityBytes: 480 * 1024 ** 3, availableBytes: 280 * 1024 ** 3, state: "available", filesystemUuid: "09e275fe-794a" }],
      storagePlan: plan,
    });
    const user = userEvent.setup();
    render(<App />);
    await openSettingsSection(user, await screen.findByRole("region", { name: "桌面应用" }), "存储");
    const settings = screen.getByRole("dialog", { name: "设置" });
    expect(await within(settings).findByText("发现 1 块未使用的硬盘")).toBeTruthy();
    expect(within(settings).getAllByText("有旧数据").length).toBeGreaterThan(0);
    expect(within(settings).getByText("USB 等外接硬盘不能加入数据卷；可移除设备不能加入数据卷")).toBeTruthy();

    await user.click(within(settings).getByRole("button", { name: "设置用途…" }));
    const wizard = within(settings).getByRole("region", { name: "设置硬盘用途" });
    expect((within(wizard).getByRole("radio", { name: /用作备份盘/ }) as HTMLInputElement).disabled).toBe(true);
    expect((within(wizard).getByRole("checkbox", { name: /ST500DM002/ }) as HTMLInputElement).checked).toBe(true);
    await user.click(within(wizard).getByRole("button", { name: "下一步" }));
    const layouts = within(wizard).getByRole("radiogroup", { name: "冗余方式" });
    expect((within(layouts).getByRole("radio", { name: /镜像/ }) as HTMLInputElement).checked).toBe(true);
    expect(within(layouts).getByText("推荐")).toBeTruthy();
    expect(within(wizard).getByText(/不能代替备份/)).toBeTruthy();
    await user.click(within(wizard).getByRole("button", { name: "生成计划" }));
    const created = fetchMock.mock.calls.find(([path, init]) => path === "/api/v1/storage/plans" && init?.method === "POST");
    expect(JSON.parse(String(created?.[1]?.body))).toEqual({ diskIds: ["disk:spare"], layout: "mirror" });

    expect(await within(wizard).findByRole("heading", { name: "破坏性操作计划" })).toBeTruthy();
    expect(within(wizard).getByText("将清除：linux_raid_member（OrangePi3B:0）")).toBeTruthy();
    expect(within(wizard).getByText("硬盘上有其他系统留下的数据")).toBeTruthy();
    expect(within(wizard).getByText("镜像（RAID 1）")).toBeTruthy();
    await user.type(within(wizard).getByLabelText(/输入确认短语/), "ERASE k:spare");
    await user.click(within(wizard).getByRole("button", { name: "确认计划" }));
    await user.click(await within(wizard).findByRole("button", { name: "清除硬盘并加入数据卷" }));
    expect(await within(wizard).findByText("硬盘已加入数据卷")).toBeTruthy();
    await user.click(within(wizard).getByRole("button", { name: "完成" }));
    expect(within(settings).queryByRole("region", { name: "设置硬盘用途" })).toBeNull();
  });

  it("leaves the wizard by cancelling a plan that has not run", async () => {
    const fetchMock = installAPI({
      storagePlan: {
        id: "plan:leave", kind: "create_volume", layout: "single", capacityAfterBytes: 512000000000,
        disks: [{ id: "disk:fake-data-01", model: "A-NAS Fake HDD", capacityBytes: 512000000000, fingerprint: "leave-fingerprint", signatures: [] }],
        confirmationPhrase: "ERASE data-01", actions: [], state: "planned", expiresAt: "2099-01-01T00:00:00Z",
      },
    });
    const user = userEvent.setup();
    render(<App />);
    await openSettingsSection(user, await screen.findByRole("region", { name: "桌面应用" }), "存储");
    const settings = screen.getByRole("dialog", { name: "设置" });
    await user.click(await within(settings).findByRole("button", { name: "初始化数据卷…" }));
    await user.click(within(settings).getByRole("button", { name: "生成格式化计划" }));
    expect(await within(settings).findByRole("heading", { name: "破坏性操作计划" })).toBeTruthy();

    await user.click(within(settings).getByRole("button", { name: "取消计划" }));
    expect(fetchMock.mock.calls.some(([path, init]) => path === "/api/v1/storage/plans/plan%3Aleave/cancel" && init?.method === "POST")).toBe(true);
    expect(await within(settings).findByRole("heading", { name: "尚未创建数据卷" })).toBeTruthy();
    expect(within(settings).queryByRole("region", { name: "初始化数据卷" })).toBeNull();

    await user.click(within(settings).getByRole("button", { name: "初始化数据卷…" }));
    await user.click(within(settings).getByRole("button", { name: "生成格式化计划" }));
    await user.click(await within(settings).findByRole("button", { name: "取消计划并关闭" }));
    expect(await within(settings).findByRole("heading", { name: "尚未创建数据卷" })).toBeTruthy();
  });

  it("shows how far a conversion has come and continues a stopped one", async () => {
    const disks = [
      { ...healthyState.disks[0], id: "disk:data", role: "data", inUse: true, eligibleForDataVolume: false, ineligibleReasons: ["data_volume"] },
      { ...healthyState.disks[0], id: "disk:spare", role: "data", inUse: true, eligibleForDataVolume: false, ineligibleReasons: ["data_volume"] },
    ];
    const fetchMock = installAPI({
      hostState: { ...healthyState, disks },
      volumes: [{
        id: "volume:data", diskIds: ["disk:data", "disk:spare"], layout: "mirror", capacityBytes: 480 * 1024 ** 3, availableBytes: 280 * 1024 ** 3, state: "available",
        conversion: { state: "waiting", convertedBytes: 80 * 1024 ** 3, totalBytes: 200 * 1024 ** 3 },
      }],
    });
    const user = userEvent.setup();
    render(<App />);
    await openSettingsSection(user, await screen.findByRole("region", { name: "桌面应用" }), "存储");
    const settings = screen.getByRole("dialog", { name: "设置" });
    expect(await within(settings).findByText("转换为镜像已暂停")).toBeTruthy();
    expect(within(settings).getByText("Btrfs · 镜像（RAID 1） · 2 块硬盘")).toBeTruthy();
    expect(within(settings).getByRole("progressbar", { name: "转换为镜像" }).getAttribute("aria-valuenow")).toBe("40");
    expect(within(settings).getByText("转换中")).toBeTruthy();
    expect(within(settings).queryByRole("button", { name: "设置用途…" })).toBeNull();
    await user.click(within(settings).getByRole("button", { name: "继续转换" }));
    expect(fetchMock.mock.calls.some(([path, init]) => path === "/api/v1/volumes/volume%3Adata/conversion" && init?.method === "POST")).toBe(true);
  });

  it("creates a mirror when two disks are chosen for a new data volume", async () => {
    const second = { ...healthyState.disks[0], id: "disk:fake-data-02" };
    const fetchMock = installAPI({ hostState: { ...healthyState, disks: [healthyState.disks[0], second] } });
    const user = userEvent.setup();
    render(<App />);
    await openSettingsSection(user, await screen.findByRole("region", { name: "桌面应用" }), "存储");
    const settings = screen.getByRole("dialog", { name: "设置" });
    await user.click(await within(settings).findByRole("button", { name: "初始化数据卷…" }));
    const wizard = within(settings).getByRole("region", { name: "初始化数据卷" });
    const boxes = within(wizard).getAllByRole("checkbox");
    await user.click(boxes[0]);
    expect(within(wizard).getByRole("button", { name: "生成格式化计划" })).toBeTruthy();
    await user.click(boxes[1]);
    await user.click(within(wizard).getByRole("button", { name: "下一步" }));
    expect(within(wizard).getAllByText("477 GiB").length).toBeGreaterThan(0);
    await user.click(within(wizard).getByText("扩容"));
    expect((within(wizard).getByRole("radio", { name: /扩容/ }) as HTMLInputElement).checked).toBe(true);
    await user.click(within(wizard).getByRole("button", { name: "生成格式化计划" }));
    const created = fetchMock.mock.calls.find(([path, init]) => path === "/api/v1/storage/plans" && init?.method === "POST");
    expect(JSON.parse(String(created?.[1]?.body))).toEqual({ diskIds: ["disk:fake-data-01", "disk:fake-data-02"], layout: "span" });
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

  it("checks a new member's name and password against the NAS rules before creating it", async () => {
    const fetchMock = installAPI({ users: [session.user] });
    const user = userEvent.setup();
    render(<App />);
    await openSettingsSection(user, await screen.findByRole("region", { name: "桌面应用" }), "用户与权限");
    const settings = screen.getByRole("dialog", { name: "设置" });
    await user.click(await within(settings).findByRole("button", { name: "添加成员" }));
    const form = within(settings).getByRole("form", { name: "添加成员" });
    expect(within(form).getByText(/3–32 位英文字母/)).toBeTruthy();
    const created = () => fetchMock.mock.calls.filter(([path, init]) => path === "/api/v1/users" && init?.method === "POST");

    await user.type(within(form).getByLabelText("账号"), "Shared");
    await user.type(within(form).getByLabelText("初始密码"), "carol password 1");
    await user.click(within(form).getByRole("button", { name: "创建成员" }));
    expect(await within(form).findByText("“shared”留给系统使用，请换一个账号名")).toBeTruthy();
    expect(created()).toHaveLength(0);

    await user.clear(within(form).getByLabelText("账号"));
    await user.type(within(form).getByLabelText("账号"), "Carol");
    await user.clear(within(form).getByLabelText("初始密码"));
    await user.type(within(form).getByLabelText("初始密码"), "x".repeat(65));
    await user.click(within(form).getByRole("button", { name: "创建成员" }));
    expect(await within(form).findByText("密码最多 64 个字符")).toBeTruthy();
    expect(created()).toHaveLength(0);

    await user.clear(within(form).getByLabelText("初始密码"));
    await user.type(within(form).getByLabelText("初始密码"), "carol password 1");
    await user.click(within(form).getByRole("button", { name: "创建成员" }));
    expect(await within(settings).findByText("已添加成员 carol。")).toBeTruthy();
    expect(JSON.parse(String(created()[0]?.[1]?.body))).toEqual({ username: "Carol", password: "carol password 1" });
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

describe("Avatars and nicknames", () => {
  const alice = { id: "user:alice", username: "alice", role: "member", status: "active", createdAt: "2026-10-07T10:00:00Z" };

  // jsdom neither decodes images nor draws on a canvas.
  function stubPictures() {
    class DecodedImage {
      naturalWidth = 400; naturalHeight = 320; decoding = ""; onload?: () => void; onerror?: () => void; private source = "";
      get src() { return this.source; }
      set src(value: string) { this.source = value; queueMicrotask(() => (value.includes("broken") ? this.onerror?.() : this.onload?.())); }
    }
    vi.stubGlobal("Image", DecodedImage);
    vi.stubGlobal("URL", Object.assign(URL, { createObjectURL: vi.fn(() => "blob:avatar-source"), revokeObjectURL: vi.fn() }));
    const drawImage = vi.fn();
    vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue({ fillRect: vi.fn(), drawImage } as never);
    vi.spyOn(HTMLCanvasElement.prototype, "toBlob").mockImplementation((callback) => callback(new Blob(["jpeg"], { type: "image/jpeg" })));
    return drawImage;
  }

  it("lets a member choose a nickname that the desktop shows instead of the account name", async () => {
    const fetchMock = installAPI({ session: { ...session, user: alice } });
    const user = userEvent.setup();
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    await openSettingsSection(user, desktop, "我的账号");
    const settings = screen.getByRole("dialog", { name: "设置" });
    expect(within(settings).getByText("未设置，显示账号名 alice")).toBeTruthy();
    await user.click(within(settings).getByRole("button", { name: "设置昵称…" }));
    const form = within(settings).getByRole("form", { name: "修改昵称" });
    await user.type(within(form).getByLabelText("昵称"), "一二三四五六七八九十一二三四五六七八九十一二三四五");
    await user.click(within(form).getByRole("button", { name: "保存昵称" }));
    expect(within(form).getByText("昵称最多 24 个字符")).toBeTruthy();
    expect(fetchMock.mock.calls.some(([path]) => path === "/api/v1/session/profile")).toBe(false);

    await user.clear(within(form).getByLabelText("昵称"));
    await user.type(within(form).getByLabelText("昵称"), "owner");
    await user.click(within(form).getByRole("button", { name: "保存昵称" }));
    expect(await within(form).findByText("这个名字已经是其他人的账号名或昵称，请换一个")).toBeTruthy();

    await user.clear(within(form).getByLabelText("昵称"));
    await user.type(within(form).getByLabelText("昵称"), " 小爱 ");
    await user.click(within(form).getByRole("button", { name: "保存昵称" }));
    expect(await within(settings).findByText("昵称已改为“小爱”。")).toBeTruthy();
    const call = fetchMock.mock.calls.filter(([path]) => path === "/api/v1/session/profile").at(-1);
    expect(call?.[1]?.method).toBe("PATCH");
    expect(JSON.parse(String(call?.[1]?.body))).toEqual({ nickname: "小爱" });
    expect(within(settings).getByRole("heading", { name: "小爱" })).toBeTruthy();
    expect(within(settings).getByText("账号名 alice")).toBeTruthy();
    expect(within(within(settings).getByRole("navigation", { name: "设置分区" })).getByRole("button", { name: "我的账号" }).textContent).toContain("小爱");
    expect(screen.getByRole("button", { name: "账号（小爱）" })).toBeTruthy();
  });

  it("crops a picture from this computer into the avatar and can remove it again", async () => {
    const drawImage = stubPictures();
    const fetchMock = installAPI({ session: { ...session, user: alice } });
    const user = userEvent.setup();
    render(<App />);
    await openSettingsSection(user, await screen.findByRole("region", { name: "桌面应用" }), "我的账号");
    const settings = screen.getByRole("dialog", { name: "设置" });
    await user.click(within(settings).getByRole("button", { name: "更换头像" }));
    const picker = within(settings).getByRole("dialog", { name: "更换头像" });
    expect(within(picker).getByRole("tab", { name: "本机图片" }).getAttribute("aria-selected")).toBe("true");
    await user.upload(within(picker).getByLabelText("从本机选择图片"), new File(["png"], "me.png", { type: "image/png" }));

    expect((await within(picker).findByTestId("cropper")).getAttribute("data-image")).toBe("blob:avatar-source");
    expect(within(picker).getByRole("heading", { name: "调整头像" })).toBeTruthy();
    await user.click(within(picker).getByRole("button", { name: "保存头像" }));
    expect(await within(settings).findByText("头像已更新。")).toBeTruthy();
    expect(within(settings).queryByRole("dialog", { name: "更换头像" })).toBeNull();

    // The page sends the chosen square, drawn on white at no more than 512 px.
    expect(drawImage).toHaveBeenCalledWith(expect.anything(), 40, 0, 320, 320, 0, 0, 320, 320);
    const sent = fetchMock.mock.calls.find(([path, init]) => path === "/api/v1/session/avatar" && init?.method === "PUT");
    expect((sent?.[1]?.body as Blob).type).toBe("image/jpeg");
    expect(new Headers(sent?.[1]?.headers).get("X-CSRF-Token")).toBe("csrf-test");
    const rail = screen.getByRole("button", { name: "账号（alice）" });
    expect(rail.querySelector("img")?.getAttribute("src")).toBe("/api/v1/users/user%3Aalice/avatar?v=0123456789abcdef");

    await user.click(within(settings).getByRole("button", { name: "移除" }));
    expect(await within(settings).findByText("已移除头像，现在显示名字的第一个字。")).toBeTruthy();
    expect(fetchMock.mock.calls.some(([path, init]) => path === "/api/v1/session/avatar" && init?.method === "DELETE")).toBe(true);
    expect(rail.querySelector("img")).toBeNull();
    expect(rail.textContent).toContain("A");
  });

  it("picks the avatar from a picture on the NAS, but not from a space being viewed", async () => {
    stubPictures();
    installAPI({
      session: { ...session, user: alice },
      spaces: [
        { id: "space:alice", kind: "private", name: "alice", ownerUserId: "user:alice", createdAt: "2026-10-07T10:00:00Z" },
        { id: "space:shared", kind: "shared", name: "Shared", createdAt: "2026-10-07T10:00:00Z" },
        { id: "space:bob", kind: "private", name: "bob", ownerUserId: "user:bob", createdAt: "2026-10-07T10:00:00Z", viewing: { grantId: "grant:1", expiresAt: "2026-10-08T10:00:00Z" } },
      ],
      entries: [
        { id: "file:trip", spaceId: "space:alice", name: "海边.jpg", kind: "file", sizeBytes: 2048, modifiedAt: "2026-10-07T10:00:00Z" },
        { id: "file:notes", spaceId: "space:alice", name: "notes.txt", kind: "file", sizeBytes: 12, modifiedAt: "2026-10-07T10:00:00Z" },
        { id: "dir:2026", spaceId: "space:alice", name: "2026", kind: "directory", sizeBytes: 0, modifiedAt: "2026-10-07T10:00:00Z" },
      ],
    });
    const user = userEvent.setup();
    render(<App />);
    await openSettingsSection(user, await screen.findByRole("region", { name: "桌面应用" }), "我的账号");
    const settings = screen.getByRole("dialog", { name: "设置" });
    await user.click(within(settings).getByRole("button", { name: "设置头像…" }));
    const picker = within(settings).getByRole("dialog", { name: "更换头像" });
    await user.click(within(picker).getByRole("tab", { name: "NAS 文件" }));
    const spaces = await within(picker).findByRole("group", { name: "空间" });
    expect(within(spaces).getAllByRole("button").map((button) => button.textContent)).toEqual(["个人空间", "共享空间"]);
    expect(within(picker).getByRole("button", { name: "2026" })).toBeTruthy();
    expect(within(picker).queryByText("notes.txt")).toBeNull();
    await user.click(within(picker).getByRole("button", { name: "选择 海边.jpg" }));
    expect((await within(picker).findByTestId("cropper")).getAttribute("data-image")).toBe("/api/v1/files/file%3Atrip/content?disposition=inline");
    await user.click(within(picker).getByRole("button", { name: "重新选择图片" }));
    expect(within(picker).getByRole("tab", { name: "NAS 文件" })).toBeTruthy();
    await user.keyboard("{Escape}");
    expect(within(settings).queryByRole("dialog", { name: "更换头像" })).toBeNull();
  });

  it("shows other accounts by nickname and keeps the account name where it matters", async () => {
    installAPI({
      users: [session.user, { ...alice, nickname: "小爱", avatarVersion: "feedface00000000" }],
      profiles: [{ id: "user:owner", username: "owner", nickname: "爸爸" }, { id: "user:alice", username: "alice", nickname: "小爱", avatarVersion: "feedface00000000" }],
      notifications: [{ id: "notification:1", kind: "credential_reset", actorUsername: "owner", createdAt: "2026-10-07T10:00:00Z" }],
    });
    const user = userEvent.setup();
    render(<App />);
    const notice = await screen.findByRole("alertdialog", { name: "账号通知" });
    expect(await within(notice).findByText(/管理员 爸爸（owner） 于 .* 重置了你的密码/)).toBeTruthy();
    await user.click(within(notice).getByRole("button", { name: "知道了" }));

    await openSettingsSection(user, await screen.findByRole("region", { name: "桌面应用" }), "用户与权限");
    const settings = screen.getByRole("dialog", { name: "设置" });
    const menu = await within(settings).findByRole("button", { name: "小爱（alice） 的更多操作" });
    const row = menu.closest(".set-row") as HTMLElement;
    expect(within(row).getByText("小爱")).toBeTruthy();
    expect(within(row).getByText("alice")).toBeTruthy();
    expect(row.querySelector("img")?.getAttribute("src")).toBe("/api/v1/users/user%3Aalice/avatar?v=feedface00000000");
    await user.click(menu);
    await user.click(within(settings).getByRole("menuitem", { name: "重置密码…" }));
    expect(within(settings).getByRole("form", { name: "重置 小爱（alice） 的密码" })).toBeTruthy();
  });
});
