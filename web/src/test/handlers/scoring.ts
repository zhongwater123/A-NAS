// 照片打分: a rater's batches, held photos and answers, and administrators'
// batches, statistics and raters.
import { HttpResponse } from "msw";

import type {
  ScoringBatch,
  ScoringClaim,
  ScoringRaterBatch,
  ScoringRaterEntry,
  ScoringRaterStats,
  ScoringRatingInput,
} from "../../api.generated";
import { http } from "../server";

export function raterBatch(overrides: Partial<ScoringRaterBatch> = {}): ScoringRaterBatch {
  return { id: "b1", name: "风景第一批", rubric: "看构图与光线", rated: 0, finished: false, ...overrides };
}

export function adminBatch(overrides: Partial<ScoringBatch> = {}): ScoringBatch {
  return {
    id: "b1",
    name: "风景第一批",
    source: "风景第一批",
    required: 3,
    rubric: "看构图与光线",
    state: "open",
    createdAt: "2026-11-12T01:00:00Z",
    import: { files: 120, imported: 118, skipped: 2 },
    photos: 116,
    anchors: 2,
    complete: 40,
    started: 10,
    flagged: 1,
    ...overrides,
  };
}

export function claim(token: string): ScoringClaim {
  return { token, expiresAt: "2026-11-12T01:10:00Z", width: 2048, height: 1365 };
}

export interface Answered {
  token: string;
  body: ScoringRatingInput;
}

// raterHandlers serves a rater: one batch whose photos are handed out from
// photos in order, three at a time, until none is left. Answers are recorded.
export function raterHandlers(batch: ScoringRaterBatch, photos: string[]) {
  const answered: Answered[] = [];
  const done = new Set<string>();
  let rated = batch.rated;
  const handlers = [
    http.get("/api/v1/modules/scoring/batches", ({ response }) => response(200).json({ items: [{ ...batch, rated }] })),
    http.get("/api/v1/modules/scoring/batches/{batchId}/anchors", ({ response }) =>
      response(200).json({
        items: [
          { id: 7, score: 1.5, width: 300, height: 200 },
          { id: 8, score: 4.5, width: 300, height: 200 },
        ],
      }),
    ),
    http.get("/api/v1/modules/scoring/batches/{batchId}/recent", ({ response }) => response(200).json({ items: [] })),
    http.post("/api/v1/modules/scoring/batches/{batchId}/claims", ({ response }) => {
      const held = photos.filter((token) => !done.has(token)).slice(0, 3);
      return response(200).json({ items: held.map(claim), finished: held.length === 0 });
    }),
    http.put("/api/v1/modules/scoring/claims/{token}/rating", async ({ params, request, response }) => {
      const body = await request.json();
      answered.push({ token: params.token, body });
      if (!done.has(params.token)) rated += 1;
      done.add(params.token);
      return response(200).json({ rated });
    }),
    http.untyped.get(
      "/api/v1/modules/scoring/claims/:token/image",
      () =>
        new HttpResponse(new Blob([new Uint8Array([255, 216, 255])]), { headers: { "Content-Type": "image/jpeg" } }),
    ),
    http.untyped.get(
      "/api/v1/modules/scoring/claims/:token/thumbnail",
      () =>
        new HttpResponse(new Blob([new Uint8Array([255, 216, 255])]), { headers: { "Content-Type": "image/jpeg" } }),
    ),
    http.untyped.get(
      "/api/v1/modules/scoring/batches/:batchId/anchors/:anchorId/image",
      () =>
        new HttpResponse(new Blob([new Uint8Array([255, 216, 255])]), { headers: { "Content-Type": "image/jpeg" } }),
    ),
  ];
  return { handlers, answered };
}

export function raterStats(overrides: Partial<ScoringRaterStats> = {}): ScoringRaterStats {
  return {
    rater: { id: "user:alice", username: "alice", nickname: "小艾" },
    active: true,
    scores: 120,
    skips: 3,
    flags: 1,
    repeats: 6,
    medianViewMs: 3400,
    mean: 2.9,
    standardDeviation: 0.6,
    agreement: 0.71,
    agreementPhotos: 80,
    repeatDifference: 0.4,
    fastShare: 0.02,
    longestRun: 4,
    lastAt: "2026-11-12T02:00:00Z",
    ...overrides,
  };
}

export function raterEntry(id: string, username: string, rater: boolean): ScoringRaterEntry {
  return { id, username, rater };
}
