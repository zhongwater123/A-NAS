import { X } from "lucide-react";
import { HoverCard } from "radix-ui";
import { useEffect, useLayoutEffect, useRef, useState } from "react";

import type { AppGlyph } from "../media/MediaCenterGlyph";
import styles from "./desktop.module.css";
import { snapshotWindow } from "./windowSnapshot";

export interface DockEntry {
  id: string;
  title: string;
  minimized: boolean;
  icon: AppGlyph;
  tone: string;
}

type DockState = "focused" | "background" | "minimized";

interface RenderedEntry extends DockEntry {
  leaving: boolean;
}

// One easing and duration for every dock motion keeps switching feeling continuous.
const motion = { duration: 340, easing: "cubic-bezier(.22,1,.36,1)" };
// A window preview opens after the pointer rests on an icon; while one is
// showing, moving along the dock swaps it at once, like a desktop taskbar.
const previewDelay = { open: 450, close: 160 };
// The thumbnail box in CSS pixels; each window keeps its proportions inside it.
const thumbnailBox = { width: 224, height: 146 };
// Drawing a window takes a moment, so it starts once the pointer rests on its
// icon, during the open delay; a drawing this recent is shown again as is.
const snapshotTiming = { rest: 120, fresh: 2000 };

interface DockProps {
  entries: DockEntry[];
  focusedID?: string;
  onOpen: (id: string) => void;
  onMinimize: (id: string) => void;
  onClose: (id: string) => void;
}

export function Dock({ entries, focusedID, onOpen, onMinimize, onClose }: DockProps) {
  const rendered = useRenderedEntries(entries);
  const [previewID, setPreviewID] = useState<string>();
  // A click hides the preview until the pointer leaves that icon, so the
  // hover and focus that came with the click do not bring it back.
  const dismissed = useRef<string | undefined>(undefined);
  const snapshots = useSnapshots(entries);
  const resting = useRef<number | undefined>(undefined);
  useEffect(() => () => window.clearTimeout(resting.current), []);

  const changePreview = (id: string, open: boolean) => {
    if (open && dismissed.current === id) return;
    setPreviewID((current) => (open ? id : current === id ? undefined : current));
  };
  const highlights = useRef(new Map<string, HTMLSpanElement>());
  const lastFocus = useRef<{ id: string; rect: DOMRect } | undefined>(undefined);

  // The highlight is one shared capsule: when focus moves, the newly focused
  // item's capsule starts at the previous capsule's box and glides into place.
  useLayoutEffect(() => {
    const previous = lastFocus.current;
    const next = focusedID ? highlights.current.get(focusedID) : undefined;
    if (previous?.id === focusedID) {
      if (next) lastFocus.current = { id: focusedID!, rect: next.getBoundingClientRect() };
      return;
    }
    const previousElement = previous && highlights.current.get(previous.id);
    const from =
      previousElement && previousElement.isConnected ? previousElement.getBoundingClientRect() : previous?.rect;
    if (next && typeof next.animate === "function") {
      const to = next.getBoundingClientRect();
      if (from && from.width > 0 && to.width > 0) {
        next.animate(
          [
            { transform: `translateX(${from.left - to.left}px) scaleX(${from.width / to.width})` },
            { transform: "none" },
          ],
          motion,
        );
      } else {
        next.animate(
          [
            { opacity: 0, transform: "scale(.7)" },
            { opacity: 1, transform: "none" },
          ],
          motion,
        );
      }
    } else if (!next && previousElement && typeof previousElement.animate === "function") {
      previousElement.animate([{ opacity: 1 }, { opacity: 0 }], motion);
    }
    lastFocus.current = next && focusedID ? { id: focusedID, rect: next.getBoundingClientRect() } : undefined;
  }, [focusedID, rendered]);

  return (
    <nav className={`${styles.taskShelf} ${entries.length ? styles.visible : ""}`} aria-label="已打开窗口">
      {rendered.map((entry) => {
        const state: DockState = entry.minimized ? "minimized" : entry.id === focusedID ? "focused" : "background";
        // The focused app minimizes on click, like a desktop taskbar; others come to the front.
        const label = {
          minimized: `恢复${entry.title}`,
          focused: `最小化${entry.title}`,
          background: `切换到${entry.title}`,
        }[state];
        const Icon = entry.icon;
        return (
          <HoverCard.Root
            key={entry.id}
            open={previewID === entry.id && !entry.leaving}
            onOpenChange={(open) => changePreview(entry.id, open)}
            openDelay={previewID ? 0 : previewDelay.open}
            closeDelay={previewDelay.close}
          >
            <HoverCard.Trigger asChild>
              <button
                className={`${styles.dockItem} ${styles[state] ?? ""} ${entry.leaving ? styles.leaving : ""}`}
                aria-label={label}
                aria-current={state === "focused" && !entry.leaving ? "true" : undefined}
                aria-hidden={entry.leaving || undefined}
                inert={entry.leaving}
                tabIndex={entry.leaving ? -1 : undefined}
                onClick={() => {
                  dismissed.current = entry.id;
                  setPreviewID(undefined);
                  if (state === "focused") onMinimize(entry.id);
                  else onOpen(entry.id);
                }}
                onPointerEnter={(event) => {
                  if (event.pointerType === "touch") return;
                  window.clearTimeout(resting.current);
                  resting.current = window.setTimeout(() => void snapshots.capture(entry.id), snapshotTiming.rest);
                }}
                onPointerLeave={() => {
                  window.clearTimeout(resting.current);
                  if (dismissed.current === entry.id) dismissed.current = undefined;
                }}
              >
                <span
                  className={styles.dockHighlight}
                  aria-hidden="true"
                  ref={(element) => {
                    if (element) highlights.current.set(entry.id, element);
                    else highlights.current.delete(entry.id);
                  }}
                />
                <span className={`${styles.dockIcon} ${styles[`icon-${entry.tone}`] ?? ""}`}>
                  <Icon aria-hidden="true" />
                </span>
                <span className={styles.dockDot} aria-hidden="true" />
              </button>
            </HoverCard.Trigger>
            <HoverCard.Portal>
              <HoverCard.Content
                className={styles.dockPreview}
                role="group"
                aria-label={`${entry.title}窗口预览`}
                side="top"
                sideOffset={14}
                collisionPadding={12}
              >
                <WindowPreview
                  entry={entry}
                  minimized={state === "minimized"}
                  snapshots={snapshots}
                  onShow={() => {
                    setPreviewID(undefined);
                    onOpen(entry.id);
                  }}
                  onClose={() => {
                    setPreviewID(undefined);
                    onClose(entry.id);
                  }}
                />
              </HoverCard.Content>
            </HoverCard.Portal>
          </HoverCard.Root>
        );
      })}
    </nav>
  );
}

// WindowPreview shows the window's title, a close button and its latest
// thumbnail; the thumbnail brings the window forward.
function WindowPreview({
  entry,
  minimized,
  snapshots,
  onShow,
  onClose,
}: {
  entry: DockEntry;
  minimized: boolean;
  snapshots: Snapshots;
  onShow: () => void;
  onClose: () => void;
}) {
  const [image, setImage] = useState(() => snapshots.latest(entry.id));
  useEffect(() => {
    let active = true;
    void snapshots.capture(entry.id).then((url) => {
      if (active && url) setImage(url);
    });
    return () => {
      active = false;
    };
  }, [entry.id, snapshots]);

  const Icon = entry.icon;
  return (
    <>
      <div className={styles.dockPreviewHead}>
        <span className={`${styles.dockPreviewIcon} ${styles[`icon-${entry.tone}`] ?? ""}`}>
          <Icon aria-hidden="true" />
        </span>
        <span className={styles.dockPreviewTitle}>{entry.title}</span>
        {minimized && <span className={styles.dockPreviewState}>已最小化</span>}
        <button className={styles.dockPreviewClose} aria-label={`关闭${entry.title}`} onClick={onClose}>
          <X aria-hidden="true" />
        </button>
      </div>
      <button className={styles.dockPreviewShot} aria-label={`显示${entry.title}窗口`} onClick={onShow}>
        {image ? (
          <img src={image} alt="" draggable={false} />
        ) : (
          <span className={`${styles.dockPreviewPlaceholder} ${styles[`icon-${entry.tone}`] ?? ""}`}>
            <Icon aria-hidden="true" />
          </span>
        )}
      </button>
    </>
  );
}

interface Snapshots {
  latest: (id: string) => string | undefined;
  capture: (id: string) => Promise<string | undefined>;
}

// useSnapshots keeps the last thumbnail of each open window, so a preview
// shows it at once while a fresh one draws, and forgets closed windows.
function useSnapshots(entries: DockEntry[]): Snapshots {
  const store = useRef(new Map<string, { url?: string; at: number; drawing?: Promise<string | undefined> }>());
  const openIDs = entries.map((entry) => entry.id).join("|");
  useEffect(() => {
    const open = new Set(openIDs.split("|"));
    for (const id of store.current.keys()) if (!open.has(id)) store.current.delete(id);
  }, [openIDs]);

  const [snapshots] = useState<Snapshots>(() => ({
    latest: (id) => store.current.get(id)?.url,
    capture: (id) => {
      const known = store.current.get(id);
      if (known?.drawing) return known.drawing;
      if (known?.url && performance.now() - known.at < snapshotTiming.fresh) return Promise.resolve(known.url);
      const drawing: Promise<string | undefined> = snapshotWindow(id, thumbnailBox)
        // An undrawable window keeps its last thumbnail, or the icon placeholder.
        .catch(() => undefined)
        .then((url) => {
          const current = store.current.get(id);
          if (current?.drawing !== drawing) return url;
          store.current.set(id, { url: url ?? current.url, at: performance.now() });
          return url ?? current.url;
        });
      store.current.set(id, { url: known?.url, at: known?.at ?? 0, drawing });
      return drawing;
    },
  }));
  return snapshots;
}

// Keeps closed entries mounted while they collapse so the dock width shrinks
// continuously instead of jumping.
function useRenderedEntries(entries: DockEntry[]): RenderedEntry[] {
  const [rendered, setRendered] = useState<RenderedEntry[]>(() =>
    entries.map((entry) => ({ ...entry, leaving: false })),
  );
  const signature = entries.map((entry) => `${entry.id}:${entry.minimized}:${entry.title}`).join("|");
  const [merged, setMerged] = useState(signature);
  if (merged !== signature) {
    setMerged(signature);
    const current = new Map(entries.map((entry) => [entry.id, entry]));
    setRendered((previous) => {
      const next: RenderedEntry[] = previous.map((item) => {
        const entry = current.get(item.id);
        return entry ? { ...entry, leaving: false } : { ...item, leaving: true };
      });
      entries.forEach((entry) => {
        if (!previous.some((item) => item.id === entry.id)) next.push({ ...entry, leaving: false });
      });
      return next;
    });
  }

  const leavingIDs = rendered
    .filter((item) => item.leaving)
    .map((item) => item.id)
    .join("|");
  useEffect(() => {
    if (!leavingIDs) return;
    const timer = window.setTimeout(
      () => setRendered((previous) => previous.filter((item) => !item.leaving)),
      motion.duration,
    );
    return () => window.clearTimeout(timer);
  }, [leavingIDs]);

  return rendered;
}
