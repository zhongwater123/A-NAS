// The VNAS window as Hermes's browser for the WebOS page it lives in (Hermes's
// browser control, ADR 0024 decision 8): Hermes's own browser tools read the page
// as a list of controls with refs, or look at a picture of it, and act on them by
// ref, here, in the member's own browser and session. The VNAS window itself is
// never part of the page it reads, sees or acts on, and the page is never
// navigated away.
//
// What a control is called is no boundary (#160): the page's writes are. A step
// VNAS takes holds the writes it causes until the member allows the ones that
// delete, move, share, copy or change settings (webosWrites.ts, user decision
// 2026-10-21). The terminal, Docker and 应用中心 reach the host, so every step
// there waits for the member first. What the member may see but VNAS may not
// (another member's space under a viewing grant, snapshots) is marked
// data-vnas-private and never read as text or acted on; the picture shows the
// page as it is, while vision is a feasibility trial (user decision 2026-10-21).
import { computeAccessibleName, getRole } from "dom-accessibility-api";

import { setWriteGate } from "../api";
import { drawDesktop, onScreen } from "../desktopPicture";
import "./webosControl.css";
import { writeRisk } from "./webosWrites";

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
/** Windows where every step waits for the member: what runs there reaches the host. */
const GUARDED_WINDOWS: Array<[string, string]> = [[".app-window-docker", "Docker"], [".app-window-store", "应用中心"]];
const GUARDED_SECTIONS = ["终端"];
/** What VNAS never reads or acts on; the value says what it is. */
const PRIVATE = "[data-vnas-private]";
/** Keys VNAS may press: moving around, confirming and closing; Backspace and Delete only while editing text. */
const KEYS = new Set(["Enter", "Escape", "Tab", " ", "ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight", "PageUp", "PageDown", "Home", "End"]);
const EDITING_KEYS = new Set(["Backspace", "Delete"]);
/** Hermes truncates snapshots over 15000 characters; this stays under. */
const SNAPSHOT_LIMIT = 14_000;
/** How long an allowed step in a guarded window covers the further steps in it. */
const GRANT_MS = 30_000;
/** How long after a step the page's writes count as the step's, unless the member acted since. */
const STEP_WRITES_MS = 15_000;
/** How long a step waits for the writes it causes before it answers. */
const SETTLE_MS = 400;
/** The member's own input; VNAS's events are never trusted. */
const MEMBER_INPUT = ["pointerdown", "keydown", "drop"];
/** The longest side of a screenshot: the desktop's text stays readable. */
const SHOT_EDGE = 1600;
/** A screenshot's base64 stays under this; the product service takes up to 5 MiB. */
const SHOT_LIMIT = 4_000_000;

/** The page as a PNG, for Hermes's browser_vision. */
export interface Screenshot {
  mime_type: "image/png";
  /** The PNG in base64. */
  data: string;
  width: number;
  height: number;
  /** With annotate: the controls labelled on the picture by ref. */
  annotations?: Array<{ ref: string; name: string }>;
}

interface Label {
  ref: string;
  name: string;
  box: DOMRect;
}

/** One click, input or key press of VNAS, and what the member said about its writes. */
interface Step {
  seq: number;
  at: number;
  danger: Omit<Danger, "why">;
  /** The member's answer about the step's writes, asked once for all of them. */
  answer?: Promise<boolean>;
  why?: string;
  allowed?: boolean;
}

export class WebosController {
  private refs = new Map<string, Element>();
  private last?: Element;
  private grant?: { window: Element | null; until: number };
  // The member's own input and VNAS's steps, in one order: a write belongs to
  // VNAS's last step unless the member acted after it.
  private seq = 0;
  private memberSeq = 0;
  private step?: Step;

  constructor(private options: ControllerOptions) {}

  /**
   * Holds the page's writes for the member while VNAS operates the desktop;
   * the returned function stops. The member's own presses, keys and drops
   * outside the VNAS window end VNAS's claim on the writes that follow.
   */
  install(): () => void {
    const member = (event: Event) => {
      if (!event.isTrusted || (event.target instanceof Element && this.options.exclude(event.target))) return;
      this.memberSeq = ++this.seq;
    };
    for (const type of MEMBER_INPUT) window.addEventListener(type, member, true);
    const release = setWriteGate((method, path) => this.write(method, path));
    return () => {
      for (const type of MEMBER_INPUT) window.removeEventListener(type, member, true);
      release();
    };
  }

  /** Whether a write the page is about to send may go. */
  write(method: string, path: string): Promise<boolean> {
    const why = writeRisk(method, path);
    const step = this.step;
    if (!why || !step || this.memberSeq > step.seq || Date.now() - step.at > STEP_WRITES_MS) return Promise.resolve(true);
    if (step.allowed !== undefined) return Promise.resolve(step.allowed);
    step.why ??= why;
    step.answer ??= this.options.confirm({ ...step.danger, why: step.why }).then((allowed) => {
      step.allowed = allowed;
      return allowed;
    });
    return step.answer;
  }

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
      case "browser_screenshot":
        return this.screenshot(args.annotate === true);
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

  /**
   * The page as the member sees it, for Hermes's browser_vision
   * (hermes-patches/0003): the same picture 问题反馈 attaches
   * (desktopPicture.ts), as a PNG, without VNAS's own window, toasts and
   * marks, as the snapshot is. Annotated, every control of a fresh snapshot
   * carries its ref on the picture, so what VNAS sees it can click.
   */
  async screenshot(annotate = false): Promise<Screenshot> {
    const view = { width: window.innerWidth, height: window.innerHeight };
    const labels = annotate ? this.labels(view) : [];
    let canvas = await drawDesktop({ leaveOut: this.options.exclude, maxEdge: SHOT_EDGE });
    mark(canvas, labels, canvas.width / view.width);
    let data = pngOf(canvas);
    while (data.length > SHOT_LIMIT && canvas.width > 480) {
      canvas = shrink(canvas, 0.75);
      data = pngOf(canvas);
    }
    if (data.length > SHOT_LIMIT) throw new ControlError("桌面的画面太大，没能截下来");
    return {
      mime_type: "image/png", data, width: canvas.width, height: canvas.height,
      ...(annotate ? { annotations: labels.map(({ ref, name }) => ({ ref, name })) } : {}),
    };
  }

  /** The controls of a fresh snapshot that show on the picture, with their refs: not off screen, not under another window. */
  private labels(view: { width: number; height: number }): Label[] {
    this.snapshot();
    const labels: Label[] = [];
    for (const [ref, element] of this.refs) {
      const box = element.getBoundingClientRect();
      if (!box.width || !box.height || !onScreen(element, view) || this.covered(element, box)) continue;
      labels.push({ ref, name: clip(nameOf(element), 40), box });
    }
    return labels;
  }

  /** Whether something else of the page, not VNAS's own window, lies over the middle of the control. */
  private covered(element: Element, box: DOMRect): boolean {
    if (typeof document.elementsFromPoint !== "function") return false;
    const top = document.elementsFromPoint(box.left + box.width / 2, box.top + box.height / 2).find((hit) => !this.options.exclude(hit));
    return Boolean(top) && !(top === element || element.contains(top!) || top!.contains(element));
  }

  private walk(node: Element, depth: number, full: boolean, lines: string[]) {
    for (const element of Array.from(node.children)) {
      if (this.options.exclude(element) || !visible(element)) continue;
      const indent = "  ".repeat(depth);
      const kept = element.getAttribute("data-vnas-private");
      if (kept !== null) {
        lines.push(`${indent}- region "${clip(kept, 60)}（VNAS 不能查看）"`);
        continue;
      }
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
    hidden(element);
    return element;
  }

  /**
   * Begins a step on the element: a step in a guarded window waits for the
   * member before anything happens, and the writes it causes wait for them
   * (unless they already allowed the step).
   */
  private async begin(element: Element, what: string): Promise<Step> {
    const step: Step = { seq: ++this.seq, at: Date.now(), danger: { what: `${what}${inDialog(element)}`, where: windowTitle(element) } };
    const guarded = this.guarded(element, what);
    if (guarded) {
      const window = element.closest(".app-window");
      if (!this.grant || this.grant.window !== window || Date.now() >= this.grant.until) {
        if (!(await this.options.confirm(guarded))) throw new ControlError(`成员没有允许这一步（${guarded.what}），没有执行`);
        this.grant = { window, until: Date.now() + GRANT_MS };
      }
      step.allowed = true;
      step.at = Date.now();
    }
    this.step = step;
    return step;
  }

  /** After a step's events: the writes it caused have gone, or the member did not allow them. */
  private async settle(step: Step): Promise<void> {
    await new Promise((resolve) => window.setTimeout(resolve, SETTLE_MS));
    if (step.answer && !(await step.answer)) {
      throw new ControlError(`成员没有允许这一步（${step.danger.what}：${step.why}），NAS 上什么都没有改`);
    }
  }

  /** Whether every step on the element waits for the member: the terminal, Docker and 应用中心. */
  guarded(element: Element, what: string): Danger | undefined {
    const settings = element.closest(".app-window-settings");
    if (settings && element.closest(".set-main")) {
      const section = settings.querySelector('.set-nav [aria-current="page"]')?.textContent?.trim() ?? "";
      const risky = GUARDED_SECTIONS.find((label) => section.includes(label));
      if (risky) return { what, where: `设置 · ${risky}`, why: `“${risky}”里的操作都要你先允许` };
    }
    if (element.closest(".xterm")) return { what, where: windowTitle(element), why: "终端里的输入会直接运行" };
    const window = GUARDED_WINDOWS.find(([selector]) => element.closest(selector));
    if (window) return { what, where: window[1], why: `“${window[1]}”里的操作都要你先允许` };
    return undefined;
  }

  private async click(ref: string) {
    const element = this.lookup(ref);
    const step = await this.begin(element, `点击“${labelOf(element)}”`);
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
    await this.settle(step);
    return { clicked: `@${ref.replace(/^@/, "")}`, element: describe(element) };
  }

  private async type(ref: string, text: string) {
    const element = this.lookup(ref);
    const step = await this.begin(element, `在“${labelOf(element)}”里输入“${clip(text, 40)}”`);
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
    await this.settle(step);
    return { typed: `@${ref.replace(/^@/, "")}`, element: describe(element) };
  }

  private async press(given: string) {
    if (!given) throw new ControlError("没有说要按哪个键");
    const key = given === "Space" ? " " : given;
    // The member's own typing in VNAS is never the target.
    const active = document.activeElement;
    const target = active && active !== document.body && !this.options.exclude(active) ? active : this.last ?? document.body;
    if (!KEYS.has(key) && !(EDITING_KEYS.has(key) && editsText(target))) {
      throw new ControlError(EDITING_KEYS.has(key)
        ? `${key} 只能在输入框里用来删字；要删除或移走内容，请点界面上的按钮`
        : `不能按 ${given}；可以按 Enter、Escape、Tab、空格、方向键、PageUp、PageDown、Home、End，输入框里还能按 Backspace 和 Delete`);
    }
    hidden(target);
    const step = await this.begin(target, target === document.body ? `按 ${given}` : `在“${labelOf(target)}”上按 ${given}`);
    const init = { key, bubbles: true, cancelable: true, composed: true };
    const accepted = target.dispatchEvent(new KeyboardEvent("keydown", init));
    if (accepted && key === "Enter" && (target instanceof HTMLInputElement) && target.form) target.form.requestSubmit();
    target.dispatchEvent(new KeyboardEvent("keyup", init));
    await this.settle(step);
    return { pressed: given, on: target === document.body ? "页面" : describe(target) };
  }

  private scroll(direction: "up" | "down") {
    const container = scrollable(this.last) ?? topWindowScroller(this.options) ?? document.scrollingElement;
    if (!container) throw new ControlError("页面上没有能滚动的地方");
    const before = container.scrollTop;
    container.scrollBy({ top: (direction === "down" ? 1 : -1) * Math.max(120, container.clientHeight * 0.8) });
    return { scrolled: direction, moved: container.scrollTop !== before || undefined };
  }
}

/** Outlines each labelled control on the picture and puts its ref at its corner. */
function mark(canvas: HTMLCanvasElement, labels: Label[], ratio: number) {
  const context = labels.length ? canvas.getContext("2d") : null;
  if (!context) return;
  context.font = "600 12px ui-monospace, Consolas, monospace";
  context.textBaseline = "top";
  for (const { ref, box } of labels) {
    const x = Math.round(box.left * ratio);
    const y = Math.round(box.top * ratio);
    context.strokeStyle = "rgba(225, 29, 72, .85)";
    context.lineWidth = 1;
    context.strokeRect(x + 0.5, y + 0.5, Math.round(box.width * ratio), Math.round(box.height * ratio));
    const width = context.measureText(ref).width + 6;
    context.fillStyle = "rgba(225, 29, 72, .92)";
    context.fillRect(x, Math.max(0, y - 15), width, 15);
    context.fillStyle = "#fff";
    context.fillText(ref, x + 3, Math.max(0, y - 15) + 1.5);
  }
}

function pngOf(canvas: HTMLCanvasElement): string {
  return canvas.toDataURL("image/png").replace(/^data:image\/png;base64,/, "");
}

function shrink(canvas: HTMLCanvasElement, factor: number): HTMLCanvasElement {
  const smaller = document.createElement("canvas");
  smaller.width = Math.round(canvas.width * factor);
  smaller.height = Math.round(canvas.height * factor);
  smaller.getContext("2d")?.drawImage(canvas, 0, 0, smaller.width, smaller.height);
  return smaller;
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

/** Refuses an element VNAS may not read or act on. */
function hidden(element: Element) {
  const region = element.closest(PRIVATE);
  if (region) throw new ControlError(`这里是${region.getAttribute("data-vnas-private")}，VNAS 不能查看或操作`);
}

/** The control's name for the member; the terminal is the terminal. */
function labelOf(element: Element): string {
  return element.closest(".xterm") ? "终端" : clip(nameOf(element), 40);
}

/** The app's own dialog the element is in, which says what its 确定 confirms. */
function inDialog(element: Element): string {
  const dialog = element.closest('[role="dialog"], [role="alertdialog"]');
  if (!dialog || dialog.classList.contains("app-window")) return "";
  const name = clip(computeAccessibleName(dialog), 40);
  return name ? `（“${name}”）` : "";
}

/** Whether Backspace and Delete edit text here rather than act on what is selected. */
function editsText(element: Element): boolean {
  if (element instanceof HTMLTextAreaElement) return !element.readOnly;
  if (element instanceof HTMLInputElement) return !element.readOnly && !["checkbox", "radio", "button", "submit", "reset", "file", "image", "range", "color"].includes(element.type);
  return element instanceof HTMLElement && element.isContentEditable;
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
