// 照片打分: a rater's batches, held photos and answers, and administrators'
// batches, statistics and raters.
import { HttpResponse } from "msw";

import type {
  ScoringBatch,
  ScoringClaim,
  ScoringDimension,
  ScoringDimensionStats,
  ScoringMe,
  ScoringRaterBatch,
  ScoringRaterEntry,
  ScoringRaterStats,
  ScoringRatingInput,
} from "../../api.generated";
import { http } from "../server";

// Every 打分项 a batch can choose, as the module lists them.
export const scoringCatalogue: ScoringDimension[] = [
  { id: "overall", name: "总分" },
  { id: "composition", name: "构图" },
  { id: "light_color", name: "光影色彩" },
  { id: "sharpness", name: "清晰度" },
  { id: "narrative", name: "叙事意境" },
  { id: "preference", name: "喜好程度" },
];

// dimensionsOf is the catalogue's entries for ids, in the catalogue's order.
export const dimensionsOf = (...ids: string[]) => scoringCatalogue.filter((dimension) => ids.includes(dimension.id));

export function scoringMe(admin: boolean, rater: boolean): ScoringMe {
  return { admin, rater, dimensions: scoringCatalogue };
}

export function raterBatch(overrides: Partial<ScoringRaterBatch> = {}): ScoringRaterBatch {
  return {
    id: "b1",
    name: "风景第一批",
    rubric: "看构图与光线",
    dimensions: dimensionsOf("overall"),
    rated: 0,
    finished: false,
    ...overrides,
  };
}

export function adminBatch(overrides: Partial<ScoringBatch> = {}): ScoringBatch {
  return {
    id: "b1",
    name: "风景第一批",
    source: "风景第一批",
    required: 3,
    rubric: "看构图与光线",
    dimensions: dimensionsOf("overall"),
    state: "open",
    createdAt: "2026-11-12T01:00:00Z",
    import: { files: 120, imported: 118, skipped: 2, skippedBy: [{ reason: "unsupported_type", count: 2 }] },
    photos: 116,
    anchors: 2,
    complete: 40,
    started: 10,
    flagged: 1,
    answers: 150,
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
      "/api/v1/modules/scoring/claims/:token/full",
      () =>
        new HttpResponse(new Blob([new Uint8Array([255, 216, 255])]), {
          headers: { "Content-Type": "image/jpeg", "X-Image-Orientation": "6" },
        }),
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

export function dimensionStats(overrides: Partial<ScoringDimensionStats> = {}): ScoringDimensionStats {
  return {
    dimension: "overall",
    mean: 2.9,
    standardDeviation: 0.6,
    agreement: 0.71,
    agreementPhotos: 80,
    repeatDifference: 0.4,
    longestRun: 4,
    ...overrides,
  };
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
    fastShare: 0.02,
    lastAt: "2026-11-12T02:00:00Z",
    dimensions: [dimensionStats()],
    ...overrides,
  };
}

export function raterEntry(id: string, username: string, rater: boolean): ScoringRaterEntry {
  return { id, username, rater };
}
