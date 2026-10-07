import type { LucideIcon } from "lucide-react";
import { useEffect, useLayoutEffect, useRef, useState } from "react";

export interface DockEntry {
  id: string;
  title: string;
  minimized: boolean;
  icon: LucideIcon;
  tone: string;
}

type DockState = "focused" | "background" | "minimized";

interface RenderedEntry extends DockEntry {
  leaving: boolean;
}

// One easing and duration for every dock motion keeps switching feeling continuous.
const motion = { duration: 340, easing: "cubic-bezier(.22,1,.36,1)" };

export function Dock({ entries, focusedID, onSelect }: { entries: DockEntry[]; focusedID?: string; onSelect: (id: string, state: DockState) => void }) {
  const rendered = useRenderedEntries(entries);
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
    const from = previousElement && previousElement.isConnected ? previousElement.getBoundingClientRect() : previous?.rect;
    if (next && typeof next.animate === "function") {
      const to = next.getBoundingClientRect();
      if (from && from.width > 0 && to.width > 0) {
        next.animate(
          [{ transform: `translateX(${from.left - to.left}px) scaleX(${from.width / to.width})` }, { transform: "none" }],
          motion,
        );
      } else {
        next.animate([{ opacity: 0, transform: "scale(.7)" }, { opacity: 1, transform: "none" }], motion);
      }
    } else if (!next && previousElement && typeof previousElement.animate === "function") {
      previousElement.animate([{ opacity: 1 }, { opacity: 0 }], motion);
    }
    lastFocus.current = next && focusedID ? { id: focusedID, rect: next.getBoundingClientRect() } : undefined;
  }, [focusedID, rendered]);

  return (
    <nav className={`task-shelf ${entries.length ? "visible" : ""}`} aria-label="已打开窗口">
      {rendered.map((entry) => {
        const state: DockState = entry.minimized ? "minimized" : entry.id === focusedID ? "focused" : "background";
        // The focused app minimizes on click, like a desktop taskbar; others come to the front.
        const label = { minimized: `恢复${entry.title}`, focused: `最小化${entry.title}`, background: `切换到${entry.title}` }[state];
        const Icon = entry.icon;
        return (
          <button
            key={entry.id}
            className={`dock-item ${state} ${entry.leaving ? "leaving" : ""}`}
            aria-label={label}
            aria-current={state === "focused" && !entry.leaving ? "true" : undefined}
            aria-hidden={entry.leaving || undefined}
            inert={entry.leaving}
            tabIndex={entry.leaving ? -1 : undefined}
            onClick={() => onSelect(entry.id, state)}
          >
            <span
              className="dock-highlight"
              aria-hidden="true"
              ref={(element) => {
                if (element) highlights.current.set(entry.id, element);
                else highlights.current.delete(entry.id);
              }}
            />
            <span className={`dock-icon icon-${entry.tone}`}><Icon aria-hidden="true" /></span>
            <span className="dock-dot" aria-hidden="true" />
          </button>
        );
      })}
    </nav>
  );
}

// Keeps closed entries mounted while they collapse so the dock width shrinks
// continuously instead of jumping.
function useRenderedEntries(entries: DockEntry[]): RenderedEntry[] {
  const [rendered, setRendered] = useState<RenderedEntry[]>(() => entries.map((entry) => ({ ...entry, leaving: false })));
  const signature = entries.map((entry) => `${entry.id}:${entry.minimized}:${entry.title}`).join("|");
  const latest = useRef(entries);
  latest.current = entries;

  useLayoutEffect(() => {
    const current = new Map(latest.current.map((entry) => [entry.id, entry]));
    setRendered((previous) => {
      const next: RenderedEntry[] = previous.map((item) => {
        const entry = current.get(item.id);
        return entry ? { ...entry, leaving: false } : { ...item, leaving: true };
      });
      latest.current.forEach((entry) => {
        if (!previous.some((item) => item.id === entry.id)) next.push({ ...entry, leaving: false });
      });
      return next;
    });
  }, [signature]);

  const leavingIDs = rendered.filter((item) => item.leaving).map((item) => item.id).join("|");
  useEffect(() => {
    if (!leavingIDs) return;
    const timer = window.setTimeout(() => setRendered((previous) => previous.filter((item) => !item.leaving)), motion.duration);
    return () => window.clearTimeout(timer);
  }, [leavingIDs]);

  return rendered;
}
