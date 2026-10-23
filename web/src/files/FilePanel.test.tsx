import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import App from "../App";
import { installAPI, resetDesktop } from "../test/desktop";
import { transferStore } from "../transfers/store";

// Uploads go through tus-js-client; tests answer its requests themselves.
const tus = vi.hoisted(() => {
  class FakeUpload {
    url: string | null;
    constructor(
      readonly file: File,
      readonly options: Record<string, any>,
    ) {
      this.url = options.uploadUrl ?? null;
      uploads.push(this);
    }
    start() {}
    abort() {
      return Promise.resolve();
    }
  }
  const uploads: FakeUpload[] = [];
  return { FakeUpload, uploads };
});
vi.mock("tus-js-client", () => ({ Upload: tus.FakeUpload }));

beforeEach(() => {
  localStorage.clear();
});

afterEach(() => {
  resetDesktop();
  tus.uploads.length = 0;
});

describe("member sharing in the file manager", () => {
  it("opens sharing from the rail instead of the desktop and shares read-only files", async () => {
    installAPI({
      spaces: [{ id: "shared", kind: "shared", name: "Shared", writable: false, createdAt: "2026-10-07T10:00:00Z" }],
      entries: [
        {
          id: "shared-file",
          spaceId: "shared",
          name: "分享计划.txt",
          kind: "file",
          sizeBytes: 4,
          modifiedAt: "2026-10-07T10:00:00Z",
        },
      ],
    });
    const user = userEvent.setup();
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    expect(within(desktop).queryByRole("button", { name: "打开分享" })).toBeNull();
    const rail = screen.getByRole("complementary", { name: "系统快捷栏" });
    await user.click(within(rail).getByRole("button", { name: "打开分享" }));
    expect(await screen.findByRole("dialog", { name: "分享" })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "关闭分享" }));
    await user.click(within(desktop).getByRole("button", { name: "打开文件管理" }));
    const dialog = await screen.findByRole("dialog", { name: "文件管理" });
    await user.click(await within(dialog).findByRole("row", { name: /分享计划.txt/ }));
    expect(within(dialog).getByRole("button", { name: "分享" }).hasAttribute("disabled")).toBe(false);
    await user.click(within(dialog).getByRole("button", { name: "分享" }));
    expect(await screen.findByRole("dialog", { name: "分享给成员" })).toBeTruthy();
  });

  it("does not share administrative viewing files", async () => {
    installAPI({
      spaces: [
        {
          id: "viewed",
          kind: "private",
          name: "Alice",
          ownerUserId: "alice",
          createdAt: "2026-10-07T10:00:00Z",
          viewing: { grantId: "grant", expiresAt: "2099-01-01T00:00:00Z" },
        },
      ],
      entries: [
        {
          id: "viewed-file",
          spaceId: "viewed",
          name: "查看计划.txt",
          kind: "file",
          sizeBytes: 4,
          modifiedAt: "2026-10-07T10:00:00Z",
        },
      ],
    });
    const user = userEvent.setup();
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    await user.click(within(desktop).getByRole("button", { name: "打开文件管理" }));
    const dialog = await screen.findByRole("dialog", { name: "文件管理" });
    await user.click(await within(dialog).findByRole("row", { name: /查看计划.txt/ }));
    expect(within(dialog).getByRole("button", { name: "分享" }).hasAttribute("disabled")).toBe(true);
  });
});

describe("file manager", () => {
  it.each(["clip.mp4", "photo.HEIC", "photo.heif"])("shows a thumbnail for %s in file management", async (name) => {
    installAPI({
      spaces: [{ id: "space:shared", kind: "shared", name: "Shared", createdAt: "2026-10-07T10:00:00Z" }],
      entries: [
        {
          id: "file:preview",
          spaceId: "space:shared",
          name,
          kind: "file",
          sizeBytes: 1024,
          modifiedAt: "2026-10-14T00:00:00Z",
        },
      ],
    });
    const user = userEvent.setup();
    render(<App />);
    await user.click(await screen.findByRole("button", { name: "打开文件管理" }));
    const dialog = await screen.findByRole("dialog", { name: "文件管理" });
    await within(dialog).findByText(name);
    const row = dialog.querySelector<HTMLElement>('[data-file-item="file:preview"]')!;
    expect(row.querySelector("img")?.getAttribute("src")).toContain(
      "/api/v1/files/file%3Apreview/preview?size=thumbnail",
    );
    fireEvent.error(row.querySelector("img")!);
    expect(row.querySelector("img")?.hidden).toBe(true);
    await user.click(row);
    const inspector = within(dialog).getByRole("complementary", { name: "详细信息" });
    expect(
      within(inspector)
        .getByRole("img", { name: name + " 的预览" })
        .getAttribute("src"),
    ).toContain("/preview?size=preview");
    if (name !== "clip.mp4") {
      await user.dblClick(row);
      const viewer = await within(dialog).findByRole("dialog", { name: "预览 " + name });
      expect(within(viewer).getByRole("img", { name }).getAttribute("src")).toContain("/preview?size=preview");
      fireEvent.error(within(viewer).getByRole("img", { name }));
      expect(within(viewer).getByRole("alert").textContent).toContain("无法预览这张图片");
      expect(within(viewer).getAllByRole("button", { name: "下载" }).length).toBeGreaterThan(0);
    }
  });

  it("pins folders and preserves manual ordering across views and reopening without moving files", async () => {
    const fetchMock = installAPI({
      spaces: [{ id: "space:shared", kind: "shared", name: "Shared", createdAt: "2026-10-07T10:00:00Z" }],
      entries: [
        { id: "a", spaceId: "space:shared", name: "A", kind: "directory", sizeBytes: 0, modifiedAt: "2026-10-01" },
        { id: "b", spaceId: "space:shared", name: "B", kind: "directory", sizeBytes: 0, modifiedAt: "2026-10-02" },
        { id: "c", spaceId: "space:shared", name: "C.bin", kind: "file", sizeBytes: 12, modifiedAt: "2026-10-03" },
      ],
    });
    const user = userEvent.setup();
    const mounted = render(<App />);
    await user.click(await screen.findByRole("button", { name: "打开文件管理" }));
    let dialog = await screen.findByRole("dialog", { name: "文件管理" });
    const item = (id: string) => dialog.querySelector<HTMLElement>(`[data-file-item="${id}"]`)!;
    const order = () =>
      Array.from(dialog.querySelectorAll<HTMLElement>("[data-file-item]")).map((row) => row.dataset.fileItem);
    await waitFor(() => expect(order()).toEqual(["a", "b", "c"]));
    fireEvent.contextMenu(item("b"));
    await user.click(screen.getByRole("menuitem", { name: "置顶文件夹" }));
    expect(order()).toEqual(["b", "a", "c"]);
    expect(item("b").querySelector('[aria-label="已置顶"]')).toBeTruthy();
    await user.click(within(dialog).getByRole("button", { name: "切换排序方向" }));
    expect(order()[0]).toBe("b");
    await user.selectOptions(within(dialog).getByLabelText("排序方式"), "manual");
    await user.click(item("c"));
    await user.click(within(dialog).getByRole("button", { name: "上移所选项目" }));
    expect(order()).toEqual(["b", "c", "a"]);
    expect(within(dialog).getByRole("button", { name: "上移所选项目" }).hasAttribute("disabled")).toBe(true);
    await user.click(within(dialog).getByRole("button", { name: "查看" }));
    await user.click(within(dialog).getByRole("button", { name: "图标视图" }));
    expect(order()).toEqual(["b", "c", "a"]);
    const dataTransfer = { types: ["text/plain"], setData: vi.fn(), effectAllowed: "", dropEffect: "" };
    fireEvent.dragStart(item("a"), { dataTransfer });
    fireEvent.dragOver(item("c"), { dataTransfer });
    fireEvent.drop(item("c"), { dataTransfer });
    expect(order()).toEqual(["b", "a", "c"]);
    expect(transferStore.getSnapshot().uploads).toHaveLength(0);
    await user.type(within(dialog).getByLabelText("搜索当前目录"), "A");
    expect(item("a").draggable).toBe(false);
    expect(within(dialog).getByRole("button", { name: "下移所选项目" }).hasAttribute("disabled")).toBe(true);
    await user.clear(within(dialog).getByLabelText("搜索当前目录"));
    await user.selectOptions(within(dialog).getByLabelText("排序方式"), "name");
    await user.selectOptions(within(dialog).getByLabelText("排序方式"), "manual");
    expect(order()).toEqual(["b", "a", "c"]);
    mounted.unmount();
    render(<App />);
    await user.click(await screen.findByRole("button", { name: "打开文件管理" }));
    dialog = await screen.findByRole("dialog", { name: "文件管理" });
    await waitFor(() => expect(order()).toEqual(["b", "a", "c"]));
    expect((within(dialog).getByLabelText("排序方式") as HTMLSelectElement).value).toBe("manual");
    await user.click(item("b"));
    await user.click(within(dialog).getByRole("button", { name: "取消置顶" }));
    expect(item("b").querySelector('[aria-label="已置顶"]')).toBeNull();
    expect(fetchMock.mock.calls.some(([, init]) => init?.method === "POST")).toBe(false);
  });

  it("enables file management and lists only the signed-in user's spaces", async () => {
    const fetchMock = installAPI({
      spaces: [
        {
          id: "space:owner",
          kind: "private",
          name: "owner",
          ownerUserId: "user:owner",
          createdAt: "2026-10-07T10:00:00Z",
        },
        { id: "space:shared", kind: "shared", name: "Shared", createdAt: "2026-10-07T10:00:00Z" },
      ],
      entries: [
        {
          id: "file:1",
          spaceId: "space:owner",
          name: "家庭",
          kind: "directory",
          sizeBytes: 0,
          modifiedAt: "2026-10-07T10:00:00Z",
        },
        {
          id: "file:2",
          spaceId: "space:owner",
          name: "A.txt",
          kind: "file",
          sizeBytes: 12,
          modifiedAt: "2026-10-08T10:00:00Z",
        },
      ],
      volumes: [
        {
          id: "volume:data",
          diskIds: ["disk:data"],
          layout: "single",
          capacityBytes: 480 * 1024 ** 3,
          availableBytes: 278 * 1024 ** 3,
          state: "available",
        },
      ],
    });
    const user = userEvent.setup();
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    const fileButton = within(desktop).getByRole("button", { name: "打开文件管理" });
    expect(fileButton.hasAttribute("disabled")).toBe(false);
    await user.click(fileButton);
    const dialog = await screen.findByRole("dialog", { name: "文件管理" });
    expect(await screen.findByText("家庭")).toBeTruthy();
    const managerStyle = getComputedStyle(dialog.querySelector<HTMLElement>(".file-manager")!);
    const commandStyle = getComputedStyle(within(dialog).getByRole("button", { name: "新建" }));
    expect(getComputedStyle(dialog.querySelector<HTMLElement>(".file-action-bar")!).overflowX).not.toBe("visible");
    expect(managerStyle.getPropertyValue("--file-font-command").trim()).toBe("12px");
    expect(commandStyle.fontSize).toBe("var(--file-font-command)");
    expect(commandStyle.lineHeight).toBe("16px");
    const commandIcon = within(dialog).getByRole("button", { name: "新建" }).querySelector("svg")!;
    const commandLabel = within(dialog).getByRole("button", { name: "新建" }).querySelector("span")!;
    expect(getComputedStyle(commandIcon).display).toBe("block");
    expect(getComputedStyle(commandLabel).alignItems).toBe("center");
    expect(getComputedStyle(commandLabel).height).toBe("16px");
    expect(within(dialog).getByRole("button", { name: "个人空间owner" })).toBeTruthy();
    expect(within(dialog).getByRole("button", { name: "共享空间共享" })).toBeTruthy();
    expect(within(dialog).getAllByRole("separator")).toHaveLength(2);
    expect(within(dialog).getByText("202 GiB")).toBeTruthy();
    const rows = within(dialog).getAllByRole("row");
    expect(rows[1].textContent).toContain("家庭");
    expect(rows[1].querySelector(".file-type-cell .directory")).toBeTruthy();
    await user.click(rows.find((row) => row.textContent?.includes("A.txt"))!);
    await user.click(within(dialog).getByRole("button", { name: "复制" }));
    await user.click(within(dialog).getByRole("button", { name: "粘贴" }));
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/files/file%3A2/copies",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ spaceId: "space:owner", parentId: "", name: "A - 副本.txt" }),
      }),
    );
    const inspector = within(dialog).getByRole("complementary", { name: "详细信息" });
    expect(within(inspector).queryByRole("button")).toBeNull();
    await user.dblClick(
      within(dialog)
        .getAllByRole("row")
        .find((row) => row.textContent?.includes("图库"))!,
    );
    expect(await screen.findByRole("dialog", { name: "相册" })).toBeTruthy();
  });

  it("moves a cut file into the space it is pasted in", async () => {
    const fetchMock = installAPI({
      spaces: [
        {
          id: "space:owner",
          kind: "private",
          name: "owner",
          ownerUserId: "user:owner",
          createdAt: "2026-10-07T10:00:00Z",
        },
        { id: "space:shared", kind: "shared", name: "Shared", createdAt: "2026-10-07T10:00:00Z" },
      ],
      entries: [
        {
          id: "file:film",
          spaceId: "space:owner",
          name: "Heat (1995).mp4",
          kind: "file",
          sizeBytes: 12,
          modifiedAt: "2026-10-08T10:00:00Z",
        },
      ],
    });
    const user = userEvent.setup();
    render(<App />);
    await user.click(await screen.findByRole("button", { name: "打开文件管理" }));
    const dialog = await screen.findByRole("dialog", { name: "文件管理" });
    await user.click(
      (await within(dialog).findAllByRole("row")).find((row) => row.textContent?.includes("Heat (1995).mp4"))!,
    );
    await user.click(within(dialog).getByRole("button", { name: "剪切" }));
    // Pasting where the file already is would do nothing.
    expect(within(dialog).getByRole("button", { name: "粘贴" }).hasAttribute("disabled")).toBe(true);

    await user.click(within(dialog).getByRole("button", { name: "共享空间共享" }));
    await waitFor(() =>
      expect(within(dialog).getByRole("button", { name: "粘贴" }).hasAttribute("disabled")).toBe(false),
    );
    await user.click(within(dialog).getByRole("button", { name: "粘贴" }));
    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        "/api/v1/files/file%3Afilm",
        expect.objectContaining({
          method: "PATCH",
          body: JSON.stringify({ spaceId: "space:shared", parentId: "", name: "Heat (1995).mp4" }),
        }),
      ),
    );
    expect(within(dialog).queryByText(/已剪切/)).toBeNull();
  });

  it("downloads files through the transfer center only on double click and exposes the file edit context menu", async () => {
    const fetchMock = installAPI({
      spaces: [
        {
          id: "space:owner",
          kind: "private",
          name: "owner",
          ownerUserId: "user:owner",
          createdAt: "2026-10-07T10:00:00Z",
        },
      ],
      entries: [
        {
          id: "file:note",
          spaceId: "space:owner",
          name: "notes.txt",
          kind: "file",
          sizeBytes: 12,
          modifiedAt: "2026-10-08T10:00:00Z",
        },
      ],
      textPreview: "hello",
    });
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    const user = userEvent.setup();
    render(<App />);
    await user.click(await screen.findByRole("button", { name: "打开文件管理" }));
    const dialog = await screen.findByRole("dialog", { name: "文件管理" });
    const row = (await within(dialog).findAllByRole("row")).find((item) => item.textContent?.includes("notes.txt"))!;

    await user.click(row);
    expect(click).not.toHaveBeenCalled();
    expect(await within(dialog).findByText("hello")).toBeTruthy();
    await user.dblClick(row);
    await waitFor(() => expect(click).toHaveBeenCalledTimes(1));
    const created = fetchMock.mock.calls.find(([path]) => path === "/api/v1/transfers/downloads");
    expect(JSON.parse(String(created?.[1]?.body))).toEqual({ entryIds: ["file:note"] });
    expect(click.mock.contexts[0]).toHaveProperty(
      "href",
      expect.stringContaining("/api/v1/transfers/download-1/content"),
    );
    const manager = dialog.querySelector<HTMLElement>(".file-manager")!;
    manager.getBoundingClientRect = () => ({
      left: 50,
      top: 70,
      right: 950,
      bottom: 690,
      width: 900,
      height: 620,
      x: 50,
      y: 70,
      toJSON: () => ({}),
    });
    fireEvent.contextMenu(row, { clientX: 120, clientY: 160 });
    const menu = await screen.findByRole("menu", { name: "文件操作" });
    expect(menu.getAttribute("style")).toContain("left: 70px");
    expect(menu.getAttribute("style")).toContain("top: 90px");
    expect(within(menu).getByRole("menuitem", { name: "下载" })).toBeTruthy();
    expect(within(menu).getByRole("menuitem", { name: /重命名/ })).toBeTruthy();
    expect(within(menu).getByRole("menuitem", { name: /移到回收站/ })).toBeTruthy();
  });

  it("previews images and PDFs in place on double click and keeps download a separate action", async () => {
    const fetchMock = installAPI({
      spaces: [
        {
          id: "space:owner",
          kind: "private",
          name: "owner",
          ownerUserId: "user:owner",
          createdAt: "2026-10-07T10:00:00Z",
        },
      ],
      entries: [
        {
          id: "file:beach",
          spaceId: "space:owner",
          name: "beach.jpg",
          kind: "file",
          sizeBytes: 20 * 1024 ** 2,
          modifiedAt: "2026-10-08T10:00:00Z",
        },
        {
          id: "file:cat",
          spaceId: "space:owner",
          name: "cat.png",
          kind: "file",
          sizeBytes: 300 * 1024,
          modifiedAt: "2026-10-08T10:00:00Z",
        },
        {
          id: "file:manual",
          spaceId: "space:owner",
          name: "manual.pdf",
          kind: "file",
          sizeBytes: 90 * 1024,
          modifiedAt: "2026-10-08T10:00:00Z",
        },
        {
          id: "file:archive",
          spaceId: "space:owner",
          name: "photos.zip",
          kind: "file",
          sizeBytes: 5,
          modifiedAt: "2026-10-08T10:00:00Z",
        },
      ],
    });
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    const user = userEvent.setup();
    render(<App />);
    await user.click(await screen.findByRole("button", { name: "打开文件管理" }));
    const dialog = await screen.findByRole("dialog", { name: "文件管理" });
    const rows = await within(dialog).findAllByRole("row");
    const row = (name: string) => rows.find((item) => item.textContent?.includes(name))!;
    expect(row("beach.jpg").textContent).toContain("双击预览");
    expect(row("manual.pdf").textContent).toContain("双击预览");
    expect(row("photos.zip").textContent).toContain("双击下载");

    await user.dblClick(row("beach.jpg"));
    const viewer = await within(dialog).findByRole("dialog", { name: "预览 beach.jpg" });
    expect(within(viewer).getByText("20.0 MiB · 1 / 3")).toBeTruthy();
    expect(within(viewer).getByRole("img", { name: "beach.jpg" }).getAttribute("src")).toContain(
      "/api/v1/files/file%3Abeach/content?disposition=inline",
    );
    expect(click).not.toHaveBeenCalled();
    await user.keyboard("{ArrowRight}");
    const cat = await within(dialog).findByRole("dialog", { name: "预览 cat.png" });
    await user.click(within(cat).getByRole("button", { name: "下载" }));
    await waitFor(() => expect(click).toHaveBeenCalledTimes(1));
    const created = fetchMock.mock.calls.find(([path]) => path === "/api/v1/transfers/downloads");
    expect(JSON.parse(String(created?.[1]?.body))).toEqual({ entryIds: ["file:cat"] });
    await user.keyboard("{Escape}");
    expect(within(dialog).queryByRole("dialog", { name: /^预览/ })).toBeNull();
    // The last file viewed stays selected.
    expect(row("cat.png").getAttribute("aria-selected")).toBe("true");

    const manager = dialog.querySelector<HTMLElement>(".file-manager")!;
    manager.getBoundingClientRect = () => ({
      left: 50,
      top: 70,
      right: 950,
      bottom: 690,
      width: 900,
      height: 620,
      x: 50,
      y: 70,
      toJSON: () => ({}),
    });
    fireEvent.contextMenu(row("beach.jpg"), { clientX: 120, clientY: 160 });
    const menu = await screen.findByRole("menu", { name: "文件操作" });
    await user.click(within(menu).getByRole("menuitem", { name: "预览" }));
    await user.click(
      within(await within(dialog).findByRole("dialog", { name: "预览 beach.jpg" })).getByRole("button", {
        name: "关闭预览",
      }),
    );
    fireEvent.contextMenu(row("beach.jpg"), { clientX: 120, clientY: 160 });
    await user.click(
      within(await screen.findByRole("menu", { name: "文件操作" })).getByRole("menuitem", { name: "下载" }),
    );
    await waitFor(() => expect(click).toHaveBeenCalledTimes(2));

    await user.dblClick(row("photos.zip"));
    await waitFor(() => expect(click).toHaveBeenCalledTimes(3));
    expect(within(dialog).queryByRole("dialog", { name: /^预览/ })).toBeNull();
  });

  it("uploads picked and dropped files through the transfer center without blocking the folder", async () => {
    const fetchMock = installAPI({
      spaces: [{ id: "space:shared", kind: "shared", name: "Shared", createdAt: "2026-10-07T10:00:00Z" }],
      entries: [
        {
          id: "file:old",
          spaceId: "space:shared",
          name: "old.txt",
          kind: "file",
          sizeBytes: 3,
          modifiedAt: "2026-10-08T10:00:00Z",
        },
      ],
    });
    const user = userEvent.setup();
    render(<App />);
    await user.click(await screen.findByRole("button", { name: "打开文件管理" }));
    const dialog = await screen.findByRole("dialog", { name: "文件管理" });
    await within(dialog).findByText("old.txt");
    await user.click(within(dialog).getByRole("button", { name: "上传到当前目录" }));
    expect(within(dialog).getByRole("menuitem", { name: /上传文件夹/ })).toBeTruthy();
    await user.upload(within(dialog).getByLabelText("选择要上传的文件"), [
      new File(["hello"], "a.txt"),
      new File(["world!"], "b.txt"),
    ]);
    expect(tus.uploads).toHaveLength(2);
    expect(tus.uploads[0].options.metadata).toMatchObject({
      filename: "a.txt",
      spaceId: "space:shared",
      parentId: "",
      conflict: "ask",
    });
    // Other commands stay available while files upload.
    expect(within(dialog).getByRole("button", { name: "新建" }).hasAttribute("disabled")).toBe(false);
    tus.uploads[0].options.onProgress(5, 5);
    expect(await within(dialog).findByRole("button", { name: /正在上传 2 个文件/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: /传输中心：正在上传 .+ 等 2 个任务/ })).toBeTruthy();

    const browser = within(dialog).getByRole("region", { name: "文件浏览器" });
    const dataTransfer = { types: ["Files"], items: [], files: [new File(["dropped"], "c.txt")], dropEffect: "none" };
    fireEvent.dragEnter(browser, { dataTransfer });
    expect(within(dialog).getByText("松开即可上传")).toBeTruthy();
    fireEvent.drop(browser, { dataTransfer });
    await waitFor(() => expect(tus.uploads).toHaveLength(3));
    expect(within(dialog).queryByText("松开即可上传")).toBeNull();

    const listed = () =>
      fetchMock.mock.calls.filter(([path]) => String(path).startsWith("/api/v1/spaces/space%3Ashared/entries")).length;
    const before = listed();
    tus.uploads[0].options.onSuccess();
    await waitFor(() => expect(listed()).toBeGreaterThan(before));
    await user.click(screen.getByRole("button", { name: /传输中心：正在上传 .+ 等 2 个任务/ }));
    const center = await screen.findByRole("dialog", { name: "传输中心" });
    expect(await within(center).findByRole("heading", { name: "正在传输" })).toBeTruthy();
  });

  it("downloads a folder as one archive from the context menu", async () => {
    const fetchMock = installAPI({
      spaces: [
        {
          id: "space:owner",
          kind: "private",
          name: "owner",
          ownerUserId: "user:owner",
          createdAt: "2026-10-07T10:00:00Z",
        },
      ],
      entries: [
        {
          id: "file:album",
          spaceId: "space:owner",
          name: "相册",
          kind: "directory",
          sizeBytes: 0,
          modifiedAt: "2026-10-08T10:00:00Z",
        },
      ],
    });
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    const user = userEvent.setup();
    render(<App />);
    await user.click(await screen.findByRole("button", { name: "打开文件管理" }));
    const dialog = await screen.findByRole("dialog", { name: "文件管理" });
    const row = (await within(dialog).findAllByRole("row")).find(
      (item) => item.textContent?.includes("相册") && item.textContent?.includes("文件夹"),
    )!;
    const manager = dialog.querySelector<HTMLElement>(".file-manager")!;
    manager.getBoundingClientRect = () => ({
      left: 50,
      top: 70,
      right: 950,
      bottom: 690,
      width: 900,
      height: 620,
      x: 50,
      y: 70,
      toJSON: () => ({}),
    });
    fireEvent.contextMenu(row, { clientX: 120, clientY: 160 });
    await user.click(
      within(await screen.findByRole("menu", { name: "文件操作" })).getByRole("menuitem", { name: /打包下载/ }),
    );
    await waitFor(() =>
      expect(fetchMock.mock.calls.some(([path]) => path === "/api/v1/transfers/downloads")).toBe(true),
    );
    const created = fetchMock.mock.calls.find(([path]) => path === "/api/v1/transfers/downloads");
    expect(JSON.parse(String(created?.[1]?.body))).toEqual({ entryIds: ["file:album"] });
  });

  it("selects multiple files with a pointer selection box", async () => {
    installAPI({
      spaces: [{ id: "space:shared", kind: "shared", name: "Shared", createdAt: "2026-10-07T10:00:00Z" }],
      entries: [
        {
          id: "file:a",
          spaceId: "space:shared",
          name: "A.txt",
          kind: "file",
          sizeBytes: 12,
          modifiedAt: "2026-10-08T10:00:00Z",
        },
        {
          id: "file:b",
          spaceId: "space:shared",
          name: "B.txt",
          kind: "file",
          sizeBytes: 18,
          modifiedAt: "2026-10-08T10:00:00Z",
        },
      ],
      textPreview: "hello",
    });
    const user = userEvent.setup();
    render(<App />);
    await user.click(await screen.findByRole("button", { name: "打开文件管理" }));
    const dialog = await screen.findByRole("dialog", { name: "文件管理" });
    const shell = dialog.querySelector<HTMLElement>(".file-list-shell")!;
    shell.getBoundingClientRect = () => ({
      left: 0,
      top: 0,
      right: 500,
      bottom: 500,
      width: 500,
      height: 500,
      x: 0,
      y: 0,
      toJSON: () => ({}),
    });
    Array.from(shell.querySelectorAll<HTMLElement>("[data-file-item]")).forEach((row, index) => {
      row.getBoundingClientRect = () => ({
        left: 0,
        top: 40 + index * 50,
        right: 500,
        bottom: 88 + index * 50,
        width: 500,
        height: 48,
        x: 0,
        y: 40 + index * 50,
        toJSON: () => ({}),
      });
    });

    fireEvent.pointerDown(shell, { button: 0, clientX: 480, clientY: 25 });
    fireEvent.pointerMove(window, { clientX: 10, clientY: 150 });
    expect(await within(dialog).findByText("已选择 2 个项目")).toBeTruthy();
    fireEvent.pointerUp(window, { clientX: 10, clientY: 150 });
  });

  it("previews an image file in the details pane", async () => {
    installAPI({
      spaces: [
        {
          id: "space:owner",
          kind: "private",
          name: "owner",
          ownerUserId: "user:owner",
          createdAt: "2026-10-07T10:00:00Z",
        },
      ],
      entries: [
        {
          id: "file:photo",
          spaceId: "space:owner",
          name: "family-photo.jpg",
          kind: "file",
          sizeBytes: 2048,
          modifiedAt: "2026-10-08T10:00:00Z",
        },
      ],
    });
    const user = userEvent.setup();
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    await user.click(within(desktop).getByRole("button", { name: "打开文件管理" }));
    const dialog = await screen.findByRole("dialog", { name: "文件管理" });
    const row = (await within(dialog).findAllByRole("row")).find((item) =>
      item.textContent?.includes("family-photo.jpg"),
    );
    const thumbnail = row?.querySelector(".file-type-icon img");
    expect(thumbnail?.getAttribute("src")).toBe("/api/v1/files/file%3Aphoto/content?disposition=inline");
    await user.click(row!);

    const inspector = within(dialog).getByRole("complementary", { name: "详细信息" });
    const preview = within(inspector).getByRole("img", { name: "family-photo.jpg 的预览" });
    expect(preview.getAttribute("src")).toBe("/api/v1/files/file%3Aphoto/content?disposition=inline");
  });

  it("does not report the whole data volume as used when usage is unavailable", async () => {
    installAPI({
      spaces: [
        {
          id: "space:owner",
          kind: "private",
          name: "owner",
          ownerUserId: "user:owner",
          createdAt: "2026-10-07T10:00:00Z",
        },
      ],
      volumes: [
        {
          id: "volume:data",
          diskIds: ["disk:data"],
          layout: "single",
          capacityBytes: 500_107_862_016,
          state: "available",
        },
      ],
    });
    const user = userEvent.setup();
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    await user.click(within(desktop).getByRole("button", { name: "打开文件管理" }));
    const dialog = await screen.findByRole("dialog", { name: "文件管理" });
    const values = dialog.querySelectorAll(".file-volume-values strong");

    expect(values[0]?.textContent).toBe("—");
    expect(values[1]?.textContent).toBe("466 GiB");
  });
});

describe("ADR 0008 viewing in the file manager", () => {
  it("shows a viewed private space read-only and can end the viewing", async () => {
    const fetchMock = installAPI({
      spaces: [
        {
          id: "space:alice",
          kind: "private",
          name: "alice",
          ownerUserId: "user:alice",
          createdAt: "2026-10-07T10:00:00Z",
          viewing: { grantId: "viewing:1", expiresAt: "2026-10-08T10:00:00Z" },
        },
      ],
      entries: [
        {
          id: "file:1",
          spaceId: "space:alice",
          name: "photo.jpg",
          kind: "file",
          sizeBytes: 10,
          modifiedAt: "2026-10-07T10:00:00Z",
        },
      ],
    });
    const user = userEvent.setup();
    render(<App />);
    const desktop = await screen.findByRole("region", { name: "桌面应用" });
    await user.click(within(desktop).getByRole("button", { name: "打开文件管理" }));
    expect(await screen.findByText("photo.jpg")).toBeTruthy();
    expect(screen.getByRole("status").textContent).toContain("只读查看他人个人空间");
    expect(screen.getByRole("button", { name: "新建" }).hasAttribute("disabled")).toBe(true);
    expect(screen.getByRole("button", { name: "删除" }).hasAttribute("disabled")).toBe(true);
    await user.click(screen.getByRole("button", { name: "结束查看" }));
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/viewing/viewing%3A1",
      expect.objectContaining({ method: "DELETE" }),
    );
  });
});
