import { useVirtualizer } from "@tanstack/react-virtual";
import { PointerEvent as ReactPointerEvent, ReactNode, Ref, RefObject, useCallback, useEffect, useImperativeHandle, useLayoutEffect, useMemo, useRef, useState } from "react";

import type { Box, Entry, PendingPhoto } from "./layout";
import { fullMonthLabel } from "./model";
import type { PhotoAsset } from "./photosApi";

export interface ScrollerHandle {
  // beginSwipe follows a press on a tile's check across other tiles.
  beginSwipe: (assetId: string, event: ReactPointerEvent, onSwipe: (to: string) => void) => void;
  scrollToKey: (key: string) => void;
  // revealAsset scrolls a photo into view if needed and finds its tile on
  // screen, for the viewer to close into.
  revealAsset: (assetId: string) => Promise<DOMRect | undefined>;
}

interface Props {
  entries: Entry[];
  scrollRef: RefObject<HTMLDivElement | null>;
  // Room under the floating toolbar and beside the content; the right
  // side also leaves room for the scrubber.
  topInset: number;
  inset: number;
  insetRight?: number;
  renderTile: (box: Box<PhotoAsset>) => ReactNode;
  renderPending?: (box: Box<PendingPhoto>) => ReactNode;
  renderEntry: (entry: Entry) => ReactNode;
  // onRange gets the entries in and near view, to load what they need.
  onRange?: (entries: Entry[]) => void;
  onZoom?: (factor: number) => void;
  onMarquee?: (ids: string[], additive: boolean) => void;
  onMarqueeEnd?: () => void;
  onBackgroundClick?: () => void;
  floatingLabel?: boolean;
  scrubber?: boolean;
  handle?: Ref<ScrollerHandle>;
  children?: ReactNode;
}

interface Anchor { asset?: string; key?: string; fraction: number; viewportY: number }

// useContentWidth follows the scroll area's width less the insets.
export function useContentWidth(ref: RefObject<HTMLDivElement | null>, inset: number, insetRight = inset) {
  const [width, setWidth] = useState(0);
  useLayoutEffect(() => {
    const element = ref.current;
    if (!element) return;
    // jsdom lays nothing out; tests get a desktop-sized window.
    const measure = () => setWidth(Math.max(240, (element.clientWidth || 960) - inset - insetRight));
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
  }, [ref, inset, insetRight]);
  return width;
}

const starts = (entries: Entry[], from: number) => {
  const result: number[] = [];
  let offset = from;
  for (const entry of entries) { result.push(offset); offset += entry.size; }
  return result;
};

const assetsOf = (entry: Entry) => entry.kind === "row" ? entry.boxes.map((box) => box.item.id) : [];

// PhotoScroller renders only the entries near the view. When entries change
// above the view, as when a month loads or the grid is resized, it keeps the
// photo at the top of the view (or under the pointer when zooming) in place.
export function PhotoScroller({ entries, scrollRef, topInset, inset, insetRight = inset, renderTile, renderPending, renderEntry, onRange, onZoom, onMarquee, onMarqueeEnd, onBackgroundClick, floatingLabel, scrubber, handle, children }: Props) {
  const virtualizer = useVirtualizer({
    count: entries.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: (index) => entries[index]?.size ?? 0,
    getItemKey: (index) => entries[index]?.key ?? index,
    overscan: 3,
    paddingStart: topInset,
    paddingEnd: 96,
  });
  const previous = useRef<Entry[]>(undefined);
  const pendingAnchor = useRef<Anchor>(undefined);

  // Index of every entry key and photo, for anchors and hit tests.
  const index = useMemo(() => {
    const byKey = new Map<string, number>();
    const byAsset = new Map<string, number>();
    entries.forEach((entry, position) => {
      byKey.set(entry.key, position);
      for (const id of assetsOf(entry)) byAsset.set(id, position);
    });
    return { byKey, byAsset, starts: starts(entries, topInset) };
  }, [entries, topInset]);

  useLayoutEffect(() => {
    const before = previous.current;
    previous.current = entries;
    const element = scrollRef.current;
    let anchor = pendingAnchor.current;
    pendingAnchor.current = undefined;
    if (element && before && !anchor && element.scrollTop > 0) {
      // The first entry showing below the toolbar, and a few before it in
      // case it was replaced, as a loading month's placeholder is.
      const top = element.scrollTop + topInset;
      const old = starts(before, topInset);
      let first = before.findIndex((entry, position) => old[position] + entry.size > top);
      if (first < 0) first = before.length - 1;
      for (let position = first; position >= Math.max(0, first - 6) && !anchor; position--) {
        const entry = before[position];
        const asset = assetsOf(entry)[0];
        if ((asset && index.byAsset.has(asset)) || index.byKey.has(entry.key)) {
          anchor = { asset, key: entry.key, fraction: 0, viewportY: old[position] - element.scrollTop };
        }
      }
    }
    virtualizer.measure();
    if (!element || !anchor) return;
    const target = anchor.asset !== undefined && index.byAsset.has(anchor.asset) ? index.byAsset.get(anchor.asset) : anchor.key ? index.byKey.get(anchor.key) : undefined;
    if (target === undefined) return;
    element.scrollTop = Math.max(0, index.starts[target] + anchor.fraction * entries[target].size - anchor.viewportY);
    // Read by the measure above; scroll then follows.
  }, [entries]);

  const items = virtualizer.getVirtualItems();
  // The range changes when other entries come into view, or when entries
  // that need loading do, as when a refresh brings new months.
  const inView = items.map((item) => entries[item.index]).filter(Boolean);
  const rangeKey = inView.length ? `${inView[0].key}|${inView.at(-1)?.key}|${inView.filter((entry) => entry.kind === "placeholder" || entry.kind === "more").map((entry) => entry.key).join(",")}` : "";
  useEffect(() => {
    if (onRange && inView.length) onRange(inView);
    // The range key changes whenever the entries in view do.
  }, [rangeKey]);

  // Geometry in content coordinates, without the DOM, so hit tests also
  // find tiles that are not rendered.
  const contentPoint = useCallback((clientX: number, clientY: number) => {
    const element = scrollRef.current;
    if (!element) return { x: 0, y: 0 };
    const rect = element.getBoundingClientRect();
    return { x: clientX - rect.left - inset, y: clientY - rect.top + element.scrollTop };
  }, [scrollRef, inset]);
  const entryAt = useCallback((y: number) => {
    const { starts: positions } = index;
    let low = 0;
    let high = positions.length - 1;
    while (low <= high) {
      const middle = (low + high) >> 1;
      if (positions[middle] > y) high = middle - 1;
      else if (positions[middle] + entries[middle].size <= y) low = middle + 1;
      else return middle;
    }
    return -1;
  }, [index, entries]);
  const assetAt = useCallback((x: number, y: number) => {
    const position = entryAt(y);
    const entry = entries[position];
    if (!entry || entry.kind !== "row") return undefined;
    return entry.boxes.find((box) => x >= box.left - 2 && x <= box.left + box.width + 2)?.item.id ?? entry.boxes.at(x < 0 ? 0 : -1)?.item.id;
  }, [entryAt, entries]);

  // Ctrl+wheel and trackpad pinches zoom the grid around the pointer.
  useEffect(() => {
    const element = scrollRef.current;
    if (!element || !onZoom) return;
    const wheel = (event: WheelEvent) => {
      if (!event.ctrlKey && !event.metaKey) return;
      event.preventDefault();
      const point = contentPoint(event.clientX, event.clientY);
      const position = entryAt(point.y);
      if (position >= 0) {
        const entry = entries[position];
        pendingAnchor.current = {
          asset: assetsOf(entry)[0], key: entry.key,
          fraction: (point.y - index.starts[position]) / entry.size,
          viewportY: point.y - element.scrollTop,
        };
      }
      onZoom(Math.exp(-event.deltaY * 0.0018));
    };
    element.addEventListener("wheel", wheel, { passive: false });
    return () => element.removeEventListener("wheel", wheel);
  }, [scrollRef, onZoom, contentPoint, entryAt, entries, index]);

  // Marquee selection from empty space; the view scrolls near its edges.
  const [marquee, setMarquee] = useState<{ x: number; y: number; width: number; height: number }>();
  const drag = useRef<{ startX: number; startY: number; clientX: number; clientY: number; additive: boolean; moved: boolean; frame?: number }>(undefined);
  const hitTest = useCallback((x1: number, y1: number, x2: number, y2: number) => {
    const ids: string[] = [];
    const top = Math.min(y1, y2);
    const bottom = Math.max(y1, y2);
    const left = Math.min(x1, x2);
    const right = Math.max(x1, x2);
    let position = Math.max(0, entryAt(top));
    for (; position < entries.length && index.starts[position] <= bottom; position++) {
      const entry = entries[position];
      if (entry.kind !== "row") continue;
      for (const box of entry.boxes) if (box.left < right && box.left + box.width > left) ids.push(box.item.id);
    }
    return ids;
  }, [entries, entryAt, index]);
  const updateMarquee = useCallback(() => {
    const state = drag.current;
    const element = scrollRef.current;
    if (!state || !element) return;
    const point = contentPoint(state.clientX, state.clientY);
    setMarquee({ x: Math.min(state.startX, point.x), y: Math.min(state.startY, point.y), width: Math.abs(point.x - state.startX), height: Math.abs(point.y - state.startY) });
    onMarquee?.(hitTest(state.startX, state.startY, point.x, point.y), state.additive);
  }, [contentPoint, hitTest, onMarquee, scrollRef]);
  const autoScroll = useCallback(() => {
    const state = drag.current;
    const element = scrollRef.current;
    if (!state || !element) return;
    const rect = element.getBoundingClientRect();
    const edge = 48;
    const speed = state.clientY < rect.top + topInset + edge ? -(rect.top + topInset + edge - state.clientY) : state.clientY > rect.bottom - edge ? state.clientY - (rect.bottom - edge) : 0;
    if (speed) { element.scrollTop += speed / 3; updateMarquee(); }
    state.frame = requestAnimationFrame(autoScroll);
  }, [scrollRef, topInset, updateMarquee]);
  const onPointerDown = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (event.button !== 0 || !onMarquee || (event.target as HTMLElement).closest("button, a, input, .ph-tile, .ph-scrubber, [data-no-marquee]")) return;
    if (event.clientX > event.currentTarget.getBoundingClientRect().right - 14) return; // the scrollbar
    const point = contentPoint(event.clientX, event.clientY);
    drag.current = { startX: point.x, startY: point.y, clientX: event.clientX, clientY: event.clientY, additive: event.shiftKey || event.ctrlKey || event.metaKey, moved: false };
    event.currentTarget.setPointerCapture?.(event.pointerId);
  };
  const onPointerMove = (event: ReactPointerEvent<HTMLDivElement>) => {
    const state = drag.current;
    if (!state) return;
    state.clientX = event.clientX;
    state.clientY = event.clientY;
    const point = contentPoint(event.clientX, event.clientY);
    if (!state.moved && Math.hypot(point.x - state.startX, point.y - state.startY) < 5) return;
    if (!state.moved) { state.moved = true; state.frame = requestAnimationFrame(autoScroll); }
    updateMarquee();
  };
  const onPointerUp = () => {
    const state = drag.current;
    drag.current = undefined;
    if (state?.frame) cancelAnimationFrame(state.frame);
    setMarquee(undefined);
    if (state?.moved) onMarqueeEnd?.();
    else if (state) onBackgroundClick?.();
  };

  // Swiping from a tile's check across others selects the range.
  const swallowClick = useRef(false);
  useImperativeHandle(handle, () => ({
    beginSwipe: (assetId, event, onSwipe) => {
      const element = scrollRef.current;
      if (!element || event.button !== 0) return;
      let last = assetId;
      let swiping = false;
      const move = (moveEvent: PointerEvent) => {
        const point = contentPoint(moveEvent.clientX, moveEvent.clientY);
        const over = assetAt(point.x, point.y);
        if (!over || over === last) return;
        swiping = true;
        last = over;
        onSwipe(over);
      };
      const up = () => {
        window.removeEventListener("pointermove", move);
        window.removeEventListener("pointerup", up);
        // The click that ends a swipe, if any, follows at once.
        if (swiping) { swallowClick.current = true; window.setTimeout(() => { swallowClick.current = false; }); }
      };
      window.addEventListener("pointermove", move);
      window.addEventListener("pointerup", up);
    },
    scrollToKey: (key) => {
      const position = index.byKey.get(key);
      if (position !== undefined) virtualizer.scrollToIndex(position, { align: "start" });
    },
    revealAsset: async (assetId) => {
      const element = scrollRef.current;
      const position = index.byAsset.get(assetId);
      if (!element || position === undefined) return undefined;
      const start = index.starts[position];
      const size = entries[position].size;
      if (start < element.scrollTop + topInset || start + size > element.scrollTop + element.clientHeight) {
        element.scrollTop = Math.max(0, start - topInset - (element.clientHeight - topInset - size) / 2);
        await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
      }
      return element.querySelector(`[data-asset-id="${CSS.escape(assetId)}"] img`)?.getBoundingClientRect();
    },
  }), [scrollRef, contentPoint, assetAt, index, virtualizer, entries, topInset]);

  const offset = virtualizer.scrollOffset ?? 0;
  const viewport = virtualizer.scrollRect?.height ?? 0;
  // The floating date names the day at the top of the view, unless a day's
  // own header is there or about to arrive.
  const topEntry = floatingLabel && offset > 8 ? entryAt(offset + topInset + 1) : -1;
  let nextHeader = -1;
  for (let position = topEntry + 1; topEntry >= 0 && position < entries.length && nextHeader < 0; position++) {
    if (entries[position].kind === "day" || entries[position].kind === "month") nextHeader = position;
  }
  const labelEntry = topEntry >= 0 && entries[topEntry].kind !== "day" && entries[topEntry].kind !== "month"
    && (nextHeader < 0 || index.starts[nextHeader] > offset + topInset + 46) ? entries[topEntry] : undefined;

  return (
    <div className="ph-scroll-frame">
      <div
        ref={scrollRef}
        className="ph-scroll"
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
        onPointerCancel={onPointerUp}
        onClickCapture={(event) => { if (swallowClick.current) { swallowClick.current = false; event.stopPropagation(); event.preventDefault(); } }}
      >
        <div className="ph-scroll-content" style={{ height: virtualizer.getTotalSize() }}>
          {items.map((item) => {
            const entry = entries[item.index];
            if (!entry) return null;
            return (
              <div key={item.key} className={`ph-entry ph-entry-${entry.kind}`} style={{ transform: `translateY(${item.start}px)`, height: entry.size, left: inset, right: insetRight }}>
                {entry.kind === "row" ? entry.boxes.map(renderTile) : entry.kind === "pending" ? entry.boxes.map((box) => renderPending?.(box)) : renderEntry(entry)}
              </div>
            );
          })}
          {marquee && <div className="ph-marquee" style={{ left: marquee.x + inset, top: marquee.y, width: marquee.width, height: marquee.height }} />}
        </div>
        {children}
      </div>
      {labelEntry?.label && <div className="ph-floating-date" style={{ top: topInset + 10 }} aria-hidden="true">{labelEntry.label}</div>}
      {scrubber && <Scrubber entries={entries} starts={index.starts} total={virtualizer.getTotalSize()} offset={offset} viewport={viewport} topInset={topInset} onJump={(key) => { const position = index.byKey.get(key); if (position !== undefined) virtualizer.scrollToIndex(position, { align: "start" }); }} />}
    </div>
  );
}

interface ScrubberProps { entries: Entry[]; starts: number[]; total: number; offset: number; viewport: number; topInset: number; onJump: (key: string) => void }

// Scrubber maps the timeline's height onto the window's: years are marked
// where their months begin; pointing shows the month, pressing or dragging
// jumps to it.
function Scrubber({ entries, starts: positions, total, offset, viewport, topInset, onJump }: ScrubberProps) {
  const rail = useRef<HTMLDivElement>(null);
  const [hover, setHover] = useState<{ y: number; month: string }>();
  const [dragging, setDragging] = useState(false);
  const months = useMemo(() => entries.flatMap((entry, position) => entry.kind === "month" ? [{ month: entry.month, key: entry.key, start: positions[position] }] : []), [entries, positions]);
  // The rail runs from below the toolbar to the bottom, 8px in from each.
  const height = Math.max(0, viewport - topInset - 16);
  const scale = total > 0 ? height / total : 0;
  const years = useMemo(() => {
    const marks: Array<{ year: string; y: number }> = [];
    for (const month of months) {
      const year = month.month.slice(0, 4);
      const y = month.start * scale;
      if (marks.at(-1)?.year === year) continue;
      if (marks.length && y - (marks.at(-1)?.y ?? 0) < 18) continue;
      marks.push({ year, y });
    }
    return marks;
  }, [months, scale]);
  if (months.length < 2 || total <= viewport) return null;
  const monthAt = (clientY: number) => {
    const rect = rail.current?.getBoundingClientRect();
    if (!rect) return undefined;
    const y = Math.min(Math.max(0, clientY - rect.top), rect.height);
    const target = y / (scale || 1);
    let found = months[0];
    for (const month of months) { if (month.start - topInset <= target) found = month; else break; }
    return { y, month: found };
  };
  const follow = (clientY: number, jump: boolean) => {
    const at = monthAt(clientY);
    if (!at) return;
    setHover({ y: at.y, month: at.month.month });
    if (jump) onJump(at.month.key);
  };
  return (
    <div
      ref={rail}
      className={`ph-scrubber${dragging ? " dragging" : ""}`}
      style={{ top: topInset + 8 }}
      role="slider"
      aria-label="按月份跳转"
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={Math.round((offset / Math.max(1, total - viewport)) * 100)}
      aria-valuetext={hover ? fullMonthLabel(hover.month) : undefined}
      onPointerMove={(event) => follow(event.clientY, dragging)}
      onPointerLeave={() => { if (!dragging) setHover(undefined); }}
      onPointerDown={(event) => { event.currentTarget.setPointerCapture?.(event.pointerId); setDragging(true); follow(event.clientY, true); }}
      onPointerUp={() => { setDragging(false); setHover(undefined); }}
    >
      {years.map((mark) => <span key={mark.year} className="ph-scrubber-year" style={{ top: mark.y }}>{mark.year}</span>)}
      {months.map((month) => <i key={month.key} className="ph-scrubber-tick" style={{ top: month.start * scale }} />)}
      <span className="ph-scrubber-thumb" style={{ top: Math.min(height - 2, offset * scale) }} />
      {hover && <span className="ph-scrubber-bubble" style={{ top: hover.y }}>{fullMonthLabel(hover.month)}</span>}
    </div>
  );
}
