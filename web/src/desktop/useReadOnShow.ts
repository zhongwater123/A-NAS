import { useEffect, useState } from "react";

// useReadOnShow reads a value when the account signs in and again whenever
// the page comes back into view, so an app an administrator just opened to a
// member appears without a reload. A failed read keeps what was known; a read
// that says the same as before keeps the old value, so nothing re-renders.
// read and same must be stable: module functions, not inline ones.
export function useReadOnShow<T>(
  read: (signal: AbortSignal) => Promise<T>,
  initial: T,
  userId: string,
  same: (current: T, next: T) => boolean = Object.is,
): T {
  const [value, setValue] = useState(initial);
  useEffect(() => {
    let controller = new AbortController();
    const load = () => {
      controller.abort();
      controller = new AbortController();
      const { signal } = controller;
      read(signal)
        .then((next) => {
          if (!signal.aborted) setValue((current) => (same(current, next) ? current : next));
        })
        .catch(() => undefined);
    };
    load();
    const visible = () => {
      if (!document.hidden) load();
    };
    document.addEventListener("visibilitychange", visible);
    return () => {
      controller.abort();
      document.removeEventListener("visibilitychange", visible);
    };
  }, [read, same, userId]);
  return value;
}
