import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setSession } from "../api";
import type { ContainerSnapshot, ContainerSummary } from "../containersApi";
import { DockerPanel } from "./DockerPanel";

function container(name: string, overrides: Partial<ContainerSummary> = {}): ContainerSummary {
  return {
    id: Array.from(name, (char) => char.charCodeAt(0).toString(16)).join("").padEnd(64, "0").slice(0, 64), name, image: `${name}:1`, imageId: `sha256:${name}`,
    state: "running", status: "Up 2 days", createdAt: "2026-10-01T00:00:00Z", ports: [], ...overrides,
  };
}

const snapshot: ContainerSnapshot = {
  dataSource: "live",
  observedAt: "2026-10-06T00:00:00Z",
  engine: { version: "29.1.3", apiVersion: "1.52" },
  containers: [
    container("immich-server", { project: "a-nas-immich", status: "Up 2 days (healthy)", ports: [{ hostPort: 2283, containerPort: 2283, protocol: "tcp" }], usage: { cpuPercent: 3.1, memoryBytes: 1 << 30, memoryLimitBytes: 8 << 30 } }),
    container("immich-redis", { project: "a-nas-immich", state: "exited", status: "Exited (1) 2 hours ago" }),
    container("watchtower", { state: "exited", status: "Exited (0) 3 days ago" }),
  ],
  images: [
    { id: "sha256:immich-server", tags: ["ghcr.io/immich-app/immich-server:v2.7.2"], sizeBytes: 1_780_000_000, createdAt: "2026-09-20T00:00:00Z" },
    { id: "sha256:old", tags: ["ghcr.io/immich-app/immich-server:v2.6.0"], sizeBytes: 1_700_000_000, createdAt: "2026-08-20T00:00:00Z" },
  ],
};

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

function serve() {
  const actions: Array<{ url: string; body: unknown }> = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (init?.method === "POST") {
      actions.push({ url, body: JSON.parse(String(init.body)) });
      return new Response(null, { status: 204 });
    }
    if (url === "/api/v1/apps") return json({ dataSource: "live", apps: [{ id: "immich", title: "Immich", webPort: 2283, scheme: "http", path: "/" }] });
    if (url.includes("/logs")) return json({ lines: [{ stream: "stderr", time: "2026-10-06T00:00:00Z", text: "redis: connection refused" }] });
    return json(snapshot);
  }));
  return actions;
}

beforeEach(() => {
  setSession({ csrfToken: "csrf-test", expiresAt: "2026-10-13T22:00:00Z", user: { id: "user:owner", username: "owner", role: "admin", status: "active", createdAt: "2026-10-13T00:00:00Z" } });
});
afterEach(() => vi.unstubAllGlobals());

describe("DockerPanel", () => {
  it("groups containers under their App Center app and surfaces the crashed one", async () => {
    serve();
    const user = userEvent.setup();
    render(<DockerPanel />);

    const immich = await screen.findByRole("region", { name: "Immich" });
    expect(within(immich).getByText("1/2 个运行中")).toBeTruthy();
    const redis = within(immich).getByRole("article", { name: "容器 immich-redis" });
    expect(within(redis).getByText("异常退出")).toBeTruthy();
    expect(within(redis).getByText("2 小时前停止 · 退出码 1")).toBeTruthy();
    expect(within(screen.getByRole("region", { name: "独立容器" })).getByText("已停止")).toBeTruthy();

    const nav = screen.getByRole("navigation", { name: "Docker 导航" });
    await user.click(within(nav).getByRole("button", { name: /^需要关注/ }));
    expect(screen.getAllByRole("article").map((row) => row.getAttribute("aria-label"))).toEqual(["容器 immich-redis"]);

    await user.click(screen.getByRole("button", { name: "immich-redis" }));
    expect(screen.getByText("容器异常退出（退出码 1）")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "查看日志" }));
    expect(within(await screen.findByRole("log", { name: "immich-redis 日志" })).getByText("redis: connection refused")).toBeTruthy();
  });

  it("asks before stopping and names the app that will be affected", async () => {
    const actions = serve();
    const user = userEvent.setup();
    render(<DockerPanel />);

    const server = await screen.findByRole("article", { name: "容器 immich-server" });
    await user.click(within(server).getByRole("button", { name: "停止 immich-server" }));
    const confirm = screen.getByRole("dialog", { name: "确认停止 immich-server" });
    expect(within(confirm).getByText("Immich 的部分或全部功能会不可用，之后可以随时重新启动。")).toBeTruthy();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(actions).toEqual([]);

    await user.click(within(server).getByRole("button", { name: "停止 immich-server" }));
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "确认停止" }));
    expect(actions).toEqual([{ url: `/api/v1/containers/${snapshot.containers[0].id}/actions`, body: { action: "stop" } }]);

    // Starting interrupts nothing, so it runs at once.
    await user.click(within(screen.getByRole("article", { name: "容器 watchtower" })).getByRole("button", { name: "启动 watchtower" }));
    expect(actions[1].body).toEqual({ action: "start" });
  });

  it("opens on an app's containers when the App Center asks", async () => {
    serve();
    const handled = vi.fn();
    render(<DockerPanel focus={{ project: "a-nas-immich", at: 1 }} onFocusHandled={handled} />);

    expect(await screen.findByRole("heading", { name: "Immich" })).toBeTruthy();
    expect(screen.getAllByRole("article")).toHaveLength(2);
    expect(screen.getByRole("link", { name: /打开 Immich/ }).getAttribute("href")).toMatch(/:2283\/$/);
    expect(handled).toHaveBeenCalledOnce();
  });

  it("shows which containers use each image and finds images by search", async () => {
    serve();
    const user = userEvent.setup();
    render(<DockerPanel />);

    const nav = await screen.findByRole("navigation", { name: "Docker 导航" });
    await user.click(await within(nav).findByRole("button", { name: /^镜像/ }));
    const table = screen.getByRole("table", { name: "镜像列表" });
    expect(within(within(table).getByRole("row", { name: "ghcr.io/immich-app/immich-server:v2.7.2" })).getByRole("button", { name: "immich-server" })).toBeTruthy();
    expect(within(within(table).getByRole("row", { name: "ghcr.io/immich-app/immich-server:v2.6.0" })).getByText("未使用")).toBeTruthy();
    expect(screen.getByText("2 个 · 1 个未被容器使用")).toBeTruthy();

    await user.type(within(nav).getByRole("textbox", { name: "搜索容器或镜像" }), "v2.6");
    expect(screen.queryAllByRole("article")).toHaveLength(0);
    expect(within(screen.getByRole("table", { name: "镜像列表" })).getAllByRole("row")).toHaveLength(2);
  });

  it("explains when Docker is not enabled", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => json({ error: { code: "containers_disabled", message: "disabled" } }, 503)));
    render(<DockerPanel />);
    expect(await screen.findByText("Docker 未启用")).toBeTruthy();
  });
});
