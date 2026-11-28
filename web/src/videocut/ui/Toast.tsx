// Toasts at the bottom middle (PRD A.3): 1.8 seconds, or 6 with an action,
// as the export's failure with 重试 (M9-4). Radix's Toast keeps them
// announced and keyboard reachable.
import { Toast } from "radix-ui";
import { createContext, type ReactNode, useCallback, useContext, useState } from "react";

import styles from "./venus.module.css";

interface Shown {
  id: number;
  text: string;
  action?: { label: string; run: () => void };
}

type Show = (text: string, action?: Shown["action"]) => void;
const ToastContext = createContext<Show>(() => undefined);
export const useToast = () => useContext(ToastContext);

let next = 0;

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Shown[]>([]);
  const show = useCallback<Show>((text, action) => {
    next += 1;
    setToasts((all) => [...all, { id: next, text, action }]);
  }, []);
  const drop = (id: number) => setToasts((all) => all.filter((toast) => toast.id !== id));
  return (
    <ToastContext.Provider value={show}>
      <Toast.Provider swipeDirection="down">
        {children}
        {toasts.map((toast) => (
          <Toast.Root
            key={toast.id}
            className={styles.toast}
            duration={toast.action ? 6000 : 1800}
            onOpenChange={(open) => {
              if (!open) drop(toast.id);
            }}
          >
            <Toast.Description>{toast.text}</Toast.Description>
            {toast.action && (
              <Toast.Action altText={toast.action.label} onClick={toast.action.run} className={styles.toastAction}>
                {toast.action.label}
              </Toast.Action>
            )}
          </Toast.Root>
        ))}
        <Toast.Viewport className={styles.toasts} />
      </Toast.Provider>
    </ToastContext.Provider>
  );
}
