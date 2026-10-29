import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { SharesProvider } from "../shares/SharesProvider";
import {
  downloads,
  entry,
  type FilesOptions,
  filesHandlers,
  fileWrites,
  folder,
  ownSpace,
  sharedSpace,
  space,
  volume,
} from "../test/handlers/files";
import { http, server } from "../test/server";
import { transferStore } from "../transfers/store";
import { FilePanel } from "./FilePanel";

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

// serve answers the file manager's reads, and the share provider's, which
// polls for notices and counts unread shares around every window: nothing is
// shared here.
function serve(options: FilesOptions) {
  server.use(
    ...filesHandlers(options),
    http.get("/api/v1/shares", ({ response }) => response(200).json({ items: [] })),
    http.get("/api/v1/shares/notifications", ({ response }) => response(200).json({ items: [] })),
  );
}

// renderFiles renders the file manager as the desktop does, inside the share
// provider; the windows it opens are callbacks the test sees.
function renderFiles() {
  const props = {
    userID: "user:owner",
    onIntentHandled: vi.fn(),
    onOpenTransfers: vi.fn(),
    onOpenPhotos: vi.fn(),
    onOpenMedia: vi.fn(),
    onOpenMusic: vi.fn(),
  };
  const view = render(
    <SharesProvider userId={props.userID} active={false} onOpen={() => undefined}>
      <FilePanel {...props} />
    </SharesProvider>,
  );
  return { ...props, ...view };
}

// The file manager's body, which holds its panes and places its menus.
const manager = () => screen.getByRole("region", { name: "文件浏览器" }).parentElement!;
// jsdom has no layout; rect is the box a test gives an element.
const rect = (left: number, top: number, width: number, height: number): DOMRect => ({
  left,
  top,
  right: left + width,
  bottom: top + height,
  width,
  height,
  x: left,
  y: top,
  toJSON: () => ({}),
});

beforeEach(() => {
  localStorage.clear();
});

afterEach(() => {
  transferStore.reset();
  tus.uploads.length = 0;
  vi.restoreAllMocks();
});

describe("member sharing in the file manager", () => {
  it("shares read-only files", async () => {
    serve({
      spaces: [space("shared", { writable: false })],
      entries: [
        entry("分享计划.txt", {
          id: "shared-file",
          spaceId: "shared",
          sizeBytes: 4,
          modifiedAt: "2026-10-07T10:00:00Z",
        }),
      ],
    });
    // The composer lists the members to share with.
    server.use(http.get("/api/v1/shares/members", ({ response }) => response(200).json({ items: [] })));
    const user = userEvent.setup();
    renderFiles();
    await user.click(await screen.findByRole("row", { name: /分享计划.txt/ }));
    expect(screen.getByRole("button", { name: "分享" }).hasAttribute("disabled")).toBe(false);
    await user.click(screen.getByRole("button", { name: "分享" }));
    expect(await screen.findByRole("dialog", { name: "分享给成员" })).toBeTruthy();
  });

  it("does not share administrative viewing files", async () => {
    serve({
      spaces: [
        space("viewed", {
          kind: "private",
          name: "Alice",
          ownerUserId: "alice",
          viewing: { grantId: "grant", expiresAt: "2099-01-01T00:00:00Z" },
        }),
      ],
      entries: [
        entry("查看计划.txt", {
          id: "viewed-file",
          spaceId: "viewed",
          sizeBytes: 4,
          modifiedAt: "2026-10-07T10:00:00Z",
        }),
      ],
    });
    const user = userEvent.setup();
    renderFiles();
    await user.click(await screen.findByRole("row", { name: /查看计划.txt/ }));
    expect(screen.getByRole("button", { name: "分享" }).hasAttribute("disabled")).toBe(true);
  });
});

describe("file manager", () => {
  it.each(["clip.mp4", "photo.HEIC", "photo.heif"])("shows a thumbnail for %s in file management", async (name) => {
    serve({
      spaces: [sharedSpace],
      entries: [entry(name, { id: "file:preview", sizeBytes: 1024, modifiedAt: "2026-10-14T00:00:00Z" })],
    });
    const user = userEvent.setup();
    renderFiles();
    const row = await screen.findByRole("row", { name: new RegExp(name) });
    expect(row.querySelector("img")?.getAttribute("src")).toContain(
      "/api/v1/files/file%3Apreview/preview?size=thumbnail",
    );
    fireEvent.error(row.querySelector("img")!);
    expect(row.querySelector("img")?.hidden).toBe(true);
    await user.click(row);
    const inspector = screen.getByRole("complementary", { name: "详细信息" });
    expect(
      within(inspector)
        .getByRole("img", { name: name + " 的预览" })
        .getAttribute("src"),
    ).toContain("/preview?size=preview");
    if (name !== "clip.mp4") {
      await user.dblClick(row);
      const viewer = await screen.findByRole("dialog", { name: "预览 " + name });
      expect(within(viewer).getByRole("img", { name }).getAttribute("src")).toContain("/preview?size=preview");
      fireEvent.error(within(viewer).getByRole("img", { name }));
      expect(within(viewer).getByRole("alert").textContent).toContain("无法预览这张图片");
      expect(within(viewer).getAllByRole("button", { name: "下载" }).length).toBeGreaterThan(0);
    }
  });

  it("pins folders and preserves manual ordering across views and reopening without moving files", async () => {
    // The order is kept in this browser: no write is served, so one would
    // fail the test.
    serve({
      spaces: [sharedSpace],
      entries: [
        folder("A", { id: "a", modifiedAt: "2026-10-01" }),
        folder("B", { id: "b", modifiedAt: "2026-10-02" }),
        entry("C.bin", { id: "c", modifiedAt: "2026-10-03" }),
      ],
    });
    const user = userEvent.setup();
    const mounted = renderFiles();
    const item = (id: string) => document.querySelector<HTMLElement>(`[data-file-item="${id}"]`)!;
    const order = () =>
      Array.from(document.querySelectorAll<HTMLElement>("[data-file-item]")).map((row) => row.dataset.fileItem);
    await waitFor(() => expect(order()).toEqual(["a", "b", "c"]));
    fireEvent.contextMenu(item("b"));
    await user.click(screen.getByRole("menuitem", { name: "置顶文件夹" }));
    expect(order()).toEqual(["b", "a", "c"]);
    expect(item("b").querySelector('[aria-label="已置顶"]')).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "切换排序方向" }));
    expect(order()[0]).toBe("b");
    await user.selectOptions(screen.getByLabelText("排序方式"), "manual");
    await user.click(item("c"));
    await user.click(screen.getByRole("button", { name: "上移所选项目" }));
    expect(order()).toEqual(["b", "c", "a"]);
    expect(screen.getByRole("button", { name: "上移所选项目" }).hasAttribute("disabled")).toBe(true);
    await user.click(screen.getByRole("button", { name: "查看" }));
    await user.click(screen.getByRole("button", { name: "图标视图" }));
    expect(order()).toEqual(["b", "c", "a"]);
    const dataTransfer = { types: ["text/plain"], setData: vi.fn(), effectAllowed: "", dropEffect: "" };
    fireEvent.dragStart(item("a"), { dataTransfer });
    fireEvent.dragOver(item("c"), { dataTransfer });
    fireEvent.drop(item("c"), { dataTransfer });
    expect(order()).toEqual(["b", "a", "c"]);
    expect(transferStore.getSnapshot().uploads).toHaveLength(0);
    await user.type(screen.getByLabelText("搜索当前目录"), "A");
    expect(item("a").draggable).toBe(false);
    expect(screen.getByRole("button", { name: "下移所选项目" }).hasAttribute("disabled")).toBe(true);
    await user.clear(screen.getByLabelText("搜索当前目录"));
    await user.selectOptions(screen.getByLabelText("排序方式"), "name");
    await user.selectOptions(screen.getByLabelText("排序方式"), "manual");
    expect(order()).toEqual(["b", "a", "c"]);
    mounted.unmount();
    renderFiles();
    await waitFor(() => expect(order()).toEqual(["b", "a", "c"]));
    expect((screen.getByLabelText("排序方式") as HTMLSelectElement).value).toBe("manual");
    await user.click(item("b"));
    await user.click(screen.getByRole("button", { name: "取消置顶" }));
    expect(item("b").querySelector('[aria-label="已置顶"]')).toBeNull();
  });

  it("enables file management and lists only the signed-in user's spaces", async () => {
    const writes = fileWrites();
    serve({
      spaces: [ownSpace, sharedSpace],
      entries: [
        folder("家庭", { id: "file:1", spaceId: ownSpace.id, modifiedAt: "2026-10-07T10:00:00Z" }),
        entry("A.txt", { id: "file:2", spaceId: ownSpace.id }),
      ],
      volumes: [volume({ capacityBytes: 480 * 1024 ** 3, availableBytes: 278 * 1024 ** 3 })],
    });
    server.use(...writes.handlers);
    const user = userEvent.setup();
    const { onOpenPhotos } = renderFiles();
    expect(await screen.findByText("家庭")).toBeTruthy();
    const managerStyle = getComputedStyle(manager());
    const commandStyle = getComputedStyle(screen.getByRole("button", { name: "新建" }));
    expect(getComputedStyle(screen.getByLabelText("文件命令")).overflowX).not.toBe("visible");
    expect(managerStyle.getPropertyValue("--file-font-command").trim()).toBe("12px");
    expect(commandStyle.fontSize).toBe("var(--file-font-command)");
    expect(commandStyle.lineHeight).toBe("16px");
    const commandIcon = screen.getByRole("button", { name: "新建" }).querySelector("svg")!;
    const commandLabel = screen.getByRole("button", { name: "新建" }).querySelector("span")!;
    expect(getComputedStyle(commandIcon).display).toBe("block");
    expect(getComputedStyle(commandLabel).alignItems).toBe("center");
    expect(getComputedStyle(commandLabel).height).toBe("16px");
    expect(screen.getByRole("button", { name: "个人空间owner" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "共享空间共享" })).toBeTruthy();
    expect(screen.getAllByRole("separator")).toHaveLength(2);
    expect(screen.getByText("202 GiB")).toBeTruthy();
    const rows = screen.getAllByRole("row");
    expect(rows[1].textContent).toContain("家庭");
    // The type column marks a folder apart from a file.
    const type = (row: HTMLElement) => within(row).getAllByRole("cell")[2];
    const fileRow = rows.find((row) => row.textContent?.includes("A.txt"))!;
    expect(type(rows[1]).textContent).toBe("文件夹");
    expect(getComputedStyle(type(rows[1]).firstElementChild!).backgroundColor).not.toBe(
      getComputedStyle(type(fileRow).firstElementChild!).backgroundColor,
    );
    await user.click(fileRow);
    await user.click(screen.getByRole("button", { name: "复制" }));
    await user.click(screen.getByRole("button", { name: "粘贴" }));
    await waitFor(() =>
      expect(writes.copies).toEqual([{ fileId: "file:2", spaceId: "space:owner", parentId: "", name: "A - 副本.txt" }]),
    );
    const inspector = screen.getByRole("complementary", { name: "详细信息" });
    expect(within(inspector).queryByRole("button")).toBeNull();
    // Pasting refreshes asynchronously; do not click a row that the loading
    // state is about to remove from the document.
    await waitFor(() => {
      expect(screen.queryByText("正在复制 1 个项目…")).toBeNull();
      expect(screen.getByRole("table", { name: "文件列表" }).getAttribute("aria-busy")).toBe("false");
    });
    await user.dblClick(await screen.findByRole("row", { name: /图库/ }));
    expect(onOpenPhotos).toHaveBeenCalledOnce();
  });

  it("moves a cut file into the space it is pasted in", async () => {
    const writes = fileWrites();
    serve({
      spaces: [ownSpace, sharedSpace],
      entries: [entry("Heat (1995).mp4", { id: "file:film", spaceId: ownSpace.id })],
    });
    server.use(...writes.handlers);
    const user = userEvent.setup();
    renderFiles();
    await user.click(await screen.findByRole("row", { name: /Heat \(1995\)\.mp4/ }));
    await user.click(screen.getByRole("button", { name: "剪切" }));
    // Pasting where the file already is would do nothing.
    expect(screen.getByRole("button", { name: "粘贴" }).hasAttribute("disabled")).toBe(true);

    await user.click(screen.getByRole("button", { name: "共享空间共享" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "粘贴" }).hasAttribute("disabled")).toBe(false));
    await user.click(screen.getByRole("button", { name: "粘贴" }));
    await waitFor(() =>
      expect(writes.moves).toEqual([
        { fileId: "file:film", spaceId: "space:shared", parentId: "", name: "Heat (1995).mp4" },
      ]),
    );
    await waitFor(() => expect(screen.queryByText(/已剪切/)).toBeNull());
  });

  it("downloads files through the transfer center only on double click and exposes the file edit context menu", async () => {
    const notes = entry("notes.txt", { id: "file:note", spaceId: ownSpace.id });
    const asked = downloads([notes]);
    serve({ spaces: [ownSpace], entries: [notes], text: "hello" });
    server.use(asked.handler);
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    const user = userEvent.setup();
    renderFiles();
    const row = await screen.findByRole("row", { name: /notes\.txt/ });

    await user.click(row);
    expect(click).not.toHaveBeenCalled();
    expect(await screen.findByText("hello")).toBeTruthy();
    await user.dblClick(row);
    await waitFor(() => expect(click).toHaveBeenCalledTimes(1));
    expect(asked.sent).toEqual([["file:note"]]);
    expect(click.mock.contexts[0]).toHaveProperty(
      "href",
      expect.stringContaining("/api/v1/transfers/download-1/content"),
    );
    manager().getBoundingClientRect = () => rect(50, 70, 900, 620);
    fireEvent.contextMenu(row, { clientX: 120, clientY: 160 });
    const menu = await screen.findByRole("menu", { name: "文件操作" });
    expect(menu.getAttribute("style")).toContain("left: 70px");
    expect(menu.getAttribute("style")).toContain("top: 90px");
    expect(within(menu).getByRole("menuitem", { name: "下载" })).toBeTruthy();
    expect(within(menu).getByRole("menuitem", { name: /重命名/ })).toBeTruthy();
    expect(within(menu).getByRole("menuitem", { name: /移到回收站/ })).toBeTruthy();
  });

  it("previews images and PDFs in place on double click and keeps download a separate action", async () => {
    const entries = [
      entry("beach.jpg", { id: "file:beach", spaceId: ownSpace.id, sizeBytes: 20 * 1024 ** 2 }),
      entry("cat.png", { id: "file:cat", spaceId: ownSpace.id, sizeBytes: 300 * 1024 }),
      entry("manual.pdf", { id: "file:manual", spaceId: ownSpace.id, sizeBytes: 90 * 1024 }),
      entry("photos.zip", { id: "file:archive", spaceId: ownSpace.id, sizeBytes: 5 }),
    ];
    const asked = downloads(entries);
    serve({ spaces: [ownSpace], entries });
    server.use(asked.handler);
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    const user = userEvent.setup();
    renderFiles();
    await screen.findByRole("row", { name: /photos\.zip/ });
    const row = (name: string) => screen.getAllByRole("row").find((item) => item.textContent?.includes(name))!;
    expect(row("beach.jpg").textContent).toContain("双击预览");
    expect(row("manual.pdf").textContent).toContain("双击预览");
    expect(row("photos.zip").textContent).toContain("双击下载");

    await user.dblClick(row("beach.jpg"));
    const viewer = await screen.findByRole("dialog", { name: "预览 beach.jpg" });
    expect(within(viewer).getByText("20.0 MiB · 1 / 3")).toBeTruthy();
    expect(within(viewer).getByRole("img", { name: "beach.jpg" }).getAttribute("src")).toContain(
      "/api/v1/files/file%3Abeach/content?disposition=inline",
    );
    expect(click).not.toHaveBeenCalled();
    await user.keyboard("{ArrowRight}");
    const cat = await screen.findByRole("dialog", { name: "预览 cat.png" });
    await user.click(within(cat).getByRole("button", { name: "下载" }));
    await waitFor(() => expect(click).toHaveBeenCalledTimes(1));
    expect(asked.sent).toEqual([["file:cat"]]);
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog", { name: /^预览/ })).toBeNull();
    // The last file viewed stays selected.
    expect(row("cat.png").getAttribute("aria-selected")).toBe("true");

    manager().getBoundingClientRect = () => rect(50, 70, 900, 620);
    fireEvent.contextMenu(row("beach.jpg"), { clientX: 120, clientY: 160 });
    const menu = await screen.findByRole("menu", { name: "文件操作" });
    await user.click(within(menu).getByRole("menuitem", { name: "预览" }));
    await user.click(
      within(await screen.findByRole("dialog", { name: "预览 beach.jpg" })).getByRole("button", {
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
    expect(screen.queryByRole("dialog", { name: /^预览/ })).toBeNull();
    expect(asked.sent).toEqual([["file:cat"], ["file:beach"], ["file:archive"]]);
  });

  it("uploads picked and dropped files through the transfer center without blocking the folder", async () => {
    const listed = vi.fn();
    serve({ spaces: [sharedSpace], entries: [entry("old.txt", { id: "file:old", sizeBytes: 3 })], onList: listed });
    // The desktop attaches the transfer store to the signed-in account, which
    // reads the account's transfers; uploads start only then.
    server.use(http.get("/api/v1/transfers", ({ response }) => response(200).json({ items: [] })));
    transferStore.attach("user:owner");
    const user = userEvent.setup();
    const { onOpenTransfers } = renderFiles();
    await screen.findByText("old.txt");
    await user.click(screen.getByRole("button", { name: "上传到当前目录" }));
    expect(screen.getByRole("menuitem", { name: /上传文件夹/ })).toBeTruthy();
    await user.upload(screen.getByLabelText("选择要上传的文件"), [
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
    expect(screen.getByRole("button", { name: "新建" }).hasAttribute("disabled")).toBe(false);
    tus.uploads[0].options.onProgress(5, 5);
    expect(await screen.findByRole("button", { name: /正在上传 2 个文件/ })).toBeTruthy();

    const browser = screen.getByRole("region", { name: "文件浏览器" });
    const dataTransfer = { types: ["Files"], items: [], files: [new File(["dropped"], "c.txt")], dropEffect: "none" };
    fireEvent.dragEnter(browser, { dataTransfer });
    expect(screen.getByText("松开即可上传")).toBeTruthy();
    fireEvent.drop(browser, { dataTransfer });
    await waitFor(() => expect(tus.uploads).toHaveLength(3));
    expect(screen.queryByText("松开即可上传")).toBeNull();

    // A finished upload lists the folder again.
    const before = listed.mock.calls.length;
    tus.uploads[0].options.onSuccess();
    await waitFor(() => expect(listed.mock.calls.length).toBeGreaterThan(before));
    expect(listed).toHaveBeenLastCalledWith("space:shared", "");
    await user.click(screen.getByRole("button", { name: /在传输中心查看/ }));
    expect(onOpenTransfers).toHaveBeenCalledOnce();
  });

  it("downloads a folder as one archive from the context menu", async () => {
    const album = folder("相册", { id: "file:album", spaceId: ownSpace.id });
    const asked = downloads([album]);
    serve({ spaces: [ownSpace], entries: [album] });
    server.use(asked.handler);
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    const user = userEvent.setup();
    renderFiles();
    const row = await screen.findByRole("row", { name: /相册.*文件夹/ });
    manager().getBoundingClientRect = () => rect(50, 70, 900, 620);
    fireEvent.contextMenu(row, { clientX: 120, clientY: 160 });
    await user.click(
      within(await screen.findByRole("menu", { name: "文件操作" })).getByRole("menuitem", { name: /打包下载/ }),
    );
    await waitFor(() => expect(asked.sent).toEqual([["file:album"]]));
  });

  it("selects multiple files with a pointer selection box", async () => {
    serve({
      spaces: [sharedSpace],
      entries: [entry("A.txt", { id: "file:a" }), entry("B.txt", { id: "file:b", sizeBytes: 18 })],
    });
    renderFiles();
    await screen.findByRole("row", { name: /B\.txt/ });
    // The list's scrolling area, where a press on empty space starts the box.
    const shell = screen.getByRole("table", { name: "文件列表" }).parentElement!;
    shell.getBoundingClientRect = () => rect(0, 0, 500, 500);
    Array.from(shell.querySelectorAll<HTMLElement>("[data-file-item]")).forEach((row, index) => {
      row.getBoundingClientRect = () => rect(0, 40 + index * 50, 500, 48);
    });

    fireEvent.pointerDown(shell, { button: 0, clientX: 480, clientY: 25 });
    fireEvent.pointerMove(window, { clientX: 10, clientY: 150 });
    expect(await screen.findByText("已选择 2 个项目")).toBeTruthy();
    fireEvent.pointerUp(window, { clientX: 10, clientY: 150 });
  });

  it("previews an image file in the details pane", async () => {
    serve({
      spaces: [ownSpace],
      entries: [entry("family-photo.jpg", { id: "file:photo", spaceId: ownSpace.id, sizeBytes: 2048 })],
    });
    const user = userEvent.setup();
    renderFiles();
    const row = await screen.findByRole("row", { name: /family-photo\.jpg/ });
    const thumbnail = row.querySelector("img");
    expect(thumbnail?.getAttribute("src")).toBe("/api/v1/files/file%3Aphoto/content?disposition=inline");
    await user.click(row);

    const inspector = screen.getByRole("complementary", { name: "详细信息" });
    const preview = within(inspector).getByRole("img", { name: "family-photo.jpg 的预览" });
    expect(preview.getAttribute("src")).toBe("/api/v1/files/file%3Aphoto/content?disposition=inline");
  });

  it("does not report the whole data volume as used when usage is unavailable", async () => {
    serve({ spaces: [ownSpace], volumes: [volume({ capacityBytes: 500_107_862_016 })] });
    renderFiles();
    // The used space, then the total, under their labels.
    const values = () =>
      Array.from(screen.getByText("已用空间").parentElement!.querySelectorAll("strong"), (value) => value.textContent);

    await waitFor(() => expect(values()).toEqual(["—", "466 GiB"]));
    expect(screen.getByRole("progressbar", { name: "数据卷使用情况暂不可用" })).toBeTruthy();
  });
});

describe("ADR 0008 viewing in the file manager", () => {
  it("shows a viewed private space read-only and can end the viewing", async () => {
    const ended = vi.fn();
    serve({
      spaces: [
        space("space:alice", {
          kind: "private",
          name: "alice",
          ownerUserId: "user:alice",
          viewing: { grantId: "viewing:1", expiresAt: "2026-10-08T10:00:00Z" },
        }),
      ],
      entries: [
        entry("photo.jpg", {
          id: "file:1",
          spaceId: "space:alice",
          sizeBytes: 10,
          modifiedAt: "2026-10-07T10:00:00Z",
        }),
      ],
    });
    server.use(
      http.delete("/api/v1/viewing/{grantId}", ({ params, response }) => {
        ended(params.grantId);
        return response(204).empty();
      }),
    );
    const user = userEvent.setup();
    renderFiles();
    expect(await screen.findByText("photo.jpg")).toBeTruthy();
    expect(screen.getByRole("status").textContent).toContain("只读查看他人个人空间");
    expect(screen.getByRole("button", { name: "新建" }).hasAttribute("disabled")).toBe(true);
    expect(screen.getByRole("button", { name: "删除" }).hasAttribute("disabled")).toBe(true);
    await user.click(screen.getByRole("button", { name: "结束查看" }));
    await waitFor(() => expect(ended).toHaveBeenCalledWith("viewing:1"));
  });
});
