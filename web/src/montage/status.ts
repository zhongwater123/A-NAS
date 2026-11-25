// Whether this desktop shows 剪辑中心 (ADR 0038): GET /api/v1/montage answers
// 204 while ANAS_MONTAGE_ENABLED is on and 404 while it is off. The Product
// Service answers it alone, without asking the montage service. The desktop
// imports this file eagerly, so it stays small.
import { client } from "../api/client";

export async function montageEnabled(signal?: AbortSignal): Promise<boolean> {
  const { response } = await client.GET("/api/v1/montage", { signal });
  if (response.status === 204) return true;
  if (response.status === 404) return false;
  throw new Error(`GET /api/v1/montage answered ${response.status}`);
}
