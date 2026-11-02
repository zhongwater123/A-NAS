// The desktop shell: sign-in and setup, the desktop and its icons, window
// management, the dock, the system rail and launcher, and windows opening
// each other. What a window shows is tested with that window (docs/architecture/web-desktop.md).
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { delay, HttpResponse } from "msw";
import type { ComponentProps } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import App from "./App";
import type { AppShortcut, Transfer } from "./api.generated";
import { healthyState, metricsState, session as signIn } from "./test/fixtures";
import { appsHandlers } from "./test/handlers/apps";
import { containerSnapshot, dockerHandlers } from "./test/handlers/docker";
import { entry, filesHandlers, folder, ownSpace, sharedSpace, space } from "./test/handlers/files";
import { photosHandlers } from "./test/handlers/photos";
import { settingsHandlers, terminalHandlers } from "./test/handlers/settings";
import { serveDesktop, session } from "./test/handlers/shell";
import { storagePlan } from "./test/handlers/storage";
import { taskManagerHandlers } from "./test/handlers/taskmanager";
import { trashHandlers } from "./test/handlers/trash";
import { accountHandlers, alice } from "./test/handlers/users";
import { webSearchStatus } from "./test/handlers/websearch";
import { apiError, http, server } from "./test/server";
import { transferStore } from "./transfers/store";
import { resetWebSearch } from "./websearch/status";

// The trash and the transfer center count how often the desktop renders and
// mounts them, and the trash can be made to fail, for the tests of window
// isolation; otherwise they are the real windows.
const panels = vi.hoisted(() => ({ trashRenders: 0, trashMounts: 0, trashFails: false, transfersMounts: 0 }));
vi.mock("./trash/TrashPanel", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./trash/TrashPanel")>();
  const { useEffect } = await import("react");
  return {
    ...actual,
    TrashPanel(props: ComponentProps<typeof actual.TrashPanel>) {
      panels.trashRenders++;
      useEffect(() => {
        panels.trashMounts++;
      }, []);
      if (panels.trashFails) throw new Error("the trash failed");
      return <actual.TrashPanel {...props} />;
    },
  };
});
vi.mock("./transfers/TransferCenter", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./transfers/TransferCenter")>();
  const { useEffect } = await import("react");
  return {
    ...actual,
    TransferCenter(props: ComponentProps<typeof actual.TransferCenter>) {
      useEffect(() => {
        panels.transfersMounts++;
      }, []);
      return <actual.TransferCenter {...props} />;
    },
  };
});

// Uploads go through tus-js-client, whose Node build cannot read a jsdom
// File; the tests report the uploads' progress themselves.
const tus = vi.hoisted(() => {
  class FakeUpload {
    constructor(
      readonly file: File,
      readonly options: { onProgress: (sent: number, total: number) => void },
    ) {
      uploads.push(this);
    }
    url = null;
    start() {}
    abort() {
      return Promise.resolve();
    }
  }
  const uploads: FakeUpload[] = [];
  return { FakeUpload, uploads };
});
vi.mock("tus-js-client", () => ({ Upload: tus.FakeUpload }));

// Settings → 终端 runs a shell through xterm.js, which needs layout jsdom lacks.
const xterm = vi.hoisted(() => ({ mounts: 0 }));
vi.mock("./settings/XtermSession", async () => {
  const { useEffect, useEffectEvent } = await import("react");
  return {
    default: function MockXtermSession({ onStatus }: { onStatus: (status: { state: "connected" }) => void }) {
      const mounted = useEffectEvent(() => {
        xterm.mounts += 1;
        onStatus({ state: "connected" });
      });
      useEffect(() => mounted(), []);
      return <div data-testid="xterm-session" />;
    },
  };
});

// The avatar cropper needs layout jsdom lacks; this one reports a fixed square.
vi.mock("react-easy-crop", async () => {
  const { useEffect, useEffectEvent } = await import("react");
  return {
    default: function MockCropper({
      image,
      onCropComplete,
    }: {
      image: string;
      onCropComplete: (area: unknown, pixels: unknown) => void;
    }) {
      const complete = useEffectEvent(onCropComplete);
      useEffect(() => {
        complete({ x: 10, y: 0, width: 80, height: 100 }, { x: 40, y: 0, width: 320, height: 320 });
      }, [image]);
      return <div data-testid="cropper" data-image={image} />;
    },
  };
});

// The windows the shell's tests open read their own lists; empty ones do here.
function serveWindows() {
  server.use(
    ...filesHandlers({ spaces: [ownSpace] }),
    ...trashHandlers(() => []).handlers,
    ...taskManagerHandlers(),
    ...settingsHandlers().handlers,
    ...photosHandlers(),
  );
}

// A signed-in route answers 401 once the session has ended. The contract
// leaves 401 out of most signed-in routes, so the answer is untyped.
const sessionExpired = () =>
  HttpResponse.json(apiError("authentication_required", "authentication is required"), { status: 401 });

beforeEach(() => {
  localStorage.clear();
  Object.assign(panels, { trashRenders: 0, trashMounts: 0, trashFails: false, transfersMounts: 0 });
  xterm.mounts = 0;
});

afterEach(() => {
  transferStore.reset();
  resetWebSearch();
  tus.uploads.length = 0;
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("A-NAS v1.0.1 desktop", () => {
  it("sends a LAN browser to the device's own screen to set it up", async () => {
    // /api/v1/setup/admin is not served: a request to it fails the test.
    serveDesktop({ setup: { setupRequired: true, setupAllowed: false } });
    render(<App />);
    expect(await screen.findByRole("heading", { name: "请在 NAS 本机启用" })).toBeTruthy();
    expect(screen.getByText(/SSH 隧道/)).toBeTruthy();
    expect(screen.queryByLabelText("账号")).toBeNull();
  });

  it("activates the local device with only an administrator account and password", async () => {
    const setups: unknown[] = [];
    serveDesktop({ setup: { setupRequired: true } });
    server.use(
      http.post("/api/v1/setup/admin", async ({ request, response }) => {
        setups.push(await request.json());
        return response(201).json(session);
      }),
    );
    const user = userEvent.setup();
    render(<App />);
    expect(await screen.findByRole("heading", { name: "启用 A-NAS" })).toBeTruthy();
    expect(screen.queryByLabelText("一次性初始化码")).toBeNull();
    await user.type(screen.getByLabelText("账号"), "owner");
    await user.type(screen.getByLabelText("密码"), "correct horse battery staple");
    await user.click(screen.getByRole("button", { name: "创建管理员并启用" }));
    expect(await screen.findByRole("region", { name: "桌面应用" })).toBeTruthy();
    expect(setups).toEqual([{ username: "owner", password: "correct horse battery staple" }]);
  });

  it("asks for a session that lasts until sign-out on the local console", async () => {
    window.history.replaceState(null, "", "/?local-console=1");
    try {
      const setups: unknown[] = [];
      serveDesktop({ setup: { setupRequired: true } });
      server.use(
        http.post("/api/v1/setup/admin", async ({ request, response }) => {
          setups.push(await request.json());
          return response(201).json(session);
        }),
        http.get("/api/v1/local-display", ({ response }) => response(200).json({ sleepAfterMinutes: 30 })),
        // The screen saver's videos are static files beside the page, outside the product API.
        http.untyped.get(`${window.location.origin}/local-console/screensavers`, () =>
          HttpResponse.json({ videos: [] }),
        ),
      );
      const user = userEvent.setup();
      render(<App />);
      await user.type(await screen.findByLabelText("账号"), "owner");
      await user.type(screen.getByLabelText("密码"), "correct horse battery staple");
      await user.click(screen.getByRole("button", { name: "创建管理员并启用" }));
      expect(await screen.findByRole("region", { name: "桌面应用" })).toBeTruthy();
      expect(setups).toEqual([{ username: "owner", password: "correct horse battery staple", localConsole: true }]);
    } finally {
      window.history.replaceState(null, "", "/");
    }
  });

  it("shows authenticated host health and keeps the clock advancing", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.setSystemTime(new Date("2026-10-07T10:00:00Z"));
    serveDesktop();
    serveWindows();
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    await user.click(within(desktop).getByRole("button", { name: "打开设置" }));
    expect(await screen.findByText("anas-fake")).toBeTruthy();
    const statusBar = screen.getByRole("banner", { name: "设备状态" });
    const clock = () => within(statusBar).getByText(/^\d{2}:\d{2}$/).textContent;
    const before = clock();
    act(() => vi.advanceTimersByTime(60_000));
    expect(clock()).not.toBe(before);
  });

  it("enables photos, trash, the backup center, and one settings entry", async () => {
    serveDesktop();
    serveWindows();
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

  it("opens, minimizes, and restores settings", async () => {
    serveDesktop();
    serveWindows();
    const user = userEvent.setup();
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    await user.click(within(desktop).getByRole("button", { name: "打开设置" }));
    const settings = screen.getByRole("dialog", { name: "设置" });
    await user.click(within(settings).getByRole("button", { name: "最小化设置" }));
    await user.click(screen.getByRole("button", { name: "恢复设置" }));
    expect(screen.getByRole("dialog", { name: "设置" })).toBeTruthy();
  });

  it("maximizes a window above the dock and restores it", async () => {
    serveDesktop();
    serveWindows();
    const user = userEvent.setup();
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    await user.click(within(desktop).getByRole("button", { name: "打开回收站" }));
    const trash = screen.getByRole("dialog", { name: "回收站" });
    const placed = { left: trash.style.left, top: trash.style.top, width: trash.style.width };
    await user.click(within(trash).getByRole("button", { name: "最大化回收站" }));
    // A maximized window ends above the dock, which floats over the desktop's bottom.
    expect(trash.style.bottom).toBe("90px");
    expect(trash.style.width).toBe("");
    await user.click(within(trash).getByRole("button", { name: "还原回收站" }));
    expect({ left: trash.style.left, top: trash.style.top, width: trash.style.width }).toEqual(placed);
  });

  it("shows utilisation gauges, network rates and the clock in the status bar, and opens transfers from the rates", async () => {
    serveDesktop();
    serveWindows();
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

  it("opens the task manager from the utilisation gauges and brings settings in front on another section", async () => {
    serveDesktop();
    serveWindows();
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
    expect(await within(settings).findByRole("img", { name: /CPU 使用率，最近/ })).toBeTruthy();

    await user.click(section("概览"));
    expect(within(settings).queryByRole("heading", { name: "任务管理器" })).toBeNull();
    await user.click(
      within(screen.getByRole("region", { name: "桌面应用" })).getByRole("button", { name: "打开回收站" }),
    );
    const dock = screen.getByRole("navigation", { name: "已打开窗口" });
    expect(within(dock).getByRole("button", { name: "最小化回收站" }).getAttribute("aria-current")).toBe("true");

    await user.click(gauges);
    expect(within(dock).getByRole("button", { name: "最小化设置" }).getAttribute("aria-current")).toBe("true");
    expect(section("任务管理器").getAttribute("aria-current")).toBe("page");
    expect(await within(settings).findByRole("img", { name: /CPU 使用率，最近/ })).toBeTruthy();
  });

  it("restores minimized settings on the task manager when the gauges are activated from the keyboard", async () => {
    serveDesktop();
    serveWindows();
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
    serveDesktop();
    serveWindows();
    server.use(http.get("/api/v1/users", ({ response }) => response(200).json({ items: [session.user] })));
    const user = userEvent.setup();
    render(<App />);
    const openAccount = async () => {
      await user.click(await screen.findByRole("button", { name: "账号（owner）" }));
      await user.click(screen.getByRole("menuitem", { name: /^我的账号/ }));
      return screen.getByRole("dialog", { name: "设置" });
    };
    let settings = await openAccount();
    await user.click(await within(settings).findByRole("button", { name: "修改密码…" }));
    await user.type(within(settings).getByLabelText("当前密码"), "correct horse battery staple");

    const statusBar = screen.getByRole("banner", { name: "设备状态" });
    await user.click(within(statusBar).getByRole("button", { name: "CPU 与内存占用，打开任务管理器" }));
    expect(within(settings).getByRole("heading", { name: "任务管理器" })).toBeTruthy();

    settings = await openAccount();
    expect(within(settings).queryByRole("form", { name: "修改密码" })).toBeNull();
    expect(within(settings).getByRole("button", { name: "修改密码…" })).toBeTruthy();
  });

  it("marks the status bar disconnected when metrics cannot be read", async () => {
    serveDesktop();
    server.use(
      http.get("/api/v1/metrics", ({ response }) =>
        response(503).json(apiError("state_unavailable", "host state is unavailable")),
      ),
    );
    render(<App />);

    const statusBar = await screen.findByRole("banner", { name: "设备状态" });
    // The connection dot and the source badge both say so.
    expect(await within(statusBar).findAllByText("连接中断")).not.toHaveLength(0);
    expect(within(statusBar).queryByText("设备在线")).toBeNull();
    expect(within(statusBar).getByRole("meter", { name: "CPU 占用" }).getAttribute("aria-valuetext")).toBe("暂无数据");
  });

  it("returns to the login screen when the session expires instead of reporting a lost connection", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let expired = false;
    serveDesktop();
    server.use(
      http.get("/api/v1/host-state", ({ response }) =>
        expired ? response.untyped(sessionExpired()) : response(200).json(healthyState),
      ),
      http.get("/api/v1/metrics", ({ response }) =>
        expired ? response.untyped(sessionExpired()) : response(200).json(metricsState),
      ),
      http.post("/api/v1/session", ({ response }) => response(200).json(session)),
    );
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    render(<App />);
    await screen.findByRole("region", { name: "桌面应用" });

    expired = true;
    await act(async () => {
      vi.advanceTimersByTime(2_000);
    });
    expect(await screen.findByRole("heading", { name: "欢迎回来" })).toBeTruthy();
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
    serveDesktop();
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    layOutGrid(desktop);
    expect(desktopOrder(desktop).slice(0, 5)).toEqual(["文件管理", "回收站", "设置", "应用中心", "影视中心"]);

    const store = within(desktop).getByRole("button", { name: "打开应用中心" }).parentElement!;
    fireEvent.pointerDown(store, { button: 0, pointerId: 1, pointerType: "mouse", clientX: 50, clientY: 160 });
    fireEvent.pointerMove(window, { pointerId: 1, clientX: 40, clientY: 140 });
    // The icon follows the pointer as a copy outside the grid.
    const preview = () => screen.queryAllByText("应用中心").filter((label) => !desktop.contains(label));
    expect(preview()).toHaveLength(1);
    expect(preview()[0].closest('[aria-hidden="true"]')).toBeTruthy();
    fireEvent.pointerMove(window, { pointerId: 1, clientX: 40, clientY: 50 });
    fireEvent.pointerUp(window, { pointerId: 1, clientX: 40, clientY: 50 });
    expect(preview()).toHaveLength(0);
    fireEvent.click(within(desktop).getByRole("button", { name: "打开应用中心" }));

    expect(desktopOrder(desktop).slice(0, 5)).toEqual(["应用中心", "文件管理", "回收站", "设置", "影视中心"]);
    expect(JSON.parse(localStorage.getItem("a-nas.desktop-order.v1")!).slice(0, 2)).toEqual(["store", "files"]);
    expect(screen.queryByRole("dialog", { name: "应用中心" })).toBeNull();
    expect(screen.getByText("已将应用中心移动到第 1 位")).toBeTruthy();
  });

  it("restores the original order when a drag is cancelled with Escape", async () => {
    serveDesktop();
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
    serveDesktop();
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
    serveDesktop();
    serveWindows();
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
    serveDesktop();
    serveWindows();
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
    serveDesktop();
    serveWindows();
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
    serveDesktop();
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
    serveDesktop();
    serveWindows();
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
    serveDesktop();
    serveWindows();
    const user = userEvent.setup();
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    const wallpaper = desktop.closest("main")!;
    const rail = screen.getByRole("complementary", { name: "系统快捷栏" });
    const showDesktop = within(rail).getByRole("button", { name: "显示桌面" });
    const openWindows = () => screen.queryAllByRole("dialog").map((dialog) => dialog.getAttribute("aria-label"));
    await user.click(within(desktop).getByRole("button", { name: "打开设置" }));
    await user.click(within(desktop).getByRole("button", { name: "打开回收站" }));

    // A press inside a window keeps the windows.
    await user.click(screen.getByRole("dialog", { name: "设置" }));
    expect(openWindows()).toEqual(["回收站", "设置"]);
    // So does a window drag released over the wallpaper, and a press on the wallpaper that moves away.
    await user.pointer([
      { keys: "[MouseLeft>]", target: titleOf("回收站"), coords: { clientX: 400, clientY: 90 } },
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

  it("shows web search only while an administrator leaves it on", async () => {
    serveDesktop({ webSearch: webSearchStatus(true) });
    serveWindows();
    const { unmount } = render(<App />);
    expect((await screen.findAllByRole("button", { name: "打开网页搜索" })).length).toBeGreaterThan(0);
    unmount();
    resetWebSearch();

    serveDesktop({ webSearch: webSearchStatus(false) });
    render(<App />);
    await screen.findAllByRole("button", { name: "打开相册" });
    expect(screen.queryByRole("button", { name: "打开网页搜索" })).not.toBeInTheDocument();
  });

  it("finds an app in the launcher by what it does and opens it with Enter", async () => {
    serveDesktop();
    serveWindows();
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
    serveDesktop();
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
    const signOuts = vi.fn();
    serveDesktop();
    server.use(
      http.delete("/api/v1/session", ({ response }) => {
        signOuts();
        return response(204).empty();
      }),
    );
    const user = userEvent.setup();
    render(<App />);
    await user.click(await screen.findByRole("button", { name: "账号（owner）" }));
    const account = screen.getByRole("region", { name: "账号" });
    expect(account.textContent).toContain("管理员");
    expect(account.textContent).toContain("浏览器登录 · 有效至");
    expect(document.activeElement).toBe(within(account).getByRole("menuitem", { name: /^我的账号/ }));
    await user.keyboard("{ArrowDown}");
    expect(document.activeElement).toBe(within(account).getByRole("menuitem", { name: "退出登录" }));
    expect(signOuts).not.toHaveBeenCalled();
    await user.keyboard("{Enter}");
    expect(await screen.findByRole("button", { name: "登录" })).toBeTruthy();
    expect(signOuts).toHaveBeenCalledOnce();
  });
});

describe("Windows stay apart", () => {
  it("re-renders no other window's content while one is dragged or another is focused", async () => {
    serveDesktop();
    serveWindows();
    const user = userEvent.setup();
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    await user.click(within(desktop).getByRole("button", { name: "打开回收站" }));
    const trash = screen.getByRole("dialog", { name: "回收站" });
    expect(await within(trash).findByText("回收站是空的")).toBeTruthy();
    await user.click(within(desktop).getByRole("button", { name: "打开传输中心" }));
    const transfers = screen.getByRole("dialog", { name: "传输中心" });
    await within(transfers).findByRole("heading", { name: "正在传输" });
    const rendered = panels.trashRenders;

    // Dragging the transfer center moves only its own window.
    const before = transfers.style.left;
    await user.pointer([
      { keys: "[MouseLeft>]", target: titleOf("传输中心"), coords: { clientX: 500, clientY: 100 } },
      { coords: { clientX: 540, clientY: 120 } },
      { coords: { clientX: 580, clientY: 140 } },
      { keys: "[/MouseLeft]" },
    ]);
    expect(transfers.style.left).not.toBe(before);
    // Bringing one window or the other to the front does not render either's content again.
    await user.click(trash);
    await user.click(transfers);
    expect(panels.trashRenders).toBe(rendered);
  });

  it("shows a failed window's error in that window alone and reopens only it", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    serveDesktop();
    serveWindows();
    const user = userEvent.setup();
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    await user.click(within(desktop).getByRole("button", { name: "打开传输中心" }));
    const transfers = screen.getByRole("dialog", { name: "传输中心" });
    await within(transfers).findByRole("heading", { name: "正在传输" });

    panels.trashFails = true;
    await user.click(within(desktop).getByRole("button", { name: "打开回收站" }));
    const trash = screen.getByRole("dialog", { name: "回收站" });
    expect(await within(trash).findByRole("heading", { name: "这个窗口暂时无法显示" })).toBeTruthy();
    // The other window and the rest of the desktop keep working.
    expect(within(transfers).getByRole("heading", { name: "正在传输" })).toBeTruthy();
    await user.click(within(desktop).getByRole("button", { name: "打开设置" }));
    expect(await screen.findByRole("dialog", { name: "设置" })).toBeTruthy();

    panels.trashFails = false;
    const mounted = { trash: panels.trashMounts, transfers: panels.transfersMounts };
    await user.click(within(trash).getByRole("button", { name: "重新打开" }));
    expect(await within(trash).findByText("回收站是空的")).toBeTruthy();
    expect(panels.trashMounts).toBe(mounted.trash + 1);
    expect(panels.transfersMounts).toBe(mounted.transfers);
  });

  it("shows the folder another window asks for each time it asks, even the same one twice", async () => {
    const photos = folder("Photos", { spaceId: ownSpace.id });
    const uploaded: Transfer = {
      id: "upload-1",
      direction: "upload",
      kind: "file",
      name: "beach.jpg",
      location: { spaceId: ownSpace.id, spaceKind: "private", folders: [{ id: photos.id, name: photos.name }] },
      totalBytes: 12,
      transferredBytes: 12,
      bytesPerSecond: 0,
      state: "completed",
      createdAt: "2026-10-12T10:00:00Z",
      updatedAt: "2026-10-12T10:00:00Z",
      finishedAt: "2026-10-12T10:00:00Z",
    };
    let listed = false;
    serveDesktop();
    serveWindows();
    server.use(
      ...filesHandlers({
        spaces: [ownSpace],
        entries: [photos, entry("beach.jpg", { spaceId: ownSpace.id, parentId: photos.id })],
      }),
      http.get("/api/v1/transfers", async ({ response }) => {
        // Later polls never answer, so the transfer center hands the desktop
        // the very same location object both times.
        if (listed) await delay("infinite");
        listed = true;
        return response(200).json({ items: [uploaded] });
      }),
    );
    const user = userEvent.setup();
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    await user.click(within(desktop).getByRole("button", { name: "打开传输中心" }));
    const transfers = screen.getByRole("dialog", { name: "传输中心" });
    await user.click(within(transfers).getByRole("button", { name: /^传输记录/ }));
    const showPhotos = () => within(transfers).findByRole("button", { name: /Photos/ });

    await user.click(await showPhotos());
    const files = await screen.findByRole("dialog", { name: "文件管理" });
    expect(await within(files).findByText("beach.jpg")).toBeTruthy();

    // Leave the folder, then ask for it again from the transfer center.
    await user.click(within(files).getByRole("button", { name: "向上一级" }));
    await waitFor(() => expect(within(files).queryByText("beach.jpg")).toBeNull());
    await user.click(await showPhotos());
    expect(await within(files).findByText("beach.jpg")).toBeTruthy();
  });
});

// openSettingsOn opens Settings from its desktop icon on section.
async function openSettingsOn(user: ReturnType<typeof userEvent.setup>, section: string) {
  const desktop = await screen.findByRole("region", { name: "桌面应用" });
  await user.click(within(desktop).getByRole("button", { name: "打开设置" }));
  // The settings window loads its code when first opened.
  const sections = await within(screen.getByRole("dialog", { name: "设置" })).findByRole("navigation", {
    name: "设置分区",
  });
  await user.click(within(sections).getByRole("button", { name: section }));
  return screen.getByRole("dialog", { name: "设置" });
}

describe("Settings on the desktop", () => {
  it("keeps the shell while Settings is minimized and ends it when Settings closes", async () => {
    serveDesktop();
    server.use(...settingsHandlers().handlers, ...terminalHandlers(true).handlers);
    const user = userEvent.setup();
    render(<App />);
    await openSettingsOn(user, "终端");
    await screen.findByTestId("xterm-session");

    await user.click(within(screen.getByRole("dialog", { name: "设置" })).getByRole("button", { name: "最小化设置" }));
    expect(screen.queryByRole("dialog", { name: "设置" })).toBeNull();
    await user.click(screen.getByRole("button", { name: "恢复设置" }));
    expect(screen.getByTestId("xterm-session")).toBeTruthy();
    expect(xterm.mounts).toBe(1);

    await user.click(within(screen.getByRole("dialog", { name: "设置" })).getByRole("button", { name: "关闭设置" }));
    expect(screen.queryByTestId("xterm-session")).toBeNull();
    // Settings reopens on the terminal with a new shell.
    await user.click(
      within(screen.getByRole("region", { name: "桌面应用" })).getByRole("button", { name: "打开设置" }),
    );
    expect(await screen.findByTestId("xterm-session")).toBeTruthy();
    expect(xterm.mounts).toBe(2);
  });

  it("keeps a pending storage plan across sections and when settings closes", async () => {
    serveDesktop();
    server.use(...settingsHandlers({ plan: storagePlan("plan:pending") }).handlers);
    const user = userEvent.setup();
    render(<App />);
    let settings = await openSettingsOn(user, "存储");
    await user.click(await within(settings).findByRole("button", { name: "初始化数据卷…" }));
    await user.click(within(settings).getByRole("button", { name: "生成格式化计划" }));
    await user.type(await within(settings).findByLabelText(/输入确认短语/), "ERASE");

    await user.click(within(settings).getByRole("button", { name: "概览" }));
    expect(within(settings).queryByRole("heading", { name: "破坏性操作计划" })).toBeNull();
    await user.click(within(settings).getByRole("button", { name: "关闭设置" }));
    await user.click(
      within(screen.getByRole("region", { name: "桌面应用" })).getByRole("button", { name: "打开设置" }),
    );
    settings = screen.getByRole("dialog", { name: "设置" });
    expect((await within(settings).findByRole("button", { name: "概览" })).getAttribute("aria-current")).toBe("page");

    await user.click(within(settings).getByRole("button", { name: "存储" }));
    expect(within(settings).getByRole("heading", { name: "破坏性操作计划" })).toBeTruthy();
    expect((within(settings).getByLabelText(/输入确认短语/) as HTMLInputElement).value).toBe("ERASE");
  });

  it("opens My account in Settings from the account menu", async () => {
    serveDesktop();
    server.use(...settingsHandlers().handlers);
    const user = userEvent.setup();
    render(<App />);
    await user.click(await screen.findByRole("button", { name: "账号（owner）" }));
    await user.click(screen.getByRole("menuitem", { name: /^我的账号/ }));
    const settings = screen.getByRole("dialog", { name: "设置" });
    expect((await within(settings).findByRole("button", { name: "我的账号" })).getAttribute("aria-current")).toBe(
      "page",
    );
    expect(within(settings).getByRole("button", { name: "修改密码…" })).toBeTruthy();
  });

  it("lets a member choose a nickname that the desktop shows instead of the account name", async () => {
    serveDesktop({ session: signIn(alice) });
    server.use(...settingsHandlers().handlers, ...accountHandlers(alice).handlers);
    const user = userEvent.setup();
    render(<App />);
    expect(await screen.findByRole("button", { name: "账号（alice）" })).toBeTruthy();
    const settings = await openSettingsOn(user, "我的账号");
    await user.click(within(settings).getByRole("button", { name: "设置昵称…" }));
    const form = within(settings).getByRole("form", { name: "修改昵称" });
    await user.type(within(form).getByLabelText("昵称"), "小爱");
    await user.click(within(form).getByRole("button", { name: "保存昵称" }));
    expect(await within(settings).findByText("昵称已改为“小爱”。")).toBeTruthy();
    expect(screen.getByRole("button", { name: "账号（小爱）" })).toBeTruthy();
  });

  it("shows the avatar chosen in Settings on the account button, and the initial once it is removed", async () => {
    // jsdom neither decodes images nor draws on a canvas.
    vi.stubGlobal(
      "Image",
      class {
        naturalWidth = 400;
        naturalHeight = 320;
        decoding = "";
        onload?: () => void;
        set src(_: string) {
          queueMicrotask(() => this.onload?.());
        }
      },
    );
    vi.stubGlobal(
      "URL",
      Object.assign(URL, { createObjectURL: vi.fn(() => "blob:avatar-source"), revokeObjectURL: vi.fn() }),
    );
    vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue({
      fillRect: vi.fn(),
      drawImage: vi.fn(),
    } as never);
    vi.spyOn(HTMLCanvasElement.prototype, "toBlob").mockImplementation((callback) =>
      callback(new Blob(["jpeg"], { type: "image/jpeg" })),
    );
    serveDesktop({ session: signIn(alice) });
    server.use(...settingsHandlers().handlers, ...accountHandlers(alice).handlers);
    const user = userEvent.setup();
    render(<App />);
    const settings = await openSettingsOn(user, "我的账号");
    await user.click(within(settings).getByRole("button", { name: "更换头像" }));
    const picker = within(settings).getByRole("dialog", { name: "更换头像" });
    await user.upload(
      within(picker).getByLabelText("从本机选择图片"),
      new File(["png"], "me.png", { type: "image/png" }),
    );
    await within(picker).findByTestId("cropper");
    await user.click(within(picker).getByRole("button", { name: "保存头像" }));
    expect(await within(settings).findByText("头像已更新。")).toBeTruthy();
    const account = screen.getByRole("button", { name: "账号（alice）" });
    expect(account.querySelector("img")?.getAttribute("src")).toBe(
      "/api/v1/users/user%3Aalice/avatar?v=0123456789abcdef",
    );

    await user.click(within(settings).getByRole("button", { name: "移除" }));
    expect(await within(settings).findByText("已移除头像，现在显示文字头像。")).toBeTruthy();
    expect(account.querySelector("img")).toBeNull();
    expect(account.textContent).toContain("A");
  });

  it("names the administrator who reset the password by nickname in the account notice", async () => {
    serveDesktop({
      profiles: [
        { id: "user:owner", username: "owner", nickname: "爸爸" },
        { id: "user:alice", username: "alice", nickname: "小爱", avatarVersion: "feedface00000000" },
      ],
      notifications: [
        { id: "notification:1", kind: "credential_reset", actorUsername: "owner", createdAt: "2026-10-07T10:00:00Z" },
      ],
    });
    const acknowledged: string[] = [];
    server.use(
      http.post("/api/v1/notifications/{notificationId}/acknowledge", ({ params, response }) => {
        acknowledged.push(params.notificationId);
        return response(204).empty();
      }),
    );
    const user = userEvent.setup();
    render(<App />);
    const notice = await screen.findByRole("alertdialog", { name: "账号通知" });
    expect(await within(notice).findByText(/管理员 爸爸（owner） 于 .* 重置了你的密码/)).toBeTruthy();
    await user.click(within(notice).getByRole("button", { name: "知道了" }));
    await waitFor(() => expect(screen.queryByRole("alertdialog", { name: "账号通知" })).toBeNull());
    expect(acknowledged).toEqual(["notification:1"]);
  });
});

describe("Windows opening each other from the file manager", () => {
  it("opens sharing from the rail instead of the desktop and shares read-only files", async () => {
    serveDesktop();
    server.use(
      ...filesHandlers({
        spaces: [space("shared", { writable: false })],
        entries: [
          entry("分享计划.txt", {
            id: "shared-file",
            spaceId: "shared",
            sizeBytes: 4,
            modifiedAt: "2026-10-07T10:00:00Z",
          }),
        ],
      }),
      http.get("/api/v1/shares/members", ({ response }) => response(200).json({ items: [] })),
    );
    const user = userEvent.setup();
    render(<App />);
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

  it("enables file management and opens the photo library from its 图库 row", async () => {
    serveDesktop();
    server.use(...filesHandlers({ spaces: [ownSpace] }), ...photosHandlers());
    const user = userEvent.setup();
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    const fileButton = within(desktop).getByRole("button", { name: "打开文件管理" });
    expect(fileButton.hasAttribute("disabled")).toBe(false);
    await user.click(fileButton);
    const dialog = await screen.findByRole("dialog", { name: "文件管理" });
    await user.dblClick(await within(dialog).findByRole("row", { name: /图库/ }));
    expect(await screen.findByRole("dialog", { name: "相册" })).toBeTruthy();
  });

  it("follows the file manager's uploads in the status bar, which opens the transfer center", async () => {
    serveDesktop();
    server.use(
      ...filesHandlers({ spaces: [sharedSpace], entries: [entry("old.txt", { id: "file:old", sizeBytes: 3 })] }),
    );
    const user = userEvent.setup();
    render(<App />);
    await user.click(await screen.findByRole("button", { name: "打开文件管理" }));
    const dialog = await screen.findByRole("dialog", { name: "文件管理" });
    await within(dialog).findByText("old.txt");
    await user.upload(within(dialog).getByLabelText("选择要上传的文件"), [
      new File(["hello"], "a.txt"),
      new File(["world!"], "b.txt"),
    ]);
    tus.uploads[0].options.onProgress(5, 5);
    await user.click(await screen.findByRole("button", { name: /传输中心：正在上传 .+ 等 2 个任务/ }));
    const center = await screen.findByRole("dialog", { name: "传输中心" });
    expect(await within(center).findByRole("heading", { name: "正在传输" })).toBeTruthy();
  });
});

function desktopOrder(desktop: HTMLElement): string[] {
  return within(desktop)
    .getAllByRole("button")
    .map((button) => button.textContent!.replace("规划中", ""));
}

// titleOf returns the title in a window's title bar, where a drag starts.
function titleOf(title: string): HTMLElement {
  return within(screen.getByRole("dialog", { name: title })).getAllByText(title)[0];
}

// jsdom has no layout; give each icon's slot, the element around its button,
// the geometry of a 3-column, 100px grid.
function layOutGrid(desktop: HTMLElement) {
  const slots = () => Array.from(desktop.querySelectorAll("button"), (button) => button.parentElement!);
  const geometry = (slot: HTMLElement) => {
    const index = slots().indexOf(slot);
    return { left: (index % 3) * 100, top: Math.floor(index / 3) * 100 };
  };
  for (const slot of slots()) {
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
  }
}

describe("ADR 0008 account safeguards", () => {
  it("requires a member whose password was reset to choose a new one first", async () => {
    const changes: unknown[] = [];
    serveDesktop({
      session: { ...session, user: { ...session.user, username: "alice", role: "member", mustChangePassword: true } },
    });
    server.use(
      http.post("/api/v1/session/password", async ({ request, response }) => {
        changes.push(await request.json());
        return response(204).empty();
      }),
    );
    const user = userEvent.setup();
    render(<App />);
    expect(await screen.findByRole("heading", { name: "设置新密码" })).toBeTruthy();
    expect(screen.queryByRole("region", { name: "桌面应用" })).toBeNull();
    await user.type(screen.getByLabelText("当前密码"), "temporary password 1");
    await user.type(screen.getByLabelText("新密码"), "alice chosen password");
    await user.type(screen.getByLabelText("确认新密码"), "alice chosen password");
    await user.click(screen.getByRole("button", { name: "保存新密码" }));
    expect(await screen.findByRole("region", { name: "桌面应用" })).toBeTruthy();
    expect(changes).toEqual([{ currentPassword: "temporary password 1", newPassword: "alice chosen password" }]);
  });

  it("tells the owner when an administrator viewed the private space", async () => {
    const acknowledged: string[] = [];
    serveDesktop({
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
    server.use(
      http.post("/api/v1/notifications/{notificationId}/acknowledge", ({ params, response }) => {
        acknowledged.push(params.notificationId);
        return response(204).empty();
      }),
    );
    const user = userEvent.setup();
    render(<App />);
    const notice = await screen.findByRole("alertdialog", { name: "账号通知" });
    expect(within(notice).getByText(/管理员 owner .*只读查看.*原因：找回照片/)).toBeTruthy();
    await user.click(within(notice).getByRole("button", { name: "知道了" }));
    expect(screen.queryByRole("alertdialog", { name: "账号通知" })).toBeNull();
    await waitFor(() => expect(acknowledged).toEqual(["notification:1"]));
  });

  it("tells the owner when an administrator viewed the private photo library", async () => {
    serveDesktop({
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
    serveDesktop({ session: { ...session, user: { ...session.user, username: "alice", role: "member" } } });
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    expect(within(desktop).queryByRole("button", { name: "打开 Docker" })).toBeNull();
    expect(within(desktop).queryByRole("button", { name: "打开应用中心" })).toBeNull();
  });
});

describe("Installed apps on the desktop", () => {
  const immich: AppShortcut = {
    id: "immich",
    title: "Immich",
    webPort: 2283,
    scheme: "http",
    path: "/",
    running: 4,
    total: 4,
  };
  const navidrome: AppShortcut = {
    id: "navidrome",
    title: "Navidrome",
    webPort: 4533,
    scheme: "http",
    path: "/",
    running: 0,
    total: 1,
  };

  it("opens an installed app's web page from its desktop icon and from the launcher", async () => {
    serveDesktop({ shortcuts: [immich, navidrome] });
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
    serveDesktop({ shortcuts: [navidrome] });
    server.use(...appsHandlers(() => []), ...dockerHandlers(containerSnapshot()));
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
    serveDesktop({
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
