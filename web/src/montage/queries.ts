import { APIError } from "../api/client";
import { getProject, getSong, type MontageSong } from "./api";

// The queries the project's pages share: one project, and its song (null
// before one is chosen).
export const projectQuery = (projectId: string) => ({
  queryKey: ["montage", "project", projectId],
  queryFn: ({ signal }: { signal: AbortSignal }) => getProject(projectId, signal),
});

export const songQuery = (projectId: string) => ({
  queryKey: ["montage", "song", projectId],
  queryFn: async ({ signal }: { signal: AbortSignal }): Promise<MontageSong | null> => {
    try {
      return await getSong(projectId, signal);
    } catch (error) {
      if (error instanceof APIError && error.code === "no_song") return null;
      throw error;
    }
  },
  // A song being analysed is asked about again until it is done, while the window can be seen.
  refetchInterval: (query: { state: { data?: MontageSong | null } }) =>
    query.state.data?.state === "analyzing" ? 4_000 : false,
  refetchIntervalInBackground: false,
});

export const clock = (seconds: number) =>
  `${Math.floor(seconds / 60)}:${String(Math.floor(seconds % 60)).padStart(2, "0")}`;
