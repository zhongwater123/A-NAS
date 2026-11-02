import { useEffect, useMemo, useSyncExternalStore } from "react";

import type { CompanionClientKind } from "../api.generated";
import { CompanionSync, initialSync } from "./sync";

function newShared(kind: CompanionClientKind) {
  const api = async (signal?: AbortSignal) => {
    const module = await import("./api");
    signal?.throwIfAborted();
    return module.companionAPI;
  };
  const sync = new CompanionSync(crypto.randomUUID(), kind, {
    read: async (signal) => (await api(signal)).read(signal),
    claim: async (body, signal) => (await api(signal)).claim(body, signal),
    heartbeat: async (body, signal) => (await api(signal)).heartbeat(body, signal),
    release: async (body, signal) => (await api(signal)).release(body, signal),
    act: async (body, signal) => (await api(signal)).act(body, signal),
  });
  let users = 0;
  return {
    sync,
    subscribe: (listener: () => void) => {
      users++;
      const unsubscribe = sync.subscribe(listener);
      if (users === 1 && !document.hidden) sync.start();
      return () => {
        unsubscribe();
        users--;
        if (!users) sync.stop();
      };
    },
    visibility: () => {
      if (document.hidden) sync.stop();
      else if (users) sync.start();
    },
  };
}
const remotes = new Map<string, ReturnType<typeof newShared>>();
function shared(owner: string, kind: CompanionClientKind) {
  const key = `${owner}:${kind}`;
  let item = remotes.get(key);
  if (!item) {
    item = newShared(kind);
    remotes.set(key, item);
  }
  return item;
}
const noop = () => () => undefined;
const localSnapshot = () => initialSync;

// Offline lab never starts API requests or imports the product API client.
export function useRemote(owner: string, enabled: boolean, kind: CompanionClientKind = "web") {
  const item = useMemo(() => (enabled ? shared(owner, kind) : undefined), [owner, enabled, kind]);
  const snapshot = useSyncExternalStore(item?.subscribe ?? noop, item?.sync.get ?? localSnapshot);
  useEffect(() => {
    if (!item) return;
    document.addEventListener("visibilitychange", item.visibility);
    return () => document.removeEventListener("visibilitychange", item.visibility);
  }, [item]);
  return { ...snapshot, sync: item?.sync };
}
