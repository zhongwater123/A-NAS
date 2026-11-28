import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { use, useMemo, useRef, useState } from "react";

import { APIError } from "../api/client";
import { useWorkEvents } from "./app/events";
import { NavContext, type Route } from "./app/nav";
import { ShownProvider, useWindowShown } from "./app/shown";
import { Poll, useWork } from "./app/works";
import { pageOf } from "./lib/pages";
import { CutPage } from "./screens/cut/CutPage";
import layout from "./screens/layout.module.css";
import { PreparePage } from "./screens/prepare/PreparePage";
import { StartPage } from "./screens/start/StartPage";
import { BannerProvider } from "./ui/Banner";
import { Empty } from "./ui/Empty";
import { messages } from "./ui/messages";
import { ToastProvider } from "./ui/Toast";
import styles from "./ui/venus.module.css";
import { WindowRoot } from "./ui/windowRoot";

// VideoCutPanel is the 剪辑中心 window as Venus (docs/specs/venus.md): the
// start page, or a project opened, which shows the prepare or the film page
// by its state on the server (lib/pages.ts). Data comes through TanStack
// Query (#271), which loads with this window only; what polls stops while
// the window is minimized (shown.ts). poll is for tests.
// The Fake preview answers Venus's operations from the stand-ins until VideoCut
// serves them; any other build leaves this, and the stand-ins, out.
const standIns =
  import.meta.env.VITE_VIDEOCUT_STANDINS === "1"
    ? import("./mocks/browser").then((module) => module.startStandIns())
    : undefined;

export function VideoCutPanel({ poll = 5_000 }: { poll?: number }) {
  if (standIns) use(standIns);
  const [client] = useState(
    () => new QueryClient({ defaultOptions: { queries: { retry: 1, staleTime: 2_000, refetchOnWindowFocus: false } } }),
  );
  const [route, setRoute] = useState<Route>({ page: "start" });
  const nav = useMemo(
    () => ({
      toStart: () => setRoute({ page: "start" }),
      openWork: (workId: string) => setRoute({ page: "work", workId }),
    }),
    [],
  );
  const root = useRef<HTMLDivElement>(null);
  const shown = useWindowShown(root);
  return (
    <QueryClientProvider client={client}>
      <ShownProvider value={shown}>
        <Poll.Provider value={poll}>
          <NavContext.Provider value={nav}>
            <div className={styles.root} ref={root}>
              <WindowRoot.Provider value={root}>
                <ToastProvider>
                  <BannerProvider>
                    <div className={layout.content}>
                      {route.page === "start" ? <StartPage /> : <WorkRoute key={route.workId} workId={route.workId} />}
                    </div>
                    <div className={layout.tooSmall}>{messages.windowTooSmall}</div>
                  </BannerProvider>
                </ToastProvider>
              </WindowRoot.Provider>
            </div>
          </NavContext.Provider>
        </Poll.Provider>
      </ShownProvider>
    </QueryClientProvider>
  );
}

function WorkRoute({ workId }: { workId: string }) {
  const work = useWork(workId);
  useWorkEvents(work.data);
  // Only the server's not_found says the project is gone; a failed read
  // keeps what was read before, or offers to read again.
  const gone = work.error instanceof APIError && work.error.status === 404;
  if (gone) return <Empty text={messages.workGone} />;
  if (!work.data) {
    if (!work.isError) return null;
    return (
      <Empty
        text={messages.loadFailed}
        action={
          <button type="button" onClick={() => void work.refetch()}>
            {messages.retry}
          </button>
        }
      />
    );
  }
  return pageOf(work.data.state) === "prepare" ? <PreparePage work={work.data} /> : <CutPage work={work.data} />;
}
