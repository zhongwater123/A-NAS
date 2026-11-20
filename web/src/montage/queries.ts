import { APIError } from "../api/client";
import { getProject, getSong, listProjects, type MontageSong } from "./api";

// The queries the window's pages share: the projects, one project, and its
// song (null before one is chosen).
export const projectsKey = ["montage", "projects"] as const;
export const projectKey = (projectId: string) => ["montage", "project", projectId] as const;
export const songKey = (projectId: string) => ["montage", "song", projectId] as const;
export const jobKey = (jobId?: string) => ["montage", "job", jobId] as const;
// A version's draft (#596), as its edit page reads it, and the version as
// its film is, as its card shows it: a film does not change.
export const draftKey = (versionId: string) => ["montage", "draft", versionId] as const;
export const outlineKey = (versionId: string) => ["montage", "outline", versionId] as const;

export const projectsQuery = {
  queryKey: projectsKey,
  queryFn: ({ signal }: { signal: AbortSignal }) => listProjects(signal),
};

export const projectQuery = (projectId: string) => ({
  queryKey: projectKey(projectId),
  queryFn: ({ signal }: { signal: AbortSignal }) => getProject(projectId, signal),
});

// A song being analysed is asked about again until it is done, while the
// window can be seen (shown).
export const songQuery = (projectId: string, shown: boolean) => ({
  queryKey: songKey(projectId),
  queryFn: async ({ signal }: { signal: AbortSignal }): Promise<MontageSong | null> => {
    try {
      return await getSong(projectId, signal);
    } catch (error) {
      if (error instanceof APIError && error.code === "no_song") return null;
      throw error;
    }
  },
  refetchInterval: (query: { state: { data?: MontageSong | null } }) =>
    shown && query.state.data?.state === "analyzing" ? 4_000 : false,
  refetchIntervalInBackground: false,
});
