// The VideoCut API (api/paths/videocut.yaml), called only from app/. Venus's
// operations are added here as their hooks are written (#695); until VideoCut
// serves one it answers 501, and tests and the preview answer it from
// ../mocks.
import { call, client } from "../../api/client";
import type { components } from "../../api.generated";

export type VideoCutNotice = components["schemas"]["VideoCutNotice"];

export async function listNotices(signal?: AbortSignal): Promise<VideoCutNotice[]> {
  return (await call(client.GET("/api/v1/videocut/notices", { signal }))).items;
}
