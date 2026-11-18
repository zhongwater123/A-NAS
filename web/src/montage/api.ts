import { call, client } from "../api/client";
import type { components } from "../api.generated";

export type MontageProject = components["schemas"]["MontageProjectSummary"];

export async function listProjects(signal?: AbortSignal): Promise<MontageProject[]> {
  return (await call(client.GET("/api/v1/montage/projects", { signal }))).items;
}

export type MontageNotice = components["schemas"]["MontageNotice"];

export async function listNotices(signal?: AbortSignal): Promise<MontageNotice[]> {
  return (await call(client.GET("/api/v1/montage/notices", { signal }))).items;
}
