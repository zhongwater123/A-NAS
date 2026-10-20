// The VNAS window as Hermes's browser for the WebOS page it lives in (Hermes's
// browser control, ADR 0024 decision 8): Hermes's own browser tools read the page
// as a list of controls with refs and act on them by ref, here, in the member's
// own browser and session. The VNAS window itself is never part of the page it
// reads or acts on, and the page is never navigated away. A control that deletes,
// replaces or resets something, and anything in 设置's 存储, 用户与权限 and 终端,
// waits for the member to allow it (user decision, 2026-10-20).
import { computeAccessibleName, getRole } from "dom-accessibility-api";

import "./webosControl.css";

/** What the member is asked to allow. */
export interface Danger {
  /** The action, in the member's words: 点击“删除”. */
  what: string;
  /** Where on the desktop: the window, and the settings section. */
  where: string;
  /** Why the member is asked. */
  why: string;
}

export interface ControllerOptions {
  /** Elements that are never part of the page: the VNAS window. */
  exclude(element: Element): boolean;
  /** Asks the member to allow a step; false when they do not, or do not answer. */
  confirm(danger: Danger): Promise<boolean>;
  root?: () => Element;
}

/** A command the page cannot carry out; its message goes back to the model. */
export class ControlError extends Error {}

const INTERACTIVE = new Set([
  "button", "link", "textbox", "searchbox", "combobox", "checkbox", "radio", "switch", "tab", "menuitem",
  "menuitemcheckbox", "menuitemradio", "option", "slider", "spinbutton", "treeitem",
]);
// What counts as a control inside an element that is focusable only to pass focus on.
const CONTROLS = "button, a[href], input, select, textarea, [role=button], [role=link], [role=menuitem], [role=tab], [role=option], [role=checkbox], [role=switch], [contenteditable=true], [contenteditable='']";
const DANGER_WORDS = /删除|移除|清空|彻底|永久|替换|覆盖|格式化|抹掉|擦除|重置|停用|禁用|卸载|移出|退出登录|注销|还原/;
const DANGER_SECTIONS = ["存储", "用户与权限", "终端"];
/** Hermes truncates snapshots over 15000 characters; this stays under. */
const SNAPSHOT_LIMIT = 14_000;
/** How long an allowed step covers the further steps in the same window. */
const GRANT_MS = 30_000;

export class WebosController {
  private refs = new Map<string, Element>();
  private last?: Element;
  private grant?: { window: Element | null; until: number };

  constructor(private options: ControllerOptions) {}

  /** Carries out one of Hermes's browser commands; the result goes back as the tool's result. */
  async run(action: string, args: Record<string, unknown>): Promise<unknown> {
    switch (action) {
      case "browser_snapshot":
        return { snapshot: this.snapshot(args.full === true) };
      case "browser_click":
        return this.click(String(args.ref ?? ""));
      case "browser_type":
        return this.type(String(args.ref ?? ""), String(args.text ?? ""));
      case "browser_press":
        return this.press(String(args.key ?? ""));
      case "browser_scroll":
        return this.scroll(args.direction === "up" ? "up" : "down");
      default:
        throw new ControlError(`VNAS 的窗口不支持 ${action}`);
    }
  }

  /** The page as Hermes's snapshot: one line per window, heading and control; controls carry [ref=eN]. */
  snapshot(full = false): string {
    this.refs.clear();
    const lines: string[] = [];
    const root = this.options.root?.() ?? document.body;
    this.walk(root, 0, full, lines);
    let text = lines.join("\n");
    if (text.length > SNAPSHOT_LIMIT) text = `${text.slice(0, SNAPSHOT_LIMIT)}\n…（页面内容太多，后面的省略了；滚动或关掉不需要的窗口后再查看）`;
    return text || "（桌面上没有可操作的内容）";
  }

  private walk(node: Element, depth: number, full: boolean, lines: string[]) {
    for (const element of Array.from(node.children)) {
      if (this.options.exclude(element) || !visible(element)) continue;
      const indent = "  ".repeat(depth);
      if (element.classList.contains("app-window")) {
        lines.push(`${indent}- window "${clip(element.getAttribute("aria-label") ?? "", 60)}"`);
        this.walk(element, depth + 1, full, lines);
        continue;
      }
      const role = roleOf(element);
      const focusable = element.getAttribute("tabindex") !== null && (element as HTMLElement).tabIndex >= 0 && !element.querySelector(CONTROLS);
      if (INTERACTIVE.has(role) || isField(element) || isEditable(element) || focusable) {
        const ref = `e${this.refs.size + 1}`;
        this.refs.set(ref, element);
        lines.push(`${indent}- ${role || "control"} "${clip(nameOf(element), 80)}"${states(element)} [ref=${ref}]`);
        // A control's content is its name.
        continue;
      }
      if (role === "heading") {
        lines.push(`${indent}- heading "${clip(textOf(element), 80)}"`);
        continue;
      }
      if (full && hasOwnText(element)) {
        lines.push(`${indent}- text "${clip(textOf(element), 160)}"`);
        continue;
      }
      this.walk(element, depth, full, lines);
    }
  }

  private lookup(ref: string): Element {
    const element = this.refs.get(ref.replace(/^@/, ""));
    if (!element || !element.isConnected || !visible(element)) {
      throw new ControlError(`${ref} 已经不在页面上了，请先用 browser_snapshot 重新查看`);
    }
    return element;
  }

  private async allowed(element: Element, verb: string): Promise<void> {
    const danger = this.danger(element, verb);
    if (!danger) return;
    const window = element.closest(".app-window");
    if (this.grant && this.grant.window === window && Date.now() < this.grant.until) return;
    if (!(await this.options.confirm(danger))) {
      throw new ControlError(`成员没有允许这一步（${danger.what}），没有执行`);
    }
    this.grant = { window, until: Date.now() + GRANT_MS };
  }

  /** Whether acting on the element needs the member: what deletes, replaces or resets, and the risky parts of 设置. */
  danger(element: Element, verb: string): Danger | undefined {
    const name = nameOf(element);
    const where = windowTitle(element);
    const settings = element.closest(".app-window-settings");
    if (settings && element.closest(".set-main")) {
      const section = settings.querySelector('.set-nav [aria-current="page"]')?.textContent?.trim() ?? "";
      const risky = DANGER_SECTIONS.find((label) => section.includes(label));
      if (risky) return { what: `${verb}“${clip(name, 40)}”`, where: `设置 · ${risky}`, why: `“${risky}”里的操作都要你先允许` };
    }
    if (element.closest(".xterm")) return { what: `${verb}到终端`, where, why: "终端里的输入会直接运行" };
    if (verb === "点击" && (DANGER_WORDS.test(name) || /\bdanger\b/.test(String((element as HTMLElement).className ?? "")))) {
      return { what: `点击“${clip(name, 40)}”`, where, why: "这一步可能删除、替换或改动内容" };
    }
    return undefined;
  }

  private async click(ref: string) {
    const element = this.lookup(ref);
    await this.allowed(element, "点击");
    this.last = element;
    (element as HTMLElement).scrollIntoView?.({ block: "nearest", inline: "nearest" });
    flash(element);
    const rect = element.getBoundingClientRect();
    const at = { bubbles: true, cancelable: true, composed: true, clientX: rect.left + rect.width / 2, clientY: rect.top + rect.height / 2, button: 0 };
    const pointer = (type: string) => typeof PointerEvent === "function"
      ? new PointerEvent(type, { ...at, pointerType: "mouse", isPrimary: true })
      : new MouseEvent(type, at);
    element.dispatchEvent(pointer("pointerdown"));
    element.dispatchEvent(new MouseEvent("mousedown", at));
    element.dispatchEvent(pointer("pointerup"));
    element.dispatchEvent(new MouseEvent("mouseup", at));
    if (element instanceof HTMLElement) {
      element.focus({ preventScroll: true });
      element.click();
    } else {
      element.dispatchEvent(new MouseEvent("click", at));
    }
    return { clicked: `@${ref.replace(/^@/, "")}`, element: describe(element) };
  }

  private async type(ref: string, text: string) {
    const element = this.lookup(ref);
    await this.allowed(element, "输入");
    this.last = element;
    flash(element);
    if (element instanceof HTMLInputElement || element instanceof HTMLTextAreaElement) {
      element.focus({ preventScroll: true });
      // React tracks the value it set; the prototype's setter makes the change its own.
      const setter = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(element), "value")?.set;
      setter?.call(element, text);
      element.dispatchEvent(new Event("input", { bubbles: true }));
      element.dispatchEvent(new Event("change", { bubbles: true }));
    } else if (element instanceof HTMLElement && element.isContentEditable) {
      element.focus({ preventScroll: true });
      element.textContent = text;
      element.dispatchEvent(new InputEvent("input", { bubbles: true, data: text, inputType: "insertText" }));
    } else {
      throw new ControlError(`${describe(element)} 不能输入文字`);
    }
    return { typed: `@${ref.replace(/^@/, "")}`, element: describe(element) };
  }

  private async press(key: string) {
    if (!key) throw new ControlError("没有说要按哪个键");
    // The member's own typing in VNAS is never the target.
    const active = document.activeElement;
    const target = active && active !== document.body && !this.options.exclude(active) ? active : this.last ?? document.body;
    if (target !== document.body) await this.allowed(target, `按 ${key}`);
    const init = { key, bubbles: true, cancelable: true, composed: true };
    const accepted = target.dispatchEvent(new KeyboardEvent("keydown", init));
    if (accepted && key === "Enter" && (target instanceof HTMLInputElement) && target.form) target.form.requestSubmit();
    target.dispatchEvent(new KeyboardEvent("keyup", init));
    return { pressed: key, on: target === document.body ? "页面" : describe(target) };
  }

  private scroll(direction: "up" | "down") {
    const container = scrollable(this.last) ?? topWindowScroller(this.options) ?? document.scrollingElement;
    if (!container) throw new ControlError("页面上没有能滚动的地方");
    const before = container.scrollTop;
    container.scrollBy({ top: (direction === "down" ? 1 : -1) * Math.max(120, container.clientHeight * 0.8) });
    return { scrolled: direction, moved: container.scrollTop !== before || undefined };
  }
}

function visible(element: Element): boolean {
  if ((element as HTMLElement).hidden || element.getAttribute("aria-hidden") === "true") return false;
  const check = (element as Element & { checkVisibility?: (options?: object) => boolean }).checkVisibility;
  if (check) return check.call(element, { checkOpacity: true, checkVisibilityCSS: true });
  const style = getComputedStyle(element);
  return style.display !== "none" && style.visibility !== "hidden";
}

/** The element's ARIA role; fields without one (a password, a file picker) say what they are. */
function roleOf(element: Element): string {
  const role = getRole(element) ?? "";
  if (role || !(element instanceof HTMLInputElement)) return role;
  return element.type === "file" ? "button" : "textbox";
}

function isField(element: Element): boolean {
  return (element instanceof HTMLInputElement && element.type !== "hidden") || element instanceof HTMLSelectElement || element instanceof HTMLTextAreaElement;
}

function isEditable(element: Element): boolean {
  return element instanceof HTMLElement && element.isContentEditable && !element.parentElement?.isContentEditable;
}

function hasOwnText(element: Element): boolean {
  return Array.from(element.childNodes).some((node) => node.nodeType === Node.TEXT_NODE && (node.textContent ?? "").trim() !== "");
}

function textOf(element: Element): string {
  return (element.textContent ?? "").replace(/\s+/g, " ").trim();
}

function nameOf(element: Element): string {
  const name = computeAccessibleName(element).replace(/\s+/g, " ").trim();
  return name || element.getAttribute("title") || element.getAttribute("placeholder") || textOf(element);
}

function states(element: Element): string {
  const parts: string[] = [];
  if (element instanceof HTMLInputElement && element.type === "password") parts.push("value=<hidden>");
  else if ((element instanceof HTMLInputElement && !["checkbox", "radio", "button", "submit"].includes(element.type)) || element instanceof HTMLTextAreaElement) {
    if (element.value) parts.push(`value="${clip(element.value, 60)}"`);
  }
  if (element instanceof HTMLSelectElement && element.selectedOptions[0]) parts.push(`value="${clip(element.selectedOptions[0].text, 40)}"`);
  const checked = element.getAttribute("aria-checked") ?? (element instanceof HTMLInputElement && ["checkbox", "radio"].includes(element.type) ? String(element.checked) : null);
  if (checked === "true") parts.push("checked");
  if ((element as HTMLButtonElement).disabled || element.getAttribute("aria-disabled") === "true") parts.push("disabled");
  if (element.getAttribute("aria-expanded") === "true") parts.push("expanded");
  if (element.getAttribute("aria-selected") === "true" || element.getAttribute("aria-pressed") === "true") parts.push("selected");
  if (element.getAttribute("aria-current") && element.getAttribute("aria-current") !== "false") parts.push("current");
  return parts.length ? ` [${parts.join(", ")}]` : "";
}

function windowTitle(element: Element): string {
  return element.closest(".app-window")?.getAttribute("aria-label") ?? "桌面";
}

function describe(element: Element): string {
  return `${roleOf(element) || "control"}“${clip(nameOf(element), 40)}”（${windowTitle(element)}）`;
}

function clip(text: string, limit: number): string {
  const flat = text.replace(/\s+/g, " ").trim().replace(/"/g, "'");
  return flat.length > limit ? `${flat.slice(0, limit - 1)}…` : flat;
}

function scrollable(from?: Element): Element | undefined {
  for (let element = from?.parentElement; element; element = element.parentElement) {
    const overflow = getComputedStyle(element).overflowY;
    if ((overflow === "auto" || overflow === "scroll") && element.scrollHeight > element.clientHeight) return element;
  }
  return undefined;
}

/** The scrolling area of the topmost window that is not VNAS. */
function topWindowScroller(options: ControllerOptions): Element | undefined {
  const windows = Array.from(document.querySelectorAll(".app-window")).filter((window) => !options.exclude(window) && visible(window));
  const top = windows.sort((a, b) => Number(getComputedStyle(b).zIndex || 0) - Number(getComputedStyle(a).zIndex || 0))[0];
  if (!top) return undefined;
  return Array.from(top.querySelectorAll("*")).find((element) => {
    const overflow = getComputedStyle(element).overflowY;
    return (overflow === "auto" || overflow === "scroll") && element.scrollHeight > element.clientHeight;
  });
}

/** A brief ring around the control VNAS acts on, so the member sees where. */
function flash(element: Element) {
  const rect = element.getBoundingClientRect();
  if (!rect.width && !rect.height) return;
  const ring = document.createElement("div");
  ring.className = "vnas-control-ring";
  Object.assign(ring.style, { left: `${rect.left - 4}px`, top: `${rect.top - 4}px`, width: `${rect.width + 8}px`, height: `${rect.height + 8}px` });
  document.body.appendChild(ring);
  window.setTimeout(() => ring.remove(), 900);
}
