// Client for the photo scoring module (docs/specs/photo-scoring.md), which
// the Product Service forwards to its own process (ADR 0037). A rater's calls
// never return another rater's answers; administrators' are under /admin.
import { APIError, call, client } from "../api/client";
import { errorText } from "../api/errorMessages";
import type {
  ScoringAnchor,
  ScoringBatch,
  ScoringBatchInput,
  ScoringBatchPatch,
  ScoringClaim,
  ScoringClaims,
  ScoringRaterBatch,
  ScoringRaterEntry,
  ScoringRaterStats,
  ScoringRatingInput,
  ScoringRecentScore,
} from "../api.generated";

export type {
  ScoringAnchor,
  ScoringBatch,
  ScoringBatchInput,
  ScoringClaim,
  ScoringClaims,
  ScoringRaterBatch,
  ScoringRaterEntry,
  ScoringRaterStats,
  ScoringRatingInput,
  ScoringRecentScore,
};

const prefix = "/api/v1/modules/scoring";
const batch = (batchId: string) => ({ params: { path: { batchId } } });
const token = (value: string) => ({ params: { path: { token: value } } });
const account = (accountId: string) => ({ params: { path: { accountId } } });

export const listBatches = async () => (await call(client.GET("/api/v1/modules/scoring/batches"))).items;
export const listAnchors = async (batchId: string) =>
  (await call(client.GET("/api/v1/modules/scoring/batches/{batchId}/anchors", batch(batchId)))).items;
export const claimPhotos = (batchId: string) =>
  call(client.POST("/api/v1/modules/scoring/batches/{batchId}/claims", batch(batchId)));
export const rate = (claimToken: string, body: ScoringRatingInput) =>
  call(client.PUT("/api/v1/modules/scoring/claims/{token}/rating", { ...token(claimToken), body }));

export const listRecent = async (batchId: string) =>
  (await call(client.GET("/api/v1/modules/scoring/batches/{batchId}/recent", batch(batchId)))).items;

export const claimImageURL = (claimToken: string) => `${prefix}/claims/${encodeURIComponent(claimToken)}/image`;
export const claimThumbnailURL = (claimToken: string) => `${prefix}/claims/${encodeURIComponent(claimToken)}/thumbnail`;
export const anchorImageURL = (batchId: string, anchorId: number) =>
  `${prefix}/batches/${encodeURIComponent(batchId)}/anchors/${anchorId}/image`;

export const listSources = async () =>
  (await call(client.GET("/api/v1/modules/scoring/admin/sources"))).items.map((s) => s.name);
export const listAdminBatches = async () => (await call(client.GET("/api/v1/modules/scoring/admin/batches"))).items;
export const createBatch = (body: ScoringBatchInput) =>
  call(client.POST("/api/v1/modules/scoring/admin/batches", { body }));
export const updateBatch = (batchId: string, body: ScoringBatchPatch) =>
  call(client.PATCH("/api/v1/modules/scoring/admin/batches/{batchId}", { ...batch(batchId), body }));
export const deleteBatch = (batchId: string) =>
  call(client.DELETE("/api/v1/modules/scoring/admin/batches/{batchId}", batch(batchId)));
export const batchStats = async (batchId: string) =>
  (await call(client.GET("/api/v1/modules/scoring/admin/batches/{batchId}/stats", batch(batchId)))).items;
export const coverURL = (batchId: string) => `${prefix}/admin/batches/${encodeURIComponent(batchId)}/cover`;
export const exportURL = (batchId: string) => `${prefix}/admin/batches/${encodeURIComponent(batchId)}/export`;
export const listRaters = async () => (await call(client.GET("/api/v1/modules/scoring/admin/raters"))).items;
export const setRater = (accountId: string, chosen: boolean) =>
  chosen
    ? call(client.PUT("/api/v1/modules/scoring/admin/raters/{accountId}", account(accountId)))
    : call(client.DELETE("/api/v1/modules/scoring/admin/raters/{accountId}", account(accountId)));

const messages: Record<string, string> = {
  module_not_installed: "照片打分没有安装在这台设备上",
  module_unavailable: "打分服务没有运行",
  scoring_unavailable: "打分服务没有运行",
  not_rater: "管理员没有选你为打分人",
  rating_locked: "只能改上一张的分数",
  validation_failed: "填写的内容不符合要求",
  already_rated: "这张照片你已经打过分了",
  batch_importing: "批次还在导入",
  batch_in_use: "批次已有打分，不能删除",
  photos_folder_missing: "共享空间里没有“照片打分”文件夹",
};

export function scoringError(caught: unknown): string {
  if (caught instanceof APIError) {
    const message = messages[caught.code];
    if (message) return message;
  }
  return errorText(caught);
}

// retryable tells answers worth sending again from ones the server refused.
export function retryable(caught: unknown): boolean {
  return !(caught instanceof APIError) || caught.status === 0 || caught.status >= 500;
}
