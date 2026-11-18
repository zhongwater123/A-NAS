import { call, client } from "../api/client";
import type { components } from "../api.generated";

export type MontageProject = components["schemas"]["MontageProjectSummary"];
export type MontageProjectDetail = components["schemas"]["MontageProject"];
export type MontageJob = components["schemas"]["MontageJob"];
export type MontageSong = components["schemas"]["MontageSong"];
export type MontageNotice = components["schemas"]["MontageNotice"];
export type MontageVersion = components["schemas"]["MontageVersion"];

const project = (projectId: string) => ({ params: { path: { projectId } } });
const job = (jobId: string) => ({ params: { path: { jobId } } });
// A request key per action: the same click retried repeats nothing.
const key = () => crypto.randomUUID();

export async function listProjects(): Promise<MontageProject[]> {
  return (await call(client.GET("/api/v1/montage/projects"))).items;
}

export const getProject = (projectId: string) =>
  call(client.GET("/api/v1/montage/projects/{projectId}", project(projectId)));

export const createProject = (assetIds: string[], name = "") =>
  call(client.POST("/api/v1/montage/projects", { body: { requestKey: key(), name, assetIds } }));

export const addAssets = (projectId: string, assetIds: string[]) =>
  call(
    client.POST("/api/v1/montage/projects/{projectId}/assets", {
      ...project(projectId),
      body: { requestKey: key(), assetIds },
    }),
  );

export const updateProject = (
  projectId: string,
  change: { name?: string; ask?: string; chronological?: boolean; songStart?: number },
) => call(client.PATCH("/api/v1/montage/projects/{projectId}", { ...project(projectId), body: change }));

export const deleteProject = (projectId: string) =>
  call(client.DELETE("/api/v1/montage/projects/{projectId}", project(projectId)));

export const generate = (projectId: string) =>
  call(
    client.POST("/api/v1/montage/projects/{projectId}/generate", {
      ...project(projectId),
      body: { requestKey: key() },
    }),
  );

export const getJob = (jobId: string) => call(client.GET("/api/v1/montage/jobs/{jobId}", job(jobId)));
export const cancelJob = (jobId: string) => call(client.POST("/api/v1/montage/jobs/{jobId}/cancel", job(jobId)));
export const dropAssets = (jobId: string, assetIds: string[]) =>
  call(client.POST("/api/v1/montage/jobs/{jobId}/drop-assets", { ...job(jobId), body: { assetIds } }));
export const retryAnalysis = (jobId: string) =>
  call(client.POST("/api/v1/montage/jobs/{jobId}/retry-analysis", job(jobId)));
export const retryJob = (jobId: string) => call(client.POST("/api/v1/montage/jobs/{jobId}/retry", job(jobId)));
export const replan = (jobId: string) => call(client.POST("/api/v1/montage/jobs/{jobId}/replan", job(jobId)));

export const getSong = (projectId: string) =>
  call(client.GET("/api/v1/montage/projects/{projectId}/song", project(projectId)));
export const chooseSong = (projectId: string, trackId: string) =>
  call(client.PUT("/api/v1/montage/projects/{projectId}/song", { ...project(projectId), body: { trackId } }));
export const songAudioURL = (projectId: string, start?: number) =>
  `/api/v1/montage/projects/${encodeURIComponent(projectId)}/song/audio${start === undefined ? "" : `?start=${start.toFixed(2)}`}`;

export async function listNotices(): Promise<MontageNotice[]> {
  return (await call(client.GET("/api/v1/montage/notices"))).items;
}
