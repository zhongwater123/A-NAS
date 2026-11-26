import { call, client } from "../../api/client";
import { requestKey } from "../../api/requestKey";
import type { components, operations } from "../../api.generated";

export type VideoCutProject = components["schemas"]["VideoCutProjectSummary"];
export type VideoCutProjectDetail = components["schemas"]["VideoCutProject"];
export type VideoCutJob = components["schemas"]["VideoCutJob"];
export type VideoCutSong = components["schemas"]["VideoCutSong"];
export type VideoCutNotice = components["schemas"]["VideoCutNotice"];
export type VideoCutVersion = components["schemas"]["VideoCutVersion"];

const project = (projectId: string) => ({ params: { path: { projectId } } });
const job = (jobId: string) => ({ params: { path: { jobId } } });
// A request key per action: the same click retried repeats nothing. The
// desktop's own keys work on plain LAN HTTP too, where crypto.randomUUID is
// not there.
const key = requestKey;
export type ProjectChange = operations["updateVideoCutProject"]["requestBody"]["content"]["application/json"];

export async function listProjects(signal?: AbortSignal): Promise<VideoCutProject[]> {
  return (await call(client.GET("/api/v1/videocut/projects", { signal }))).items;
}

export const getProject = (projectId: string, signal?: AbortSignal) =>
  call(client.GET("/api/v1/videocut/projects/{projectId}", { ...project(projectId), signal }));

export const createProject = (assetIds: string[], name = "") =>
  call(client.POST("/api/v1/videocut/projects", { body: { requestKey: key(), name, assetIds } }));

export const addAssets = (projectId: string, assetIds: string[]) =>
  call(
    client.POST("/api/v1/videocut/projects/{projectId}/assets", {
      ...project(projectId),
      body: { requestKey: key(), assetIds },
    }),
  );

export const updateProject = (projectId: string, change: ProjectChange) =>
  call(client.PATCH("/api/v1/videocut/projects/{projectId}", { ...project(projectId), body: change }));

export const deleteProject = (projectId: string) =>
  call(client.DELETE("/api/v1/videocut/projects/{projectId}", project(projectId)));

export const generate = (projectId: string) =>
  call(
    client.POST("/api/v1/videocut/projects/{projectId}/generate", {
      ...project(projectId),
      body: { requestKey: key() },
    }),
  );

export const getJob = (jobId: string, signal?: AbortSignal) =>
  call(client.GET("/api/v1/videocut/jobs/{jobId}", { ...job(jobId), signal }));
export const cancelJob = (jobId: string) => call(client.POST("/api/v1/videocut/jobs/{jobId}/cancel", job(jobId)));
export const dropAssets = (jobId: string, assetIds: string[]) =>
  call(client.POST("/api/v1/videocut/jobs/{jobId}/drop-assets", { ...job(jobId), body: { assetIds } }));
export const retryAnalysis = (jobId: string) =>
  call(client.POST("/api/v1/videocut/jobs/{jobId}/retry-analysis", job(jobId)));
export const retryJob = (jobId: string) => call(client.POST("/api/v1/videocut/jobs/{jobId}/retry", job(jobId)));
export const replan = (jobId: string) => call(client.POST("/api/v1/videocut/jobs/{jobId}/replan", job(jobId)));

export const getSong = (projectId: string, signal?: AbortSignal) =>
  call(client.GET("/api/v1/videocut/projects/{projectId}/song", { ...project(projectId), signal }));
export const chooseSong = (projectId: string, trackId: string) =>
  call(client.PUT("/api/v1/videocut/projects/{projectId}/song", { ...project(projectId), body: { trackId } }));
export const songAudioURL = (projectId: string, start?: number) =>
  `/api/v1/videocut/projects/${encodeURIComponent(projectId)}/song/audio${start === undefined ? "" : `?start=${start.toFixed(2)}`}`;

export async function listNotices(signal?: AbortSignal): Promise<VideoCutNotice[]> {
  return (await call(client.GET("/api/v1/videocut/notices", { signal }))).items;
}

// The device's background film for the home page, and its poster; 404 without one.
export const heroURL = "/api/v1/videocut/hero";
// Whether the device keeps a film of its own for the home page: its first
// byte is asked for, and the montage center answers 404 without one.
export async function hasHero(signal?: AbortSignal): Promise<boolean> {
  const { response } = await client.GET("/api/v1/videocut/hero", {
    params: { header: { Range: "bytes=0-0" } },
    parseAs: "stream",
    signal,
  });
  await response.body?.cancel();
  return response.ok;
}
export const heroPosterURL = "/api/v1/videocut/hero/poster";
