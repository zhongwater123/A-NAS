import { apiFetch, call, client, failure } from "../api/client";
import type { CreateMemberShare, MemberShareEntry, operations, SaveMemberShare } from "../api.generated";

export type ShareEntry = MemberShareEntry;
export type {
  MemberShare as Share,
  MemberShareJob as Job,
  MemberShareMessage as Message,
  MemberShareNotification as ShareNotification,
  MemberShareRecipient as Recipient,
} from "../api.generated";

type Direction = NonNullable<NonNullable<operations["listMemberShares"]["parameters"]["query"]>["direction"]>;

const share = (shareId: string) => ({ params: { path: { shareId } } });

export const listMembers = async () => (await call(client.GET("/api/v1/shares/members"))).items;
export const listShares = (direction: Direction, offset = 0) =>
  call(client.GET("/api/v1/shares", { params: { query: { direction, offset, limit: 100 } } }));
export const getShare = (shareId: string) => call(client.GET("/api/v1/shares/{shareId}", share(shareId)));
export const viewShare = (shareId: string) => call(client.POST("/api/v1/shares/{shareId}/view", share(shareId)));
export const createShare = (body: CreateMemberShare) => call(client.POST("/api/v1/shares", { body }));
export const revokeShare = (shareId: string) => call(client.POST("/api/v1/shares/{shareId}/revoke", share(shareId)));
export const listMessages = (shareId: string, recipientId: string, after = 0) =>
  call(
    client.GET("/api/v1/shares/{shareId}/messages", {
      params: { path: { shareId }, query: { recipientId, after, limit: 100 } },
    }),
  );
export const sendMessage = (shareId: string, body: { recipientId: string; requestId: string; text: string }) =>
  call(client.POST("/api/v1/shares/{shareId}/messages", { ...share(shareId), body }));
export const markRead = (shareId: string, recipientId: string, through: number) =>
  call(client.POST("/api/v1/shares/{shareId}/read", { ...share(shareId), body: { recipientId, through } }));
export const saveShare = (shareId: string, body: SaveMemberShare) =>
  call(client.POST("/api/v1/shares/{shareId}/save", { ...share(shareId), body }));
export const listJobs = async (shareId: string) =>
  (await call(client.GET("/api/v1/shares/{shareId}/jobs", share(shareId)))).items;
export const listShareNotifications = async (after: number) =>
  (await call(client.GET("/api/v1/shares/notifications", { params: { query: { after, limit: 100 } } }))).items;
export const contentURL = (shareId: string, entry: ShareEntry, preview = false) =>
  `/api/v1/shares/${encodeURIComponent(shareId)}/entries/${encodeURIComponent(entry.id)}/${preview ? "preview" : "content"}`;
export async function readText(id: string, entry: ShareEntry, signal: AbortSignal) {
  // A bounded request avoids loading an arbitrary large text file into the UI.
  const response = await apiFetch(contentURL(id, entry), {
    signal,
    credentials: "same-origin",
    headers: { Range: "bytes=0-262143" },
  });
  if (!response.ok) throw failure(response.status, await response.json().catch(() => undefined));
  if (!response.body) return "";
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let result = "",
    size = 0;
  try {
    while (size < 262144) {
      const { done, value } = await reader.read();
      if (done) break;
      const part = value.subarray(0, 262144 - size);
      result += decoder.decode(part, { stream: true });
      size += part.length;
    }
    return result + decoder.decode();
  } finally {
    await reader.cancel();
  }
}
export { requestKey } from "../api/requestKey";
