import { useCallback, useRef, useState } from "react";

import type { PhotoAsset } from "./photosApi";

const between = (order: PhotoAsset[], from: string, to: string) => {
  const start = order.findIndex((asset) => asset.id === from);
  const end = order.findIndex((asset) => asset.id === to);
  if (start < 0 || end < 0) return [to];
  return order.slice(Math.min(start, end), Math.max(start, end) + 1).map((asset) => asset.id);
};

// useSelection keeps the selected photo IDs of the current view. order is
// the view's loaded photos in display order, for ranges.
export function useSelection(order: PhotoAsset[]) {
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set());
  const anchor = useRef<string>(undefined);
  // What was selected when a marquee or swipe began, which it adds to.
  const base = useRef<ReadonlySet<string>>(undefined);
  const orderRef = useRef(order);
  orderRef.current = order;
  const selectedRef = useRef(selected);
  selectedRef.current = selected;

  const toggle = useCallback((asset: PhotoAsset, range: boolean) => {
    const from = anchor.current;
    setSelected((current) => {
      const next = new Set(current);
      if (range && from) {
        for (const id of between(orderRef.current, from, asset.id)) next.add(id);
      } else if (next.has(asset.id)) next.delete(asset.id);
      else next.add(asset.id);
      return next;
    });
    anchor.current = asset.id;
  }, []);

  // setGroup selects a group such as a day, or clears it when all of it is
  // selected already.
  const setGroup = useCallback((ids: string[]) => {
    setSelected((current) => {
      const next = new Set(current);
      const all = ids.every((id) => next.has(id));
      for (const id of ids) if (all) next.delete(id); else next.add(id);
      return next;
    });
  }, []);

  const marquee = useCallback((ids: string[], additive: boolean) => {
    base.current ??= additive ? selectedRef.current : new Set();
    const from = base.current;
    setSelected(new Set([...from, ...ids]));
  }, []);

  // swipe applies the state the first photo took to every photo from it to
  // the one under the pointer.
  const beginSwipe = useCallback((asset: PhotoAsset) => {
    const snapshot = selectedRef.current;
    const target = !snapshot.has(asset.id);
    return (to: string) => setSelected(() => {
      const next = new Set(snapshot);
      for (const id of between(orderRef.current, asset.id, to)) if (target) next.add(id); else next.delete(id);
      anchor.current = to;
      return next;
    });
  }, []);

  const endGesture = useCallback(() => { base.current = undefined; }, []);
  const clear = useCallback(() => { setSelected(new Set()); anchor.current = undefined; }, []);
  const replace = useCallback((ids: Iterable<string>) => setSelected(new Set(ids)), []);
  return { selected, toggle, setGroup, marquee, beginSwipe, endGesture, clear, replace };
}
