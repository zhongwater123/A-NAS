import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";

import { AskPage } from "./AskPage";
import styles from "./montage.module.css";
import { NewProject } from "./NewProject";
import { Home } from "./ProjectList";
import { ProjectView } from "./ProjectView";
import { SongPage } from "./SongPage";

// MontagePanel is the 剪辑中心 window (docs/specs/montage-center.md), laid
// out as the 第八版 prototype's pages: the member's projects; a new one from
// an upload or from the photo library; a project with its videos, the bar
// that starts the film, the job making it and the film; and the song and the
// request, each on its own page. Data comes through TanStack Query (#271),
// which loads with this window only.
export type MontageView =
  | { kind: "home" }
  | { kind: "new"; into?: string }
  | { kind: "project"; id: string; page?: "song" | "ask" };

export function MontagePanel({ userId }: { userId: string }) {
  const [client] = useState(
    () => new QueryClient({ defaultOptions: { queries: { retry: 1, staleTime: 5_000, refetchOnWindowFocus: false } } }),
  );
  const [view, setView] = useState<MontageView>({ kind: "home" });
  const open = (id: string, page?: "song" | "ask") => setView({ kind: "project", id, page });
  return (
    <QueryClientProvider client={client}>
      <div className={styles.mtApp}>
        {view.kind === "home" && <Home onOpen={(id) => open(id)} onNew={() => setView({ kind: "new" })} />}
        {view.kind === "new" && (
          <NewProject
            userId={userId}
            into={view.into}
            onDone={(id) => open(id)}
            onCancel={() => setView(view.into ? { kind: "project", id: view.into } : { kind: "home" })}
          />
        )}
        {view.kind === "project" && view.page === "song" && <SongPage id={view.id} onBack={() => open(view.id)} />}
        {view.kind === "project" && view.page === "ask" && <AskPage id={view.id} onBack={() => open(view.id)} />}
        {view.kind === "project" && !view.page && (
          <ProjectView
            key={view.id}
            id={view.id}
            onBack={() => setView({ kind: "home" })}
            onPage={(page) => open(view.id, page)}
            onAddVideos={() => setView({ kind: "new", into: view.id })}
          />
        )}
      </div>
    </QueryClientProvider>
  );
}
