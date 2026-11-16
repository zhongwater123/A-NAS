// Whether this desktop shows 照片打分: the module is installed on the device
// (GET /api/v1/modules, ADR 0037) and the account administers it or an
// administrator chose it as a rater. The desktop imports this file eagerly,
// so it stays small; the window's own client is ./api.
import { call, client } from "../api/client";

export interface ScoringAccess {
  admin: boolean;
  rater: boolean;
}

export const noScoring: ScoringAccess = { admin: false, rater: false };

export async function scoringAccess(): Promise<ScoringAccess> {
  const installed = await call(client.GET("/api/v1/modules"));
  if (!installed.items.some((module) => module.id === "scoring")) return noScoring;
  return call(client.GET("/api/v1/modules/scoring/me"));
}
