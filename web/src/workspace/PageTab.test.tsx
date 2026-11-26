import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { session } from "../test/fixtures";
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
import type { FromFrame } from "./frame/protocol";
import { PageTab } from "./PageTab";

let store: WorkspaceStore;
beforeEach(() => {
  store = workspaceStore([workspacePage("page_prd", "家庭相册分享 PRD", [version(1), version(2, { note: "" })])]);
  server.use(
    http.get("/api/v1/session", ({ response }) =>
      response(200).json(session({ id: reader.id, username: reader.name, role: "member" }, { csrfToken: "csrf" })),
    ),
    ...workspaceHandlers(store),
  );
});
afterEach(() => window.history.replaceState(null, "", "/"));

const frame = async () => (await screen.findByTitle(/^页面：/)) as HTMLIFrameElement;
// fromFrame is a message from the page's frame, whose origin is opaque.
async function fromFrame(message: FromFrame) {
  const source = (await frame()).contentWindow;
  act(() => {
    window.dispatchEvent(new MessageEvent("message", { data: message, origin: "null", source }));
  });
}

describe("共享页面的标签页", () => {
  it("shows the page alone, in a sandbox of its own, named after it", async () => {
    render(<PageTab pageId="page_prd" />);
    const shown = await frame();
    expect(shown.getAttribute("sandbox")).toBe("allow-scripts");
    expect(shown.getAttribute("src")).toBe("/api/v1/workspace/pages/page_prd/versions/2/view");
    expect(screen.getByRole("heading", { level: 1, name: "家庭相册分享 PRD" })).toBeTruthy();
    await waitFor(() => expect(document.title).toBe("家庭相册分享 PRD · 共享工作区"));
    // Managing the page is the window's: the tab picks, switches and exports.
    expect(screen.getByRole("button", { name: "选取" })).toBeTruthy();
    expect(screen.getByRole("link", { name: "导出" }).getAttribute("href")).toBe(
      "/api/v1/workspace/pages/page_prd/versions/2/file",
    );
    expect(screen.queryByRole("button", { name: /更多操作/ })).toBeNull();
    expect(screen.queryByRole("button", { name: "关闭" })).toBeNull();
  });

  it("tells the member's agent the page is open until the tab closes", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    render(<PageTab pageId="page_prd" />);
    await waitFor(() => expect(store.viewing).toEqual({ pageId: "page_prd", version: 2 }));
    // Said again every minute: the service forgets a desktop that goes quiet.
    store.viewing = undefined;
    await act(() => vi.advanceTimersByTimeAsync(60_000));
    await waitFor(() => expect(store.viewing).toEqual({ pageId: "page_prd", version: 2 }));
    act(() => {
      window.dispatchEvent(new Event("pagehide"));
    });
    await waitFor(() => expect(store.viewing).toBeUndefined());
  });

  it("shows the reader's own new version at once and points at what changed", async () => {
    render(<PageTab pageId="page_prd" />);
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
    render(<PageTab pageId="page_prd" />);
    await frame();
    addVersion(store, "page_prd", { savedBy: other, via: { kind: "codex", name: "笔记本" }, note: "表格加一列" });
    const notice = await screen.findByText("bob 经 笔记本 保存了 v3：表格加一列", undefined, { timeout: 6_000 });
    expect((await frame()).getAttribute("src")).toContain("/versions/2/view");
    await user.click(within(notice.parentElement as HTMLElement).getByRole("button", { name: "查看" }));
    expect((await frame()).getAttribute("src")).toBe("/api/v1/workspace/pages/page_prd/versions/3/view");
  });

  it("records the element the member picks, with a picture of it, for their agent", async () => {
    const user = userEvent.setup();
    render(<PageTab pageId="page_prd" />);
    const pick = await screen.findByRole("button", { name: "选取" });
    await fromFrame({ type: "anas:ready", blocked: [] });
    const toFrame = vi.spyOn((await frame()).contentWindow as Window, "postMessage");
    const picked: FromFrame = {
      type: "anas:picked",
      at: 4321,
      generated: false,
      tag: "button",
      text: "发出分享",
      dom: '<button class="btn btn-primary">发出分享</button>',
      rect: { x: 600, y: 400, w: 96, h: 36 },
      style: { "font-size": "16px" },
      rules: [".btn", ".btn-primary"],
    };
    // The page's own script can say "picked" too; unasked, it costs nothing.
    await fromFrame(picked);
    expect(toFrame).not.toHaveBeenCalled();
    await user.click(pick);
    expect(toFrame).toHaveBeenCalledWith({ type: "anas:pick", on: true }, "*");
    await fromFrame(picked);
    await fromFrame(picked);
    expect(toFrame).toHaveBeenCalledWith({ type: "anas:pick", on: false }, "*");
    const captures = toFrame.mock.calls.filter(([message]) => (message as { type: string }).type === "anas:capture");
    expect(captures).toEqual([[{ type: "anas:capture", id: 1, rect: { x: 552, y: 352, w: 192, h: 132 } }, "*"]]);
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
    render(<PageTab pageId="page_prd" />);
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
    render(<PageTab pageId="page_prd" />);
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

  it("says so when the page is deleted while it is open", async () => {
    render(<PageTab pageId="page_prd" />);
    await frame();
    store.pages = [];
    expect(await screen.findByText("这个页面已被删除", undefined, { timeout: 6_000 })).toBeTruthy();
    expect(screen.queryByTitle(/^页面：/)).toBeNull();
  });

  it("empties itself when an administrator takes the member out", async () => {
    render(<PageTab pageId="page_prd" />);
    await frame();
    server.use(
      http.get("/api/v1/workspace/pages/{pageId}", ({ response }) =>
        response(403).json(apiError("workspace_forbidden")),
      ),
    );
    expect(await screen.findByText("管理员没有让你使用共享工作区", undefined, { timeout: 6_000 })).toBeTruthy();
    expect(screen.queryByTitle(/^页面：/)).toBeNull();
  });

  it("asks a visitor who is not signed in to sign in on the desktop", async () => {
    server.use(http.get("/api/v1/session", ({ response }) => response(401).json(apiError("authentication_required"))));
    render(<PageTab pageId="page_prd" />);
    expect(await screen.findByText("请先登录 A-NAS 桌面")).toBeTruthy();
    expect(screen.getByRole("link", { name: "打开桌面" }).getAttribute("href")).toBe("/");
    expect(screen.queryByTitle(/^页面：/)).toBeNull();
  });

  it("closes itself on the device's own screen, which has no browser bar", async () => {
    const user = userEvent.setup();
    window.history.replaceState(null, "", "/workspace/page_prd?local-console=1");
    const close = vi.spyOn(window, "close").mockImplementation(() => undefined);
    render(<PageTab pageId="page_prd" />);
    await frame();
    await user.click(screen.getByRole("button", { name: "关闭" }));
    expect(close).toHaveBeenCalled();
  });
});
