import { call, client } from "../api/client";
import type { components } from "../api.generated";

export type MontageProject = components["schemas"]["MontageProject"];

export async function listProjects(): Promise<MontageProject[]> {
  return (await call(client.GET("/api/v1/montage/projects"))).items;
}
