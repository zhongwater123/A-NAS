import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { setSession } from "../api/client";
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
import { http, server } from "../test/server";
import type { FromFrame } from "./frame/protocol";
import { WorkspacePanel } from "./WorkspacePanel";

let store: WorkspaceStore;
beforeEach(() => {
  setSession(session({ id: reader.id, username: reader.name, role: "member" }, { csrfToken: "csrf" }));
  store = workspaceStore([
    workspacePage("page_prd", "家庭相册分享 PRD", [version(1), version(2, { note: "" })]),
    workspacePage("page_old", "旧方案", [version(1, { savedBy: other })]),
  ]);
  server.use(...workspaceHandlers(store));
});

const frame = async () => (await screen.findByTitle(/^页面：/)) as HTMLIFrameElement;
// fromFrame is a message from the open page's frame, whose origin is opaque.
async function fromFrame(message: FromFrame) {
  const source = (await frame()).contentWindow;
  act(() => {
    window.dispatchEvent(new MessageEvent("message", { data: message, origin: "null", source }));
  });
}
const html = (title: string) =>
  new File([`<!doctype html><title>${title}</title><p>x</p>`], `${title}.html`, { type: "text/html" });

describe("共享工作区", () => {
  it("lists the pages and opens the newest in a sandbox of its own", async () => {
    render(<WorkspacePanel userId={reader.id} admin={false} />);
    const pages = await screen.findByRole("navigation", { name: "页面" });
    expect((await within(pages).findAllByRole("button")).map((row) => row.textContent)).toEqual([
      expect.stringContaining("家庭相册分享 PRD"),
      expect.stringContaining("旧方案"),
    ]);
    const shown = await frame();
    expect(shown.getAttribute("sandbox")).toBe("allow-scripts");
    expect(shown.getAttribute("src")).toBe("/api/v1/workspace/pages/page_prd/versions/2/view");
    await waitFor(() => expect(store.viewing).toEqual({ pageId: "page_prd", version: 2 }));
    expect(screen.queryByRole("button", { name: "成员" })).toBeNull();
  });

  it("uploads an HTML file as a new page and opens it", async () => {
    const user = userEvent.setup();
    const { container } = render(<WorkspacePanel userId={reader.id} admin={false} />);
    await frame();
    const input = container.querySelector<HTMLInputElement>('aside input[type="file"]');
    await user.upload(input as HTMLInputElement, html("新的 PRD"));
    await waitFor(async () => expect((await frame()).getAttribute("src")).toContain("/page_3/versions/1/view"));
    expect(store.uploads).toEqual([
      { name: "新的 PRD.html", body: expect.stringContaining("<title>新的 PRD</title>") },
    ]);
    expect(screen.getByRole("heading", { name: "新的 PRD" })).toBeTruthy();
  });

  it("shows the reader's own new version at once and points at what changed", async () => {
    render(<WorkspacePanel userId={reader.id} admin={false} />);
    await frame();
    await fromFrame({ type: "anas:scrolled", y: 640 });
    addVersion(store, "page_prd", {
      via: { kind: "claude-code", name: "办公电脑" },
      note: "把按钮改小",
      changed: [4321],
    });
    await waitFor(
      async () =>
        expect((await frame()).getAttribute("src")).toBe(
          "/api/v1/workspace/pages/page_prd/versions/3/view?highlight=4321&scroll=640",
        ),
      { timeout: 6_000 },
    );
    expect(screen.queryByText(/保存了 v3/)).toBeNull();
  });

  it("announces another member's version and shows it when asked", async () => {
    const user = userEvent.setup();
    render(<WorkspacePanel userId={reader.id} admin={false} />);
    await frame();
    addVersion(store, "page_prd", { savedBy: other, via: { kind: "codex", name: "笔记本" }, note: "表格加一列" });
    const notice = await screen.findByText("bob 经 笔记本 保存了 v3：表格加一列", undefined, { timeout: 6_000 });
    expect((await frame()).getAttribute("src")).toContain("/versions/2/view");
    await user.click(within(notice.parentElement as HTMLElement).getByRole("button", { name: "查看" }));
    expect((await frame()).getAttribute("src")).toBe("/api/v1/workspace/pages/page_prd/versions/3/view");
  });

  it("records the element the member picks, with a picture of it, for their agent", async () => {
    const user = userEvent.setup();
    render(<WorkspacePanel userId={reader.id} admin={false} />);
    const pick = await screen.findByRole("button", { name: "选取" });
    await fromFrame({ type: "anas:ready", blocked: [] });
    const toFrame = vi.spyOn((await frame()).contentWindow as Window, "postMessage");
    await user.click(pick);
    expect(toFrame).toHaveBeenCalledWith({ type: "anas:pick", on: true }, "*");
    await fromFrame({
      type: "anas:picked",
      at: 4321,
      generated: false,
      tag: "button",
      text: "发出分享",
      dom: '<button class="btn btn-primary">发出分享</button>',
      rect: { x: 600, y: 400, w: 96, h: 36 },
      style: { "font-size": "16px" },
      rules: [".btn", ".btn-primary"],
    });
    expect(toFrame).toHaveBeenCalledWith({ type: "anas:pick", on: false }, "*");
    expect(toFrame).toHaveBeenCalledWith(
      { type: "anas:capture", id: 1, rect: { x: 552, y: 352, w: 192, h: 132 } },
      "*",
    );
    await fromFrame({ type: "anas:captured", id: 1, image: "data:image/jpeg;base64,/9j/4AAQ" });
    await screen.findByText("你的 agent 能读到它");
    expect(store.selection).toMatchObject({
      pageId: "page_prd",
      version: 2,
      at: 4321,
      tag: "button",
      text: "发出分享",
      image: "data:image/jpeg;base64,/9j/4AAQ",
    });
    await user.click(screen.getByRole("button", { name: "清除选中" }));
    await waitFor(() => expect(store.selection).toBeUndefined());
    expect(toFrame).toHaveBeenCalledWith({ type: "anas:clear" }, "*");
  });

  it("ignores messages from anything but the page's frame", async () => {
    render(<WorkspacePanel userId={reader.id} admin={false} />);
    await frame();
    act(() => {
      window.dispatchEvent(
        new MessageEvent("message", { data: { type: "anas:error", message: "伪造" }, origin: "null", source: window }),
      );
    });
    expect(screen.queryByText(/伪造/)).toBeNull();
  });

  it("says which outside resources the page could not load and which script failed", async () => {
    const user = userEvent.setup();
    render(<WorkspacePanel userId={reader.id} admin={false} />);
    await fromFrame({
      type: "anas:ready",
      blocked: [{ uri: "https://cdn.example.com/a.css", directive: "style-src-elem" }],
    });
    await fromFrame({ type: "anas:blocked", uri: "https://img.example.com/x.png", directive: "img-src" });
    await fromFrame({ type: "anas:error", message: "SecurityError: localStorage" });
    await user.click(screen.getByText("2 项外网资源未加载"));
    expect(screen.getByText("https://img.example.com/x.png")).toBeTruthy();
    expect(screen.getByText("页面脚本出错：SecurityError: localStorage")).toBeTruthy();
  });

  it("asks before uploading over a version saved since", async () => {
    const user = userEvent.setup();
    const { container } = render(<WorkspacePanel userId={reader.id} admin={false} />);
    await frame();
    // bob's agent saves v3 while the reader still has v2 open.
    addVersion(store, "page_prd", { savedBy: other, via: { kind: "codex", name: "笔记本" } });
    await user.click(screen.getByRole("button", { name: "更多操作：家庭相册分享 PRD" }));
    await user.click(await screen.findByRole("menuitem", { name: "上传新版本…" }));
    const input = container.querySelector<HTMLInputElement>('section input[type="file"]');
    await user.upload(input as HTMLInputElement, html("家庭相册分享 PRD"));
    const dialog = await screen.findByRole("dialog", { name: "页面已有新版本" });
    expect(within(dialog).getByText(/bob 刚保存了 v3/)).toBeTruthy();
    await user.click(within(dialog).getByRole("button", { name: "上传为 v4" }));
    await waitFor(async () => expect((await frame()).getAttribute("src")).toContain("/versions/4/view"));
    expect(store.uploads).toEqual([{ name: null, base: 3, body: expect.stringContaining("<p>x</p>") }]);
  });

  it("gives the command that connects Claude Code and waits for its first call", async () => {
    const user = userEvent.setup();
    render(<WorkspacePanel userId={reader.id} admin={false} />);
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
    store.tokens = store.tokens.map((token) => ({ ...token, lastUsedAt: new Date().toISOString() }));
    await within(dialog).findByText("已连接", undefined, { timeout: 5_000 });
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
