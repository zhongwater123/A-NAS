// Projects through TanStack Query (#271): the list and one project, read
// every few seconds while the window shows (shown.ts) and stopped while it
// is minimized. A project's state decides its page (lib/pages.ts).
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createContext, useContext } from "react";

import { deleteProject, getProject, listProjects, type Project } from "../api/client";
import { useShown } from "./shown";

// Poll is how often projects are read again: 5 seconds, less in tests.
export const Poll = createContext(5_000);

export const keys = {
  all: ["videocut"] as const,
  works: ["videocut", "works"] as const,
  work: (id: string) => ["videocut", "work", id] as const,
  timeline: (id: string) => ["videocut", "work", id, "timeline"] as const,
  song: (id: string) => ["videocut", "work", id, "song"] as const,
  allCandidates: (id: string) => ["videocut", "work", id, "candidates"] as const,
  candidates: (id: string, slot: number) => ["videocut", "work", id, "candidates", slot] as const,
  imported: (id: string) => ["videocut", "work", id, "imported"] as const,
  catalog: ["videocut", "catalog"] as const,
};

function useInterval(): number | false {
  const shown = useShown();
  const poll = useContext(Poll);
  return shown ? poll : false;
}

/** The member's projects, newest first, and last the sample every member has (M1-1). */
export function useWorks() {
  return useQuery({
    queryKey: keys.works,
    queryFn: ({ signal }) => listProjects(signal),
    refetchInterval: useInterval(),
  });
}

/** One project: its state, progress and settings, read again while its work goes on. */
export function useWork(workId: string) {
  return useQuery({
    queryKey: keys.work(workId),
    queryFn: ({ signal }) => getProject(workId, signal),
    refetchInterval: useInterval(),
  });
}

/** Deleting a project (M1-2); the list reads again after. */
export function useRemoveWork() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (workId: string) => deleteProject(workId),
    onSuccess: () => client.invalidateQueries({ queryKey: keys.works }),
  });
}

// setWork puts a project an operation answered with into the cache, so the
// shell moves to its page without waiting for the next read.
export function useSetWork() {
  const client = useQueryClient();
  return (work: Project) => client.setQueryData(keys.work(work.id), work);
}
