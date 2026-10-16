import { apiFetch, request, type Profile } from "../api";

export interface ShareEntry { id: string; path: string; isDir: boolean; size: number }
export interface Recipient { userId: string; viewedAt?: string; readThrough: number; senderReadThrough: number; saved: boolean; savedFiles: number; saveUncertain?: boolean; lastMessage: number }
export interface Share { id: string; sender: { id: string; username: string }; status: string; createdAt: string; publishedAt?: string; expiresAt?: string; entries: ShareEntry[]; recipients: Recipient[]; error?: string; preparedBytes: number }
export interface Message { id: number; sender: { id: string; username: string }; text: string; createdAt: string }
export interface Job { id: string; shareId: string; status: string; bytesDone: number; bytesTotal: number; completedIds: string[]; entryIds: string[]; error?: string }
export interface ShareNotification { id: number; shareId: string; recipientId: string; kind: string; messageId?: number; createdAt: string }
interface Page<T> { items: T[] | null; nextOffset?: number }
const base = "/api/v1/shares";
const path = (id: string) => `${base}/${encodeURIComponent(id)}`;
const post = <T,>(url: string, body: unknown = {}) => request<T>(url, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) }, true);
export const listMembers = async () => (await request<Page<Profile>>(`${base}/members`)).items ?? [];
export const listShares = (direction: string, offset = 0) => request<Page<Share>>(`${base}?direction=${direction}&offset=${offset}&limit=100`);
export const getShare = (id: string) => request<Share>(path(id));
export const viewShare = (id: string) => post<Share>(`${path(id)}/view`);
export const createShare = (body: { requestId: string; sourceIds: string[]; recipientIds: string[]; message: string }) => post<Share>(base, body);
export const revokeShare = (id: string) => post<void>(`${path(id)}/revoke`);
export const listMessages = (id: string, recipientId: string, after = 0) => request<Page<Message>>(`${path(id)}/messages?recipientId=${encodeURIComponent(recipientId)}&after=${after}&limit=100`);
export const sendMessage = (id: string, body: { recipientId: string; requestId: string; text: string }) => post<Message>(`${path(id)}/messages`, body);
export const markRead = (id: string, recipientId: string, through: number) => post<void>(`${path(id)}/read`, { recipientId, through });
export const saveShare = (id: string, body: { requestId: string; entryIds: string[]; target: { rootId: string; parentId: string } }) => post<Job>(`${path(id)}/save`, body);
export const listJobs = async (id: string) => (await request<Page<Job>>(`${path(id)}/jobs`)).items ?? [];
export const listShareNotifications = async (after: number) => (await request<Page<ShareNotification>>(`${base}/notifications?after=${after}&limit=100`)).items ?? [];
export const contentURL = (id: string, entry: ShareEntry, preview = false) => `${path(id)}/entries/${encodeURIComponent(entry.id)}/${preview ? "preview" : "content"}`;
export async function readText(id: string, entry: ShareEntry, signal: AbortSignal) {
  // A bounded request avoids loading an arbitrary large text file into the UI.
  const response = await apiFetch(contentURL(id, entry), { signal, credentials: "same-origin", headers: { Range: "bytes=0-262143" } });
  if (!response.ok) throw new Error(`无法预览文件 (${response.status})`);
  if (!response.body) return "";
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let result = "", size = 0;
  try {
    while (size < 262144) {
      const { done, value } = await reader.read();
      if (done) break;
      const part = value.subarray(0, 262144 - size);
      result += decoder.decode(part, { stream: true }); size += part.length;
    }
    return result + decoder.decode();
  } finally { await reader.cancel(); }
}
export const isActive = (share: Share) => ["published", "active"].includes(share.status) && (!share.expiresAt || new Date(share.expiresAt).getTime() > Date.now());
export const statusLabel = (share: Share) => ({ preparing: "正在准备副本", published: "分享中", active: "分享中", revoked: "已撤回", failed: "准备失败", expired: "已到期" }[share.status] ?? share.status);
export const errorText = (error: unknown) => error instanceof Error ? error.message : "请求失败，请重试";
export const requestKey = () => crypto.randomUUID();
