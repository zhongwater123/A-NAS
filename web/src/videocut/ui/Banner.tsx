// Banner is the one channel above the content (PRD A.2): it overlays the
// page and never pushes the layout. Exporting (M9-2), a blocked download, a
// lost network, an interrupted upload and queueing all use it, in any state.
import { createContext, type ReactNode, useCallback, useContext, useMemo, useState } from "react";

import styles from "./venus.module.css";

export interface BannerItem {
  key: string;
  text: string;
  action?: { label: string; run: () => void };
}

interface Banners {
  show: (item: BannerItem) => void;
  hide: (key: string) => void;
}

const BannerContext = createContext<Banners>({ show: () => undefined, hide: () => undefined });
export const useBanner = () => useContext(BannerContext);

export function BannerProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<BannerItem[]>([]);
  const show = useCallback((item: BannerItem) => {
    setItems((all) => [...all.filter((other) => other.key !== item.key), item]);
  }, []);
  // Hiding what is not shown leaves the list as it is, so nothing redraws.
  const hide = useCallback(
    (key: string) =>
      setItems((all) => (all.some((item) => item.key === key) ? all.filter((item) => item.key !== key) : all)),
    [],
  );
  const value = useMemo(() => ({ show, hide }), [show, hide]);
  return (
    <BannerContext.Provider value={value}>
      {children}
      {items.length > 0 && (
        <div className={styles.banners} role="status">
          {items.map((item) => (
            <div key={item.key} className={styles.banner}>
              <span>{item.text}</span>
              {item.action && (
                <button type="button" onClick={item.action.run}>
                  {item.action.label}
                </button>
              )}
            </div>
          ))}
        </div>
      )}
    </BannerContext.Provider>
  );
}
