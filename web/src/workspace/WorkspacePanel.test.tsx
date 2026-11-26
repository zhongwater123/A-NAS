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
    expect(open).toHaveBeenCalledWith("/workspace/page_3", "anas-workspace-page_3", "noopener");
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

  it("renames a page and lets its uploader delete it", async () => {
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
    expect(within(confirm).getByText(/全部 2 个版本/)).toBeTruthy();
    await user.click(within(confirm).getByRole("button", { name: "删除" }));
    await waitFor(() => expect(screen.queryByRole("link", { name: /分享 PRD/ })).toBeNull());
    expect(store.pages.map((page) => page.id)).toEqual(["page_old"]);
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
