import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ControlError, type Danger, WebosController } from "./webosControl";

// A desktop as App.tsx renders one: windows are .app-window sections named by aria-label.
function desktop() {
  document.body.innerHTML = `
    <nav aria-label="程序坞"><button aria-label="打开相册">相册</button></nav>
    <section class="app-window app-window-files" role="dialog" aria-label="文件管理">
      <h2>个人空间</h2>
      <button id="open">打开文件夹</button>
      <div role="menuitem" tabindex="-1" id="delete">删除</div>
      <input id="search" aria-label="搜索文件" value="旧的">
      <input id="pw" type="password" aria-label="密码" value="hunter2">
      <div tabindex="0" id="slot"><button id="inner">计划.md</button></div>
      <p>3 个项目</p>
    </section>
    <section class="app-window app-window-settings" role="dialog" aria-label="设置">
      <nav class="set-nav" aria-label="设置分区"><button aria-current="page">存储</button><button>日志</button></nav>
      <div class="set-main"><button id="create">创建数据卷</button></div>
    </section>
    <section class="app-window app-window-photos" role="dialog" aria-label="相册" hidden><button>隐藏的按钮</button></section>
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
  return { control, asked };
}

function refOf(snapshot: string, name: string): string {
  const line = snapshot.split("\n").find((entry) => entry.includes(`"${name}"`));
  const ref = line?.match(/\[ref=(e\d+)\]/)?.[1];
  if (!ref) throw new Error(`no ref for ${name} in\n${snapshot}`);
  return `@${ref}`;
}

beforeEach(desktop);
afterEach(() => { document.body.innerHTML = ""; vi.useRealTimers(); });

describe("WebosController", () => {
  it("reads the page as windows and controls with refs, without VNAS itself", async () => {
    const { control } = controller();
    const { snapshot } = await control.run("browser_snapshot", {}) as { snapshot: string };
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
    const full = (await control.run("browser_snapshot", { full: true }) as { snapshot: string }).snapshot;
    expect(full).toContain('- text "3 个项目"');
  });

  it("clicks a control by its ref", async () => {
    const { control, asked } = controller();
    const clicked = vi.fn();
    document.getElementById("open")!.addEventListener("click", clicked);
    const { snapshot } = await control.run("browser_snapshot", {}) as { snapshot: string };
    const result = await control.run("browser_click", { ref: refOf(snapshot, "打开文件夹") });
    expect(clicked).toHaveBeenCalledTimes(1);
    expect(result).toMatchObject({ element: "button“打开文件夹”（文件管理）" });
    expect(asked).toEqual([]);
  });

  it("asks the member before a step that deletes, and does nothing when they do not allow it", async () => {
    const { control, asked } = controller(false);
    const deleted = vi.fn();
    document.getElementById("delete")!.addEventListener("click", deleted);
    const { snapshot } = await control.run("browser_snapshot", {}) as { snapshot: string };
    await expect(control.run("browser_click", { ref: refOf(snapshot, "删除") })).rejects.toThrow(ControlError);
    expect(asked).toEqual([{ what: "点击“删除”", where: "文件管理" }]);
    expect(deleted).not.toHaveBeenCalled();
  });

  it("asks once for a window, then lets the window's own confirmation through for a while", async () => {
    const { control, asked } = controller(true);
    const { snapshot } = await control.run("browser_snapshot", {}) as { snapshot: string };
    await control.run("browser_click", { ref: refOf(snapshot, "删除") });
    await control.run("browser_click", { ref: refOf(snapshot, "删除") });
    expect(asked).toHaveLength(1);
  });

  it("asks for anything in 设置's 存储, and for typing into it", async () => {
    const { control, asked } = controller(false);
    const { snapshot } = await control.run("browser_snapshot", {}) as { snapshot: string };
    await expect(control.run("browser_click", { ref: refOf(snapshot, "创建数据卷") })).rejects.toThrow("成员没有允许");
    expect(asked[0]).toEqual({ what: "点击“创建数据卷”", where: "设置 · 存储" });
    // The settings navigation itself is not risky.
    await control.run("browser_click", { ref: refOf(snapshot, "日志") });
    expect(asked).toHaveLength(1);
  });

  it("types into a field as the member would, with input and change events", async () => {
    const { control } = controller();
    const search = document.getElementById("search") as HTMLInputElement;
    const events: string[] = [];
    search.addEventListener("input", () => events.push(`input:${search.value}`));
    search.addEventListener("change", () => events.push("change"));
    const { snapshot } = await control.run("browser_snapshot", {}) as { snapshot: string };
    await control.run("browser_type", { ref: refOf(snapshot, "搜索文件"), text: "旅行" });
    expect(search.value).toBe("旅行");
    expect(events).toEqual(["input:旅行", "change"]);
  });

  it("presses keys on the control it last used, never on VNAS's own composer", async () => {
    const { control } = controller();
    const keys: string[] = [];
    document.getElementById("search")!.addEventListener("keydown", (event) => keys.push(`search:${event.key}`));
    document.getElementById("composer")!.addEventListener("keydown", (event) => keys.push(`composer:${event.key}`));
    const { snapshot } = await control.run("browser_snapshot", {}) as { snapshot: string };
    await control.run("browser_type", { ref: refOf(snapshot, "搜索文件"), text: "旅行" });
    (document.getElementById("composer") as HTMLTextAreaElement).focus();
    await control.run("browser_press", { key: "Enter" });
    expect(keys).toEqual(["search:Enter"]);
  });

  it("tells the model to look again when a ref is gone", async () => {
    const { control } = controller();
    const { snapshot } = await control.run("browser_snapshot", {}) as { snapshot: string };
    const ref = refOf(snapshot, "打开文件夹");
    document.getElementById("open")!.remove();
    await expect(control.run("browser_click", { ref })).rejects.toThrow("browser_snapshot");
    await expect(control.run("browser_navigate", { url: "https://example.com" })).rejects.toThrow(ControlError);
  });
});
