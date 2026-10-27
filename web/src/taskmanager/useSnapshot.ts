import { errorText } from "../api/errorMessages";
import { useEffect, useRef, useState } from "react";

export interface SnapshotView<T> {
  data?: T;
  history: T[];
  error: string;
  retry: () => void;
}

// A single in-flight request and a completion-scheduled timer avoid piling up
// requests on a slow host. Cancellation also invalidates late responses.
export function useSnapshot<T extends { observedAt: string }>(
  read: (signal: AbortSignal) => Promise<T>,
  historyLimit = 60,
): SnapshotView<T> {
  const [view, setView] = useState<Omit<SnapshotView<T>, "retry">>({ history: [], error: "" });
  const retry = useRef<() => void>(() => {});
  useEffect(() => {
    let disposed = false;
    let controller: AbortController | undefined;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const poll = async () => {
      if (disposed || document.hidden || controller) return;
      clearTimeout(timer);
      const current = new AbortController();
      controller = current;
      try {
        const data = await read(current.signal);
        if (!disposed && !current.signal.aborted)
          setView((previous) => ({
            data,
            error: "",
            history:
              previous.history.at(-1)?.observedAt === data.observedAt
                ? previous.history
                : [...previous.history, data].slice(-historyLimit),
          }));
      } catch (error) {
        if (!disposed && !current.signal.aborted) setView((previous) => ({ ...previous, error: errorText(error) }));
      } finally {
        if (controller === current) {
          controller = undefined;
          if (!disposed && !document.hidden) timer = setTimeout(() => void poll(), 2000);
        }
      }
    };
    const visibility = () => {
      clearTimeout(timer);
      if (document.hidden) {
        controller?.abort();
        controller = undefined;
      } else void poll();
    };
    retry.current = () => void poll();
    document.addEventListener("visibilitychange", visibility);
    void poll();
    return () => {
      disposed = true;
      clearTimeout(timer);
      controller?.abort();
      document.removeEventListener("visibilitychange", visibility);
      retry.current = () => {};
    };
  }, [read, historyLimit]);
  return { ...view, retry: () => retry.current() };
}
