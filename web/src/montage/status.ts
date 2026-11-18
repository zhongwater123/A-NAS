// Whether this desktop shows 剪辑中心 (ADR 0038). The Product Service mounts
// /api/v1/montage only while ANAS_MONTAGE_ENABLED is on, so a 404 means the
// app is off and the desktop shows no icon. Any other answer, even 503 while
// the data volume is away, shows it: the window says why it cannot work.
// The desktop imports this file eagerly, so it stays small.
import { client } from "../api/client";

export async function montageAvailable(): Promise<boolean> {
  const { response } = await client.GET("/api/v1/montage/projects");
  return response.status !== 404;
}
