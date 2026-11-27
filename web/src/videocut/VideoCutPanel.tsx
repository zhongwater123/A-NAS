import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useRef, useState } from "react";

import { ShownProvider, useWindowShown } from "./app/shown";
import styles from "./VideoCutPanel.module.css";

// VideoCutPanel is the 剪辑中心 window, being made into Venus
// (docs/specs/venus.md): the shell with its start, prepare and film pages
// comes with #695. Data comes through TanStack Query (#271), which loads
// with this window only; what polls stops while the window is minimized
// (shown.ts).
export function VideoCutPanel() {
  const [client] = useState(
    () => new QueryClient({ defaultOptions: { queries: { retry: 1, staleTime: 5_000, refetchOnWindowFocus: false } } }),
  );
  const root = useRef<HTMLDivElement>(null);
  const shown = useWindowShown(root);
  return (
    <QueryClientProvider client={client}>
      <ShownProvider value={shown}>
        <div className={styles.shell} ref={root}>
          <p>新版剪辑中心正在开发中</p>
        </div>
      </ShownProvider>
    </QueryClientProvider>
  );
}
