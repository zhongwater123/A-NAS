// 家庭中心's calls to /api/v1/family. Photos and videos go into the shared
// photo library through the photo API first; records name them by asset ID.

import { APIError, call, client, rawBody, uploadForm } from "../api/client";
import { errorText } from "../api/errorMessages";
import type {
  FamilyAlbumPage,
  FamilyBaby,
  FamilyBabyFields,
  FamilyCatalog,
  FamilyComment,
  FamilyGrowth,
  FamilyHint,
  FamilyMe,
  FamilyMember,
  FamilyOverview,
  FamilyReaction,
  FamilyReactionName,
  FamilyRecord,
  FamilyRecordInput,
  FamilyRecordPage,
  FamilyTimelineEntry,
  FamilyVaccineSchedule,
} from "../api.generated";

export type {
  FamilyAlbumPage,
  FamilyBaby,
  FamilyBabyFields,
  FamilyCatalog,
  FamilyComment,
  FamilyGrowth,
  FamilyHint,
  FamilyMe,
  FamilyMember,
  FamilyOverview,
  FamilyReaction,
  FamilyReactionName,
  FamilyRecord,
  FamilyRecordInput,
  FamilyRecordPage,
  FamilyTimelineEntry,
  FamilyVaccineSchedule,
};
export type FamilyKind = FamilyRecord["kind"];
export type FamilyScheduledDose = FamilyVaccineSchedule["doses"][number];
export type FamilyCatalogVaccine = FamilyCatalog["vaccines"]["vaccines"][number];

const baby = (babyId: string) => ({ params: { path: { babyId } } });
const record = (recordId: string) => ({ params: { path: { recordId } } });
const account = (accountId: string) => ({ params: { path: { accountId } } });
const id = encodeURIComponent;

export const getFamily = () => call(client.GET("/api/v1/family"));
export const getCatalog = () => call(client.GET("/api/v1/family/catalog"));
export const joinFamily = (role: string) => call(client.POST("/api/v1/family/join", { body: { role } }));
export const cancelJoin = () => call(client.DELETE("/api/v1/family/join"));
export const leaveFamily = () => call(client.POST("/api/v1/family/leave"));
export const setRole = (role: string) => call(client.PATCH("/api/v1/family/me", { body: { role } }));
export const approveJoin = (accountId: string) =>
  call(client.POST("/api/v1/family/requests/{accountId}/approve", account(accountId)));
export const rejectJoin = (accountId: string) =>
  call(client.DELETE("/api/v1/family/requests/{accountId}", account(accountId)));
export const setGuardian = (accountId: string, guardian: boolean) =>
  call(client.PATCH("/api/v1/family/members/{accountId}", { ...account(accountId), body: { guardian } }));
export const removeMember = (accountId: string) =>
  call(client.DELETE("/api/v1/family/members/{accountId}", account(accountId)));
export const listHints = async () => (await call(client.GET("/api/v1/family/hints"))).items;
export const markHintsSeen = (keys: string[]) => call(client.POST("/api/v1/family/hints/seen", { body: { keys } }));

export const addBaby = (fields: FamilyBabyFields) => call(client.POST("/api/v1/family/babies", { body: fields }));
export const updateBaby = (babyId: string, fields: FamilyBabyFields) =>
  call(client.PATCH("/api/v1/family/babies/{babyId}", { ...baby(babyId), body: fields }));
export const deleteBaby = (babyId: string, name: string) =>
  call(client.DELETE("/api/v1/family/babies/{babyId}", { params: { path: { babyId }, query: { name } } }));
export const restoreBaby = (babyId: string) =>
  call(client.POST("/api/v1/family/babies/{babyId}/restore", baby(babyId)));
export function setBabyAvatar(babyId: string, picture: Blob) {
  const form = new FormData();
  form.set("file", picture);
  return call(client.PUT("/api/v1/family/babies/{babyId}/avatar", { ...baby(babyId), ...rawBody(form) }));
}
export const removeBabyAvatar = (babyId: string) =>
  call(client.DELETE("/api/v1/family/babies/{babyId}/avatar", baby(babyId)));
// The avatar's URL carries its version, so a new one is never served from
// the cache of the old.
export const babyAvatarURL = (who: Pick<FamilyBaby, "id" | "avatarVersion">) =>
  `/api/v1/family/babies/${id(who.id)}/avatar?v=${who.avatarVersion}`;

export const listRecords = (babyId: string, cursor = "", kind?: FamilyKind) =>
  call(
    client.GET("/api/v1/family/babies/{babyId}/records", {
      params: { path: { babyId }, query: { ...(cursor ? { cursor } : {}), ...(kind ? { kind } : {}) } },
    }),
  );
export const addRecord = (babyId: string, input: FamilyRecordInput) =>
  call(client.POST("/api/v1/family/babies/{babyId}/records", { ...baby(babyId), body: input }));
export const updateRecord = (recordId: string, input: FamilyRecordInput) =>
  call(client.PATCH("/api/v1/family/records/{recordId}", { ...record(recordId), body: input }));
export const getRecord = (recordId: string) => call(client.GET("/api/v1/family/records/{recordId}", record(recordId)));
export const deleteRecord = (recordId: string) =>
  call(client.DELETE("/api/v1/family/records/{recordId}", record(recordId)));
export const restoreRecord = (recordId: string) =>
  call(client.POST("/api/v1/family/records/{recordId}/restore", record(recordId)));
export const listTrash = async (babyId: string) =>
  (await call(client.GET("/api/v1/family/babies/{babyId}/trash", baby(babyId)))).items;
export const addComment = (recordId: string, text: string, requestKey: string) =>
  call(client.POST("/api/v1/family/records/{recordId}/comments", { ...record(recordId), body: { text, requestKey } }));
export const deleteComment = (commentId: string) =>
  call(client.DELETE("/api/v1/family/comments/{commentId}", { params: { path: { commentId } } }));
// react leaves the caller's reaction on a record, or takes it back, and
// returns the record's reactions.
export async function react(recordId: string, reaction: FamilyReactionName, on: boolean) {
  const where = { params: { path: { recordId, reaction } } };
  const path = "/api/v1/family/records/{recordId}/reactions/{reaction}" as const;
  return (await call(on ? client.PUT(path, where) : client.DELETE(path, where))).items;
}
export const listWall = async (babyId: string) =>
  (await call(client.GET("/api/v1/family/babies/{babyId}/wall", baby(babyId)))).items;
export const addWallMessage = (babyId: string, text: string, requestKey: string) =>
  call(client.POST("/api/v1/family/babies/{babyId}/wall", { ...baby(babyId), body: { text, requestKey } }));

export const getAlbum = (babyId: string, cursor = "") =>
  call(
    client.GET("/api/v1/family/babies/{babyId}/album", {
      params: { path: { babyId }, query: cursor ? { cursor } : {} },
    }),
  );
export const getGrowth = (babyId: string) => call(client.GET("/api/v1/family/babies/{babyId}/growth", baby(babyId)));
export const getVaccines = (babyId: string) =>
  call(client.GET("/api/v1/family/babies/{babyId}/vaccines", baby(babyId)));
export const setVaccinePlan = (babyId: string, codes: string[]) =>
  call(client.PUT("/api/v1/family/babies/{babyId}/vaccine-plan", { ...baby(babyId), body: { codes } }));
export const listMilestones = async (babyId: string) =>
  (await call(client.GET("/api/v1/family/babies/{babyId}/milestones", baby(babyId)))).items;

// addRecording streams a recording, its text fields first, reporting upload
// progress.
export function addRecording(
  babyId: string,
  file: Blob,
  fileName: string,
  fields: { title: string; occurredOn: string; requestKey: string },
  onProgress: (fraction: number) => void = () => undefined,
) {
  const form = new FormData();
  form.set("requestKey", fields.requestKey);
  form.set("title", fields.title);
  form.set("occurredOn", fields.occurredOn);
  form.set("file", file, fileName);
  return uploadForm<FamilyRecord>(`/api/v1/family/babies/${id(babyId)}/recordings`, form, onProgress);
}
export const audioURL = (recordId: string) => `/api/v1/family/records/${id(recordId)}/audio`;

// requestKey names one attempt, so a retried request does not add twice.
export const requestKey = () => crypto.randomUUID();

// CustomVaccineCode records a dose of a vaccine the catalog does not list.
export const CustomVaccineCode = "custom";

// The family center's own error codes, in its words. They stay here rather
// than in api/errorMessages.ts, which the desktop loads before any window.
const familyMessages: Record<string, string> = {
  not_family_member: "加入家庭后才能看到这里",
  forbidden: "只有家长或添加它的人可以这样做",
  last_guardian: "家庭里至少要有一位家长，请先把另一位家人设为家长",
  changed: "已经有人先保存了修改，请关闭后重新打开再改",
  conflict: "这和现有的内容冲突，例如这个大事记已经配过说明，或有记录早于新的出生日",
  validation_failed: "填写的内容不符合要求，请检查日期和数值",
  photos_unavailable: "相册暂时不可用，请稍后再试照片相关的内容",
  family_unavailable: "家庭中心暂时不可用，请稍后再试",
  too_large: "文件太大了",
  unsupported_image: "请选择 JPEG、PNG、GIF 或 WebP 图片",
  media_unavailable: "这段录音暂时不能播放",
  not_found: "内容已不存在",
};

// familyError explains a failed request in the family center's words.
export function familyError(error: unknown): string {
  if (error instanceof APIError && Object.hasOwn(familyMessages, error.code)) return familyMessages[error.code];
  return errorText(error);
}
