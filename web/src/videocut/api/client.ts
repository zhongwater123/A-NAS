// The VideoCut API (api/paths/videocut.yaml), called only from app/. Until
// VideoCut serves an operation it answers 501, and tests and the preview
// answer it from ../mocks.
import { call, client } from "../../api/client";
import { requestKey } from "../../api/requestKey";
import type { components } from "../../api.generated";

type Schemas = components["schemas"];
export type VideoCutNotice = Schemas["VideoCutNotice"];
export type Project = Schemas["VideoCutProject"];
export type ProjectSummary = Schemas["VideoCutProjectSummary"];
export type LocalFile = Schemas["VideoCutLocalFile"];
export type Timeline = Schemas["VideoCutTimeline"];
export type Shot = Schemas["VideoCutShot"];
export type Edit = Schemas["VideoCutEdit"];
export type SongStructure = Schemas["VideoCutSongStructure"];
export type Candidates = Schemas["VideoCutCandidates"];
export type ProjectAsset = Schemas["VideoCutProjectAsset"];
export type AssistantReply = Schemas["VideoCutAssistantReply"];
export type Catalog = Schemas["VideoCutCatalog"];

const project = (projectId: string) => ({ params: { path: { projectId } } });

export async function listNotices(signal?: AbortSignal): Promise<VideoCutNotice[]> {
  return (await call(client.GET("/api/v1/videocut/notices", { signal }))).items;
}

export const getCatalog = (signal?: AbortSignal) => call(client.GET("/api/v1/videocut/catalog", { signal }));

export async function listProjects(signal?: AbortSignal): Promise<ProjectSummary[]> {
  return (await call(client.GET("/api/v1/videocut/projects", { signal }))).items;
}

export const getProject = (projectId: string, signal?: AbortSignal) =>
  call(client.GET("/api/v1/videocut/projects/{projectId}", { ...project(projectId), signal }));

export const createProject = (files: LocalFile[]) =>
  call(client.POST("/api/v1/videocut/projects", { body: { requestKey: requestKey(), files } }));

export const addFiles = (projectId: string, files: LocalFile[]) =>
  call(client.POST("/api/v1/videocut/projects/{projectId}/files", { ...project(projectId), body: { files } }));

export const deleteProject = (projectId: string) =>
  call(client.DELETE("/api/v1/videocut/projects/{projectId}", project(projectId)));

export const updateSelection = (
  projectId: string,
  files: { fileId: string; selected?: boolean; starred?: boolean }[],
) => call(client.PATCH("/api/v1/videocut/projects/{projectId}/selection", { ...project(projectId), body: { files } }));

export const updateSettings = (projectId: string, change: { aspect?: string; style?: string; builtinSong?: string }) =>
  call(client.PATCH("/api/v1/videocut/projects/{projectId}/settings", { ...project(projectId), body: change }));

export const report = (projectId: string) =>
  call(client.POST("/api/v1/videocut/projects/{projectId}/heartbeat", project(projectId)));

export const generate = (projectId: string) =>
  call(
    client.POST("/api/v1/videocut/projects/{projectId}/generate", {
      ...project(projectId),
      body: { requestKey: requestKey() },
    }),
  );

export const resumeUpload = (projectId: string) =>
  call(client.POST("/api/v1/videocut/projects/{projectId}/resume", project(projectId)));

export const retry = (projectId: string) =>
  call(client.POST("/api/v1/videocut/projects/{projectId}/retry", project(projectId)));

export const getSongStructure = (projectId: string, signal?: AbortSignal) =>
  call(client.GET("/api/v1/videocut/projects/{projectId}/song", { ...project(projectId), signal }));

export const getTimeline = (projectId: string, signal?: AbortSignal) =>
  call(client.GET("/api/v1/videocut/projects/{projectId}/timeline", { ...project(projectId), signal }));

export const editTimeline = (projectId: string, baseRevision: number, edit: Edit) =>
  call(
    client.POST("/api/v1/videocut/projects/{projectId}/timeline/edits", {
      ...project(projectId),
      body: { baseRevision, edit },
    }),
  );

export const undoTimeline = (projectId: string, baseRevision: number) =>
  call(
    client.POST("/api/v1/videocut/projects/{projectId}/timeline/undo", {
      ...project(projectId),
      body: { baseRevision },
    }),
  );

export const listCandidates = (projectId: string, slot: number, signal?: AbortSignal) =>
  call(
    client.GET("/api/v1/videocut/projects/{projectId}/slots/{slot}/candidates", {
      params: { path: { projectId, slot } },
      signal,
    }),
  );

export async function listImported(projectId: string, signal?: AbortSignal): Promise<ProjectAsset[]> {
  return (await call(client.GET("/api/v1/videocut/projects/{projectId}/assets", { ...project(projectId), signal })))
    .items;
}

export const ask = (projectId: string, text: string, slot?: number, signal?: AbortSignal) =>
  call(
    client.POST("/api/v1/videocut/projects/{projectId}/assistant", {
      ...project(projectId),
      body: slot === undefined ? { text } : { text, slot },
      signal,
    }),
  );

export const exportFilm = (projectId: string) =>
  call(
    client.POST("/api/v1/videocut/projects/{projectId}/export", {
      ...project(projectId),
      body: { requestKey: requestKey() },
    }),
  );

// filmURL is where the exported film downloads from.
export const filmURL = (projectId: string) => `/api/v1/videocut/projects/${encodeURIComponent(projectId)}/film`;

// frameURL is a frame of one of the project's videos, t milliseconds in.
export const frameURL = (projectId: string, assetId: string, t = 0) =>
  `/api/v1/videocut/projects/${encodeURIComponent(projectId)}/assets/${encodeURIComponent(assetId)}/frame?t=${t}`;
