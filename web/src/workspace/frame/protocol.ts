// The messages between the desktop and the helper script that runs inside a
// shared page's sandboxed frame (docs/architecture/shared-workspace.md, “隔离
// 框架的消息”). Both sides import this file; the helper is built on its own
// (vite.frame.config.ts), so it holds types and checks only.
//
// The page's own scripts run beside the helper and can post anything, so the
// desktop reads every message from the frame through readFromFrame, which
// keeps only well-formed messages and cuts long strings. What a page can
// forge this way is a claim about the page, never more (ADR 0040).

export interface Rect {
  x: number;
  y: number;
  w: number;
  h: number;
}

export interface Blocked {
  uri: string;
  directive: string;
}

// Picked is what the helper measured of the element a member picked.
export interface Picked {
  // at is the element's data-anas-at, or its nearest written ancestor's when
  // generated; -1 when none is written.
  at: number;
  generated: boolean;
  tag: string;
  text: string;
  dom: string;
  rect: Rect;
  style: Record<string, string>;
  rules: string[];
}

// Desktop → frame.
export type ToFrame =
  | { type: "anas:pick"; on: boolean }
  | { type: "anas:clear" }
  | { type: "anas:capture"; id: number; rect?: Rect };

// Frame → desktop.
export type FromFrame =
  | { type: "anas:ready"; title: string; blocked: Blocked[] }
  | ({ type: "anas:blocked" } & Blocked)
  | { type: "anas:error"; message: string }
  | ({ type: "anas:picked" } & Picked)
  | { type: "anas:captured"; id: number; image?: string; error?: string; drawMs?: number }
  | { type: "anas:scrolled"; y: number };

// Limits on what the desktop keeps of a message; the service cuts again.
export const limits = { text: 2000, dom: 8000, tag: 40, uri: 2000, message: 500, rules: 20, style: 20, blocked: 50 };
// Only a JPEG data URL of at most this many characters is a screenshot; the
// service takes at most 512 KB of JPEG.
export const maxImageChars = Math.ceil((512 << 10) / 3) * 4 + "data:image/jpeg;base64,".length;

type Record_ = Record<string, unknown>;
const isRecord = (value: unknown): value is Record_ =>
  typeof value === "object" && value !== null && !Array.isArray(value);
const text = (value: unknown, max: number) => (typeof value === "string" ? value.slice(0, max) : undefined);
const finite = (value: unknown) => (typeof value === "number" && Number.isFinite(value) ? value : undefined);

function readRect(value: unknown): Rect | undefined {
  if (!isRecord(value)) return undefined;
  const [x, y, w, h] = [finite(value.x), finite(value.y), finite(value.w), finite(value.h)];
  if (x === undefined || y === undefined || w === undefined || h === undefined || w < 0 || h < 0) return undefined;
  return { x, y, w, h };
}

function readBlocked(value: unknown): Blocked | undefined {
  if (!isRecord(value)) return undefined;
  const uri = text(value.uri, limits.uri);
  const directive = text(value.directive, 100);
  return uri === undefined || directive === undefined ? undefined : { uri, directive };
}

function readPicked(value: Record_): Picked | undefined {
  const at = finite(value.at);
  const rect = readRect(value.rect);
  const tag = text(value.tag, limits.tag);
  if (at === undefined || !Number.isInteger(at) || at < -1 || !rect || !tag || typeof value.generated !== "boolean") {
    return undefined;
  }
  const style: Record<string, string> = {};
  if (isRecord(value.style)) {
    for (const [name, property] of Object.entries(value.style).slice(0, limits.style)) {
      if (typeof property === "string") style[name.slice(0, 60)] = property.slice(0, 200);
    }
  }
  const rules = Array.isArray(value.rules)
    ? value.rules
        .filter((rule): rule is string => typeof rule === "string")
        .slice(0, limits.rules)
        .map((rule) => rule.slice(0, 300))
    : [];
  return {
    at,
    generated: value.generated,
    tag,
    text: text(value.text, limits.text) ?? "",
    dom: text(value.dom, limits.dom) ?? "",
    rect,
    style,
    rules,
  };
}

// readFromFrame returns a message from the frame as this file defines it,
// or undefined for anything else.
export function readFromFrame(data: unknown): FromFrame | undefined {
  if (!isRecord(data)) return undefined;
  switch (data.type) {
    case "anas:ready": {
      const blocked = Array.isArray(data.blocked)
        ? data.blocked
            .slice(0, limits.blocked)
            .map(readBlocked)
            .filter((item) => item !== undefined)
        : [];
      return { type: "anas:ready", title: text(data.title, 200) ?? "", blocked };
    }
    case "anas:blocked": {
      const blocked = readBlocked(data);
      return blocked && { type: "anas:blocked", ...blocked };
    }
    case "anas:error": {
      const message = text(data.message, limits.message);
      return message === undefined ? undefined : { type: "anas:error", message };
    }
    case "anas:picked": {
      const picked = readPicked(data);
      return picked && { type: "anas:picked", ...picked };
    }
    case "anas:captured": {
      const id = finite(data.id);
      if (id === undefined) return undefined;
      const image =
        typeof data.image === "string" &&
        data.image.startsWith("data:image/jpeg;base64,") &&
        data.image.length <= maxImageChars
          ? data.image
          : undefined;
      const error = text(data.error, limits.message);
      if (!image && error === undefined) return { type: "anas:captured", id, error: "截图无效" };
      return { type: "anas:captured", id, image, error, drawMs: finite(data.drawMs) };
    }
    case "anas:scrolled": {
      const y = finite(data.y);
      return y === undefined ? undefined : { type: "anas:scrolled", y: Math.max(0, Math.round(y)) };
    }
    default:
      return undefined;
  }
}

// readToFrame returns a message from the desktop as this file defines it,
// or undefined for anything else.
export function readToFrame(data: unknown): ToFrame | undefined {
  if (!isRecord(data)) return undefined;
  switch (data.type) {
    case "anas:pick":
      return typeof data.on === "boolean" ? { type: "anas:pick", on: data.on } : undefined;
    case "anas:clear":
      return { type: "anas:clear" };
    case "anas:capture": {
      const id = finite(data.id);
      if (id === undefined) return undefined;
      const rect = data.rect === undefined ? undefined : readRect(data.rect);
      if (data.rect !== undefined && !rect) return undefined;
      return rect ? { type: "anas:capture", id, rect } : { type: "anas:capture", id };
    }
    default:
      return undefined;
  }
}
