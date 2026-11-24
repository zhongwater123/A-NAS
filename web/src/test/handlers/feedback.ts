// 问题反馈: the list every account shares, with each item's comments. Writes
// send a FormData, which a handler reads back with formOf(request).
import type { FeedbackComment, FeedbackItem } from "../../api.generated";
import { http } from "../server";

// feedbackItem is an open item without images or comments. Its author's ID
// follows the fixtures' accounts, user:<name>.
export function feedbackItem(id: string, overrides: Partial<FeedbackItem> = {}): FeedbackItem {
  const createdBy = overrides.createdBy ?? "owner";
  return {
    id,
    text: "",
    images: [],
    version: 1,
    createdBy,
    createdById: `user:${createdBy}`,
    createdAt: "2026-10-12T07:00:00Z",
    resolved: false,
    comments: [],
    ...overrides,
  };
}

// feedbackComment is a comment without images.
export function feedbackComment(id: string, overrides: Partial<FeedbackComment> = {}): FeedbackComment {
  const createdBy = overrides.createdBy ?? "owner";
  return {
    id,
    text: "",
    images: [],
    version: 1,
    createdBy,
    createdById: `user:${createdBy}`,
    createdAt: "2026-10-12T07:00:00Z",
    ...overrides,
  };
}

// feedbackHandlers answers the list from items, read on every request so a
// test changes it as the server would. Writes are left to the test, which
// sees what it is sent.
export function feedbackHandlers(items: () => FeedbackItem[]) {
  return [http.get("/api/v1/feedback", ({ response }) => response(200).json({ items: items() }))];
}
