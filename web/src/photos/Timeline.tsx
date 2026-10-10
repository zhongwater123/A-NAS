import { RotateCcw } from "lucide-react";
import { ReactNode, useCallback, useEffect, useMemo, useRef, useState } from "react";

import { DayHeader, gridInset, type GridProps, scrubberInset, usePendingThumbnails, useTileRenderer } from "./grid";
import { type Entry, type MonthState, type PendingPhoto, timelineEntries } from "./layout";
import { messageOf } from "./model";
import { listTimelineMonth, listTimelineMonths, type PhotoAsset, type PhotoMonth } from "./photosApi";
import { PhotoScroller, useContentWidth } from "./PhotoScroller";
import { PendingTile } from "./Tile";

interface Props extends GridProps {
  libraryId: string;
  pending: PendingPhoto[];
  onRetryUpload: (key: string) => void;
  // onReloaded runs after a refresh has loaded the photos again.
  onReloaded: () => void;
  onCount: (photos: number | undefined) => void;
  onError: (message: string) => void;
  empty: ReactNode;
}

async function loadMonth(libraryId: string, month: string) {
  const assets: PhotoAsset[] = [];
  let cursor = "";
  do {
    const page = await listTimelineMonth(libraryId, month, cursor);
    assets.push(...page.items);
    cursor = page.next ?? "";
  } while (cursor);
  return assets;
}

// Timeline shows a library newest first. It knows every month's size up
// front and loads a month once it scrolls near, so the scrubber can jump
// anywhere in a large library.
export function Timeline(props: Props) {
  const { libraryId, pending, refreshKey, hidden, rowHeight, onAssets, onCount, onReloaded, onError } = props;
  const scrollRef = useRef<HTMLDivElement>(null);
  const width = useContentWidth(scrollRef, gridInset, scrubberInset);
  const [months, setMonths] = useState<PhotoMonth[]>();
  const [loaded, setLoaded] = useState<Map<string, MonthState>>(new Map());
  const loading = useRef(new Set<string>());
  const generation = useRef(0);
  const loadedRef = useRef(loaded);
  loadedRef.current = loaded;

  // A new library starts over; a refresh reloads the months already shown
  // and swaps them in at once, so nothing flickers.
  const libraryRef = useRef(libraryId);
  useEffect(() => {
    const fresh = libraryRef.current !== libraryId;
    libraryRef.current = libraryId;
    const current = ++generation.current;
    loading.current.clear();
    if (fresh) { setMonths(undefined); setLoaded(new Map()); }
    void (async () => {
      try {
        const nextMonths = await listTimelineMonths(libraryId);
        const keep = fresh ? [] : [...loadedRef.current.entries()].filter(([month, state]) => state.assets && nextMonths.some((item) => item.month === month)).map(([month]) => month);
        const reloaded = await Promise.all(keep.map(async (month) => [month, { assets: await loadMonth(libraryId, month) }] as const));
        if (current !== generation.current) return;
        setMonths(nextMonths);
        setLoaded(new Map(reloaded));
        onReloaded();
      } catch (caught) {
        if (current !== generation.current) return;
        setMonths((value) => value ?? []);
        onError(messageOf(caught));
      }
    })();
  }, [libraryId, refreshKey]);

  const ensure = useCallback((month: string) => {
    if (loading.current.has(month) || loadedRef.current.get(month)?.assets) return;
    loading.current.add(month);
    const current = generation.current;
    loadMonth(libraryId, month).then(
      (assets) => { if (current === generation.current) setLoaded((value) => new Map(value).set(month, { assets })); },
      () => { if (current === generation.current) setLoaded((value) => new Map(value).set(month, { failed: true })); },
    ).finally(() => loading.current.delete(month));
  }, [libraryId]);

  const visible = useMemo(() => {
    if (!hidden.size) return loaded;
    const filtered = new Map<string, MonthState>();
    for (const [month, state] of loaded) filtered.set(month, state.assets ? { assets: state.assets.filter((asset) => !hidden.has(asset.id)) } : state);
    return filtered;
  }, [loaded, hidden]);
  const entries = useMemo(() => timelineEntries(months ?? [], visible, pending, width, rowHeight), [months, visible, pending, width, rowHeight]);
  const ordered = useMemo(() => (months ?? []).flatMap((month) => visible.get(month.month)?.assets ?? []), [months, visible]);
  usePendingThumbnails(ordered, (ready) => setLoaded((value) => new Map([...value].map(([month, state]) => [month, state.assets ? { assets: state.assets.map((asset) => ready.get(asset.id) ?? asset) } : state]))));
  useEffect(() => onAssets(ordered), [ordered]);
  const total = months?.reduce((sum, month) => sum + (visible.get(month.month)?.assets?.length ?? month.photos), 0);
  useEffect(() => onCount(total), [total]);

  const renderTile = useTileRenderer(props);
  const renderEntry = (entry: Entry): ReactNode => {
    switch (entry.kind) {
      case "month":
        return <div className="ph-month"><h2>{entry.label}</h2><small>{entry.photos} 张</small></div>;
      case "day":
        return <DayHeader entry={entry} selected={props.selected} onSelect={props.onSelectGroup} />;
      case "heading":
        return <div className="ph-heading"><h2>{entry.label}</h2>{entry.detail && <small>{entry.detail}</small>}</div>;
      case "placeholder":
        return entry.failed
          ? <div className="ph-skeleton failed"><span>这个月的照片没能载入</span><button type="button" onClick={() => { setLoaded((value) => { const next = new Map(value); next.delete(entry.month); return next; }); ensure(entry.month); }}><RotateCcw />重试</button></div>
          : <div className="ph-skeleton" style={{ ["--row" as string]: `${rowHeight + 4}px` }} aria-label={`正在载入${entry.label}`} />;
      default:
        return null;
    }
  };

  return (
    <PhotoScroller
      entries={entries}
      scrollRef={scrollRef}
      topInset={props.topInset}
      inset={gridInset}
      insetRight={scrubberInset}
      renderTile={renderTile}
      renderPending={(box) => <PendingTile key={box.item.key} box={box} onRetry={props.onRetryUpload} />}
      renderEntry={renderEntry}
      onRange={(inView) => { for (const entry of inView) if (entry.kind === "placeholder" && !entry.failed) ensure(entry.month); }}
      onZoom={props.onZoom}
      onMarquee={props.onMarquee}
      onMarqueeEnd={props.onGestureEnd}
      onBackgroundClick={props.onBackgroundClick}
      floatingLabel
      scrubber
      handle={props.scroller}
    >
      {months && !entries.length && props.empty}
    </PhotoScroller>
  );
}
