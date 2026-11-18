import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";

import workspaceStyles from "../ui/workspace.module.css";
import styles from "./montage.module.css";
import { NewProject } from "./NewProject";
import { ProjectList } from "./ProjectList";
import { ProjectView } from "./ProjectView";

// MontagePanel is the 剪辑中心 window (docs/specs/montage-center.md): the
// member's projects; a new one from an upload or from the photo library; a
// project with its videos, song and request, the job making its film, and
// its versions. Data comes through TanStack Query (#271), which loads with
// this window only.
export type MontageView = { kind: "list" } | { kind: "new"; into?: string } | { kind: "project"; id: string };

export function MontagePanel({ userId }: { userId: string }) {
  const [client] = useState(
    () => new QueryClient({ defaultOptions: { queries: { retry: 1, staleTime: 5_000, refetchOnWindowFocus: false } } }),
  );
  const [view, setView] = useState<MontageView>({ kind: "list" });
  return (
    <QueryClientProvider client={client}>
      <div className={`${workspaceStyles.bkApp} ${styles.mtApp}`}>
        {view.kind === "list" && (
          <ProjectList onOpen={(id) => setView({ kind: "project", id })} onNew={() => setView({ kind: "new" })} />
        )}
        {view.kind === "new" && (
          <NewProject
            userId={userId}
            into={view.into}
            onDone={(id) => setView({ kind: "project", id })}
            onCancel={() => setView(view.into ? { kind: "project", id: view.into } : { kind: "list" })}
          />
        )}
        {view.kind === "project" && (
          <ProjectView
            id={view.id}
            onBack={() => setView({ kind: "list" })}
            onAddVideos={() => setView({ kind: "new", into: view.id })}
          />
        )}
      </div>
    </QueryClientProvider>
  );
}
