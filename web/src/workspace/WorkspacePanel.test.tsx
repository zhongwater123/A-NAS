import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setSession } from "../api/client";
import { AgentTokens } from "../settings/AgentTokens";
import { user as account, session } from "../test/fixtures";
import {
  addVersion,
  other,
  reader,
  version,
  type WorkspaceStore,
  workspaceHandlers,
  workspacePage,
  workspaceStore,
} from "../test/handlers/workspace";
import { apiError, http, server } from "../test/server";
import { agentTokenClient } from "./agentTokens";
import { WorkspacePanel } from "./WorkspacePanel";

let store: WorkspaceStore;
beforeEach(() => {
  setSession(session({ id: reader.id, username: reader.name, role: "member" }, { csrfToken: "csrf" }));
  agentTokenClient.clear();
  store = workspaceStore([
    workspacePage("page_prd", "家庭相册分享 PRD", [version(1), version(2, { note: "" })]),
    workspacePage("page_old", "旧方案", [version(1, { savedBy: other })]),
  ]);
  server.use(...workspaceHandlers(store));
});
afterEach(() => window.history.replaceState(null, "", "/"));

const html = (title: string) =>
  new File([`<!doctype html><title>${title}</title><p>x</p>`], `${title}.html`, { type: "text/html" });
const row = async (title: string) =>
  (await screen.findByRole("link", { name: new RegExp(title) })).closest("li") as HTMLElement;

describe("共享工作区", () => {
  it("lists the pages, each opening in a browser tab of its own", async () => {
    render(<WorkspacePanel userId={reader.id} admin={false} />);
    const pages = await screen.findByRole("list", { name: "页面" });
    const links = await within(pages).findAllByRole("link");
    expect(links.map((link) => link.textContent)).toEqual([
      expect.stringContaining("家庭相册分享 PRD"),
      expect.stringContaining("旧方案"),
    ]);
    expect(links[0].getAttribute("href")).toBe("/workspace/page_prd");
    expect(links[0].getAttribute("target")).toBe("anas-workspace-page_prd");
    // noopener would make a browser open a new tab on every click.
    expect(links[0].getAttribute("rel")).toBeNull();
    // The window shows no page itself.
    expect(screen.queryByTitle(/^页面：/)).toBeNull();
    expect(screen.queryByRole("button", { name: "成员" })).toBeNull();
  });

  it("keeps the device's own screen's flag on the tabs it opens", async () => {
    window.history.replaceState(null, "", "/?local-console=1");
    render(<WorkspacePanel userId={reader.id} admin={false} />);
    const link = await screen.findByRole("link", { name: /家庭相册分享 PRD/ });
    expect(link.getAttribute("href")).toBe("/workspace/page_prd?local-console=1");
  });

  it("uploads an HTML file as a new page and offers to open it", async () => {
    const user = userEvent.setup();
    const open = vi.spyOn(window, "open").mockImplementation(() => null);
    const { container } = render(<WorkspacePanel userId={reader.id} admin={false} />);
    await row("家庭相册分享 PRD");
    const input = container.querySelector<HTMLInputElement>('header input[type="file"]');
    await user.upload(input as HTMLInputElement, html("新的 PRD"));
    await screen.findByRole("link", { name: /新的 PRD/ });
    expect(store.uploads).toEqual([
      { name: "新的 PRD.html", body: expect.stringContaining("<title>新的 PRD</title>") },
    ]);
    const toast = (await screen.findByText("已上传“新的 PRD”")).parentElement as HTMLElement;
    await user.click(within(toast).getByRole("button", { name: "打开" }));
    expect(open).toHaveBeenCalledWith("/workspace/page_3", "anas-workspace-page_3");
  });

  it("shows on each row whose agent is at work and who else has it open", async () => {
    store.pages[0].presence = {
      viewers: [
        { member: reader, version: 2, since: "2026-11-26T08:00:00Z" },
        { member: other, version: 2, since: "2026-11-26T08:01:00Z" },
      ],
      agents: [
        {
          member: other,
          via: { kind: "claude-code", name: "办公电脑" },
          action: "download",
          version: 2,
          at: new Date().toISOString(),
        },
      ],
    };
    render(<WorkspacePanel userId={reader.id} admin={false} />);
    const prd = await row("家庭相册分享 PRD");
    expect(within(prd).getByRole("img", { name: "正在看：bob" })).toBeTruthy();
    expect(within(prd).getByText("bob 经 办公电脑 下载了 v2")).toBeTruthy();
    const old = await row("旧方案");
    expect(within(old).queryByRole("img")).toBeNull();
  });

  it("asks before uploading over a version saved since", async () => {
    const user = userEvent.setup();
    render(<WorkspacePanel userId={reader.id} admin={false} />);
    const prd = await row("家庭相册分享 PRD");
    // bob's agent saves v3 while the list still shows v2.
    addVersion(store, "page_prd", { savedBy: other, via: { kind: "codex", name: "笔记本" } });
    await user.click(within(prd).getByRole("button", { name: "更多操作：家庭相册分享 PRD" }));
    await user.click(await screen.findByRole("menuitem", { name: "上传新版本…" }));
    await user.upload(prd.querySelector<HTMLInputElement>('input[type="file"]') as HTMLInputElement, html("PRD"));
    const dialog = await screen.findByRole("dialog", { name: "页面已有新版本" });
    expect(within(dialog).getByText(/bob 刚保存了 v3/)).toBeTruthy();
    await user.click(within(dialog).getByRole("button", { name: "上传为 v4" }));
    await screen.findByText("“家庭相册分享 PRD”已保存为 v4");
    expect(store.uploads).toEqual([{ name: null, base: 3, body: expect.stringContaining("<p>x</p>") }]);
  });

  it("renames a page and lets its uploader delete it into the trash", async () => {
    const user = userEvent.setup();
    render(<WorkspacePanel userId={reader.id} admin={false} />);
    const prd = await row("家庭相册分享 PRD");
    await user.click(within(prd).getByRole("button", { name: "更多操作：家庭相册分享 PRD" }));
    await user.click(await screen.findByRole("menuitem", { name: "改名…" }));
    const rename = await screen.findByRole("dialog", { name: "改名" });
    const title = within(rename).getByRole("textbox", { name: "标题" });
    await user.clear(title);
    await user.type(title, "分享 PRD");
    await user.click(within(rename).getByRole("button", { name: "保存" }));
    const renamed = await row("分享 PRD");
    // bob uploaded the old plan: alice may not delete it.
    await user.click(within(await row("旧方案")).getByRole("button", { name: "更多操作：旧方案" }));
    expect(screen.queryByRole("menuitem", { name: "删除…" })).toBeNull();
    await user.keyboard("{Escape}");
    await user.click(within(renamed).getByRole("button", { name: "更多操作：分享 PRD" }));
    await user.click(await screen.findByRole("menuitem", { name: "删除…" }));
    const confirm = await screen.findByRole("dialog", { name: "删除“分享 PRD”？" });
    expect(within(confirm).getByText(/全部 2 个版本一起移到回收站/)).toBeTruthy();
    await user.click(within(confirm).getByRole("button", { name: "删除" }));
    await waitFor(() => expect(screen.queryByRole("link", { name: /分享 PRD/ })).toBeNull());
    expect(store.pages.map((page) => page.id)).toEqual(["page_old"]);
    expect(store.trash.map((item) => item.page.id)).toEqual(["page_prd"]);
    // The toast takes it back at once.
    await user.click(await screen.findByRole("button", { name: "撤销" }));
    await row("分享 PRD");
    expect(store.trash).toEqual([]);
  });

  it("brings a deleted page back from the trash, or removes it for good", async () => {
    const user = userEvent.setup();
    const daysAgo = (days: number) => new Date(Date.now() - days * 86_400_000).toISOString();
    store.trash = [
      {
        page: workspacePage("page_plan", "去年的方案", [version(1), version(2)]),
        deletedBy: reader,
        deletedAt: daysAgo(2),
      },
      { page: workspacePage("page_draft", "旧草稿", [version(1)]), deletedBy: reader, deletedAt: daysAgo(5) },
      {
        page: workspacePage("page_bob", "bob 的草稿", [version(1, { savedBy: other })]),
        deletedBy: other,
        deletedAt: daysAgo(1),
      },
    ];
    render(<WorkspacePanel userId={reader.id} admin={false} />);
    await row("家庭相册分享 PRD");
    await user.click(screen.getByRole("button", { name: "回收站" }));
    const dialog = await screen.findByRole("dialog", { name: "回收站" });
    const items = await within(dialog).findByRole("list", { name: "删除的页面" });
    expect(within(items).getByText(/^alice 删除 · .+ · 2 个版本 · 28 天后彻底删除$/)).toBeTruthy();
    // bob uploaded his draft: alice sees it in the trash, as she saw the
    // page, but only bob and administrators bring it back.
    expect(within(items).getByText("bob 的草稿")).toBeTruthy();
    expect(within(dialog).queryByRole("button", { name: "恢复“bob 的草稿”" })).toBeNull();

    await user.click(within(dialog).getByRole("button", { name: "恢复“去年的方案”" }));
    await row("去年的方案");
    await waitFor(() => expect(within(dialog).queryByText("去年的方案")).toBeNull());

    await user.click(within(dialog).getByRole("button", { name: "彻底删除“旧草稿”" }));
    const confirm = await screen.findByRole("dialog", { name: "彻底删除“旧草稿”？" });
    expect(within(confirm).getByText("全部 1 个版本一起删除，不能恢复。")).toBeTruthy();
    await user.click(within(confirm).getByRole("button", { name: "彻底删除" }));
    await waitFor(() => expect(store.trash.map((item) => item.page.id)).toEqual(["page_bob"]));
    await waitFor(() => expect(within(dialog).queryByText("旧草稿")).toBeNull());
  });

  it("empties the window when an administrator takes the member out", async () => {
    const user = userEvent.setup();
    const { container } = render(<WorkspacePanel userId={reader.id} admin={false} />);
    await row("家庭相册分享 PRD");
    server.use(
      http.get("/api/v1/workspace/pages", ({ response }) => response(403).json(apiError("workspace_forbidden"))),
    );
    // The next read of the list, here after an upload, is refused.
    await user.upload(
      container.querySelector<HTMLInputElement>('header input[type="file"]') as HTMLInputElement,
      html("x"),
    );
    expect(await screen.findByText("管理员没有让你使用共享工作区")).toBeTruthy();
    expect(screen.queryByRole("list", { name: "页面" })).toBeNull();
  });

  it("gives the command that connects Claude Code and waits for its first call", async () => {
    const user = userEvent.setup();
    // 设置 → 我的账号, open beside it, follows the same list.
    render(
      <>
        <WorkspacePanel userId={reader.id} admin={false} />
        <AgentTokens account={reader.id} />
      </>,
    );
    await user.click(screen.getByRole("button", { name: "连接 agent" }));
    const dialog = await screen.findByRole("dialog", { name: "连接 agent" });
    await user.type(within(dialog).getByRole("textbox", { name: "名字" }), "办公电脑");
    await user.click(within(dialog).getByRole("button", { name: "生成命令" }));
    const command = await within(dialog).findByText(/^claude mcp add/);
    expect(command.textContent).toBe(
      `claude mcp add --transport http --scope user a-nas ${location.origin}/api/v1/workspace/mcp ` +
        `--header "Authorization: Bearer anas_ws_${"0".repeat(64)}"`,
    );
    expect(store.tokens).toMatchObject([{ name: "办公电脑", kind: "claude-code" }]);
    within(dialog).getByText("等待 agent 第一次调用");
    await screen.findByText("Claude Code · 还没用过");
    store.tokens = store.tokens.map((token) => ({ ...token, lastUsedAt: new Date().toISOString() }));
    await within(dialog).findByText("已连接", undefined, { timeout: 5_000 });
    expect(screen.getByText("Claude Code · 刚刚使用")).toBeTruthy();
  });

  it("lets administrators choose who may use it", async () => {
    const user = userEvent.setup();
    store.members = [
      { id: "user:owner", username: "owner", member: false },
      { id: "user:bob", username: "bob", member: false },
    ];
    server.use(
      http.get("/api/v1/users", ({ response }) =>
        response(200).json({
          items: [
            account({ id: "user:owner", username: "owner", role: "admin" }),
            account({ id: "user:bob", username: "bob", role: "member" }),
          ],
        }),
      ),
    );
    render(<WorkspacePanel userId="user:owner" admin />);
    await user.click(screen.getByRole("button", { name: "成员" }));
    const dialog = await screen.findByRole("dialog", { name: "成员" });
    const owner = await within(dialog).findByRole("checkbox", { name: /owner/ });
    await waitFor(() => expect((owner as HTMLInputElement).disabled).toBe(true));
    expect((owner as HTMLInputElement).checked).toBe(true);
    await user.click(within(dialog).getByRole("checkbox", { name: "bob" }));
    await waitFor(() => expect(store.members.find((item) => item.id === "user:bob")?.member).toBe(true));
  });
});
