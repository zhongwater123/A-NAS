import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HttpResponse } from "msw";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setSession } from "../api/client";
import type { ActivityPage, Disk, FileEntry, Session, Space } from "../api.generated";
import { useHostState } from "../desktop/useHostState";
import { healthyState, session as signIn } from "../test/fixtures";
import { type SettingsOptions, settingsHandlers, terminalHandlers } from "../test/handlers/settings";
import { disk, storagePlan, volume } from "../test/handlers/storage";
import { accountHandlers, alice, membersHandlers } from "../test/handlers/users";
import { apiError, http, pathOf, server } from "../test/server";
import { SettingsPanel, type SettingsSection, useStoragePlan } from "./SettingsPanel";
import type { SessionStatus } from "./terminal";

const xterm = vi.hoisted(() => ({ mounts: 0, onStatus: undefined as ((status: SessionStatus) => void) | undefined }));

// The cropper needs layout jsdom lacks; this one reports a fixed square.
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

vi.mock("./XtermSession", async () => {
  const { useEffect, useEffectEvent } = await import("react");
  return {
    default: function MockXtermSession({ onStatus }: { onStatus: (status: SessionStatus) => void }) {
      const mounted = useEffectEvent(() => {
        xterm.mounts += 1;
        xterm.onStatus = onStatus;
        onStatus({ state: "connected" });
      });
      useEffect(() => mounted(), []);
      return <div data-testid="xterm-session" />;
    },
  };
});

const owner = signIn();
const member = signIn(alice);

// Settings as the desktop shows it (Desktop.tsx): the desktop holds the
// section, the host state and any storage plan, which outlive the window,
// and App takes an account its owner changed into the session. open shows
// the window or leaves it closed.
function Settings({ signedIn, open = true }: { signedIn: Session; open?: boolean }) {
  const [current, setCurrent] = useState(signedIn);
  const host = useHostState(true);
  const [section, setSection] = useState<SettingsSection>("overview");
  const storagePlan = useStoragePlan();
  if (!open) return null;
  return (
    <SettingsPanel
      session={current}
      host={host}
      section={section}
      onSectionChange={setSection}
      storagePlan={storagePlan}
      onUserChange={(user) => setCurrent((value) => ({ ...value, user }))}
      onLogout={() => undefined}
    />
  );
}

// renderSettings opens Settings on 概览, as the desktop first does; close and
// reopen close the window and open it again.
function renderSettings(signedIn: Session = owner) {
  setSession(signedIn);
  const view = render(<Settings signedIn={signedIn} />);
  return {
    close: () => view.rerender(<Settings signedIn={signedIn} open={false} />),
    reopen: () => view.rerender(<Settings signedIn={signedIn} />),
  };
}

// serve answers what Settings reads and the storage section; it returns what
// was sent to storage.
function serve(options: SettingsOptions = {}) {
  const settings = settingsHandlers(options);
  server.use(...settings.handlers);
  return settings.sent;
}

// serveActivity answers the activity log with no events and returns the
// queries it was asked.
function serveActivity(scope: ActivityPage["scope"]) {
  const asked: string[] = [];
  server.use(
    http.get("/api/v1/activity", ({ request, response }) => {
      asked.push(pathOf(request));
      return response(200).json({ items: [], scope });
    }),
  );
  return asked;
}

const sections = () => screen.getByRole("navigation", { name: "设置分区" });

// openSection opens a section from Settings' navigation.
async function openSection(user: ReturnType<typeof userEvent.setup>, name: string) {
  await user.click(within(sections()).getByRole("button", { name }));
}

const unavailable = () => apiError("state_unavailable", "host state is unavailable");

beforeEach(() => {
  localStorage.clear();
  xterm.mounts = 0;
  xterm.onStatus = undefined;
});

afterEach(() => {
  setSession();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

// Moved from the desktop's tests, where Settings opened from its icon.
describe("Settings host state", () => {
  it("shows a loading state while the first host observation is pending", async () => {
    let answer = () => {};
    const answered = new Promise<void>((resolve) => {
      answer = resolve;
    });
    serve();
    server.use(
      http.get("/api/v1/host-state", async ({ response }) => {
        await answered;
        return response(200).json(healthyState);
      }),
    );
    renderSettings();
    expect(await screen.findByText("正在读取设备状态…")).toBeTruthy();
    await act(async () => answer());
    expect(await screen.findByText("anas-fake")).toBeTruthy();
  });

  it("keeps the last successful state when refresh loses connection", async () => {
    let reads = 0;
    serve();
    server.use(
      http.get("/api/v1/host-state", ({ response }) => {
        reads += 1;
        return reads === 1 ? response(200).json(healthyState) : response(503).json(unavailable());
      }),
    );
    const user = userEvent.setup();
    renderSettings();
    expect(await screen.findByText("anas-fake")).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "刷新设备状态" }));

    expect(await screen.findByText("连接中断，正在显示上次成功读取的数据")).toBeTruthy();
    expect(screen.getByText("anas-fake")).toBeTruthy();
  });

  it("retries after an initial 503 response", async () => {
    let reads = 0;
    serve();
    server.use(
      http.get("/api/v1/host-state", ({ response }) => {
        reads += 1;
        return reads === 1 ? response(503).json(unavailable()) : response(200).json(healthyState);
      }),
    );
    const user = userEvent.setup();
    renderSettings();
    expect(await screen.findByText("暂时无法读取设备状态")).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "重新连接" }));
    expect(await screen.findByText("anas-fake")).toBeTruthy();
    expect(reads).toBe(2);
  });
});

describe("Settings storage", () => {
  it("keeps the desktop usable when a blank-disk plan contains legacy null arrays", async () => {
    serve();
    // Servers before the contract sent null for empty lists. The contract no
    // longer allows it, so this answer is untyped.
    const legacy = storagePlan("plan:blank", { expiresAt: "2026-10-07T10:10:00Z" });
    server.use(
      http.post("/api/v1/storage/plans", ({ response }) =>
        response.untyped(
          HttpResponse.json(
            {
              ...legacy,
              actions: null,
              disks: legacy.disks.map((planned) => ({ ...planned, signatures: null })),
            },
            { status: 201 },
          ),
        ),
      ),
    );
    const user = userEvent.setup();
    renderSettings();
    await openSection(user, "存储");
    await user.click(await screen.findByRole("button", { name: "初始化数据卷…" }));
    await user.click(screen.getByRole("button", { name: "生成格式化计划" }));
    expect(await screen.findByRole("heading", { name: "破坏性操作计划" })).toBeTruthy();
    expect(screen.getByText("未检测到文件系统签名")).toBeTruthy();
    expect(sections()).toBeTruthy();
  });

  it("shows an explicit empty disk inventory", async () => {
    serve({ hostState: { ...healthyState, disks: [] } });
    const user = userEvent.setup();
    renderSettings();
    await openSection(user, "存储");

    expect(await screen.findByText("未检测到磁盘")).toBeTruthy();
  });
});

describe("Settings terminal", () => {
  // That the desktop has no terminal icon of its own is the desktop's test
  // (App.test.tsx, "enables photos, trash, the backup center, and one settings entry").
  it("opens the administrator terminal in Settings rather than on the desktop", async () => {
    serve();
    server.use(...terminalHandlers(true).handlers);
    const user = userEvent.setup();
    renderSettings();

    await openSection(user, "终端");
    expect(await screen.findByTestId("xterm-session")).toBeTruthy();
    expect(screen.getByRole("heading", { name: "终端" })).toBeTruthy();
    expect(await screen.findByText("以 owner 的 Linux 账号在 anas-fake 上运行的 Shell")).toBeTruthy();
    expect(await screen.findByText("已连接")).toBeTruthy();
    expect(xterm.mounts).toBe(1);
  });

  it("keeps the shell running while another settings section is shown", async () => {
    serve();
    serveActivity("all");
    server.use(...terminalHandlers(true).handlers, ...membersHandlers([owner.user]).handlers);
    const user = userEvent.setup();
    renderSettings();
    await openSection(user, "终端");
    await screen.findByTestId("xterm-session");
    const terminal = () => within(sections()).getByRole("button", { name: "终端" });

    await openSection(user, "日志");
    expect(screen.getByRole("heading", { name: "日志" })).toBeTruthy();
    expect(screen.getByTestId("xterm-session").closest("[hidden]")).toBeTruthy();
    // The navigation marks the shell that runs out of sight.
    expect(within(terminal()).queryByTitle("Shell 正在运行")).toBeTruthy();

    await openSection(user, "终端");
    expect(screen.getByTestId("xterm-session").closest("[hidden]")).toBeNull();
    expect(within(terminal()).queryByTitle("Shell 正在运行")).toBeNull();
    expect(xterm.mounts).toBe(1);
  });

  // Minimizing the window, which keeps the shell, is the desktop's test.
  it("ends the shell when Settings closes and starts a new one when it opens again", async () => {
    serve();
    server.use(...terminalHandlers(true).handlers);
    const user = userEvent.setup();
    const settings = renderSettings();
    await openSection(user, "终端");
    await screen.findByTestId("xterm-session");

    settings.close();
    expect(screen.queryByTestId("xterm-session")).toBeNull();
    // Settings reopens on the terminal with a new shell.
    settings.reopen();
    expect(await screen.findByTestId("xterm-session")).toBeTruthy();
    expect(xterm.mounts).toBe(2);
  });

  it("offers a new session after the shell exits", async () => {
    serve();
    server.use(...terminalHandlers(true).handlers);
    const user = userEvent.setup();
    renderSettings();
    await openSection(user, "终端");
    await screen.findByTestId("xterm-session");

    act(() => xterm.onStatus?.({ state: "ended", exitCode: 0 }));
    const notice = screen.getByRole("status");
    expect(notice.textContent).toContain("Shell 已退出（代码 0）");
    expect(screen.getByText("已结束")).toBeTruthy();

    await user.click(within(notice).getByRole("button", { name: "新建会话" }));
    expect(screen.queryByText("Shell 已退出（代码 0）")).toBeNull();
    expect(xterm.mounts).toBe(2);
  });

  it("explains how the device's root turns the terminal on", async () => {
    serve();
    const terminal = terminalHandlers(false);
    server.use(...terminal.handlers);
    const user = userEvent.setup();
    renderSettings();
    await openSection(user, "终端");

    expect(await screen.findByRole("heading", { name: "终端尚未开启" })).toBeTruthy();
    expect(screen.getByLabelText("开启终端的命令").textContent).toContain("ANAS_TERMINAL=enabled");
    expect(screen.queryByTestId("xterm-session")).toBeNull();
    await user.click(screen.getByRole("button", { name: "重新检查" }));
    await waitFor(() => expect(terminal.sent.checks).toBe(2));
  });
});

describe("ADR 0008 account safeguards", () => {
  // Opening My account from the account menu is the desktop's test.
  it("lets the signed-in administrator change their own password and rebuild SMB credentials", async () => {
    serve();
    const account = accountHandlers(owner.user);
    server.use(...account.handlers);
    const user = userEvent.setup();
    renderSettings();
    await openSection(user, "我的账号");
    expect(within(sections()).getByRole("button", { name: "我的账号" }).getAttribute("aria-current")).toBe("page");
    await user.click(screen.getByRole("button", { name: "修改密码…" }));
    const form = screen.getByRole("form", { name: "修改密码" });
    await user.type(within(form).getByLabelText("当前密码"), "correct horse battery staple");
    await user.type(within(form).getByLabelText("新密码"), "administrator chosen password");
    await user.type(within(form).getByLabelText("确认新密码"), "administrator chosen password");
    await user.click(within(form).getByRole("button", { name: "保存新密码" }));
    expect(await screen.findByText("密码已更新，SMB 凭据已同步。")).toBeTruthy();
    expect(screen.queryByRole("form", { name: "修改密码" })).toBeNull();
    expect(account.sent.passwords).toEqual([
      { currentPassword: "correct horse battery staple", newPassword: "administrator chosen password" },
    ]);
  });

  it("lets an administrator start read-only viewing with a reason and password", async () => {
    serve();
    const members = membersHandlers([owner.user, alice]);
    server.use(...members.handlers);
    const user = userEvent.setup();
    renderSettings();
    await openSection(user, "用户与权限");
    await user.click(await screen.findByRole("button", { name: "alice 的更多操作" }));
    await user.click(
      within(screen.getByRole("menu", { name: "alice 的更多操作" })).getByRole("menuitem", { name: "查看个人空间…" }),
    );
    const form = screen.getByRole("form", { name: "查看 alice 的个人空间" });
    await user.type(within(form).getByLabelText("查看原因"), "Alice 请求找回文件");
    await user.type(within(form).getByLabelText("你的密码"), "correct horse battery staple");
    await user.click(within(form).getByRole("button", { name: "开始只读查看" }));
    expect(await screen.findByText(/已开启对 alice 个人空间的只读查看/)).toBeTruthy();
    expect(members.sent.viewings).toEqual([
      {
        path: "/api/v1/users/user%3Aalice/viewing",
        body: { password: "correct horse battery staple", reason: "Alice 请求找回文件", scope: "space" },
      },
    ]);
  });

  it("lets an administrator view a member's private photo library as its own grant", async () => {
    serve();
    const members = membersHandlers([owner.user, alice]);
    server.use(...members.handlers);
    const user = userEvent.setup();
    renderSettings();
    await openSection(user, "用户与权限");
    await user.click(await screen.findByRole("button", { name: "alice 的更多操作" }));
    await user.click(
      within(screen.getByRole("menu", { name: "alice 的更多操作" })).getByRole("menuitem", { name: "查看私有图库…" }),
    );
    const form = screen.getByRole("form", { name: "查看 alice 的私有图库" });
    expect(within(form).getByText(/查看他人私有图库会写入审计/)).toBeTruthy();
    await user.type(within(form).getByLabelText("查看原因"), "找婚礼照片");
    await user.type(within(form).getByLabelText("你的密码"), "correct horse battery staple");
    await user.click(within(form).getByRole("button", { name: "开始只读查看" }));
    expect(await screen.findByText(/已开启对 alice 私有图库的只读查看，可在相册中选择/)).toBeTruthy();
    expect(members.sent.viewings).toEqual([
      {
        path: "/api/v1/users/user%3Aalice/viewing",
        body: { password: "correct horse battery staple", reason: "找婚礼照片", scope: "library" },
      },
    ]);
  });
});

describe("Settings", () => {
  it("shows members their account, task manager, overview and read-only storage", async () => {
    // Members never list the accounts: /api/v1/users is left unserved, so
    // reading it fails the test.
    serve();
    const user = userEvent.setup();
    renderSettings(member);
    await openSection(user, "存储");
    const enabled = within(sections())
      .getAllByRole("button")
      .filter((button) => !button.hasAttribute("disabled"));
    expect(enabled.map((button) => button.getAttribute("aria-label") ?? button.textContent)).toEqual([
      "我的账号",
      "概览",
      "任务管理器",
      "存储",
      "日志",
    ]);
    expect(await screen.findByRole("heading", { name: "尚未创建数据卷" })).toBeTruthy();
    expect(screen.getByText(/请联系管理员初始化/)).toBeTruthy();
    expect(screen.getByText("A-NAS Fake HDD")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "初始化数据卷…" })).toBeNull();
    expect(screen.queryByRole("region", { name: "初始化数据卷" })).toBeNull();
  });

  it("opens a member's sign-in records from My account", async () => {
    serve();
    const asked = serveActivity("self");
    const user = userEvent.setup();
    renderSettings(member);
    await openSection(user, "我的账号");
    await user.click(screen.getByRole("button", { name: "查看" }));
    expect(within(sections()).getByRole("button", { name: "日志" }).getAttribute("aria-current")).toBe("page");
    expect(screen.getByRole("heading", { name: "我的活动" })).toBeTruthy();
    expect(screen.getByRole("radio", { name: "登录" }).getAttribute("aria-checked")).toBe("true");
    await waitFor(() =>
      expect(asked.some((path) => path.startsWith("/api/v1/activity?") && path.includes("category=session"))).toBe(
        true,
      ),
    );
    expect(screen.queryByRole("tab", { name: "系统日志" })).toBeNull();
  });

  it("keeps showing a member who reset their password", async () => {
    serve();
    const user = userEvent.setup();
    renderSettings(signIn(alice, { credentialReset: { actorUsername: "owner", at: "2026-10-14T09:10:00Z" } }));
    await openSection(user, "我的账号");
    expect(screen.getByText("管理员重置过密码")).toBeTruthy();
    expect(screen.getByText(/owner 于 .* 重置了这个账号的密码/)).toBeTruthy();
  });

  it("lets a member change their own password", async () => {
    serve();
    const account = accountHandlers(alice);
    server.use(...account.handlers);
    const user = userEvent.setup();
    renderSettings(member);
    await openSection(user, "我的账号");
    await user.click(screen.getByRole("button", { name: "修改密码…" }));
    const form = screen.getByRole("form", { name: "修改密码" });
    await user.type(within(form).getByLabelText("当前密码"), "alice old password");
    await user.type(within(form).getByLabelText("新密码"), "alice new password");
    await user.type(within(form).getByLabelText("确认新密码"), "alice another password");
    await user.click(within(form).getByRole("button", { name: "保存新密码" }));
    expect(within(form).getByText("两次输入的新密码不一致")).toBeTruthy();
    expect(account.sent.passwords).toHaveLength(0);

    await user.clear(within(form).getByLabelText("确认新密码"));
    await user.type(within(form).getByLabelText("确认新密码"), "alice new password");
    await user.click(within(form).getByRole("button", { name: "保存新密码" }));
    expect(await screen.findByText("密码已更新，SMB 凭据已同步。")).toBeTruthy();
    expect(account.sent.passwords).toEqual([
      { currentPassword: "alice old password", newPassword: "alice new password" },
    ]);
  });

  it("keeps a pending storage plan across sections and when settings closes", async () => {
    serve({ plan: storagePlan("plan:pending") });
    const user = userEvent.setup();
    const settings = renderSettings();
    await openSection(user, "存储");
    await user.click(await screen.findByRole("button", { name: "初始化数据卷…" }));
    await user.click(screen.getByRole("button", { name: "生成格式化计划" }));
    await user.type(await screen.findByLabelText(/输入确认短语/), "ERASE");

    await openSection(user, "概览");
    expect(screen.queryByRole("heading", { name: "破坏性操作计划" })).toBeNull();
    settings.close();
    settings.reopen();
    expect(within(sections()).getByRole("button", { name: "概览" }).getAttribute("aria-current")).toBe("page");

    await openSection(user, "存储");
    expect(screen.getByRole("heading", { name: "破坏性操作计划" })).toBeTruthy();
    expect((screen.getByLabelText(/输入确认短语/) as HTMLInputElement).value).toBe("ERASE");
    expect(screen.getByRole("button", { name: "确认计划" }).hasAttribute("disabled")).toBe(true);
    expect(screen.queryByRole("button", { name: "生成格式化计划" })).toBeNull();
  });

  it("says why the system is rated as it is and leads to storage", async () => {
    serve({
      hostState: {
        ...healthyState,
        system: {
          ...healthyState.system,
          health: "critical",
          healthFindings: [
            { code: "volume_read_only", severity: "critical", diskId: "disk:fake-data-01" },
            { code: "disk_hot", severity: "warning", diskId: "disk:fake-data-01", value: 57 },
          ],
        },
      },
    });
    const user = userEvent.setup();
    renderSettings();
    await openSection(user, "概览");
    expect(await screen.findByText("严重故障")).toBeTruthy();
    expect(screen.getByText("2 项需要处理")).toBeTruthy();
    expect(screen.getByText("数据卷变成了只读")).toBeTruthy();
    expect(screen.getByText("A-NAS Fake HDD 温度 57 °C，偏高")).toBeTruthy();
    expect(screen.queryByText("健康未评估")).toBeNull();
    await user.click(screen.getByRole("button", { name: "在存储中查看磁盘与数据卷" }));
    expect(within(sections()).getByRole("button", { name: "存储" }).getAttribute("aria-current")).toBe("page");
  });

  it("says what was checked when nothing is wrong", async () => {
    serve();
    const user = userEvent.setup();
    renderSettings();
    await openSection(user, "概览");
    expect(await screen.findByText("运行正常")).toBeTruthy();
    expect(screen.getByText("未发现问题")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "在存储中查看磁盘与数据卷" })).toBeNull();
  });

  it("reports unknown data volume usage instead of a full volume", async () => {
    serve({ volumes: [volume({ capacityBytes: 500_107_862_016, filesystemUuid: "09e275fe-794a" })] });
    const user = userEvent.setup();
    renderSettings();
    await openSection(user, "存储");
    expect(await screen.findByText("已用空间未知 · 共 466 GiB")).toBeTruthy();
    expect(screen.getByText("在线")).toBeTruthy();
    expect(screen.queryByRole("meter", { name: "数据卷已用空间" })).toBeNull();
    expect(screen.queryByRole("region", { name: "初始化数据卷" })).toBeNull();
  });

  it("shows unusable devices with reasons and never offers them for initialization", async () => {
    const unusable: Disk[] = [
      disk("observation:empty", {
        model: "",
        capacityBytes: 0,
        eligibleForDataVolume: false,
        ineligibleReasons: ["missing_model", "missing_capacity", "missing_identity"],
      }),
      ...["one", "two"].map((name) =>
        disk(`observation:${name}`, {
          model: `Bridge ${name}`,
          eligibleForDataVolume: false,
          ineligibleReasons: ["duplicate_identity"],
        }),
      ),
    ];
    serve({ hostState: { ...healthyState, disks: [...healthyState.disks, ...unusable] } });
    const user = userEvent.setup();
    renderSettings();
    await openSection(user, "存储");
    expect(await screen.findByText("未识别设备")).toBeTruthy();
    expect(screen.getByText("容量未知")).toBeTruthy();
    expect(screen.getAllByText("硬盘身份与其他设备重复，不能安全选择")).toHaveLength(2);
    await user.click(screen.getByRole("button", { name: "初始化数据卷…" }));
    const candidates = screen.getByRole("group", { name: "候选硬盘" });
    for (const box of within(candidates).getAllByRole("checkbox")) {
      const input = box as HTMLInputElement;
      expect(input.disabled).toBe(input.value.startsWith("observation:"));
    }
    expect(within(candidates).getAllByRole("checkbox")).toHaveLength(4);
  });

  it("adds a new disk to the data volume as a mirror", async () => {
    const dataDisk = disk("disk:data", {
      model: "ST500DM002-1BD142",
      capacityBytes: 500_107_862_016,
      role: "data",
      inUse: true,
      filesystems: ["btrfs"],
      eligibleForDataVolume: false,
      ineligibleReasons: ["data_volume", "in_use"],
    });
    const spareDisk = disk("disk:spare", {
      model: "ST500DM002-1BD142",
      capacityBytes: 500_107_862_016,
      filesystems: ["linux_raid_member"],
    });
    const usbDisk = disk("disk:usb", {
      model: "USB Stick",
      transport: "usb",
      removable: true,
      eligibleForDataVolume: false,
      ineligibleReasons: ["unsupported_transport", "removable"],
    });
    const plan = storagePlan("plan:add", {
      kind: "add_disks",
      layout: "mirror",
      capacityBeforeBytes: 500_107_862_016,
      capacityAfterBytes: 500_107_862_016,
      disks: [
        {
          id: "disk:spare",
          model: "ST500DM002-1BD142",
          capacityBytes: 500_107_862_016,
          fingerprint: "spare-fingerprint",
          signatures: [{ type: "linux_raid_member", label: "OrangePi3B:0" }],
        },
      ],
      confirmationPhrase: "ERASE k:spare",
      actions: [{ kind: "convert_layout", description: "在后台把数据卷从单盘转换为镜像（RAID 1），期间可以照常使用" }],
    });
    const storage = serve({
      hostState: { ...healthyState, disks: [dataDisk, spareDisk, usbDisk] },
      volumes: [
        volume({
          capacityBytes: 480 * 1024 ** 3,
          availableBytes: 280 * 1024 ** 3,
          filesystemUuid: "09e275fe-794a",
        }),
      ],
      plan,
    });
    const user = userEvent.setup();
    renderSettings();
    await openSection(user, "存储");
    expect(await screen.findByText("发现 1 块未使用的硬盘")).toBeTruthy();
    expect(screen.getAllByText("有旧数据").length).toBeGreaterThan(0);
    expect(screen.getByText("USB 等外接硬盘不能加入数据卷；可移除设备不能加入数据卷")).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "设置用途…" }));
    const wizard = screen.getByRole("region", { name: "设置硬盘用途" });
    expect((within(wizard).getByRole("radio", { name: /用作备份盘/ }) as HTMLInputElement).disabled).toBe(true);
    expect((within(wizard).getByRole("checkbox", { name: /ST500DM002/ }) as HTMLInputElement).checked).toBe(true);
    await user.click(within(wizard).getByRole("button", { name: "下一步" }));
    const layouts = within(wizard).getByRole("radiogroup", { name: "冗余方式" });
    expect((within(layouts).getByRole("radio", { name: /镜像/ }) as HTMLInputElement).checked).toBe(true);
    expect(within(layouts).getByText("推荐")).toBeTruthy();
    expect(within(wizard).getByText(/不能代替备份/)).toBeTruthy();
    await user.click(within(wizard).getByRole("button", { name: "生成计划" }));

    expect(await within(wizard).findByRole("heading", { name: "破坏性操作计划" })).toBeTruthy();
    expect(storage.plans).toEqual([{ diskIds: ["disk:spare"], layout: "mirror" }]);
    expect(within(wizard).getByText("将清除：linux_raid_member（OrangePi3B:0）")).toBeTruthy();
    expect(within(wizard).getByText("硬盘上有其他系统留下的数据")).toBeTruthy();
    expect(within(wizard).getByText("镜像（RAID 1）")).toBeTruthy();
    await user.type(within(wizard).getByLabelText(/输入确认短语/), "ERASE k:spare");
    await user.click(within(wizard).getByRole("button", { name: "确认计划" }));
    await user.click(await within(wizard).findByRole("button", { name: "清除硬盘并加入数据卷" }));
    expect(await within(wizard).findByText("硬盘已加入数据卷")).toBeTruthy();
    await user.click(within(wizard).getByRole("button", { name: "完成" }));
    expect(screen.queryByRole("region", { name: "设置硬盘用途" })).toBeNull();
  });

  it("leaves the wizard by cancelling a plan that has not run", async () => {
    const storage = serve({ plan: storagePlan("plan:leave") });
    const user = userEvent.setup();
    renderSettings();
    await openSection(user, "存储");
    await user.click(await screen.findByRole("button", { name: "初始化数据卷…" }));
    await user.click(screen.getByRole("button", { name: "生成格式化计划" }));
    expect(await screen.findByRole("heading", { name: "破坏性操作计划" })).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "取消计划" }));
    expect(await screen.findByRole("heading", { name: "尚未创建数据卷" })).toBeTruthy();
    expect(storage.requests).toContain("POST /api/v1/storage/plans/plan%3Aleave/cancel");
    expect(screen.queryByRole("region", { name: "初始化数据卷" })).toBeNull();

    await user.click(screen.getByRole("button", { name: "初始化数据卷…" }));
    await user.click(screen.getByRole("button", { name: "生成格式化计划" }));
    await user.click(await screen.findByRole("button", { name: "取消计划并关闭" }));
    expect(await screen.findByRole("heading", { name: "尚未创建数据卷" })).toBeTruthy();
  });

  it("follows a plan that runs on the device when the page comes back", async () => {
    const plan = storagePlan("plan:run", { state: "running", step: "format" });
    const storage = serve({ unfinished: [plan], operation: { ...plan, state: "succeeded", step: undefined } });
    const user = userEvent.setup();
    renderSettings();
    await openSection(user, "存储");
    const wizard = await screen.findByRole("region", { name: "初始化数据卷" });
    expect(within(wizard).getByText("正在格式化，固态硬盘可能要几分钟…")).toBeTruthy();
    expect(within(wizard).getByText(/关闭这个窗口或页面不会打断它/)).toBeTruthy();
    expect(await within(wizard).findByText("数据卷已创建")).toBeTruthy();
    expect(storage.requests).toContain("GET /api/v1/operations/plan%3Arun");
  });

  it("offers to retry a plan that stopped on the device", async () => {
    const plan = storagePlan("plan:stopped", {
      state: "needs_attention",
      step: "mount",
      expiresAt: "2026-10-21T09:10:00Z",
      failure: "the Host Agent stopped during the mount step; retrying continues after the steps that are done",
    });
    const storage = serve({ unfinished: [plan], plan });
    const user = userEvent.setup();
    renderSettings();
    await openSection(user, "存储");
    expect(await screen.findByText("上次的存储操作没有完成")).toBeTruthy();
    expect(screen.getByText(/创建数据卷在“挂载”这一步停下了/)).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "查看并处理" }));
    const wizard = screen.getByRole("region", { name: "初始化数据卷" });
    expect(within(wizard).getByText("需要人工处理")).toBeTruthy();
    expect(within(wizard).getByText(/已经完成的步骤不会重做/)).toBeTruthy();
    await user.click(within(wizard).getByRole("button", { name: "重试" }));
    expect(await within(wizard).findByText("数据卷已创建")).toBeTruthy();
    expect(storage.requests).toContain("POST /api/v1/storage/plans/plan%3Astopped/execute");
  });

  it("shows why a retried plan stopped again", async () => {
    const plan = storagePlan("plan:again", {
      kind: "add_disks",
      layout: "mirror",
      disks: [
        {
          id: "disk:fake-data-02",
          model: "A-NAS Fake HDD",
          capacityBytes: 512000000000,
          fingerprint: "again-fingerprint",
          signatures: [],
        },
      ],
      confirmationPhrase: "ERASE data-02",
      state: "needs_attention",
      step: "erase",
      expiresAt: "2026-10-21T09:10:00Z",
      failure: "the Host Agent stopped during the erase step; retrying continues after the steps that are done",
    });
    serve({
      unfinished: [plan],
      plan,
      executeFails: true,
      operation: { ...plan, step: "erase", failure: "disk identity changed after planning" },
    });
    const user = userEvent.setup();
    renderSettings();
    await openSection(user, "存储");
    await user.click(await screen.findByRole("button", { name: "查看并处理" }));
    const wizard = screen.getByRole("region", { name: "设置硬盘用途" });
    await user.click(within(wizard).getByRole("button", { name: "重试" }));
    expect(await within(wizard).findByText(/disk identity changed after planning/)).toBeTruthy();
    expect(within(wizard).queryByText(/the Host Agent stopped during the erase step/)).toBeNull();
    expect(within(wizard).queryByRole("alert")).toBeNull();
  });

  it("shows how far a conversion has come and continues a stopped one", async () => {
    const disks = ["disk:data", "disk:spare"].map((id) =>
      disk(id, { role: "data", inUse: true, eligibleForDataVolume: false, ineligibleReasons: ["data_volume"] }),
    );
    const storage = serve({
      hostState: { ...healthyState, disks },
      volumes: [
        volume({
          diskIds: ["disk:data", "disk:spare"],
          layout: "mirror",
          capacityBytes: 480 * 1024 ** 3,
          availableBytes: 280 * 1024 ** 3,
          conversion: { state: "waiting", convertedBytes: 80 * 1024 ** 3, totalBytes: 200 * 1024 ** 3 },
        }),
      ],
    });
    const user = userEvent.setup();
    renderSettings();
    await openSection(user, "存储");
    expect(await screen.findByText("转换为镜像已暂停")).toBeTruthy();
    expect(screen.getByText("Btrfs · 镜像（RAID 1） · 2 块硬盘")).toBeTruthy();
    expect(screen.getByRole("progressbar", { name: "转换为镜像" }).getAttribute("aria-valuenow")).toBe("40");
    expect(screen.getByText("转换中")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "设置用途…" })).toBeNull();
    await user.click(screen.getByRole("button", { name: "继续转换" }));
    await waitFor(() => expect(storage.requests).toContain("POST /api/v1/volumes/volume%3Adata/conversion"));
  });

  it("creates a mirror when two disks are chosen for a new data volume", async () => {
    const second = disk("disk:fake-data-02");
    const diskIds = ["disk:fake-data-01", "disk:fake-data-02"];
    const storage = serve({
      hostState: { ...healthyState, disks: [healthyState.disks[0], second] },
      plan: storagePlan("plan:span", {
        layout: "span",
        capacityAfterBytes: 1024000000000,
        disks: diskIds.map((id) => ({
          id,
          model: "A-NAS Fake HDD",
          capacityBytes: 512000000000,
          fingerprint: `${id}-fingerprint`,
          signatures: [],
        })),
      }),
    });
    const user = userEvent.setup();
    renderSettings();
    await openSection(user, "存储");
    await user.click(await screen.findByRole("button", { name: "初始化数据卷…" }));
    const wizard = screen.getByRole("region", { name: "初始化数据卷" });
    const boxes = within(wizard).getAllByRole("checkbox");
    await user.click(boxes[0]);
    expect(within(wizard).getByRole("button", { name: "生成格式化计划" })).toBeTruthy();
    await user.click(boxes[1]);
    await user.click(within(wizard).getByRole("button", { name: "下一步" }));
    expect(within(wizard).getAllByText("477 GiB").length).toBeGreaterThan(0);
    await user.click(within(wizard).getByText("扩容"));
    expect((within(wizard).getByRole("radio", { name: /扩容/ }) as HTMLInputElement).checked).toBe(true);
    await user.click(within(wizard).getByRole("button", { name: "生成格式化计划" }));
    expect(await within(wizard).findByRole("heading", { name: "破坏性操作计划" })).toBeTruthy();
    expect(storage.plans).toEqual([{ diskIds, layout: "span" }]);
  });

  it("resets a member password from a form inside the window", async () => {
    const prompt = vi.spyOn(window, "prompt");
    serve();
    const members = membersHandlers([owner.user, alice]);
    server.use(...members.handlers);
    const user = userEvent.setup();
    renderSettings();
    await openSection(user, "用户与权限");
    expect(await screen.findByText("成员 · 正常")).toBeTruthy();
    expect(screen.getByText("管理员 · 正常")).toBeTruthy();
    expect(screen.getByText("你")).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "alice 的更多操作" }));
    await user.click(screen.getByRole("menuitem", { name: "重置密码…" }));
    const form = screen.getByRole("form", { name: "重置 alice 的密码" });
    await user.type(within(form).getByLabelText("新密码"), "temporary password 1");
    await user.click(within(form).getByRole("button", { name: "确认重置" }));
    expect(await screen.findByText(/已重置 alice 的密码/)).toBeTruthy();
    expect(prompt).not.toHaveBeenCalled();
    expect(members.sent.resets).toEqual([
      { path: "/api/v1/users/user%3Aalice/credential", body: { password: "temporary password 1" } },
    ]);

    await user.click(screen.getByRole("button", { name: "修改我的密码" }));
    expect(within(sections()).getByRole("button", { name: "我的账号" }).getAttribute("aria-current")).toBe("page");
    expect(screen.getByRole("form", { name: "修改密码" })).toBeTruthy();
    // The request opens the form once; coming back to My account later does not.
    await openSection(user, "概览");
    await openSection(user, "我的账号");
    expect(screen.queryByRole("form", { name: "修改密码" })).toBeNull();
  });

  it("checks a new member's name and password against the NAS rules before creating it", async () => {
    serve();
    const members = membersHandlers([owner.user]);
    server.use(...members.handlers);
    const user = userEvent.setup();
    renderSettings();
    await openSection(user, "用户与权限");
    await user.click(await screen.findByRole("button", { name: "添加成员" }));
    const form = screen.getByRole("form", { name: "添加成员" });
    expect(within(form).getByText(/3–32 位英文字母/)).toBeTruthy();

    await user.type(within(form).getByLabelText("账号"), "Shared");
    await user.type(within(form).getByLabelText("初始密码"), "carol password 1");
    await user.click(within(form).getByRole("button", { name: "创建成员" }));
    expect(await within(form).findByText("“shared”留给系统使用，请换一个账号名")).toBeTruthy();
    expect(members.sent.created).toHaveLength(0);

    await user.clear(within(form).getByLabelText("账号"));
    await user.type(within(form).getByLabelText("账号"), "Carol");
    await user.clear(within(form).getByLabelText("初始密码"));
    await user.type(within(form).getByLabelText("初始密码"), "x".repeat(65));
    await user.click(within(form).getByRole("button", { name: "创建成员" }));
    expect(await within(form).findByText("密码最多 64 个字符")).toBeTruthy();
    expect(members.sent.created).toHaveLength(0);

    await user.clear(within(form).getByLabelText("初始密码"));
    await user.type(within(form).getByLabelText("初始密码"), "carol password 1");
    await user.click(within(form).getByRole("button", { name: "创建成员" }));
    expect(await screen.findByText("已添加成员 carol。")).toBeTruthy();
    expect(members.sent.created).toEqual([{ username: "Carol", password: "carol password 1" }]);
  });

  it("asks for confirmation before disabling a member", async () => {
    serve();
    const members = membersHandlers([owner.user, alice]);
    server.use(...members.handlers);
    const user = userEvent.setup();
    renderSettings();
    await openSection(user, "用户与权限");
    await user.click(await screen.findByRole("button", { name: "alice 的更多操作" }));
    await user.click(screen.getByRole("menuitem", { name: "禁用账号…" }));
    const confirm = screen.getByRole("form", { name: "禁用 alice" });
    expect(members.sent.disabled).toHaveLength(0);

    await user.click(within(confirm).getByRole("button", { name: "取消" }));
    expect(screen.queryByRole("form", { name: "禁用 alice" })).toBeNull();
    await user.click(screen.getByRole("button", { name: "alice 的更多操作" }));
    await user.click(screen.getByRole("menuitem", { name: "禁用账号…" }));
    await user.click(
      within(screen.getByRole("form", { name: "禁用 alice" })).getByRole("button", { name: "禁用账号" }),
    );
    expect(await screen.findByText("已禁用 alice。")).toBeTruthy();
    expect(members.sent.disabled).toEqual(["/api/v1/users/user%3Aalice"]);
  });
});

describe("Avatars and nicknames", () => {
  // jsdom neither decodes images nor draws on a canvas.
  function stubPictures() {
    class DecodedImage {
      naturalWidth = 400;
      naturalHeight = 320;
      decoding = "";
      onload?: () => void;
      onerror?: () => void;
      private source = "";
      get src() {
        return this.source;
      }
      set src(value: string) {
        this.source = value;
        queueMicrotask(() => (value.includes("broken") ? this.onerror?.() : this.onload?.()));
      }
    }
    vi.stubGlobal("Image", DecodedImage);
    vi.stubGlobal(
      "URL",
      Object.assign(URL, { createObjectURL: vi.fn(() => "blob:avatar-source"), revokeObjectURL: vi.fn() }),
    );
    const drawImage = vi.fn();
    vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue({ fillRect: vi.fn(), drawImage } as never);
    vi.spyOn(HTMLCanvasElement.prototype, "toBlob").mockImplementation((callback) =>
      callback(new Blob(["jpeg"], { type: "image/jpeg" })),
    );
    return drawImage;
  }

  // My account in the navigation shows the signed-in account as Settings has it.
  const myAccount = () => within(sections()).getByRole("button", { name: "我的账号" });

  // That the desktop's account button shows the nickname too is the desktop's test.
  it("lets a member choose a nickname that Settings shows instead of the account name", async () => {
    serve();
    const account = accountHandlers(alice);
    server.use(...account.handlers);
    const user = userEvent.setup();
    renderSettings(member);
    await openSection(user, "我的账号");
    expect(screen.getByText("未设置，显示账号名 alice")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "设置昵称…" }));
    const form = screen.getByRole("form", { name: "修改昵称" });
    await user.type(within(form).getByLabelText("昵称"), "一二三四五六七八九十一二三四五六七八九十一二三四五");
    await user.click(within(form).getByRole("button", { name: "保存昵称" }));
    expect(within(form).getByText("昵称最多 24 个字符")).toBeTruthy();
    expect(account.sent.profiles).toHaveLength(0);

    await user.clear(within(form).getByLabelText("昵称"));
    await user.type(within(form).getByLabelText("昵称"), "owner");
    await user.click(within(form).getByRole("button", { name: "保存昵称" }));
    expect(await within(form).findByText("这个名字已经是其他人的账号名或昵称，请换一个")).toBeTruthy();

    await user.clear(within(form).getByLabelText("昵称"));
    await user.type(within(form).getByLabelText("昵称"), " 小爱 ");
    await user.click(within(form).getByRole("button", { name: "保存昵称" }));
    expect(await screen.findByText("昵称已改为“小爱”。")).toBeTruthy();
    expect(account.sent.profiles.at(-1)).toEqual({ nickname: "小爱" });
    expect(screen.getByRole("heading", { name: "小爱" })).toBeTruthy();
    expect(screen.getByText("账号名 alice")).toBeTruthy();
    expect(myAccount().textContent).toContain("小爱");
  });

  // That the desktop's account button shows the avatar too is the desktop's test.
  it("crops a picture from this computer into the avatar and can remove it again", async () => {
    const drawImage = stubPictures();
    serve();
    const account = accountHandlers(alice);
    server.use(...account.handlers);
    const user = userEvent.setup();
    renderSettings(member);
    await openSection(user, "我的账号");
    await user.click(screen.getByRole("button", { name: "更换头像" }));
    const picker = screen.getByRole("dialog", { name: "更换头像" });
    expect(within(picker).getByRole("tab", { name: "本机图片" }).getAttribute("aria-selected")).toBe("true");
    await user.upload(
      within(picker).getByLabelText("从本机选择图片"),
      new File(["png"], "me.png", { type: "image/png" }),
    );

    expect((await within(picker).findByTestId("cropper")).getAttribute("data-image")).toBe("blob:avatar-source");
    expect(within(picker).getByRole("heading", { name: "调整头像" })).toBeTruthy();
    await user.click(within(picker).getByRole("button", { name: "保存头像" }));
    expect(await screen.findByText("头像已更新。")).toBeTruthy();
    expect(screen.queryByRole("dialog", { name: "更换头像" })).toBeNull();

    // The page sends the chosen square, drawn on white at no more than 512 px.
    expect(drawImage).toHaveBeenCalledWith(expect.anything(), 40, 0, 320, 320, 0, 0, 320, 320);
    expect(account.sent.avatars).toEqual([{ type: "image/jpeg", csrf: member.csrfToken, bytes: "jpeg" }]);
    expect(myAccount().querySelector("img")?.getAttribute("src")).toBe(
      "/api/v1/users/user%3Aalice/avatar?v=0123456789abcdef",
    );

    await user.click(screen.getByRole("button", { name: "移除" }));
    expect(await screen.findByText("已移除头像，现在显示名字的第一个字。")).toBeTruthy();
    expect(account.sent.avatarRemovals).toBe(1);
    expect(myAccount().querySelector("img")).toBeNull();
    expect(myAccount().textContent).toContain("A");
  });

  it("picks the avatar from a picture on the NAS, but not from a space being viewed", async () => {
    stubPictures();
    const spaces: Space[] = [
      {
        id: "space:alice",
        kind: "private",
        name: "alice",
        ownerUserId: "user:alice",
        createdAt: "2026-10-07T10:00:00Z",
      },
      { id: "space:shared", kind: "shared", name: "Shared", createdAt: "2026-10-07T10:00:00Z" },
      {
        id: "space:bob",
        kind: "private",
        name: "bob",
        ownerUserId: "user:bob",
        createdAt: "2026-10-07T10:00:00Z",
        viewing: { grantId: "grant:1", expiresAt: "2026-10-08T10:00:00Z" },
      },
    ];
    const entry = (id: string, name: string, kind: FileEntry["kind"], sizeBytes: number): FileEntry => ({
      id,
      spaceId: "space:alice",
      name,
      kind,
      sizeBytes,
      modifiedAt: "2026-10-07T10:00:00Z",
    });
    const entries = [
      entry("file:trip", "海边.jpg", "file", 2048),
      entry("file:notes", "notes.txt", "file", 12),
      entry("dir:2026", "2026", "directory", 0),
    ];
    serve();
    server.use(
      http.get("/api/v1/spaces", ({ response }) => response(200).json({ items: spaces })),
      http.get("/api/v1/spaces/{spaceId}/entries", ({ params, response }) =>
        response(200).json({ items: entries.filter((item) => item.spaceId === params.spaceId) }),
      ),
    );
    const user = userEvent.setup();
    renderSettings(member);
    await openSection(user, "我的账号");
    await user.click(screen.getByRole("button", { name: "设置头像…" }));
    const picker = screen.getByRole("dialog", { name: "更换头像" });
    await user.click(within(picker).getByRole("tab", { name: "NAS 文件" }));
    const spaceList = await within(picker).findByRole("group", { name: "空间" });
    expect(
      within(spaceList)
        .getAllByRole("button")
        .map((button) => button.textContent),
    ).toEqual(["个人空间", "共享空间"]);
    expect(await within(picker).findByRole("button", { name: "2026" })).toBeTruthy();
    expect(within(picker).queryByText("notes.txt")).toBeNull();
    await user.click(within(picker).getByRole("button", { name: "选择 海边.jpg" }));
    expect((await within(picker).findByTestId("cropper")).getAttribute("data-image")).toBe(
      "/api/v1/files/file%3Atrip/content?disposition=inline",
    );
    await user.click(within(picker).getByRole("button", { name: "重新选择图片" }));
    expect(within(picker).getByRole("tab", { name: "NAS 文件" })).toBeTruthy();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog", { name: "更换头像" })).toBeNull();
  });

  // The account notice that names the administrator by nickname is the desktop's test.
  it("shows other accounts by nickname and keeps the account name where it matters", async () => {
    serve();
    server.use(
      ...membersHandlers([owner.user, { ...alice, nickname: "小爱", avatarVersion: "feedface00000000" }]).handlers,
    );
    const user = userEvent.setup();
    renderSettings();
    await openSection(user, "用户与权限");
    const menu = await screen.findByRole("button", { name: "小爱（alice） 的更多操作" });
    const row = rowOf(menu);
    expect(within(row).getByText("小爱")).toBeTruthy();
    expect(within(row).getByText("alice")).toBeTruthy();
    expect(row.querySelector("img")?.getAttribute("src")).toBe("/api/v1/users/user%3Aalice/avatar?v=feedface00000000");
    await user.click(menu);
    await user.click(screen.getByRole("menuitem", { name: "重置密码…" }));
    expect(screen.getByRole("form", { name: "重置 小爱（alice） 的密码" })).toBeTruthy();
  });
});

// rowOf is an account's row in 用户与权限: the nearest element around the
// account's menu that also holds its avatar.
function rowOf(menu: HTMLElement): HTMLElement {
  for (let row = menu.parentElement; row; row = row.parentElement) if (row.querySelector("img")) return row;
  throw new Error("the menu is not in a row with an avatar");
}
