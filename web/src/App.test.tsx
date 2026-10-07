import { act, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import App from "./App";

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

afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); });

describe("A-NAS v1.0.1 desktop", () => {
  it("requires the one-time code for first-run administrator setup", async () => {
    const fetchMock = installAPI({ setupRequired: true });
    const user = userEvent.setup();
    render(<App />);
    expect(await screen.findByRole("heading", { name: "初始化管理员" })).toBeTruthy();
    await user.type(screen.getByLabelText("一次性初始化码"), "setup-once");
    await user.type(screen.getByLabelText("账号"), "owner");
    await user.type(screen.getByLabelText("密码"), "correct horse battery staple");
    await user.click(screen.getByRole("button", { name: "创建管理员" }));
    expect(await screen.findByRole("region", { name: "桌面应用" })).toBeTruthy();
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/setup/admin", expect.objectContaining({ method: "POST" }));
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
    const before = screen.getByText(/\d{2}:\d{2}/, { selector: ".clock" }).textContent;
    act(() => vi.advanceTimersByTime(60_000));
    expect(screen.getByText(/\d{2}:\d{2}/, { selector: ".clock" }).textContent).not.toBe(before);
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

  it("opens, minimizes, and restores system settings", async () => {
    installAPI(); const user = userEvent.setup(); render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    await user.click(within(desktop).getByRole("button", { name: "打开系统设置" }));
    const settings = screen.getByRole("dialog", { name: "系统设置" });
    await user.click(within(settings).getByRole("button", { name: "最小化系统设置" }));
    await user.click(screen.getByRole("button", { name: "恢复系统设置" }));
    expect(screen.getByRole("dialog", { name: "系统设置" })).toBeTruthy();
  });
});

function installAPI(options: { setupRequired?: boolean; spaces?: unknown[]; entries?: unknown[] } = {}) {
  const mock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input);
    if (path === "/api/v1/setup/status") return ok({ setupRequired: options.setupRequired ?? false });
    if (path === "/api/v1/setup/admin" && init?.method === "POST") return ok(session, 201);
    if (path === "/api/v1/session") return ok(session);
    if (path === "/api/v1/host-state") return ok(healthyState);
    if (path === "/api/v1/spaces") return ok({ items: options.spaces ?? [] });
    if (path.startsWith("/api/v1/spaces/") && path.includes("/entries")) return ok({ items: options.entries ?? [] });
    if (path === "/api/v1/volumes") return ok({ items: [] });
    if (path === "/api/v1/users") return ok({ items: [session.user] });
    if (path === "/api/v1/trash") return ok({ items: [] });
    return ok({ items: [] });
  });
  vi.stubGlobal("fetch", mock);
  return mock;
}

function ok(body: unknown, status = 200) { return { ok: true, status, json: async () => body } as Response; }
