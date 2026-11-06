import { useMemo, useSyncExternalStore } from "react";

import { deviceStorage } from "./deviceStorage";
import { type Preferences, preferenceKey, readPreferences, writePreferences } from "./preferences";

// The panel and floating pet in one page share a store. Separate clients
// remain independent until the NAS activity/handoff API is implemented.
function createStore(owner: string) {
  let value = readPreferences(owner, deviceStorage);
  const listeners = new Set<() => void>();
  const notify = () => {
    for (const listener of listeners) listener();
  };
  const refresh = (event: StorageEvent) => {
    if (event.key !== null && !event.key.startsWith(preferenceKey(owner))) return;
    value = readPreferences(owner, deviceStorage);
    notify();
  };
  return {
    get: () => value,
    subscribe: (listener: () => void) => {
      if (listeners.size === 0) window.addEventListener("storage", refresh);
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
        if (listeners.size === 0) window.removeEventListener("storage", refresh);
      };
    },
    update: (change: Partial<Preferences>) => {
      value = { ...value, ...change };
      writePreferences(owner, value, deviceStorage);
      notify();
    },
  };
}

type Store = ReturnType<typeof createStore>;
const stores = new Map<string, Store>();

export function useCompanion(owner: string) {
  const store = useMemo(() => {
    let found = stores.get(owner);
    if (!found) {
      found = createStore(owner);
      stores.set(owner, found);
    }
    return found;
  }, [owner]);
  const value = useSyncExternalStore(store.subscribe, store.get);
  return [value, store.update] as const;
}
