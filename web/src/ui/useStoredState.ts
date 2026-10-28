import { useCallback, useState } from "react";

// useStoredState is useState kept in localStorage under key as JSON. Without
// storage, as in a private window, the value lasts for the page only.
export function useStoredState<T>(key: string, initial: T): [T, (value: T | ((current: T) => T)) => void] {
  const [value, setValue] = useState<T>(() => {
    try {
      const stored = window.localStorage.getItem(key);
      return stored === null ? initial : (JSON.parse(stored) as T);
    } catch {
      return initial;
    }
  });
  const set = useCallback(
    (next: T | ((current: T) => T)) =>
      setValue((current) => {
        const resolved = typeof next === "function" ? (next as (current: T) => T)(current) : next;
        try {
          window.localStorage.setItem(key, JSON.stringify(resolved));
        } catch {
          /* storage is optional */
        }
        return resolved;
      }),
    [key],
  );
  return [value, set];
}
