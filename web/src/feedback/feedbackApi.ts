import { APIError, request } from "../api";

export interface FeedbackImage { id: string; contentType: string; sizeBytes: number }
export interface FeedbackItem {
  id: string;
  text: string;
  images: FeedbackImage[];
  // Counts edits to the text and images; an edit names the version it started from.
  version: number;
  createdBy: string;
  createdAt: string;
  updatedBy?: string;
  updatedAt?: string;
  resolved: boolean;
  resolvedBy?: string;
  resolvedAt?: string;
}

// Limits the server enforces; the editor checks them first to explain the problem.
export const MAX_IMAGES = 6;
export const MAX_IMAGE_BYTES = 10 * 1024 * 1024;
export const MAX_TEXT = 5000;
export const IMAGE_TYPES = ["image/png", "image/jpeg", "image/gif", "image/webp"];

const base = "/api/v1/feedback";
const itemPath = (id: string) => `${base}/${encodeURIComponent(id)}`;

export const listFeedback = async () => (await request<{ items: FeedbackItem[] }>(base)).items;

export function createFeedback(text: string, images: Blob[]) {
  return request<FeedbackItem>(base, { method: "POST", body: form(text, images) }, true);
}

// editFeedback replaces the text, keeps the listed images and adds new ones;
// it fails with feedback_changed when someone edited the item after version.
export function editFeedback(id: string, version: number, text: string, keep: string[], images: Blob[]) {
  const body = form(text, images);
  body.set("version", String(version));
  for (const imageId of keep) body.append("keep", imageId);
  return request<FeedbackItem>(itemPath(id), { method: "PUT", body }, true);
}

export const resolveFeedback = (id: string, resolved: boolean) =>
  request<FeedbackItem>(itemPath(id), { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ resolved }) }, true);

export const deleteFeedback = (id: string) => request<void>(itemPath(id), { method: "DELETE" }, true);

export const feedbackImageURL = (itemId: string, imageId: string) => `${itemPath(itemId)}/images/${encodeURIComponent(imageId)}`;

function form(text: string, images: Blob[]) {
  const body = new FormData();
  body.set("text", text);
  images.forEach((image, index) => body.append("image", image, `image-${index + 1}`));
  return body;
}

// describeFeedbackError explains a failed request in the panel's words.
export function describeFeedbackError(error: unknown): string {
  if (!(error instanceof APIError)) return "操作失败，请稍后再试";
  switch (error.code) {
    case "feedback_empty": return "写几句描述，或添加一张图片";
    case "text_too_long": return `文字最多 ${MAX_TEXT} 字`;
    case "too_many_images": return `每条反馈最多 ${MAX_IMAGES} 张图片`;
    case "image_too_large": return "单张图片不能超过 10 MB";
    case "unsupported_image": return "只支持 PNG、JPEG、GIF 或 WebP 图片";
    case "feedback_changed": return "其他人刚刚修改了这条反馈";
    case "not_found": return "这条反馈已被删除";
    case "network_error": return "网络连接中断";
    default: return error.status >= 500 ? "服务暂时不可用，请稍后再试" : error.message || "操作失败，请稍后再试";
  }
}
