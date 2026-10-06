import { useCallback, useEffect, useRef, useState } from "react";

import { HostState, readHostState } from "./api";

export interface HostStateView {
  snapshot?: HostState;
  loading: boolean;
  refreshing: boolean;
  disconnected: boolean;
  refresh: () => Promise<void>;
}

export function useHostState(): HostStateView {
  const [snapshot, setSnapshot] = useState<HostState>();
  const snapshotRef = useRef<HostState | undefined>(undefined);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [disconnected, setDisconnected] = useState(false);
  const activeController = useRef<AbortController | undefined>(undefined);

  const refresh = useCallback(async () => {
    activeController.current?.abort();
    const controller = new AbortController();
    activeController.current = controller;
    if (snapshotRef.current) setRefreshing(true);
    else setLoading(true);
    try {
      const state = await readHostState(controller.signal);
      snapshotRef.current = state;
      setSnapshot(state);
      setDisconnected(false);
    } catch (error) {
      if (!(error instanceof DOMException && error.name === "AbortError")) {
        setDisconnected(true);
      }
    } finally {
      if (activeController.current === controller) {
        setLoading(false);
        setRefreshing(false);
      }
    }
  }, []);

  useEffect(() => {
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
  }, [refresh]);

  return { snapshot, loading, refreshing, disconnected, refresh };
}
