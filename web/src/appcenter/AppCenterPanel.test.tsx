import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setSession } from "../api/client";
import type { AppJob } from "../api.generated";
import { session } from "../test/fixtures";
import { app, appsHandlers, job, memosPlan } from "../test/handlers/apps";
import { apiError, http, pathOf, server } from "../test/server";
import { AppCenterPanel } from "./AppCenterPanel";
import type { CatalogApp, InstallPlan } from "./api";

// Navidrome as the current container agent plans it: the app identity reads
// Music through a-nas-users; a second service keeps its image's user.
const navidromePlan: InstallPlan = {
  ...memosPlan,
  appId: "navidrome",
  title: "Navidrome",
  project: "a-nas-navidrome",
  identity: { username: "app-navidrome", uid: 30004, gid: 30004 },
  services: [
    { service: "navidrome", runAs: "app", user: "30004:30004", groups: ["20000"], capDrop: ["ALL"], shared: "read" },
    { service: "cache", runAs: "image" },
  ],
  images: ["deluan/navidrome:0.61.2"],
  containers: ["navidrome"],
  mounts: [
    { hostPath: "/srv/a-nas/data/apps/navidrome/data", containerPath: "/data", kind: "appdata", readOnly: false },
    { hostPath: "/srv/a-nas/data/spaces/shared/Media/Music", containerPath: "/music", kind: "shared", readOnly: true },
  ],
};

// serve answers the App Center API from a list the test changes as the server
// would, and returns the installs and uninstalls it accepted. Like the server,
// it refuses a write without the session's CSRF token.
function serve(apps: () => CatalogApp[], onPost: (path: string) => AppJob = () => job(), plan = memosPlan) {
  const posts: string[] = [];
  const accept = (request: Request) => {
    if (request.headers.get("X-CSRF-Token") !== session().csrfToken) return undefined;
    posts.push(pathOf(request));
    return onPost(pathOf(request));
  };
  server.use(
    ...appsHandlers(apps, plan),
    http.post("/api/v1/apps/{appId}/install", ({ request, response }) => {
      const started = accept(request);
      return started ? response(202).json(started) : response(403).json(apiError("csrf_invalid"));
    }),
    http.post("/api/v1/apps/{appId}/uninstall", ({ request, response }) => {
      const started = accept(request);
      return started ? response(202).json(started) : response(403).json(apiError("csrf_invalid"));
    }),
  );
  return posts;
}

// The catalog the desktop's tests used: Memos and Navidrome, neither installed.
function catalog(memos: Partial<CatalogApp> = {}): CatalogApp[] {
  return [
    app("memos", { title: "Memos", tagline: "轻量笔记", category: "notes", webPort: 5230, ...memos }),
    app("navidrome", { title: "Navidrome", tagline: "音乐流媒体", category: "media", webPort: 4533 }),
  ];
}

// The panel polls; a visibility change asks it to read the list right away.
async function refresh() {
  await act(async () => {
    document.dispatchEvent(new Event("visibilitychange"));
  });
}

beforeEach(() => {
  setSession(session({}, { expiresAt: "2026-10-12T22:00:00Z" }));
});

afterEach(() => {
  setSession();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("AppCenterPanel", () => {
  it("browses A-NAS categories and ranks search matches by where they match", async () => {
    serve(() => [
      app("memos", { tagline: "轻量笔记", description: "也能同步到手机" }),
      app("navidrome", { category: "media", tagline: "音乐流媒体" }),
      app("syncthing", { category: "files", tagline: "去中心化文件同步" }),
    ]);
    const user = userEvent.setup();
    render(<AppCenterPanel />);

    const discover = await screen.findByRole("region", { name: "影音" });
    expect(within(discover).getByRole("button", { name: "Navidrome，可安装" })).toBeTruthy();
    expect(screen.getByRole("region", { name: "笔记与知识" })).toBeTruthy();

    const nav = screen.getByRole("navigation", { name: "应用中心导航" });
    expect(within(nav).queryByRole("button", { name: /阅读/ })).toBeNull();
    await user.click(within(nav).getByRole("button", { name: /影音/ }));
    expect(screen.getByRole("heading", { name: "影音" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Memos/ })).toBeNull();

    await user.type(within(nav).getByRole("textbox", { name: "搜索应用" }), "同步");
    expect(screen.getByText("找到 2 个应用")).toBeTruthy();
    const results = screen.getAllByRole("button", { name: /，可安装$/ }).map((button) => button.textContent);
    expect(results).toEqual(["Syncthing", "Memos"]);

    await user.type(within(nav).getByRole("textbox", { name: "搜索应用" }), "zzz");
    expect(screen.getByText("没有匹配的应用")).toBeTruthy();
    await user.keyboard("{Escape}");
    expect((within(nav).getByRole("textbox", { name: "搜索应用" }) as HTMLInputElement).value).toBe("");
  });

  it("renders manifest Markdown without images and shows first-use notes", async () => {
    serve(() => [
      app("memos", {
        description: "**轻量**笔记，见[官网](https://usememos.com)。\n![截图](https://example.com/shot.png)",
        notes: "| 用户名 | 密码 |\n| --- | --- |\n| `admin` | `admin123` |",
        repo: "https://github.com/usememos/memos",
      }),
    ]);
    const user = userEvent.setup();
    render(<AppCenterPanel />);
    await user.click(await screen.findByRole("button", { name: "Memos，可安装" }));

    const notes = screen.getByRole("region", { name: "使用须知" });
    expect(await within(notes).findByRole("table")).toBeTruthy();
    expect(within(notes).getByText("admin123").tagName).toBe("CODE");
    expect(await screen.findByText("轻量")).toHaveProperty("tagName", "STRONG");
    expect(screen.getByRole("link", { name: "官网" }).getAttribute("target")).toBe("_blank");
    expect(screen.getByRole("link", { name: /源码/ }).getAttribute("href")).toBe("https://github.com/usememos/memos");
    // The description's image is dropped, not shown.
    expect(screen.queryByRole("img", { name: "截图" })).toBeNull();
    expect(document.querySelector('img[src="https://example.com/shot.png"]')).toBeNull();
  });

  it("shows notes written for A-NAS and keeps the manifest's own folded away", async () => {
    serve(() => [
      app("immich", {
        notes: "照片保存在**共享空间 › Gallery › immich**。\n\n## 手机备份\n\n服务器地址填 `http://<NAS 地址>:2283`。",
        upstreamNotes:
          "## 授权的媒体文件和数据\n1. 点击应用右上角的设置图标。\n2. 在 Volumes 部分映射您的相册存储位置。",
      }),
      app("memos", { notes: "默认账号 `admin`" }),
    ]);
    const user = userEvent.setup();
    render(<AppCenterPanel />);
    await user.click(await screen.findByRole("button", { name: "Immich，可安装" }));

    const notes = screen.getByRole("region", { name: "使用须知" });
    expect(await within(notes).findByText("共享空间 › Gallery › immich")).toHaveProperty("tagName", "STRONG");
    // Note headings sit below the section's own h3.
    expect(within(notes).getByRole("heading", { name: "手机备份" }).tagName).toBe("H4");
    const upstream = notes.querySelector("details")!;
    expect(upstream.open).toBe(false);
    expect(within(upstream).getByText("上游原始说明")).toBeTruthy();
    expect(within(upstream).getByText(/在 A-NAS 中不适用/)).toBeTruthy();
    expect(within(upstream).getByText(/右上角的设置图标/)).toBeTruthy();
    expect(within(notes).queryByText(/文件夹与端口以安装计划为准/)).toBeNull();

    // The detail's back button is named after the list it returns to.
    const nav = screen.getByRole("navigation", { name: "应用中心导航" });
    await user.click(screen.getAllByRole("button", { name: "发现" }).find((button) => !nav.contains(button))!);
    await user.click(await screen.findByRole("button", { name: "Memos，可安装" }));
    const memosNotes = screen.getByRole("region", { name: "使用须知" });
    expect(memosNotes.querySelector("details")).toBeNull();
    expect(within(memosNotes).getByText("来自应用的上游清单；文件夹与端口以安装计划为准。")).toBeTruthy();
  });

  it("says why an installed app's container is not running", async () => {
    serve(() => [
      app("n8n", {
        state: "installed",
        running: 0,
        total: 1,
        webPort: 5678,
        problems: [{ service: "n8n", state: "restarting", exitCode: 1 }],
      }),
    ]);
    const user = userEvent.setup();
    render(<AppCenterPanel onOpenDocker={() => undefined} />);
    await user.click(await screen.findByRole("button", { name: /^N8n/ }));
    const status = await screen.findByRole("region", { name: "运行状态" });
    expect(within(status).getByText("已安装 · 0/1 个容器运行中")).toBeTruthy();
    expect(within(status).getByRole("list", { name: "没有运行的容器" }).textContent).toContain(
      "容器 n8n 不断重启（退出码 1）",
    );
  });

  it("keeps a generated login hidden until asked, and copies it", async () => {
    const secret = "generated-for-this-nas";
    serve(() => [
      app("openlist", {
        state: "installed",
        running: 1,
        total: 1,
        credentials: [{ label: "管理员密码", username: "admin", secret }],
      }),
    ]);
    const copy = vi.fn(() => true);
    Object.defineProperty(document, "execCommand", { value: copy, configurable: true });
    const user = userEvent.setup();
    render(<AppCenterPanel />);
    await user.click(await screen.findByRole("button", { name: /^Openlist/ }));
    const logins = await screen.findByLabelText("登录信息");
    expect(within(logins).getByText("admin")).toBeTruthy();
    expect(logins.textContent).not.toContain(secret);

    await user.click(within(logins).getByRole("button", { name: "显示管理员密码" }));
    expect(within(logins).getByText(secret)).toBeTruthy();
    const copyButton = within(logins).getByRole("button", { name: "复制管理员密码" });
    await user.click(copyButton);
    expect(copy).toHaveBeenCalledWith("copy");
    expect(within(logins).getByRole("button", { name: "已复制" })).toBe(copyButton);
    expect(document.activeElement).toBe(copyButton);
    expect(logins.textContent).toContain("随机生成");
    Reflect.deleteProperty(document, "execCommand");
  });

  it("warns that an app installed before generated logins still uses the published one", async () => {
    serve(() => [
      app("photoprism", {
        state: "installed",
        running: 1,
        total: 1,
        credentials: [{ label: "管理员密码", username: "admin", secret: "published", upstream: true }],
      }),
    ]);
    const user = userEvent.setup();
    render(<AppCenterPanel />);
    await user.click(await screen.findByRole("button", { name: /^Photoprism/ }));
    const logins = await screen.findByLabelText("登录信息");
    expect(within(logins).getByText(/安装清单公开的默认密码/)).toBeTruthy();
    expect(logins.textContent).not.toContain("随机生成");
  });

  it("follows an install from its plan through progress to opening the app", async () => {
    let memos = app("memos", { webPort: 5230, notes: "默认账号 `admin`" });
    const posts = serve(
      () => [memos],
      () => {
        memos = { ...memos, state: "installing", job: job({ output: ["Pulling images"] }) };
        return job();
      },
    );
    const open = vi.spyOn(window, "open").mockReturnValue(null);
    const user = userEvent.setup();
    render(<AppCenterPanel />);

    await user.click(await screen.findByRole("button", { name: "安装 Memos" }));
    const plan = await screen.findByRole("dialog", { name: "安装计划" });
    expect(await within(plan).findByText("/srv/a-nas/data/apps/memos/memos")).toBeTruthy();
    expect(within(plan).getByText("app-memos")).toBeTruthy();
    await user.click(within(plan).getByRole("button", { name: "确认安装" }));
    expect(posts).toEqual(["/api/v1/apps/memos/install"]);

    const progress = await screen.findByRole("dialog", { name: "安装进度" });
    expect(within(progress).getByText("正在安装 Memos")).toBeTruthy();
    expect(within(progress).getAllByText("Pulling images").length).toBeGreaterThan(0);

    memos = {
      ...memos,
      state: "installed",
      running: 1,
      total: 1,
      job: job({ state: "succeeded", output: ["Started"] }),
    };
    await refresh();
    const done = await screen.findByRole("dialog", { name: "安装完成" });
    expect(within(done).getByText("Memos 已安装")).toBeTruthy();
    expect(await within(done).findByText("admin")).toHaveProperty("tagName", "CODE");
    await user.click(within(done).getByRole("button", { name: "打开 Memos" }));
    expect(open).toHaveBeenCalledWith(
      expect.stringMatching(/^http:\/\/[^/]+:5230\/$/),
      "_blank",
      "noopener,noreferrer",
    );
    expect(screen.queryByRole("dialog", { name: "安装完成" })).toBeNull();
    expect(screen.getByText(/已安装 · 1\/1 个容器运行中/)).toBeTruthy();
  });

  // Moved from the desktop's tests, where the App Center opened from its icon.
  it("installs an app after confirming its plan, sending the plan's digest", async () => {
    const installs: unknown[] = [];
    let installed = false;
    server.use(
      ...appsHandlers(() =>
        catalog(
          installed
            ? {
                state: "installed",
                running: 1,
                total: 1,
                job: job({
                  state: "succeeded",
                  startedAt: "2026-10-07T00:00:00Z",
                  output: ["Container memos  Started"],
                }),
              }
            : {},
        ),
      ),
      http.post("/api/v1/apps/{appId}/install", async ({ request, response }) => {
        installs.push({ path: pathOf(request), body: await request.json() });
        installed = true;
        return response(202).json(job({ startedAt: "2026-10-07T00:00:00Z" }));
      }),
    );
    const user = userEvent.setup();
    render(<AppCenterPanel />);

    await user.click(await screen.findByRole("button", { name: "安装 Memos" }));
    const plan = await screen.findByRole("dialog", { name: "安装计划" });
    expect(await within(plan).findByText("neosmemo/memos:0.28.0")).toBeTruthy();
    expect(within(plan).getByText("5230/tcp")).toBeTruthy();
    expect(within(plan).getByText("/srv/a-nas/data/apps/memos/memos")).toBeTruthy();
    expect(within(plan).getByText("app-memos")).toBeTruthy();
    expect(within(plan).getByText(/只访问自己的应用数据/)).toBeTruthy();
    expect(within(plan).getByText("使用 Docker 默认网桥，不新建网络")).toBeTruthy();
    expect(installs).toHaveLength(0);

    await user.click(within(plan).getByRole("button", { name: "确认安装" }));
    expect(installs).toEqual([{ path: "/api/v1/apps/memos/install", body: { digest: memosPlan.digest } }]);
    expect(await screen.findByText("Memos 已安装")).toBeTruthy();
    expect(screen.getByText(/已安装 · 1\/1 个容器运行中/)).toBeTruthy();
    expect(screen.getByText(/Container memos\s+Started/)).toBeTruthy();
  });

  it("returns focus to the install button when the plan is dismissed", async () => {
    serve(() => [app("memos")]);
    const user = userEvent.setup();
    render(<AppCenterPanel />);
    await user.click(await screen.findByRole("button", { name: "Memos，可安装" }));
    const install = screen.getByRole("button", { name: "安装" });
    await user.click(install);
    await screen.findByRole("dialog", { name: "安装计划" });
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "安装计划" })).toBeNull());
    expect(document.activeElement).toBe(install);
  });

  it("shows why the last install failed and plans again", async () => {
    serve(() => [
      app("memos", {
        job: job({ state: "failed", error: "pull access denied", output: ["Error response from daemon"] }),
      }),
    ]);
    const user = userEvent.setup();
    render(<AppCenterPanel />);

    expect(await screen.findByText("上次安装失败")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Memos，安装失败" }));
    const failed = screen.getByRole("region", { name: "安装失败" });
    expect(within(failed).getByText("pull access denied")).toBeTruthy();
    expect(within(failed).getByText("Error response from daemon")).toBeTruthy();
    await user.click(within(failed).getByRole("button", { name: "重新安装" }));
    expect(await screen.findByRole("dialog", { name: "安装计划" })).toBeTruthy();
  });

  it("retries an incomplete uninstall after its containers have gone", async () => {
    let memos = app("memos", {
      state: "failed",
      total: 0,
      running: 0,
      job: job({ action: "uninstall", state: "failed", error: "镜像清理失败" }),
    });
    const posts = serve(
      () => [memos],
      () => {
        const pending = job({ action: "uninstall" });
        memos = { ...memos, state: "uninstalling", job: pending };
        return pending;
      },
    );
    const user = userEvent.setup();
    render(<AppCenterPanel />);
    await user.click(await screen.findByRole("button", { name: "Memos，卸载未完成" }));
    const failed = screen.getByRole("region", { name: "卸载未完成" });
    expect(within(failed).getByText("镜像清理失败")).toBeTruthy();
    expect(screen.queryByRole("region", { name: "安装失败" })).toBeNull();
    await user.click(within(failed).getByRole("button", { name: "重试卸载" }));
    const confirm = screen.getByRole("dialog", { name: "确认卸载Memos" });
    await user.click(within(confirm).getByRole("button", { name: "确认卸载" }));
    expect(posts).toEqual(["/api/v1/apps/memos/uninstall"]);
    expect(await screen.findByRole("region", { name: "卸载进度" })).toBeTruthy();
  });

  // #169: Compose failed partway and the rollback did not finish either.
  it("shows the containers a failed install left as a failure, never as installed", async () => {
    const error =
      "Docker Compose 没有完成启动（compose command failed: exit status 1）; the install could not be rolled back";
    serve(() => [
      app("linkwarden", {
        state: "failed",
        running: 2,
        total: 3,
        webPort: 3428,
        job: job({ appId: "linkwarden", state: "failed", error }),
      }),
    ]);
    const user = userEvent.setup();
    render(<AppCenterPanel />);

    expect(await screen.findByText("上次安装失败")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "打开 Linkwarden" })).toBeNull();
    await user.click(screen.getByRole("button", { name: "Linkwarden，安装失败" }));
    const failed = screen.getByRole("region", { name: "安装失败" });
    expect(within(failed).getByText(error)).toBeTruthy();
    expect(within(failed).getByText(/3 个容器留在 Docker 中（2 个运行中）/)).toBeTruthy();
    expect(screen.queryByText(/个容器运行中/)).toBeNull();

    await user.click(screen.getByRole("button", { name: "更多操作" }));
    expect(screen.getByRole("menuitem", { name: "卸载" })).toBeTruthy();
    await user.keyboard("{Escape}");
    await user.click(within(failed).getByRole("button", { name: "重新安装" }));
    expect(await screen.findByRole("dialog", { name: "安装计划" })).toBeTruthy();
  });

  it("does not show an earlier failure as the result of a new install", async () => {
    const earlier = job({ state: "failed", error: "pull access denied", startedAt: "2026-10-12T08:00:00Z" });
    const retried = job({ startedAt: "2026-10-12T09:00:00Z" });
    let memos = app("memos", { job: earlier });
    serve(
      () => [memos],
      () => retried,
    );
    const user = userEvent.setup();
    render(<AppCenterPanel />);

    await user.click(await screen.findByRole("button", { name: "安装 Memos" }));
    const plan = await screen.findByRole("dialog", { name: "安装计划" });
    await within(plan).findByText("app-memos");
    await user.click(within(plan).getByRole("button", { name: "确认安装" }));
    // The list still reports the earlier failed job until the agent's next answer.
    expect(await screen.findByRole("dialog", { name: "安装进度" })).toBeTruthy();

    memos = { ...memos, job: { ...retried, state: "failed", error: "port is already allocated" } };
    await refresh();
    const failed = await screen.findByRole("dialog", { name: "安装失败" });
    expect(within(failed).getByText("port is already allocated")).toBeTruthy();
  });

  it("holds an install while another app's job runs", async () => {
    serve(() => [
      app("memos"),
      app("navidrome", { category: "media", state: "installing", job: job({ appId: "navidrome" }) }),
    ]);
    const user = userEvent.setup();
    render(<AppCenterPanel />);

    await user.click(await screen.findByRole("button", { name: "安装 Memos" }));
    const plan = await screen.findByRole("dialog", { name: "安装计划" });
    await within(plan).findByText("app-memos");
    expect(within(plan).getByText("Navidrome 正在安装，完成后才能开始安装。")).toBeTruthy();
    expect((within(plan).getByRole("button", { name: "确认安装" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("lists installed apps and uninstalls one from its menu", async () => {
    let memos = app("memos", { state: "installed", running: 1, total: 1, webPort: 5230 });
    const posts = serve(
      () => [memos, app("navidrome", { category: "media" })],
      () => {
        memos = { ...memos, state: "uninstalling", job: job({ action: "uninstall" }) };
        return job({ action: "uninstall" });
      },
    );
    const openDocker = vi.fn();
    const user = userEvent.setup();
    render(<AppCenterPanel onOpenDocker={openDocker} />);

    const nav = await screen.findByRole("navigation", { name: "应用中心导航" });
    await user.click(within(nav).getByRole("button", { name: /已安装/ }));
    const list = screen.getByRole("list");
    expect(within(list).getAllByRole("listitem")).toHaveLength(1);
    expect(within(list).getByText("运行中 · 1 个容器 · 端口 5230")).toBeTruthy();

    await user.click(within(list).getByRole("button", { name: "Memos，已安装" }));
    await user.click(screen.getByRole("button", { name: "更多操作" }));
    await user.click(screen.getByRole("menuitem", { name: "在 Docker 中查看" }));
    expect(openDocker).toHaveBeenCalledExactlyOnceWith("memos");

    const more = screen.getByRole("button", { name: "更多操作" });
    await user.click(more);
    await user.click(screen.getByRole("menuitem", { name: "卸载" }));
    await user.click(
      within(screen.getByRole("dialog", { name: "确认卸载Memos" })).getByRole("button", { name: "取消" }),
    );
    expect(document.activeElement).toBe(more);
    expect(posts).toEqual([]);

    await user.click(more);
    await user.click(screen.getByRole("menuitem", { name: "卸载" }));
    const confirm = screen.getByRole("dialog", { name: "确认卸载Memos" });
    expect(within(confirm).getByText("保留应用数据与命名卷，重新安装后可继续使用")).toBeTruthy();
    await user.click(within(confirm).getByRole("button", { name: "确认卸载" }));
    expect(posts).toEqual(["/api/v1/apps/memos/uninstall"]);
    expect(await screen.findByRole("region", { name: "卸载进度" })).toBeTruthy();
    expect(screen.queryByRole("dialog", { name: "确认卸载Memos" })).toBeNull();
  });

  it("shows how each service runs and that Shared is only read", async () => {
    serve(() => [app("navidrome", { category: "media" })], undefined, navidromePlan);
    const user = userEvent.setup();
    render(<AppCenterPanel />);

    await user.click(await screen.findByRole("button", { name: "安装 Navidrome" }));
    const plan = await screen.findByRole("dialog", { name: "安装计划" });
    expect(await within(plan).findByText("将能读取共享空间中的 1 个文件夹", { exact: false })).toBeTruthy();
    expect(within(plan).queryByText("读写", { exact: false })).toBeNull();
    expect(within(plan).getByText("部分服务以镜像自带的身份运行，其余以 app-navidrome 运行")).toBeTruthy();
    expect(within(plan).getByText("UID 30004，没有特权能力，经 a-nas-users 只读访问共享空间")).toBeTruthy();
    expect(within(plan).getByText("可能是 root，只能访问自己的应用数据")).toBeTruthy();
    expect(within(plan).getByText("共享空间 · 只读")).toBeTruthy();
  });

  it("tells the owner an app runs under older runtime rules", async () => {
    serve(() => [app("memos", { state: "installed", running: 1, total: 1, runtimeOutdated: true })]);
    const user = userEvent.setup();
    render(<AppCenterPanel />);

    await user.click(await screen.findByRole("button", { name: "Memos，已安装" }));
    const status = screen.getByRole("region", { name: "运行状态" });
    expect(within(status).getByRole("note").textContent).toContain("卸载（应用数据会保留）后重新安装");
  });

  it("lists an app from an older agent without an A-NAS category under 其他", async () => {
    serve(() => [app("memos", { category: "" })]);
    render(<AppCenterPanel />);
    expect(
      within(await screen.findByRole("region", { name: "其他" })).getByRole("button", { name: "Memos，可安装" }),
    ).toBeTruthy();
  });

  // Moved from the desktop's tests.
  it("explains install conflicts and a disabled App Center", async () => {
    server.use(
      ...appsHandlers(() => catalog()),
      http.post("/api/v1/apps/{appId}/install", ({ response }) =>
        response(409).json(apiError("port_in_use", "a published port is already in use: 5230 is used by other")),
      ),
    );
    const user = userEvent.setup();
    const { unmount } = render(<AppCenterPanel />);
    await user.click(await screen.findByRole("button", { name: "安装 Memos" }));
    await user.click(await screen.findByRole("button", { name: "确认安装" }));
    const conflict = (await screen.findByRole("alert")).textContent;
    expect(conflict).toContain("端口已被占用（5230）");
    expect(conflict).not.toContain("is used by");
    unmount();

    server.use(http.get("/api/v1/apps", ({ response }) => response(503).json(apiError("apps_disabled", "disabled"))));
    render(<AppCenterPanel />);
    expect(await screen.findByText("应用中心未启用")).toBeTruthy();
  });

  it("shows the Docker networks an app creates and refuses them without an address pool", async () => {
    let poolConfigured = true;
    server.use(...appsHandlers(() => catalog()));
    server.use(
      http.get("/api/v1/apps/{appId}/plan", ({ response }) =>
        poolConfigured
          ? response(200).json({ ...memosPlan, networks: ["default"], addressPools: ["10.96.64.0/19"] })
          : response(409).json(apiError("address_pool_missing", "Docker has no address pool for app networks")),
      ),
    );
    const user = userEvent.setup();
    render(<AppCenterPanel />);
    await user.click(await screen.findByRole("button", { name: "Memos，可安装" }));
    await user.click(screen.getByRole("button", { name: "安装" }));
    const plan = await screen.findByRole("dialog", { name: "安装计划" });
    expect(await within(plan).findByText("default")).toBeTruthy();
    expect(within(plan).getByText(/地址只从 10\.96\.64\.0\/19 分配/)).toBeTruthy();

    await user.click(within(plan).getByRole("button", { name: "取消" }));
    poolConfigured = false;
    await user.click(screen.getByRole("button", { name: "安装" }));
    expect((await screen.findByRole("alert")).textContent).toContain("Docker 尚未配置 A-NAS 网络地址池");
  });

  it("explains when app installation is blocked by an unavailable data volume", async () => {
    server.use(
      ...appsHandlers(() => catalog()),
      http.post("/api/v1/apps/{appId}/install", ({ response }) =>
        response(423).json(apiError("volume_unavailable", "the data volume is not available")),
      ),
    );
    const user = userEvent.setup();
    render(<AppCenterPanel />);
    await user.click(await screen.findByRole("button", { name: "Memos，可安装" }));
    await user.click(screen.getByRole("button", { name: "安装" }));
    await user.click(await screen.findByRole("button", { name: "确认安装" }));
    expect((await screen.findByRole("alert")).textContent).toContain("数据卷当前不可用");
  });
});
