import { useEffect, useState } from "react";

import { HostMetrics, readHostMetrics } from "./api";

export const metricsPollInterval = 2_000;

export interface HostMetricsView {
  metrics?: HostMetrics;
  disconnected: boolean;
}

// Polls utilisation while the page is visible; the last successful reading is
// kept and marked stale when a poll fails.
export function useHostMetrics(): HostMetricsView {
  const [view, setView] = useState<HostMetricsView>({ disconnected: false });

  useEffect(() => {
    let controller: AbortController | undefined;
    const poll = async () => {
      if (document.hidden) return;
      controller?.abort();
      const current = new AbortController();
      controller = current;
      try {
        const metrics = await readHostMetrics(current.signal);
        setView({ metrics, disconnected: false });
      } catch (error) {
        if (!(error instanceof DOMException && error.name === "AbortError")) {
          setView((previous) => ({ ...previous, disconnected: true }));
        }
      }
    };
    void poll();
    const interval = window.setInterval(() => void poll(), metricsPollInterval);
    const resume = () => void poll();
    document.addEventListener("visibilitychange", resume);
    return () => {
      window.clearInterval(interval);
      document.removeEventListener("visibilitychange", resume);
      controller?.abort();
    };
  }, []);

  return view;
}
