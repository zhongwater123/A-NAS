import { ChevronsDown } from "lucide-react";
import { ReactNode, useCallback, useEffect, useEffectEvent, useMemo, useRef, useState } from "react";

import { type ErrorDomain, errorText } from "../api/errorMessages";
import type { PhotoAsset, PhotoPage } from "./api";
import { type GridProps, gridInset, usePendingThumbnails, useTileRenderer } from "./grid";
import { type Entry, flatEntries, type Split } from "./layout";
import { PhotoScroller, useContentWidth } from "./PhotoScroller";

interface Props extends GridProps {
  // listKey names the list; a new key starts it over.
  listKey: string;
  load: (cursor: string) => Promise<PhotoPage>;
  onPage?: (page: PhotoPage, first: boolean) => void;
  onCount?: (photos: number | undefined) => void;
  onError: (message: string) => void;
  errorDomain?: ErrorDomain;
  badge?: (asset: PhotoAsset) => ReactNode;
  empty: ReactNode;
  // loading replaces the spinner shown until the first page arrives.
  loading?: ReactNode;
  end?: string;
  // split ends the first group after that many of the loaded photos.
  split?: Split;
}

// AssetGrid shows a list without dates, such as an album, search results or
// the trash, loading the next page as its end scrolls into view.
export function AssetGrid(props: Props) {
  const {
    listKey,
    load,
    refreshKey,
    hidden,
    rowHeight,
    onAssets,
    onCount,
    onPage,
    onError,
    errorDomain = "photos",
  } = props;
  const scrollRef = useRef<HTMLDivElement>(null);
  const width = useContentWidth(scrollRef, gridInset);
  const [assets, setAssets] = useState<PhotoAsset[]>();
  const [next, setNext] = useState<string>();
  const generation = useRef(0);
  const busy = useRef(false);

  const fetchPage = useCallback(
    (cursor: string, current: number) => {
      busy.current = true;
      // A load that throws at once fails like one that rejects.
      return new Promise<PhotoPage>((resolve) => resolve(load(cursor)))
        .then((page) => {
          if (current !== generation.current) return;
          setAssets((value) => (cursor && value ? [...value, ...page.items] : page.items));
          setNext(page.next);
          onPage?.(page, !cursor);
        })
        .catch((caught: unknown) => {
          if (current !== generation.current) return;
          setAssets((value) => value ?? []);
          setNext(undefined);
          onError(errorText(caught, errorDomain));
        })
        .finally(() => {
          if (current === generation.current) busy.current = false;
        });
    },
    [load, onPage, onError, errorDomain],
  );

  const keyRef = useRef(listKey);
  const startOver = useEffectEvent(() => {
    const fresh = keyRef.current !== listKey;
    keyRef.current = listKey;
    const current = ++generation.current;
    if (fresh) {
      setAssets(undefined);
      setNext(undefined);
    }
    void fetchPage("", current);
  });
  useEffect(() => startOver(), [listKey, refreshKey]);

  const shown = useMemo(() => assets?.filter((asset) => !hidden.has(asset.id)), [assets, hidden]);
  usePendingThumbnails(shown, (ready) => setAssets((value) => value?.map((asset) => ready.get(asset.id) ?? asset)));
  const { split } = props;
  const splitAt = split?.at;
  const splitTitle = split?.title ?? "";
  const splitText = split?.text ?? "";
  // Hidden photos leave the first group smaller.
  const shownSplit = useMemo(
    () =>
      splitAt !== undefined && assets
        ? {
            title: splitTitle,
            text: splitText,
            at: assets.slice(0, splitAt).filter((asset) => !hidden.has(asset.id)).length,
          }
        : undefined,
    [splitAt, splitTitle, splitText, assets, hidden],
  );
  const entries = useMemo(
    () => flatEntries(shown ?? [], width, rowHeight, Boolean(next), props.end, shownSplit),
    [shown, width, rowHeight, next, props.end, shownSplit],
  );
  const reportAssets = useEffectEvent(onAssets);
  const reportCount = useEffectEvent((photos: number | undefined) => onCount?.(photos));
  useEffect(() => reportAssets(shown ?? []), [shown]);
  useEffect(() => reportCount(shown && !next ? shown.length : undefined), [shown, next]);

  const renderTile = useTileRenderer(props, props.badge);
  const renderEntry = (entry: Entry): ReactNode =>
    entry.kind === "more" ? (
      <div className="ph-more">
        <span className="ph-spinner" />
        正在载入更多
      </div>
    ) : entry.kind === "end" ? (
      <div className="ph-end">{entry.text}</div>
    ) : entry.kind === "divider" ? (
      <div className="ph-divider" role="separator" aria-label={entry.title}>
        <span className="ph-divider-title">
          <ChevronsDown />
          {entry.title}
        </span>
        <small>{entry.text}</small>
      </div>
    ) : null;

  return (
    <PhotoScroller
      entries={entries}
      scrollRef={scrollRef}
      topInset={props.topInset}
      inset={gridInset}
      renderTile={renderTile}
      renderEntry={renderEntry}
      onRange={(inView) => {
        if (next && !busy.current && inView.some((entry) => entry.kind === "more"))
          void fetchPage(next, generation.current);
      }}
      onZoom={props.onZoom}
      onMarquee={props.onMarquee}
      onMarqueeEnd={props.onGestureEnd}
      onBackgroundClick={props.onBackgroundClick}
      floatingLabel={Boolean(split)}
      handle={props.scroller}
    >
      {assets === undefined
        ? (props.loading ?? (
            <div className="ph-loading" style={{ paddingTop: props.topInset }}>
              <span className="ph-spinner" />
            </div>
          ))
        : !shown?.length && (
            // A taller toolbar, such as one with a search box under it, moves the empty state down with it.
            <div className="ph-empty-place" style={{ top: Math.max(0, props.topInset - 72) }}>
              {props.empty}
            </div>
          )}
    </PhotoScroller>
  );
}
