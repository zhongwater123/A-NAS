import { APIError, call, client } from "../api/client";

export const listSpaces = async () => (await call(client.GET("/api/v1/spaces"))).items;
export const listEntries = async (spaceId: string, parentId = "") =>
  (await call(client.GET("/api/v1/spaces/{spaceId}/entries", { params: { path: { spaceId }, query: { parentId } } })))
    .items;
export const createDirectory = (spaceId: string, parentId: string, name: string) =>
  call(
    client.POST("/api/v1/spaces/{spaceId}/directories", { params: { path: { spaceId } }, body: { parentId, name } }),
  );
// Without spaceId the target folder is in the entry's own space.
export const moveFile = (fileId: string, parentId: string, name: string, spaceId?: string) =>
  call(client.PATCH("/api/v1/files/{fileId}", { params: { path: { fileId } }, body: { spaceId, parentId, name } }));
export const copyFile = (fileId: string, parentId: string, name: string, spaceId?: string) =>
  call(
    client.POST("/api/v1/files/{fileId}/copies", { params: { path: { fileId } }, body: { spaceId, parentId, name } }),
  );
export const deleteFile = (fileId: string) =>
  call(client.DELETE("/api/v1/files/{fileId}", { params: { path: { fileId } } }));
export const fileDownloadURL = (fileId: string) => `/api/v1/files/${encodeURIComponent(fileId)}/content`;
export const fileRenderedPreviewURL = (fileId: string, size: "thumbnail" | "preview") =>
  `/api/v1/files/${encodeURIComponent(fileId)}/preview?size=${size}`;
export const filePreviewURL = (fileId: string) => `${fileDownloadURL(fileId)}?disposition=inline`;
export async function readFileTextPreview(fileId: string) {
  const { data, response } = await client.GET("/api/v1/files/{fileId}/content", {
    params: { path: { fileId } },
    parseAs: "text",
  });
  if (!response.ok) throw new APIError(response.status, "preview_failed", "无法读取文件预览");
  return data ?? "";
}
