import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HttpResponse } from "msw";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { setSession } from "../api/client";
import { reloadSystemUpdate, resetSystemUpdate } from "../systemupdate/status";
import { session } from "../test/fixtures";
import {
  currentVersion,
  nextVersion,
  type SystemUpdateOptions,
  systemUpdateHandlers,
  systemUpdateStatus,
} from "../test/handlers/systemupdate";
import { http, server } from "../test/server";
import { SystemUpdateSection } from "./SystemUpdateSection";

const current = "1.0.1-rc.6 开发版 83bb9db1";
const next = "1.0.1-rc.6 开发版 4e842653";

function serve(options: SystemUpdateOptions = {}) {
  const update = systemUpdateHandlers(options);
  server.use(...update.handlers);
  return update.sent;
}

const renderPage = () => render(<SystemUpdateSection onOpenLogs={() => undefined} />);

describe("Settings → 系统更新", () => {
  beforeEach(() => setSession(session()));
  afterEach(() => {
    setSession();
    resetSystemUpdate();
  });

  it("installs a version that is ready once the administrator confirms", async () => {
    const sent = serve({ status: systemUpdateStatus({ phase: "ready", candidate: nextVersion, downloadBytes: 0 }) });
    const user = userEvent.setup();
    renderPage();
    expect(await screen.findByRole("heading", { name: `${next} 可以安装` })).toBeTruthy();
    const versions = screen.getByRole("region", { name: "版本" });
    expect(within(versions).getByText("83bb9db1")).toBeTruthy();
    expect(within(versions).getByText("4e842653")).toBeTruthy();
    expect(within(versions).getByText("正式版与开发版")).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "安装" }));
    const dialog = screen.getByRole("dialog", { name: `安装 A-NAS ${next}？` });
    expect(within(dialog).getByText(/服务会重启/)).toBeTruthy();
    await user.click(within(dialog).getByRole("button", { name: "安装" }));

    expect(await screen.findByRole("heading", { name: "已是最新版本" })).toBeTruthy();
    expect(sent.actions).toEqual(["install"]);
    expect(screen.getByText(`从 ${current} 更新到 ${next}`)).toBeTruthy();
    // The page's code is the version before's: it offers to load the new one.
    expect(screen.getByRole("button", { name: "刷新页面" })).toBeTruthy();
    expect(screen.getByRole("button", { name: `回到上一版本 ${current}` })).toBeTruthy();
  });

  it("goes back to the version before once the administrator confirms", async () => {
    const sent = serve({ status: systemUpdateStatus({ current: nextVersion, previous: currentVersion }) });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("button", { name: `回到上一版本 ${current}` }));
    const dialog = screen.getByRole("dialog", { name: `回到 A-NAS ${current}？` });
    expect(within(dialog).getByText(new RegExp(`不会再自动安装 ${next}`))).toBeTruthy();
    await user.click(within(dialog).getByRole("button", { name: "回到这个版本" }));
    expect(await screen.findByText(`从 ${next} 回到 ${current}`)).toBeTruthy();
    expect(sent.actions).toEqual(["revert"]);
    expect(screen.queryByRole("button", { name: /回到上一版本/ })).toBeNull();
  });

  it("holds the install while storage or an app is busy", async () => {
    serve({ status: systemUpdateStatus({ phase: "ready", candidate: nextVersion, waiting: ["apps"] }) });
    renderPage();
    expect(await screen.findByText("有应用正在安装或卸载，结束后可以安装")).toBeTruthy();
    expect(screen.getByRole("button", { name: "安装" })).toBeDisabled();
  });

  it("waits through the services' restart during an install and shows how it ended", async () => {
    serve({
      status: systemUpdateStatus({
        phase: "installing",
        running: true,
        candidate: nextVersion,
        target: nextVersion,
        startedAt: new Date().toISOString(),
      }),
    });
    renderPage();
    expect(await screen.findByRole("heading", { name: `正在安装 ${next}` })).toBeTruthy();
    expect(screen.getByText("安装时服务会重启，恢复后自动刷新")).toBeTruthy();

    // The Product Service restarts: reads fail, the page keeps the install.
    server.use(http.untyped.get("/api/v1/system/update", () => HttpResponse.error()));
    act(() => reloadSystemUpdate());
    expect(await screen.findByText("服务正在重启，恢复后自动刷新")).toBeTruthy();
    expect(screen.getByRole("heading", { name: `正在安装 ${next}` })).toBeTruthy();

    server.use(
      http.get("/api/v1/system/update", ({ response }) =>
        response(200).json(systemUpdateStatus({ current: nextVersion, previous: currentVersion })),
      ),
    );
    act(() => reloadSystemUpdate());
    expect(await screen.findByRole("heading", { name: "已是最新版本" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "刷新页面" })).toBeTruthy();
  });

  it("shows the download as it goes", async () => {
    serve({
      status: systemUpdateStatus({
        phase: "downloading",
        running: true,
        candidate: nextVersion,
        downloadBytes: 400 * 1024 ** 2,
        downloadedBytes: 100 * 1024 ** 2,
      }),
    });
    renderPage();
    expect(await screen.findByRole("heading", { name: `正在下载 ${next}` })).toBeTruthy();
    expect(screen.getByRole("progressbar").getAttribute("aria-valuenow")).toBe("25");
    expect(screen.queryByRole("button", { name: "下载" })).toBeNull();
  });

  it("says an install did not pass its check, went back, and what the check said", async () => {
    const problem = { code: "check" as const, detail: "FAIL anas-api.service is failed" };
    serve({
      status: systemUpdateStatus({
        phase: "rolled-back",
        problem,
        records: [
          {
            at: new Date().toISOString(),
            kind: "rolled-back",
            version: nextVersion,
            from: currentVersion,
            by: "automatic",
            problem,
          },
        ],
      }),
    });
    const user = userEvent.setup();
    renderPage();
    expect(await screen.findByRole("heading", { name: `${next} 安装后检查没有通过，已回到 ${current}` })).toBeTruthy();
    expect(screen.getByText(/自动 · 安装后的检查没有通过/)).toBeTruthy();
    await user.click(screen.getByText("详细信息"));
    expect(screen.getByText("FAIL anas-api.service is failed")).toBeVisible();
    expect(screen.getByRole("button", { name: "检查更新" })).toBeEnabled();
  });

  it("says why a check failed and checks again", async () => {
    const sent = serve({
      status: systemUpdateStatus({
        phase: "check-failed",
        problem: { code: "unreachable", detail: "E: Failed to fetch" },
      }),
    });
    const user = userEvent.setup();
    renderPage();
    expect(await screen.findByRole("heading", { name: "无法检查更新" })).toBeTruthy();
    expect(screen.getByText("连不上软件源")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "重试" }));
    await waitFor(() => expect(sent.actions).toEqual(["check"]));
  });

  it("checks on opening when the last check is an hour old", async () => {
    const sent = serve({
      status: systemUpdateStatus({ checkedAt: new Date(Date.now() - 2 * 3_600_000).toISOString() }),
    });
    renderPage();
    expect(await screen.findByRole("heading", { name: "已是最新版本" })).toBeTruthy();
    await waitFor(() => expect(sent.actions).toEqual(["check"]));
  });

  it("shows why the service refused", async () => {
    serve({ status: systemUpdateStatus({ phase: "ready", candidate: nextVersion }), refuse: "update_busy" });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("button", { name: "安装" }));
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "安装" }));
    expect(await screen.findByText("更新正在进行，请等它结束")).toBeTruthy();
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("saves the automatic download, the automatic install and its window", async () => {
    const sent = serve();
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("switch", { name: "自动下载新版本" }));
    await waitFor(() => expect(sent.settings.at(-1)?.autoDownload).toBe(false));
    expect(screen.queryByLabelText("安装时段开始")).toBeNull();
    await user.click(screen.getByRole("switch", { name: "自动安装" }));
    const start = await screen.findByLabelText("安装时段开始");
    expect(sent.settings.at(-1)).toEqual({
      autoDownload: false,
      autoInstall: true,
      windowStart: "03:00",
      windowEnd: "05:00",
    });

    // The same time twice is no window: nothing is saved.
    fireEvent.change(start, { target: { value: "05:00" } });
    fireEvent.blur(start);
    expect(screen.getByText("开始与结束要是两个不同的时间")).toBeTruthy();
    expect(sent.settings).toHaveLength(2);

    fireEvent.change(start, { target: { value: "23:30" } });
    fireEvent.blur(start);
    await waitFor(() => expect(sent.settings.at(-1)?.windowStart).toBe("23:30"));
    expect(sent.settings).toHaveLength(3);
  });
});
