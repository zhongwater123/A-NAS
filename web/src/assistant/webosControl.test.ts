import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { domToCanvas } from "modern-screenshot";

import { request } from "../api";
import { ControlError, type Danger, type Screenshot, WebosController } from "./webosControl";

vi.mock("modern-screenshot", () => ({ domToCanvas: vi.fn() }));

// A desktop as App.tsx renders one: windows are .app-window sections named by aria-label.
function desktop() {
  document.body.innerHTML = `
    <nav aria-label="程序坞"><button aria-label="打开相册">相册</button></nav>
    <section class="app-window app-window-files" role="dialog" aria-label="文件管理">
      <h2>个人空间</h2>
      <button id="open">打开文件夹</button>
      <button id="new-folder">新建文件夹</button>
      <button id="cut">剪切</button>
      <button id="paste">粘贴</button>
      <button id="share">分享</button>
      <div role="menuitem" tabindex="-1" id="delete">删除</div>
      <input id="search" aria-label="搜索文件" value="旧的">
      <input id="pw" type="password" aria-label="密码" value="hunter2">
      <div tabindex="0" id="slot"><button id="inner">计划.md</button></div>
      <form id="rename" aria-label="重命名"><input id="name" aria-label="新名称" value="计划.md"></form>
      <p>3 个项目</p>
      <section id="viewing" data-vnas-private="他人的个人空间（只读查看）"><button id="theirs">日记.md</button><p>别人的文字</p></section>
    </section>
    <section class="app-window app-window-photos" role="dialog" aria-label="相册">
      <div id="viewer" tabindex="0" aria-label="照片查看器"><img alt="海边"></div><button id="trash">移到回收站</button>
    </section>
    <section class="app-window app-window-settings" role="dialog" aria-label="设置">
      <nav class="set-nav" aria-label="设置分区"><button aria-current="page">终端</button><button>日志</button></nav>
      <div class="set-main"><button id="root">以 root 打开</button></div>
    </section>
    <section class="app-window app-window-docker" role="dialog" aria-label="Docker">
      <button id="stop">停止</button><button id="logs">查看日志</button>
    </section>
    <section class="app-window app-window-music" role="dialog" aria-label="音乐" hidden><button>隐藏的按钮</button></section>
    <section class="app-window app-window-assistant" role="dialog" aria-label="VNAS"><button id="send">发送</button><textarea id="composer"></textarea></section>`;
}

function controller(answer: boolean | ((danger: Danger) => boolean) = true) {
  const asked: Danger[] = [];
  const control = new WebosController({
    exclude: (element) => Boolean(element.closest(".app-window-assistant")),
    confirm: async (danger) => {
      asked.push(danger);
      return typeof answer === "function" ? answer(danger) : answer;
    },
  });
  uninstall = control.install();
  return { control, asked };
}

function refOf(snapshot: string, name: string): string {
  const line = snapshot.split("\n").find((entry) => entry.includes(`"${name}"`));
  const ref = line?.match(/\[ref=(e\d+)\]/)?.[1];
  if (!ref) throw new Error(`no ref for ${name} in\n${snapshot}`);
  return `@${ref}`;
}

async function look(control: WebosController, full = false) {
  return (await control.run("browser_snapshot", { full }) as { snapshot: string }).snapshot;
}

// The writes the page sent, as the product API saw them.
let sent: string[] = [];
let uninstall: () => void = () => undefined;
// A button whose click sends one product API write, as the apps do.
function writes(id: string, method: string, path: string) {
  document.getElementById(id)!.addEventListener("click", () => {
    void request(path, { method }, true).catch(() => undefined);
  });
}

beforeEach(() => {
  desktop();
  sent = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    sent.push(`${init?.method ?? "GET"} ${String(input)}`);
    return new Response(null, { status: 204 });
  }));
});
afterEach(() => {
  uninstall();
  document.body.innerHTML = "";
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("WebosController", () => {
  it("reads the page as windows and controls with refs, without VNAS itself", async () => {
    const { control } = controller();
    const snapshot = await look(control);
    expect(snapshot).toContain('- window "文件管理"');
    expect(snapshot).toMatch(/ {2}- button "打开文件夹" \[ref=e\d+\]/);
    expect(snapshot).toContain('- heading "个人空间"');
    expect(snapshot).toContain('textbox "搜索文件" [value="旧的"]');
    expect(snapshot).toContain("value=<hidden>");
    expect(snapshot).not.toContain("hunter2");
    // A focusable wrapper does not hide the control it holds.
    expect(snapshot).toContain('button "计划.md"');
    expect(snapshot).not.toContain("发送");
    expect(snapshot).not.toContain("隐藏的按钮");
    expect(snapshot).not.toContain("3 个项目");
    const full = await look(control, true);
    expect(full).toContain('- text "3 个项目"');
  });

  it("never reads what is beyond the assistant's range, nor acts on it", async () => {
    const { control } = controller();
    const full = await look(control, true);
    expect(full).toContain('- region "他人的个人空间（只读查看）（VNAS 不能查看）"');
    expect(full).not.toContain("日记.md");
    expect(full).not.toContain("别人的文字");
    // A control that became private after the snapshot is refused too.
    const ref = refOf(full, "打开文件夹");
    document.getElementById("viewing")!.appendChild(document.getElementById("open")!);
    await expect(control.run("browser_click", { ref })).rejects.toThrow("VNAS 不能查看或操作");
  });

  it("clicks a control by its ref; a click that only navigates is not asked about", async () => {
    const { control, asked } = controller();
    const clicked = vi.fn();
    document.getElementById("open")!.addEventListener("click", clicked);
    const result = await control.run("browser_click", { ref: refOf(await look(control), "打开文件夹") });
    expect(clicked).toHaveBeenCalledTimes(1);
    expect(result).toMatchObject({ element: "button“打开文件夹”（文件管理）" });
    expect(asked).toEqual([]);
  });

  it("lets a harmless write go without asking", async () => {
    const { control, asked } = controller(false);
    writes("new-folder", "POST", "/api/v1/spaces/s1/directories");
    await control.run("browser_click", { ref: refOf(await look(control), "新建文件夹") });
    expect(asked).toEqual([]);
    expect(sent).toEqual(["POST /api/v1/spaces/s1/directories"]);
  });

  it("holds a cut and paste into the shared space until the member allows the move, whatever the button is called", async () => {
    const { control, asked } = controller(false);
    const clipboard: string[] = [];
    document.getElementById("cut")!.addEventListener("click", () => clipboard.push("f1"));
    writes("paste", "PATCH", "/api/v1/files/f1");
    const snapshot = await look(control);
    await control.run("browser_click", { ref: refOf(snapshot, "剪切") });
    expect(asked).toEqual([]);
    await expect(control.run("browser_click", { ref: refOf(snapshot, "粘贴") })).rejects.toThrow("NAS 上什么都没有改");
    expect(asked).toEqual([{ what: "点击“粘贴”", where: "文件管理", why: "这一步会移动或改名" }]);
    expect(sent).toEqual([]);
  });

  it("sends the step's writes once the member allows it, asking once for all of them", async () => {
    const { control, asked } = controller(true);
    document.getElementById("share")!.addEventListener("click", () => {
      void request("/api/v1/shares", { method: "POST" }, true);
      void request("/api/v1/shares/x/messages", { method: "POST" }, true);
    });
    await control.run("browser_click", { ref: refOf(await look(control), "分享") });
    expect(asked).toEqual([{ what: "点击“分享”", where: "文件管理", why: "这一步会把内容分享给其他成员" }]);
    expect(sent).toEqual(["POST /api/v1/shares", "POST /api/v1/shares/x/messages"]);
  });

  it("asks about a write of a route it does not know", async () => {
    const { control, asked } = controller(false);
    writes("open", "POST", "/api/v1/something-new");
    await expect(control.run("browser_click", { ref: refOf(await look(control), "打开文件夹") })).rejects.toThrow(ControlError);
    expect(asked[0].why).toBe("这一步会改动 NAS 上的内容");
    expect(sent).toEqual([]);
  });

  it("leaves the page's own writes alone once VNAS's step is long past", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    const { control, asked } = controller(false);
    await control.run("browser_click", { ref: refOf(await look(control), "打开文件夹") });
    vi.setSystemTime(Date.now() + 16_000);
    await request("/api/v1/files/f1", { method: "DELETE" }, true);
    expect(asked).toEqual([]);
    expect(sent).toEqual(["DELETE /api/v1/files/f1"]);
  });

  it("holds Enter in a form until the member allows what the form sends", async () => {
    const { control, asked } = controller(false);
    const form = document.getElementById("rename") as HTMLFormElement;
    form.addEventListener("submit", (event) => {
      event.preventDefault();
      void request("/api/v1/files/f1", { method: "PATCH" }, true).catch(() => undefined);
    });
    await control.run("browser_type", { ref: refOf(await look(control), "新名称"), text: "公开.md" });
    await expect(control.run("browser_press", { key: "Enter" })).rejects.toThrow("成员没有允许");
    expect(asked[0]).toMatchObject({ what: "在“新名称”上按 Enter", why: "这一步会移动或改名" });
    expect(sent).toEqual([]);
  });

  it("presses Delete and Backspace only while editing text", async () => {
    const { control } = controller();
    const keys: string[] = [];
    document.getElementById("viewer")!.addEventListener("keydown", (event) => keys.push(`viewer:${event.key}`));
    document.getElementById("search")!.addEventListener("keydown", (event) => keys.push(`search:${event.key}`));
    const snapshot = await look(control);
    await control.run("browser_click", { ref: refOf(snapshot, "照片查看器") });
    await expect(control.run("browser_press", { key: "Delete" })).rejects.toThrow("只能在输入框里");
    await expect(control.run("browser_press", { key: "Control+a" })).rejects.toThrow("不能按");
    await control.run("browser_type", { ref: refOf(snapshot, "搜索文件"), text: "旅行" });
    await control.run("browser_press", { key: "Backspace" });
    expect(keys).toEqual(["search:Backspace"]);
  });

  it("presses keys on the control it last used, never on VNAS's own composer", async () => {
    const { control } = controller();
    const keys: string[] = [];
    document.getElementById("search")!.addEventListener("keydown", (event) => keys.push(`search:${event.key}`));
    document.getElementById("composer")!.addEventListener("keydown", (event) => keys.push(`composer:${event.key}`));
    await control.run("browser_type", { ref: refOf(await look(control), "搜索文件"), text: "旅行" });
    (document.getElementById("composer") as HTMLTextAreaElement).focus();
    await control.run("browser_press", { key: "Enter" });
    expect(keys).toEqual(["search:Enter"]);
  });

  it("asks before any step in Docker, and does nothing when the member does not allow it", async () => {
    const { control, asked } = controller(false);
    const stopped = vi.fn();
    document.getElementById("stop")!.addEventListener("click", stopped);
    await expect(control.run("browser_click", { ref: refOf(await look(control), "停止") })).rejects.toThrow("成员没有允许");
    expect(asked).toEqual([{ what: "点击“停止”", where: "Docker", why: "“Docker”里的操作都要你先允许" }]);
    expect(stopped).not.toHaveBeenCalled();
  });

  it("asks once for a guarded window, and lets its writes and its own confirmation through for a while", async () => {
    const { control, asked } = controller(true);
    writes("stop", "POST", "/api/v1/containers/c1/actions");
    const snapshot = await look(control);
    await control.run("browser_click", { ref: refOf(snapshot, "停止") });
    await control.run("browser_click", { ref: refOf(snapshot, "查看日志") });
    expect(asked).toHaveLength(1);
    expect(sent).toEqual(["POST /api/v1/containers/c1/actions"]);
  });

  it("asks for anything in 设置's 终端, but not for the settings navigation", async () => {
    const { control, asked } = controller(false);
    const snapshot = await look(control);
    await expect(control.run("browser_click", { ref: refOf(snapshot, "以 root 打开") })).rejects.toThrow("成员没有允许");
    expect(asked[0]).toEqual({ what: "点击“以 root 打开”", where: "设置 · 终端", why: "“终端”里的操作都要你先允许" });
    await control.run("browser_click", { ref: refOf(snapshot, "日志") });
    expect(asked).toHaveLength(1);
  });

  it("types into a field as the member would, with input and change events", async () => {
    const { control } = controller();
    const search = document.getElementById("search") as HTMLInputElement;
    const events: string[] = [];
    search.addEventListener("input", () => events.push(`input:${search.value}`));
    search.addEventListener("change", () => events.push("change"));
    await control.run("browser_type", { ref: refOf(await look(control), "搜索文件"), text: "旅行" });
    expect(search.value).toBe("旅行");
    expect(events).toEqual(["input:旅行", "change"]);
  });

  it("tells the model to look again when a ref is gone", async () => {
    const { control } = controller();
    const ref = refOf(await look(control), "打开文件夹");
    document.getElementById("open")!.remove();
    await expect(control.run("browser_click", { ref })).rejects.toThrow("browser_snapshot");
    await expect(control.run("browser_navigate", { url: "https://example.com" })).rejects.toThrow(ControlError);
  });
});

describe("the picture of the page", () => {
  // The renderer draws the page into a canvas; here a canvas that records what is written on it.
  function canvasOf(width: number, height: number) {
    const written: string[] = [];
    const context = { font: "", textBaseline: "", strokeStyle: "", fillStyle: "", lineWidth: 1, strokeRect: vi.fn(), fillRect: vi.fn(),
      measureText: () => ({ width: 18 }), fillText: (text: string) => written.push(text) };
    return { canvas: { width, height, getContext: () => context, toDataURL: () => "data:image/png;base64,iVBORw0KGgo=" }, written };
  }

  afterEach(() => vi.restoreAllMocks());

  it("draws the page as the member sees it, without VNAS's own window, as a PNG", async () => {
    const { control } = controller();
    const { canvas } = canvasOf(1024, 768);
    vi.mocked(domToCanvas).mockResolvedValue(canvas as unknown as HTMLCanvasElement);
    const shot = await control.run("browser_screenshot", { annotate: false }) as Screenshot;
    expect(shot).toEqual({ mime_type: "image/png", data: "iVBORw0KGgo=", width: 1024, height: 768 });
    const [root, options] = vi.mocked(domToCanvas).mock.calls[0] as unknown as [Element, { width?: number; height?: number; filter?(node: Node): boolean }];
    expect(root).toBe(document.body);
    expect([options?.width, options?.height]).toEqual([window.innerWidth, window.innerHeight]);
    expect(options?.filter?.(document.querySelector(".app-window-assistant")!)).toBe(false);
    expect(options?.filter?.(document.querySelector(".app-window-files")!)).toBe(true);
  });

  it("labels each control with the ref VNAS clicks it by", async () => {
    const { control } = controller();
    const { canvas, written } = canvasOf(1024, 768);
    vi.mocked(domToCanvas).mockResolvedValue(canvas as unknown as HTMLCanvasElement);
    vi.spyOn(Element.prototype, "getBoundingClientRect").mockReturnValue(new DOMRect(10, 40, 80, 24));
    const shot = await control.run("browser_screenshot", { annotate: true }) as Screenshot;
    const folder = shot.annotations?.find((label) => label.name === "新建文件夹");
    expect(folder?.ref).toMatch(/^e\d+$/);
    expect(written).toContain(folder?.ref);
    // Nothing of VNAS's own window, nothing it may not read.
    expect(shot.annotations?.some((label) => ["发送", "日记.md"].includes(label.name))).toBe(false);
    // The refs on the picture are the ones a click uses.
    await control.run("browser_click", { ref: `@${folder?.ref}` });
  });

  it("labels no control another window lies over", async () => {
    const { control } = controller();
    const { canvas } = canvasOf(1024, 768);
    vi.mocked(domToCanvas).mockResolvedValue(canvas as unknown as HTMLCanvasElement);
    vi.spyOn(Element.prototype, "getBoundingClientRect").mockReturnValue(new DOMRect(10, 40, 80, 24));
    // 相册 lies over everything, with VNAS's own window above it.
    const photos = document.querySelector(".app-window-photos")!;
    const vnas = document.querySelector(".app-window-assistant")!;
    document.elementsFromPoint = vi.fn(() => [vnas, photos, document.body]);
    try {
      const shot = await control.run("browser_screenshot", { annotate: true }) as Screenshot;
      expect(shot.annotations?.map((label) => label.name).sort()).toEqual(["照片查看器", "移到回收站"]);
    } finally {
      delete (document as Partial<Document>).elementsFromPoint;
    }
  });
});
