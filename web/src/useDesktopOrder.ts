import { useCallback, useEffect, useState } from "react";

export const desktopOrderStorageKey = "a-nas.desktop-order.v1";

// Keeps the user's icon order per browser. Unknown IDs from older layouts are
// dropped and apps added since then are appended, so a stored order never
// hides or duplicates an app.
export function useDesktopOrder(defaultIDs: string[]) {
  const signature = defaultIDs.join("\n");
  const [order, setOrder] = useState(() => mergeOrder(readStoredOrder(), defaultIDs));

  useEffect(() => {
    setOrder((current) => mergeOrder(current, signature.split("\n")));
  }, [signature]);

  const save = useCallback((next: string[]) => {
    try {
      localStorage.setItem(desktopOrderStorageKey, JSON.stringify(next));
    } catch {
      // Storage can be unavailable (private mode, quota); the order still applies for this session.
    }
  }, []);

  return { order, setOrder, save };
}

export function mergeOrder(stored: readonly string[] | undefined, ids: readonly string[]): string[] {
  const known = new Set(ids);
  const kept = (stored ?? []).filter((id, index, all) => known.has(id) && all.indexOf(id) === index);
  const keptSet = new Set(kept);
  return [...kept, ...ids.filter((id) => !keptSet.has(id))];
}

export function moveItem(order: readonly string[], id: string, toIndex: number): string[] {
  const from = order.indexOf(id);
  const target = Math.max(0, Math.min(order.length - 1, toIndex));
  if (from < 0 || from === target) return [...order];
  const next = order.filter((item) => item !== id);
  next.splice(target, 0, id);
  return next;
}

function readStoredOrder(): string[] | undefined {
  try {
    const value: unknown = JSON.parse(localStorage.getItem(desktopOrderStorageKey) ?? "null");
    return Array.isArray(value) && value.every((item) => typeof item === "string") ? value : undefined;
  } catch {
    return undefined;
  }
}
