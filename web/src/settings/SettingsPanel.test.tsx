import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import App from "../App";
import type { Disk, StoragePlan, User } from "../api.generated";
import { healthyState, installAPI, openSettingsSection, resetDesktop, session, stubFetch } from "../test/desktop";
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

beforeEach(() => {
  localStorage.clear();
  xterm.mounts = 0;
  xterm.onStatus = undefined;
});

afterEach(resetDesktop);

describe("Settings storage", () => {
  it("keeps the desktop usable when a blank-disk plan contains legacy null arrays", async () => {
    installAPI({
      storagePlan: {
        id: "plan:blank",
        kind: "create_volume",
        layout: "single",
        capacityAfterBytes: 512000000000,
        disks: [
          {
            id: "disk:fake-data-01",
            model: "A-NAS Fake HDD",
            capacityBytes: 512000000000,
            fingerprint: "blank-disk-fingerprint",
            signatures: null as unknown as [],
          },
        ],
        confirmationPhrase: "ERASE data-01",
        actions: null as unknown as [],
        state: "planned",
        createdAt: "2026-10-07T10:00:00Z",
        expiresAt: "2026-10-07T10:10:00Z",
      },
    });
    const user = userEvent.setup();
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    await openSettingsSection(user, desktop, "存储");
    await user.click(await screen.findByRole("button", { name: "初始化数据卷…" }));
    await user.click(screen.getByRole("button", { name: "生成格式化计划" }));
    expect(await screen.findByRole("heading", { name: "破坏性操作计划" })).toBeTruthy();
    expect(screen.getByText("未检测到文件系统签名")).toBeTruthy();
    expect(screen.getByRole("region", { name: "桌面应用" })).toBeTruthy();
  });

  it("shows an explicit empty disk inventory", async () => {
    stubFetch({ ...healthyState, disks: [] });
    const user = userEvent.setup();

    render(<App />);
    await openSettingsSection(user, await screen.findByRole("region", { name: "桌面应用" }), "存储");

    expect(await screen.findByText("未检测到磁盘")).toBeTruthy();
  });
});

describe("Settings terminal", () => {
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
    await user.click(
      within(screen.getByRole("region", { name: "桌面应用" })).getByRole("button", { name: "打开设置" }),
    );
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

  it("lets an administrator start read-only viewing with a reason and password", async () => {
    const alice: User = {
      id: "user:alice",
      username: "alice",
      role: "member",
      status: "active",
      createdAt: "2026-10-07T10:00:00Z",
      mustChangePassword: false,
    };
    const fetchMock = installAPI({ users: [session.user, alice] });
    const user = userEvent.setup();
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    await openSettingsSection(user, desktop, "用户与权限");
    await user.click(await screen.findByRole("button", { name: "alice 的更多操作" }));
    await user.click(
      within(screen.getByRole("menu", { name: "alice 的更多操作" })).getByRole("menuitem", { name: "查看个人空间…" }),
    );
    const form = screen.getByRole("form", { name: "查看 alice 的个人空间" });
    await user.type(within(form).getByLabelText("查看原因"), "Alice 请求找回文件");
    await user.type(within(form).getByLabelText("你的密码"), "correct horse battery staple");
    await user.click(within(form).getByRole("button", { name: "开始只读查看" }));
    expect(await screen.findByText(/已开启对 alice 个人空间的只读查看/)).toBeTruthy();
    const call = fetchMock.mock.calls.find(([path]) => path === "/api/v1/users/user%3Aalice/viewing");
    expect(JSON.parse(String(call?.[1]?.body))).toEqual({
      password: "correct horse battery staple",
      reason: "Alice 请求找回文件",
      scope: "space",
    });
  });

  it("lets an administrator view a member's private photo library as its own grant", async () => {
    const alice: User = {
      id: "user:alice",
      username: "alice",
      role: "member",
      status: "active",
      createdAt: "2026-10-07T10:00:00Z",
      mustChangePassword: false,
    };
    const fetchMock = installAPI({ users: [session.user, alice] });
    const user = userEvent.setup();
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    await openSettingsSection(user, desktop, "用户与权限");
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
    const call = fetchMock.mock.calls.find(([path]) => path === "/api/v1/users/user%3Aalice/viewing");
    expect(JSON.parse(String(call?.[1]?.body))).toEqual({
      password: "correct horse battery staple",
      reason: "找婚礼照片",
      scope: "library",
    });
  });
});

describe("Settings", () => {
  const alice: User = {
    id: "user:alice",
    username: "alice",
    role: "member",
    status: "active",
    createdAt: "2026-10-07T10:00:00Z",
    mustChangePassword: false,
  };

  it("shows members their account, task manager, overview and read-only storage", async () => {
    const fetchMock = installAPI({ session: { ...session, user: alice } });
    const user = userEvent.setup();
    render(<App />);
    await openSettingsSection(user, await screen.findByRole("region", { name: "桌面应用" }), "存储");
    const settings = screen.getByRole("dialog", { name: "设置" });
    const sections = within(settings).getByRole("navigation", { name: "设置分区" });
    const enabled = within(sections)
      .getAllByRole("button")
      .filter((button) => !button.hasAttribute("disabled"));
    expect(enabled.map((button) => button.getAttribute("aria-label") ?? button.textContent)).toEqual([
      "我的账号",
      "概览",
      "任务管理器",
      "存储",
      "日志",
    ]);
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
    await waitFor(() =>
      expect(
        fetchMock.mock.calls.some(
          ([path]) => String(path).startsWith("/api/v1/activity?") && String(path).includes("category=session"),
        ),
      ).toBe(true),
    );
    expect(within(settings).queryByRole("tab", { name: "系统日志" })).toBeNull();
  });

  it("keeps showing a member who reset their password", async () => {
    installAPI({
      session: { ...session, user: alice, credentialReset: { actorUsername: "owner", at: "2026-10-14T09:10:00Z" } },
    });
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
    expect(JSON.parse(String(call?.[1]?.body))).toEqual({
      currentPassword: "alice old password",
      newPassword: "alice new password",
    });
  });

  it("keeps a pending storage plan across sections and when settings closes", async () => {
    installAPI({
      storagePlan: {
        id: "plan:pending",
        kind: "create_volume",
        layout: "single",
        capacityAfterBytes: 512000000000,
        disks: [
          {
            id: "disk:fake-data-01",
            model: "A-NAS Fake HDD",
            capacityBytes: 512000000000,
            fingerprint: "pending-fingerprint",
            signatures: [],
          },
        ],
        confirmationPhrase: "ERASE data-01",
        actions: [],
        state: "planned",
        createdAt: "2026-10-07T10:00:00Z",
        expiresAt: "2099-01-01T00:00:00Z",
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
    installAPI({
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
    installAPI({
      volumes: [
        {
          id: "volume:data",
          diskIds: ["disk:data"],
          layout: "single",
          capacityBytes: 500_107_862_016,
          state: "available",
          filesystemUuid: "09e275fe-794a",
        },
      ],
    });
    const user = userEvent.setup();
    render(<App />);
    await openSettingsSection(user, await screen.findByRole("region", { name: "桌面应用" }), "存储");
    const settings = screen.getByRole("dialog", { name: "设置" });
    expect(await within(settings).findByText("已用空间未知 · 共 466 GiB")).toBeTruthy();
    expect(within(settings).getByText("在线")).toBeTruthy();
    expect(within(settings).queryByRole("meter", { name: "数据卷已用空间" })).toBeNull();
    expect(within(settings).queryByRole("region", { name: "初始化数据卷" })).toBeNull();
  });

  it("shows unusable devices with reasons and never offers them for initialization", async () => {
    const unusable: Disk[] = [
      {
        ...healthyState.disks[0],
        id: "observation:empty",
        model: "",
        capacityBytes: 0,
        eligibleForDataVolume: false,
        ineligibleReasons: ["missing_model", "missing_capacity", "missing_identity"],
      },
      ...["one", "two"].map(
        (name): Disk => ({
          ...healthyState.disks[0],
          id: `observation:${name}`,
          model: `Bridge ${name}`,
          eligibleForDataVolume: false,
          ineligibleReasons: ["duplicate_identity"],
        }),
      ),
    ];
    installAPI({ hostState: { ...healthyState, disks: [...healthyState.disks, ...unusable] }, volumes: [] });
    const user = userEvent.setup();
    render(<App />);
    await openSettingsSection(user, await screen.findByRole("region", { name: "桌面应用" }), "存储");
    const settings = screen.getByRole("dialog", { name: "设置" });
    expect(await within(settings).findByText("未识别设备")).toBeTruthy();
    expect(within(settings).getByText("容量未知")).toBeTruthy();
    expect(within(settings).getAllByText("硬盘身份与其他设备重复，不能安全选择")).toHaveLength(2);
    await user.click(within(settings).getByRole("button", { name: "初始化数据卷…" }));
    const candidates = within(settings).getByRole("group", { name: "候选硬盘" });
    for (const box of within(candidates).getAllByRole("checkbox")) {
      const input = box as HTMLInputElement;
      expect(input.disabled).toBe(input.value.startsWith("observation:"));
    }
    expect(within(candidates).getAllByRole("checkbox")).toHaveLength(4);
  });

  it("adds a new disk to the data volume as a mirror", async () => {
    const dataDisk: Disk = {
      ...healthyState.disks[0],
      id: "disk:data",
      model: "ST500DM002-1BD142",
      capacityBytes: 500_107_862_016,
      role: "data",
      inUse: true,
      filesystems: ["btrfs"],
      eligibleForDataVolume: false,
      ineligibleReasons: ["data_volume", "in_use"],
    };
    const spareDisk: Disk = {
      ...healthyState.disks[0],
      id: "disk:spare",
      model: "ST500DM002-1BD142",
      capacityBytes: 500_107_862_016,
      filesystems: ["linux_raid_member"],
    };
    const usbDisk: Disk = {
      ...healthyState.disks[0],
      id: "disk:usb",
      model: "USB Stick",
      transport: "usb",
      removable: true,
      eligibleForDataVolume: false,
      ineligibleReasons: ["unsupported_transport", "removable"],
    };
    const plan: StoragePlan = {
      id: "plan:add",
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
      state: "planned",
      createdAt: "2026-10-07T10:00:00Z",
      expiresAt: "2099-01-01T00:00:00Z",
    };
    const fetchMock = installAPI({
      hostState: { ...healthyState, disks: [dataDisk, spareDisk, usbDisk] },
      volumes: [
        {
          id: "volume:data",
          diskIds: ["disk:data"],
          layout: "single",
          capacityBytes: 480 * 1024 ** 3,
          availableBytes: 280 * 1024 ** 3,
          state: "available",
          filesystemUuid: "09e275fe-794a",
        },
      ],
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
    const created = fetchMock.mock.calls.find(
      ([path, init]) => path === "/api/v1/storage/plans" && init?.method === "POST",
    );
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
        id: "plan:leave",
        kind: "create_volume",
        layout: "single",
        capacityAfterBytes: 512000000000,
        disks: [
          {
            id: "disk:fake-data-01",
            model: "A-NAS Fake HDD",
            capacityBytes: 512000000000,
            fingerprint: "leave-fingerprint",
            signatures: [],
          },
        ],
        confirmationPhrase: "ERASE data-01",
        actions: [],
        state: "planned",
        createdAt: "2026-10-07T10:00:00Z",
        expiresAt: "2099-01-01T00:00:00Z",
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
    expect(
      fetchMock.mock.calls.some(
        ([path, init]) => path === "/api/v1/storage/plans/plan%3Aleave/cancel" && init?.method === "POST",
      ),
    ).toBe(true);
    expect(await within(settings).findByRole("heading", { name: "尚未创建数据卷" })).toBeTruthy();
    expect(within(settings).queryByRole("region", { name: "初始化数据卷" })).toBeNull();

    await user.click(within(settings).getByRole("button", { name: "初始化数据卷…" }));
    await user.click(within(settings).getByRole("button", { name: "生成格式化计划" }));
    await user.click(await within(settings).findByRole("button", { name: "取消计划并关闭" }));
    expect(await within(settings).findByRole("heading", { name: "尚未创建数据卷" })).toBeTruthy();
  });

  it("follows a plan that runs on the device when the page comes back", async () => {
    const plan: StoragePlan = {
      id: "plan:run",
      kind: "create_volume",
      layout: "single",
      capacityAfterBytes: 512000000000,
      disks: [
        {
          id: "disk:fake-data-01",
          model: "A-NAS Fake HDD",
          capacityBytes: 512000000000,
          fingerprint: "run-fingerprint",
          signatures: [],
        },
      ],
      confirmationPhrase: "ERASE data-01",
      actions: [],
      state: "running",
      step: "format",
      createdAt: "2026-10-07T10:00:00Z",
      expiresAt: "2099-01-01T00:00:00Z",
    };
    const fetchMock = installAPI({
      unfinishedPlans: [plan],
      operation: { ...plan, state: "succeeded", step: undefined },
    });
    const user = userEvent.setup();
    render(<App />);
    await openSettingsSection(user, await screen.findByRole("region", { name: "桌面应用" }), "存储");
    const settings = screen.getByRole("dialog", { name: "设置" });
    const wizard = await within(settings).findByRole("region", { name: "初始化数据卷" });
    expect(within(wizard).getByText("正在格式化，固态硬盘可能要几分钟…")).toBeTruthy();
    expect(within(wizard).getByText(/关闭这个窗口或页面不会打断它/)).toBeTruthy();
    expect(await within(wizard).findByText("数据卷已创建")).toBeTruthy();
    expect(fetchMock.mock.calls.some(([path]) => path === "/api/v1/operations/plan%3Arun")).toBe(true);
  });

  it("offers to retry a plan that stopped on the device", async () => {
    const plan: StoragePlan = {
      id: "plan:stopped",
      kind: "create_volume",
      layout: "single",
      capacityAfterBytes: 512000000000,
      disks: [
        {
          id: "disk:fake-data-01",
          model: "A-NAS Fake HDD",
          capacityBytes: 512000000000,
          fingerprint: "stopped-fingerprint",
          signatures: [],
        },
      ],
      confirmationPhrase: "ERASE data-01",
      actions: [],
      state: "needs_attention",
      step: "mount",
      createdAt: "2026-10-07T10:00:00Z",
      expiresAt: "2026-10-21T09:10:00Z",
      failure: "the Host Agent stopped during the mount step; retrying continues after the steps that are done",
    };
    const fetchMock = installAPI({ unfinishedPlans: [plan], storagePlan: plan });
    const user = userEvent.setup();
    render(<App />);
    await openSettingsSection(user, await screen.findByRole("region", { name: "桌面应用" }), "存储");
    const settings = screen.getByRole("dialog", { name: "设置" });
    expect(await within(settings).findByText("上次的存储操作没有完成")).toBeTruthy();
    expect(within(settings).getByText(/创建数据卷在“挂载”这一步停下了/)).toBeTruthy();

    await user.click(within(settings).getByRole("button", { name: "查看并处理" }));
    const wizard = within(settings).getByRole("region", { name: "初始化数据卷" });
    expect(within(wizard).getByText("需要人工处理")).toBeTruthy();
    expect(within(wizard).getByText(/已经完成的步骤不会重做/)).toBeTruthy();
    await user.click(within(wizard).getByRole("button", { name: "重试" }));
    expect(
      fetchMock.mock.calls.some(
        ([path, init]) => path === "/api/v1/storage/plans/plan%3Astopped/execute" && init?.method === "POST",
      ),
    ).toBe(true);
    expect(await within(wizard).findByText("数据卷已创建")).toBeTruthy();
  });

  it("shows why a retried plan stopped again", async () => {
    const plan: StoragePlan = {
      id: "plan:again",
      kind: "add_disks",
      layout: "mirror",
      capacityAfterBytes: 512000000000,
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
      actions: [],
      state: "needs_attention",
      step: "erase",
      createdAt: "2026-10-07T10:00:00Z",
      expiresAt: "2026-10-21T09:10:00Z",
      failure: "the Host Agent stopped during the erase step; retrying continues after the steps that are done",
    };
    installAPI({
      unfinishedPlans: [plan],
      storagePlan: plan,
      executeFails: true,
      operation: { ...plan, step: "erase", failure: "disk identity changed after planning" },
    });
    const user = userEvent.setup();
    render(<App />);
    await openSettingsSection(user, await screen.findByRole("region", { name: "桌面应用" }), "存储");
    const settings = screen.getByRole("dialog", { name: "设置" });
    await user.click(await within(settings).findByRole("button", { name: "查看并处理" }));
    const wizard = within(settings).getByRole("region", { name: "设置硬盘用途" });
    await user.click(within(wizard).getByRole("button", { name: "重试" }));
    expect(await within(wizard).findByText(/disk identity changed after planning/)).toBeTruthy();
    expect(within(wizard).queryByText(/the Host Agent stopped during the erase step/)).toBeNull();
    expect(within(wizard).queryByRole("alert")).toBeNull();
  });

  it("shows how far a conversion has come and continues a stopped one", async () => {
    const disks: Disk[] = [
      {
        ...healthyState.disks[0],
        id: "disk:data",
        role: "data",
        inUse: true,
        eligibleForDataVolume: false,
        ineligibleReasons: ["data_volume"],
      },
      {
        ...healthyState.disks[0],
        id: "disk:spare",
        role: "data",
        inUse: true,
        eligibleForDataVolume: false,
        ineligibleReasons: ["data_volume"],
      },
    ];
    const fetchMock = installAPI({
      hostState: { ...healthyState, disks },
      volumes: [
        {
          id: "volume:data",
          diskIds: ["disk:data", "disk:spare"],
          layout: "mirror",
          capacityBytes: 480 * 1024 ** 3,
          availableBytes: 280 * 1024 ** 3,
          state: "available",
          conversion: { state: "waiting", convertedBytes: 80 * 1024 ** 3, totalBytes: 200 * 1024 ** 3 },
        },
      ],
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
    expect(
      fetchMock.mock.calls.some(
        ([path, init]) => path === "/api/v1/volumes/volume%3Adata/conversion" && init?.method === "POST",
      ),
    ).toBe(true);
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
    const created = fetchMock.mock.calls.find(
      ([path, init]) => path === "/api/v1/storage/plans" && init?.method === "POST",
    );
    expect(JSON.parse(String(created?.[1]?.body))).toEqual({
      diskIds: ["disk:fake-data-01", "disk:fake-data-02"],
      layout: "span",
    });
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
    const call = fetchMock.mock.calls.find(
      ([path, init]) => path === "/api/v1/users/user%3Aalice/credential" && init?.method === "PATCH",
    );
    expect(JSON.parse(String(call?.[1]?.body))).toEqual({ password: "temporary password 1" });

    await user.click(within(settings).getByRole("button", { name: "修改我的密码" }));
    expect(within(settings).getByRole("button", { name: "我的账号" }).getAttribute("aria-current")).toBe("page");
    expect(within(settings).getByRole("form", { name: "修改密码" })).toBeTruthy();
    // The request opens the form once; coming back to My account later does not.
    await user.click(within(settings).getByRole("button", { name: "概览" }));
    await user.click(within(settings).getByRole("button", { name: "我的账号" }));
    expect(within(settings).queryByRole("form", { name: "修改密码" })).toBeNull();
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
    const created = () =>
      fetchMock.mock.calls.filter(([path, init]) => path === "/api/v1/users" && init?.method === "POST");

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
    await user.click(
      within(within(settings).getByRole("form", { name: "禁用 alice" })).getByRole("button", { name: "禁用账号" }),
    );
    expect(await within(settings).findByText("已禁用 alice。")).toBeTruthy();
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/users/user%3Aalice", expect.objectContaining({ method: "DELETE" }));
  });
});

describe("Avatars and nicknames", () => {
  const alice: User = {
    id: "user:alice",
    username: "alice",
    role: "member",
    status: "active",
    createdAt: "2026-10-07T10:00:00Z",
    mustChangePassword: false,
  };

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
    expect(
      within(within(settings).getByRole("navigation", { name: "设置分区" })).getByRole("button", { name: "我的账号" })
        .textContent,
    ).toContain("小爱");
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
    await user.upload(
      within(picker).getByLabelText("从本机选择图片"),
      new File(["png"], "me.png", { type: "image/png" }),
    );

    expect((await within(picker).findByTestId("cropper")).getAttribute("data-image")).toBe("blob:avatar-source");
    expect(within(picker).getByRole("heading", { name: "调整头像" })).toBeTruthy();
    await user.click(within(picker).getByRole("button", { name: "保存头像" }));
    expect(await within(settings).findByText("头像已更新。")).toBeTruthy();
    expect(within(settings).queryByRole("dialog", { name: "更换头像" })).toBeNull();

    // The page sends the chosen square, drawn on white at no more than 512 px.
    expect(drawImage).toHaveBeenCalledWith(expect.anything(), 40, 0, 320, 320, 0, 0, 320, 320);
    const sent = fetchMock.mock.calls.find(
      ([path, init]) => path === "/api/v1/session/avatar" && init?.method === "PUT",
    );
    expect((sent?.[1]?.body as Blob | undefined)?.type).toBe("image/jpeg");
    expect(new Headers(sent?.[1]?.headers).get("X-CSRF-Token")).toBe("csrf-test");
    const rail = screen.getByRole("button", { name: "账号（alice）" });
    expect(rail.querySelector("img")?.getAttribute("src")).toBe("/api/v1/users/user%3Aalice/avatar?v=0123456789abcdef");

    await user.click(within(settings).getByRole("button", { name: "移除" }));
    expect(await within(settings).findByText("已移除头像，现在显示名字的第一个字。")).toBeTruthy();
    expect(
      fetchMock.mock.calls.some(([path, init]) => path === "/api/v1/session/avatar" && init?.method === "DELETE"),
    ).toBe(true);
    expect(rail.querySelector("img")).toBeNull();
    expect(rail.textContent).toContain("A");
  });

  it("picks the avatar from a picture on the NAS, but not from a space being viewed", async () => {
    stubPictures();
    installAPI({
      session: { ...session, user: alice },
      spaces: [
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
      ],
      entries: [
        {
          id: "file:trip",
          spaceId: "space:alice",
          name: "海边.jpg",
          kind: "file",
          sizeBytes: 2048,
          modifiedAt: "2026-10-07T10:00:00Z",
        },
        {
          id: "file:notes",
          spaceId: "space:alice",
          name: "notes.txt",
          kind: "file",
          sizeBytes: 12,
          modifiedAt: "2026-10-07T10:00:00Z",
        },
        {
          id: "dir:2026",
          spaceId: "space:alice",
          name: "2026",
          kind: "directory",
          sizeBytes: 0,
          modifiedAt: "2026-10-07T10:00:00Z",
        },
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
    expect(
      within(spaces)
        .getAllByRole("button")
        .map((button) => button.textContent),
    ).toEqual(["个人空间", "共享空间"]);
    expect(within(picker).getByRole("button", { name: "2026" })).toBeTruthy();
    expect(within(picker).queryByText("notes.txt")).toBeNull();
    await user.click(within(picker).getByRole("button", { name: "选择 海边.jpg" }));
    expect((await within(picker).findByTestId("cropper")).getAttribute("data-image")).toBe(
      "/api/v1/files/file%3Atrip/content?disposition=inline",
    );
    await user.click(within(picker).getByRole("button", { name: "重新选择图片" }));
    expect(within(picker).getByRole("tab", { name: "NAS 文件" })).toBeTruthy();
    await user.keyboard("{Escape}");
    expect(within(settings).queryByRole("dialog", { name: "更换头像" })).toBeNull();
  });

  it("shows other accounts by nickname and keeps the account name where it matters", async () => {
    installAPI({
      users: [session.user, { ...alice, nickname: "小爱", avatarVersion: "feedface00000000" }],
      profiles: [
        { id: "user:owner", username: "owner", nickname: "爸爸" },
        { id: "user:alice", username: "alice", nickname: "小爱", avatarVersion: "feedface00000000" },
      ],
      notifications: [
        { id: "notification:1", kind: "credential_reset", actorUsername: "owner", createdAt: "2026-10-07T10:00:00Z" },
      ],
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
