import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { HostState, readHostState } from "./api";

export interface HostStateView {
  snapshot?: HostState;
  // When this page got the snapshot (Date.now()), for what goes by the time
  // between reads: the companion's dot (companion/health.ts).
  readAt?: number;
  loading: boolean;
  refreshing: boolean;
  disconnected: boolean;
  refresh: () => Promise<void>;
}

export function useHostState(enabled = true): HostStateView {
  const [snapshot, setSnapshot] = useState<HostState>();
  const [readAt, setReadAt] = useState<number>();
  const snapshotRef = useRef<HostState | undefined>(undefined);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [disconnected, setDisconnected] = useState(false);
  const activeController = useRef<AbortController | undefined>(undefined);

  const refresh = useCallback(async () => {
    if (!enabled) return;
    activeController.current?.abort();
    const controller = new AbortController();
    activeController.current = controller;
    if (snapshotRef.current) setRefreshing(true);
    else setLoading(true);
    return readHostState(controller.signal)
      .then((state) => {
        snapshotRef.current = state;
        setSnapshot(state);
        setReadAt(Date.now());
        setDisconnected(false);
      })
      .catch((error: unknown) => {
        if (!(error instanceof DOMException && error.name === "AbortError")) {
          setDisconnected(true);
        }
      })
      .finally(() => {
        if (activeController.current === controller) {
          setLoading(false);
          setRefreshing(false);
        }
      });
  }, [enabled]);

  useEffect(() => {
    if (!enabled) return;
    void refresh();
    const interval = window.setInterval(() => {
      if (!document.hidden) void refresh();
    }, 10_000);
    const resume = () => {
      if (!document.hidden) void refresh();
    };
    document.addEventListener("visibilitychange", resume);
    return () => {
      window.clearInterval(interval);
      document.removeEventListener("visibilitychange", resume);
      activeController.current?.abort();
    };
  }, [enabled, refresh]);

  // The desktop hands this to every window; a new object only when something changed.
  return useMemo(
    () => ({ snapshot, readAt, loading, refreshing, disconnected, refresh }),
    [snapshot, readAt, loading, refreshing, disconnected, refresh],
  );
}
