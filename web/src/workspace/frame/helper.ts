// The helper script inside a shared page's sandboxed frame (ADR 0040). The
// workspace service adds it to every page it shows, as
// <script src="/assets/workspace-frame.js" data-page data-highlight
// data-scroll>, and it talks to the desktop only through
// postMessage (./protocol.ts). It runs beside the page's own scripts, in an
// origin of its own: it can read the page, never the desktop.
//
// vite.frame.config.ts builds it as a classic script: a module script from
// an opaque origin would need CORS.
import { createContext, domToCanvas } from "modern-screenshot";

import { type FromFrame, limits, type Picked, type Rect, readToFrame } from "./protocol";

const AT = "data-anas-at";
const PIN = "data-anas-pin";
const PICKED = "#2563eb";
const GENERATED = "#d97706";
const CHANGED = "#16a34a";
// The computed style properties a member's agent reads of a picked element.
const STYLE = [
  "font-size",
  "font-weight",
  "line-height",
  "color",
  "background-color",
  "padding",
  "margin",
  "width",
  "height",
  "border",
  "border-radius",
  "display",
  "gap",
];

// written is the nearest element at or above el that the page's source has.
const written = (el: Element) => el.closest(`[${AT}]`);

// sourceOf is el's markup without the service's position attributes.
function sourceOf(el: Element): string {
  const copy = el.cloneNode(true) as Element;
  copy.removeAttribute(AT);
  for (const inner of copy.querySelectorAll(`[${AT}]`)) inner.removeAttribute(AT);
  return copy.outerHTML;
}

// rulesFor lists the selectors of the page's own style rules that match el;
// a sheet another origin owns cannot be read and is passed over.
function rulesFor(el: Element, sheets: StyleSheetList): string[] {
  const found: string[] = [];
  const walk = (rules: CSSRuleList) => {
    for (const rule of rules) {
      if (found.length >= 20) return;
      if (rule instanceof CSSStyleRule) {
        try {
          if (el.matches(rule.selectorText)) found.push(rule.selectorText);
        } catch {
          // A selector this browser cannot match, such as a vendor one.
        }
      } else if ("cssRules" in rule && rule.cssRules) {
        walk(rule.cssRules as CSSRuleList);
      }
    }
  };
  for (const sheet of sheets) {
    try {
      walk(sheet.cssRules);
    } catch {
      // Not readable.
    }
  }
  return found;
}

// measure is what the desktop reports of a picked element.
export function measure(el: Element): Picked {
  const anchor = written(el);
  const box = el.getBoundingClientRect();
  const computed = getComputedStyle(el);
  const style: Record<string, string> = {};
  for (const name of STYLE) {
    const value = computed.getPropertyValue(name);
    if (value) style[name] = value;
  }
  const shown = el instanceof HTMLElement ? el.innerText : undefined;
  return {
    at: anchor ? Number(anchor.getAttribute(AT)) : -1,
    generated: anchor !== el,
    tag: el.tagName.toLowerCase(),
    text: (shown ?? el.textContent ?? "").trim().slice(0, limits.text),
    dom: sourceOf(el).slice(0, limits.dom),
    rect: { x: box.x, y: box.y, w: box.width, h: box.height },
    style,
    rules: rulesFor(el, el.ownerDocument.styleSheets),
  };
}

// changedElements are the elements a version's change points at that show:
// a changed <style> has no box to point at.
export function changedElements(doc: Document, offsets: number[]): Element[] {
  return offsets
    .map((at) => doc.querySelector(`[${AT}="${at}"]`))
    .filter((el): el is Element => el !== null && el.getClientRects().length > 0);
}

// numbers reads a data attribute of comma-separated offsets.
export const numbers = (value: string | undefined) =>
  (value ?? "")
    .split(",")
    .filter(Boolean)
    .map(Number)
    .filter((n) => Number.isInteger(n) && n >= 0);

// Overlay draws outlines in a closed shadow root at the top of the page,
// outside the page's styles and left out of screenshots.
class Overlay {
  readonly host: HTMLElement;
  private readonly layer: ShadowRoot;
  private tracked = new Map<HTMLElement, Element>();

  constructor(
    private readonly doc: Document,
    signal: AbortSignal,
  ) {
    this.host = doc.createElement("anas-overlay");
    this.host.style.cssText = "position:fixed;inset:0;pointer-events:none;z-index:2147483647;display:block";
    this.layer = this.host.attachShadow({ mode: "closed" });
    doc.documentElement.append(this.host);
    const follow = () => {
      for (const [box, el] of this.tracked) this.place(box, el.getBoundingClientRect());
    };
    doc.defaultView?.addEventListener("scroll", follow, { passive: true, capture: true, signal });
    doc.defaultView?.addEventListener("resize", follow, { signal });
  }

  place(box: HTMLElement, rect: DOMRect) {
    Object.assign(box.style, {
      left: `${rect.left - 2}px`,
      top: `${rect.top - 2}px`,
      width: `${rect.width + 4}px`,
      height: `${rect.height + 4}px`,
    });
  }

  // outline draws a box around el, with a label above it, that follows it.
  outline(el: Element, color: string, label: string): HTMLElement {
    const box = this.doc.createElement("div");
    box.style.cssText =
      `position:fixed;outline:2px solid ${color};background:${color}1f;border-radius:3px;` + "transition:opacity .6s";
    const tag = this.doc.createElement("span");
    tag.textContent = label;
    tag.style.cssText =
      `position:absolute;left:0;top:-21px;font:500 12px/18px system-ui,sans-serif;background:${color};` +
      "color:#fff;padding:1px 7px;border-radius:4px;white-space:nowrap";
    box.append(tag);
    this.place(box, el.getBoundingClientRect());
    this.layer.append(box);
    this.tracked.set(box, el);
    return box;
  }

  remove(box: HTMLElement | undefined) {
    if (!box) return;
    this.tracked.delete(box);
    box.remove();
  }
}

// styleSandbox stands in for the srcdoc frame modern-screenshot reads default
// styles from: in a sandboxed page that frame has an origin of its own and
// cannot be read, which fails the whole picture.
function styleSandbox(doc: Document) {
  const holder = doc.createElement("anas-defaults");
  holder.style.cssText =
    "all:initial;position:fixed;left:0;top:0;width:0;height:0;overflow:hidden;visibility:hidden;pointer-events:none";
  const shadow = holder.attachShadow({ mode: "closed" });
  const body = doc.createElement("div");
  body.style.cssText = "all:initial;display:block";
  shadow.append(body);
  doc.documentElement.append(holder);
  return {
    contentWindow: {
      document: {
        body,
        createElement: (name: string) => doc.createElement(name),
        createElementNS: (namespace: string, name: string) => doc.createElementNS(namespace, name),
      },
      getComputedStyle: (el: Element, pseudo?: string | null) => getComputedStyle(el, pseudo),
    },
    remove: () => holder.remove(),
  };
}

// pinsForNow pins each sticky and fixed element where it shows now: the
// picture is of the unscrolled document, where they would sit at rest.
function pinsForNow(doc: Document): Map<string, string> {
  const pins = new Map<string, string>();
  const view = doc.defaultView;
  if (!view || !doc.body) return pins;
  for (const el of doc.body.querySelectorAll<HTMLElement>("*")) {
    const position = getComputedStyle(el).position;
    if (position !== "sticky" && position !== "fixed") continue;
    const now = el.getBoundingClientRect();
    let style: string;
    if (position === "fixed") {
      // In the picture the viewport is the whole document.
      style =
        `position:fixed;top:${now.top + view.scrollY}px;left:${now.left + view.scrollX}px;` +
        `width:${now.width}px;height:${now.height}px;right:auto;bottom:auto;margin:0`;
    } else {
      const inline = el.style.getPropertyValue("position");
      const priority = el.style.getPropertyPriority("position");
      el.style.setProperty("position", "static", "important");
      const rest = el.getBoundingClientRect();
      el.style.setProperty("position", inline, priority);
      style = `position:relative;top:${now.top - rest.top}px;left:${now.left - rest.left}px;right:auto;bottom:auto`;
    }
    const key = String(pins.size);
    el.setAttribute(PIN, key);
    pins.set(key, style);
  }
  return pins;
}

// canvasColor is what the browser paints behind the page: the root's
// background, or the body's when the root has none.
function canvasColor(doc: Document): string {
  for (const el of [doc.documentElement, doc.body]) {
    const color = el && getComputedStyle(el).backgroundColor;
    if (color && color !== "transparent" && color !== "rgba(0, 0, 0, 0)") return color;
  }
  return "#ffffff";
}

// maxEdge is a screenshot's longer side at most.
const maxEdge = 1280;

// capture draws the whole document, as the page itself shows it, and crops
// the part of the viewport in rect to a JPEG.
async function capture(doc: Document, skip: Node, rect: Rect): Promise<string> {
  const view = doc.defaultView;
  if (!view) throw new Error("no window");
  const root = doc.documentElement;
  const viewport = { w: root.clientWidth, h: root.clientHeight };
  const pins = pinsForNow(doc);
  let full: HTMLCanvasElement;
  try {
    const context = await createContext(root, {
      width: Math.max(root.scrollWidth, viewport.w),
      height: Math.max(root.scrollHeight, viewport.h),
      scale: 1,
      autoDestruct: true,
      backgroundColor: canvasColor(doc),
      // A frame inside the page is another origin; reading it fails the picture.
      filter: (node) =>
        node !== skip && (node as Element).localName !== "anas-defaults" && !(node instanceof HTMLIFrameElement),
      onCloneEachNode: (clone) => {
        const key = clone instanceof Element ? clone.getAttribute(PIN) : null;
        if (!key || !(clone instanceof HTMLElement)) return;
        clone.removeAttribute(PIN);
        clone.style.cssText += `;${(pins.get(key) ?? "").replaceAll(";", " !important;")} !important`;
      },
    });
    (context as unknown as { sandbox: unknown }).sandbox = styleSandbox(doc);
    full = await domToCanvas(context);
  } finally {
    for (const el of doc.querySelectorAll(`[${PIN}]`)) el.removeAttribute(PIN);
  }
  const left = Math.max(0, rect.x);
  const top = Math.max(0, rect.y);
  const width = Math.max(1, Math.min(viewport.w, rect.x + rect.w) - left);
  const height = Math.max(1, Math.min(viewport.h, rect.y + rect.h) - top);
  const scale = Math.min(1, maxEdge / Math.max(width, height));
  const out = doc.createElement("canvas");
  out.width = Math.max(1, Math.round(width * scale));
  out.height = Math.max(1, Math.round(height * scale));
  out
    .getContext("2d")
    ?.drawImage(full, left + view.scrollX, top + view.scrollY, width, height, 0, 0, out.width, out.height);
  // A smaller picture when the first is too large for the service.
  let image = out.toDataURL("image/jpeg", 0.85);
  if (image.length > 600_000) image = out.toDataURL("image/jpeg", 0.6);
  return image;
}

// startHelper runs the helper for the script element that loaded it, until
// the function it returns stops it.
export function startHelper(script: HTMLScriptElement, view: Window = window): () => void {
  const doc = view.document;
  const parentOrigin = new URL(script.src, view.location.href).origin;
  const post = (message: FromFrame) => view.parent.postMessage(message, parentOrigin);
  const stop = new AbortController();
  const listen = <K extends keyof WindowEventMap>(
    type: K,
    listener: (event: WindowEventMap[K]) => void,
    options: AddEventListenerOptions = {},
  ) => view.addEventListener(type, listener, { ...options, signal: stop.signal });

  const blocked: { uri: string; directive: string }[] = [];
  listen("securitypolicyviolation", (event) => {
    const item = { uri: event.blockedURI || "inline", directive: event.effectiveDirective };
    if (blocked.some((seen) => seen.uri === item.uri && seen.directive === item.directive)) return;
    blocked.push(item);
    post({ type: "anas:blocked", ...item });
  });
  listen("error", (event) => post({ type: "anas:error", message: String(event.message || "脚本错误") }));
  listen("unhandledrejection", (event) =>
    post({ type: "anas:error", message: String((event.reason as Error | undefined)?.message ?? event.reason) }),
  );

  let overlay: Overlay | undefined;
  const layer = () => {
    overlay ??= new Overlay(doc, stop.signal);
    return overlay;
  };
  let picking = false;
  let hover: HTMLElement | undefined;
  // hovered is what hover outlines: moving within it draws nothing again.
  let hovered: Element | undefined;
  let mark: HTMLElement | undefined;
  const unhover = () => {
    layer().remove(hover);
    hovered = undefined;
  };
  const label = (el: Element) => el.tagName.toLowerCase() + (written(el) === el ? "" : " · 脚本生成");
  listen(
    "pointermove",
    (event) => {
      const target = event.target;
      if (!picking || !(target instanceof Element) || target === overlay?.host || target === hovered) return;
      unhover();
      hovered = target;
      hover = layer().outline(target, written(target) === target ? PICKED : GENERATED, label(target));
    },
    { capture: true },
  );
  // While picking, the page does not see the clicks that pick.
  for (const type of ["pointerdown", "mousedown", "mouseup", "click", "dblclick", "auxclick", "submit"] as const) {
    listen(
      type,
      (event) => {
        if (!picking) return;
        event.preventDefault();
        event.stopImmediatePropagation();
        if (type !== "click" || !(event.target instanceof Element)) return;
        unhover();
        layer().remove(mark);
        mark = layer().outline(event.target, PICKED, label(event.target));
        post({ type: "anas:picked", ...measure(event.target) });
      },
      { capture: true },
    );
  }

  listen("message", (event) => {
    if (event.source !== view.parent || event.origin !== parentOrigin) return;
    const message = readToFrame(event.data);
    if (!message) return;
    if (message.type === "anas:pick") {
      picking = message.on;
      if (!picking) unhover();
      doc.documentElement.style.cursor = picking ? "crosshair" : "";
    } else if (message.type === "anas:clear") {
      layer().remove(mark);
      mark = undefined;
    } else {
      capture(doc, layer().host, message.rect).then(
        (image) => post({ type: "anas:captured", id: message.id, image }),
        () => post({ type: "anas:captured", id: message.id }),
      );
    }
  });

  let scrolled: ReturnType<typeof setTimeout> | undefined;
  listen("scroll", () => {
    clearTimeout(scrolled);
    scrolled = setTimeout(() => post({ type: "anas:scrolled", y: Math.round(view.scrollY) }), 200);
  });

  const ready = () => {
    const changed = changedElements(doc, numbers(script.dataset.highlight));
    if (changed[0]) changed[0].scrollIntoView({ block: "center" });
    else if (script.dataset.scroll) view.scrollTo(0, Number(script.dataset.scroll));
    for (const el of changed) {
      const box = layer().outline(el, CHANGED, "已修改");
      setTimeout(() => {
        box.style.opacity = "0";
      }, 4000);
      setTimeout(() => layer().remove(box), 4700);
    }
    post({ type: "anas:ready", blocked: [...blocked] });
  };
  if (doc.readyState === "complete") ready();
  else listen("load", ready, { once: true });
  return () => {
    stop.abort();
    overlay?.host.remove();
  };
}

const current = typeof document === "undefined" ? null : document.currentScript;
if (current instanceof HTMLScriptElement && current.dataset.page) startHelper(current);
