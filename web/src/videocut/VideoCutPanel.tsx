import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useRef, useState } from "react";

import { ShownProvider, useWindowShown } from "./app/shown";
import { AskPage } from "./screens/AskPage";
import { NewProject } from "./screens/NewProject";
import { Home } from "./screens/ProjectList";
import { ProjectView } from "./screens/ProjectView";
import { SongPage } from "./screens/SongPage";
import styles from "./screens/videocut.module.css";

// VideoCutPanel is the 剪辑中心 window (docs/specs/montage-center.md), laid
// out as the 第八版 prototype's pages: the home page, where a project is
// selected and its film started; a new project from an upload or from the
// photo library; a project opened with its videos; and the song and the
// request, each on its own page, returning where they were opened from.
// Data comes through TanStack Query (#271), which loads with this window
// only. What polls stops while the window is minimized (shown.ts).
export type MontageView =
  | { kind: "home" }
  | { kind: "new"; into?: string }
  | { kind: "project"; id: string }
  | { kind: "page"; id: string; page: "song" | "ask"; from: "home" | "project" };

export function VideoCutPanel({ userId }: { userId: string }) {
  const [client] = useState(
    () => new QueryClient({ defaultOptions: { queries: { retry: 1, staleTime: 5_000, refetchOnWindowFocus: false } } }),
  );
  const [view, setView] = useState<MontageView>({ kind: "home" });
  // The project selected on the home page: the newest until one is
  // clicked (undefined), none once the member unselects it (null).
  const [selected, setSelected] = useState<string | null>();
  const root = useRef<HTMLDivElement>(null);
  const shown = useWindowShown(root);
  const open = (id: string) => setView({ kind: "project", id });
  const home = () => setView({ kind: "home" });
  return (
    <QueryClientProvider client={client}>
      <ShownProvider value={shown}>
        <div className={styles.mtApp} ref={root}>
          {view.kind === "home" && (
            <Home
              selected={selected}
              onSelect={setSelected}
              onOpen={open}
              onPage={(id, page) => setView({ kind: "page", id, page, from: "home" })}
              onNew={() => setView({ kind: "new" })}
            />
          )}
          {view.kind === "new" && (
            <NewProject
              userId={userId}
              into={view.into}
              onDone={(id) => {
                setSelected(id);
                if (view.into) open(id);
                else home();
              }}
              onCancel={() => (view.into ? open(view.into) : home())}
            />
          )}
          {view.kind === "page" &&
            (view.page === "song" ? (
              <SongPage id={view.id} onBack={() => (view.from === "home" ? home() : open(view.id))} />
            ) : (
              <AskPage id={view.id} onBack={() => (view.from === "home" ? home() : open(view.id))} />
            ))}
          {view.kind === "project" && (
            <ProjectView
              key={view.id}
              id={view.id}
              onBack={home}
              onPage={(page) => setView({ kind: "page", id: view.id, page, from: "project" })}
              onAddVideos={() => setView({ kind: "new", into: view.id })}
            />
          )}
        </div>
      </ShownProvider>
    </QueryClientProvider>
  );
}
