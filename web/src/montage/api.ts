import { call, client } from "../api/client";
import { requestKey } from "../api/requestKey";
import type { components, operations } from "../api.generated";

export type MontageProject = components["schemas"]["MontageProjectSummary"];
export type MontageProjectDetail = components["schemas"]["MontageProject"];
export type MontageJob = components["schemas"]["MontageJob"];
export type MontageSong = components["schemas"]["MontageSong"];
export type MontageNotice = components["schemas"]["MontageNotice"];
export type MontageVersion = components["schemas"]["MontageVersion"];

const project = (projectId: string) => ({ params: { path: { projectId } } });
const job = (jobId: string) => ({ params: { path: { jobId } } });
// A request key per action: the same click retried repeats nothing. The
// desktop's own keys work on plain LAN HTTP too, where crypto.randomUUID is
// not there.
const key = requestKey;
export type ProjectChange = operations["updateMontageProject"]["requestBody"]["content"]["application/json"];

export async function listProjects(signal?: AbortSignal): Promise<MontageProject[]> {
  return (await call(client.GET("/api/v1/montage/projects", { signal }))).items;
}

export const getProject = (projectId: string, signal?: AbortSignal) =>
  call(client.GET("/api/v1/montage/projects/{projectId}", { ...project(projectId), signal }));

export const createProject = (assetIds: string[], name = "") =>
  call(client.POST("/api/v1/montage/projects", { body: { requestKey: key(), name, assetIds } }));

export const addAssets = (projectId: string, assetIds: string[]) =>
  call(
    client.POST("/api/v1/montage/projects/{projectId}/assets", {
      ...project(projectId),
      body: { requestKey: key(), assetIds },
    }),
  );

export const updateProject = (projectId: string, change: ProjectChange) =>
  call(client.PATCH("/api/v1/montage/projects/{projectId}", { ...project(projectId), body: change }));

export const deleteProject = (projectId: string) =>
  call(client.DELETE("/api/v1/montage/projects/{projectId}", project(projectId)));

export const generate = (projectId: string) =>
  call(
    client.POST("/api/v1/montage/projects/{projectId}/generate", {
      ...project(projectId),
      body: { requestKey: key() },
    }),
  );

export const getJob = (jobId: string, signal?: AbortSignal) =>
  call(client.GET("/api/v1/montage/jobs/{jobId}", { ...job(jobId), signal }));
export const cancelJob = (jobId: string) => call(client.POST("/api/v1/montage/jobs/{jobId}/cancel", job(jobId)));
export const dropAssets = (jobId: string, assetIds: string[]) =>
  call(client.POST("/api/v1/montage/jobs/{jobId}/drop-assets", { ...job(jobId), body: { assetIds } }));
export const retryAnalysis = (jobId: string) =>
  call(client.POST("/api/v1/montage/jobs/{jobId}/retry-analysis", job(jobId)));
export const retryJob = (jobId: string) => call(client.POST("/api/v1/montage/jobs/{jobId}/retry", job(jobId)));
export const replan = (jobId: string) => call(client.POST("/api/v1/montage/jobs/{jobId}/replan", job(jobId)));

export const getSong = (projectId: string, signal?: AbortSignal) =>
  call(client.GET("/api/v1/montage/projects/{projectId}/song", { ...project(projectId), signal }));
export const chooseSong = (projectId: string, trackId: string) =>
  call(client.PUT("/api/v1/montage/projects/{projectId}/song", { ...project(projectId), body: { trackId } }));
export const songAudioURL = (projectId: string, start?: number) =>
  `/api/v1/montage/projects/${encodeURIComponent(projectId)}/song/audio${start === undefined ? "" : `?start=${start.toFixed(2)}`}`;

export async function listNotices(signal?: AbortSignal): Promise<MontageNotice[]> {
  return (await call(client.GET("/api/v1/montage/notices", { signal }))).items;
}

// The device's background film for the home page, and its poster; 404 without one.
export const heroURL = "/api/v1/montage/hero";
// Whether the device keeps a film of its own for the home page: its first
// byte is asked for, and the montage center answers 404 without one.
export async function hasHero(signal?: AbortSignal): Promise<boolean> {
  const { response } = await client.GET("/api/v1/montage/hero", {
    params: { header: { Range: "bytes=0-0" } },
    parseAs: "stream",
    signal,
  });
  await response.body?.cancel();
  return response.ok;
}
export const heroPosterURL = "/api/v1/montage/hero/poster";

// --- editing a film after it was made (#596) ---

export type MontageDraft = components["schemas"]["MontageDraft"];
export type MontageDraftShot = components["schemas"]["MontageDraftShot"];
export type MontageDraftOp = components["schemas"]["MontageDraftOp"];
export type MontageCandidates = components["schemas"]["MontageCandidates"];
export type MontageCandidate = components["schemas"]["MontageCandidate"];
export type CandidateQuery = NonNullable<operations["listMontageDraftCandidates"]["parameters"]["query"]>;

const version = (versionId: string) => ({ params: { path: { versionId } } });
const draft = (draftId: string) => ({ params: { path: { draftId } } });

export const confirmVersion = (versionId: string) =>
  call(client.POST("/api/v1/montage/versions/{versionId}/confirm", version(versionId)));
export const recutVersion = (versionId: string, change: string) =>
  call(
    client.POST("/api/v1/montage/versions/{versionId}/recut", {
      ...version(versionId),
      body: { requestKey: key(), change },
    }),
  );
export const getVersionDraft = (versionId: string, signal?: AbortSignal) =>
  call(client.GET("/api/v1/montage/versions/{versionId}/draft", { ...version(versionId), signal }));
export const editDraft = (draftId: string, baseRevision: number, op: MontageDraftOp) =>
  call(client.POST("/api/v1/montage/drafts/{draftId}/ops", { ...draft(draftId), body: { baseRevision, op } }));
export const draftCandidates = (draftId: string, query: CandidateQuery, signal?: AbortSignal) =>
  call(client.GET("/api/v1/montage/drafts/{draftId}/candidates", { params: { path: { draftId }, query }, signal }));
export const saveDraft = (draftId: string) =>
  call(client.POST("/api/v1/montage/drafts/{draftId}/save", { ...draft(draftId), body: { requestKey: key() } }));
export const draftSongURL = (draftId: string) => `/api/v1/montage/drafts/${encodeURIComponent(draftId)}/song`;
export const versionShotsURL = (versionId: string) => `/api/v1/montage/versions/${encodeURIComponent(versionId)}/shots`;
export const draftShotsURL = (draftId: string) => `/api/v1/montage/drafts/${encodeURIComponent(draftId)}/shots`;
// A frame of a project's video, 320 on its longer side; the browser keeps it.
export const frameURL = (projectId: string, assetId: string, seconds: number) =>
  `/api/v1/montage/projects/${encodeURIComponent(projectId)}/frames/${encodeURIComponent(assetId)}?t=${Math.max(0, Math.round(seconds * 1000))}`;
