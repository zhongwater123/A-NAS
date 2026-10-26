import { APIError, call, client, rawBody } from "../api";

export type { FeedbackComment, FeedbackImage, FeedbackItem } from "../api.generated";

// Limits the server enforces; the editor checks them first to explain the problem.
export const MAX_IMAGES = 6;
export const MAX_IMAGE_BYTES = 10 * 1024 * 1024;
export const MAX_TEXT = 5000;
export const MAX_COMMENT_IMAGES = 3;
export const MAX_COMMENT_TEXT = 2000;
export const IMAGE_TYPES = ["image/png", "image/jpeg", "image/gif", "image/webp"];

const item = (feedbackId: string) => ({ params: { path: { feedbackId } } });
const comment = (feedbackId: string, commentId: string) => ({ params: { path: { feedbackId, commentId } } });

export const listFeedback = async () => (await call(client.GET("/api/v1/feedback"))).items;

export const createFeedback = (text: string, images: Blob[]) =>
  call(client.POST("/api/v1/feedback", rawBody(form(text, images))));

// editFeedback replaces the text, keeps the listed images and adds new ones;
// it fails with feedback_changed when someone edited the item after version.
export const editFeedback = (feedbackId: string, version: number, text: string, keep: string[], images: Blob[]) =>
  call(
    client.PUT("/api/v1/feedback/{feedbackId}", { ...item(feedbackId), ...rawBody(edit(version, text, keep, images)) }),
  );

export const resolveFeedback = (feedbackId: string, resolved: boolean) =>
  call(client.PATCH("/api/v1/feedback/{feedbackId}", { ...item(feedbackId), body: { resolved } }));

export const deleteFeedback = (feedbackId: string) =>
  call(client.DELETE("/api/v1/feedback/{feedbackId}", item(feedbackId)));

// Comment writes answer with the whole item, which replaces the one in the list.
export const addComment = (feedbackId: string, text: string, images: Blob[]) =>
  call(client.POST("/api/v1/feedback/{feedbackId}/comments", { ...item(feedbackId), ...rawBody(form(text, images)) }));

// editComment fails with comment_changed when someone edited the comment after version.
export const editComment = (
  feedbackId: string,
  commentId: string,
  version: number,
  text: string,
  keep: string[],
  images: Blob[],
) =>
  call(
    client.PUT("/api/v1/feedback/{feedbackId}/comments/{commentId}", {
      ...comment(feedbackId, commentId),
      ...rawBody(edit(version, text, keep, images)),
    }),
  );

export const deleteComment = (feedbackId: string, commentId: string) =>
  call(client.DELETE("/api/v1/feedback/{feedbackId}/comments/{commentId}", comment(feedbackId, commentId)));

// Images of the item and of its comments share one address.
export const feedbackImageURL = (feedbackId: string, imageId: string) =>
  `/api/v1/feedback/${encodeURIComponent(feedbackId)}/images/${encodeURIComponent(imageId)}`;

function form(text: string, images: Blob[]) {
  const body = new FormData();
  body.set("text", text);
  images.forEach((image, index) => body.append("image", image, `image-${index + 1}`));
  return body;
}

function edit(version: number, text: string, keep: string[], images: Blob[]) {
  const body = form(text, images);
  body.set("version", String(version));
  for (const imageId of keep) body.append("keep", imageId);
  return body;
}

// describeFeedbackError explains a failed request in the panel's words, for
// an item or for a comment.
export function describeFeedbackError(error: unknown, about: "item" | "comment" = "item"): string {
  if (!(error instanceof APIError)) return "操作失败，请稍后再试";
  const comment = about === "comment";
  switch (error.code) {
    case "feedback_empty":
      return comment ? "写几句评论，或添加一张图片" : "写几句描述，或添加一张图片";
    case "text_too_long":
      return `文字最多 ${comment ? MAX_COMMENT_TEXT : MAX_TEXT} 字`;
    case "too_many_images":
      return comment ? `每条评论最多 ${MAX_COMMENT_IMAGES} 张图片` : `每条反馈最多 ${MAX_IMAGES} 张图片`;
    case "comment_changed":
      return "其他人刚刚修改了这条评论";
    case "comment_not_found":
      return "这条评论已被删除";
    case "image_too_large":
      return "单张图片不能超过 10 MiB";
    case "unsupported_image":
      return "只支持 PNG、JPEG、GIF 或 WebP 图片";
    case "feedback_changed":
      return "其他人刚刚修改了这条反馈";
    case "not_found":
      return "这条反馈已被删除";
    case "network_error":
      return "网络连接中断";
    default:
      return error.status >= 500 ? "服务暂时不可用，请稍后再试" : error.message || "操作失败，请稍后再试";
  }
}
