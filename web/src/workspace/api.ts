import { call, client, rawBody } from "../api/client";
import type { components } from "../api.generated";

export type WorkspacePage = components["schemas"]["WorkspacePage"];
export type WorkspacePageSummary = components["schemas"]["WorkspacePageSummary"];
export type WorkspaceVersion = components["schemas"]["WorkspaceVersion"];
export type WorkspaceSelection = components["schemas"]["WorkspaceSelection"];
export type WorkspaceMemberAccount = components["schemas"]["WorkspaceMemberAccount"];
export type AgentToken = components["schemas"]["AgentToken"];
export type AgentKind = components["schemas"]["AgentKind"];

const page = (pageId: string) => ({ params: { path: { pageId } } });
// A page is sent as its file: the contract types text/html as a string.
const html = (file: Blob) => ({ ...rawBody(file), headers: { "Content-Type": "text/html" } });

export async function listPages(signal?: AbortSignal): Promise<WorkspacePageSummary[]> {
  return (await call(client.GET("/api/v1/workspace/pages", { signal }))).items;
}

export const getPage = (pageId: string, signal?: AbortSignal) =>
  call(client.GET("/api/v1/workspace/pages/{pageId}", { ...page(pageId), signal }));

export const createPage = (file: File) =>
  call(client.POST("/api/v1/workspace/pages", { params: { query: { name: file.name } }, ...html(file) }));

// saveVersion uploads file as the version after baseVersion; another version
// saved since answers 409 page_changed.
export const saveVersion = (pageId: string, baseVersion: number, file: File) =>
  call(
    client.POST("/api/v1/workspace/pages/{pageId}/versions", {
      params: { path: { pageId }, query: { baseVersion } },
      ...html(file),
    }),
  );

export const renamePage = (pageId: string, title: string) =>
  call(client.PATCH("/api/v1/workspace/pages/{pageId}", { ...page(pageId), body: { title } }));

export const deletePage = (pageId: string) => call(client.DELETE("/api/v1/workspace/pages/{pageId}", page(pageId)));

const versionPath = (pageId: string, version: number) =>
  `/api/v1/workspace/pages/${encodeURIComponent(pageId)}/versions/${version}`;

// viewURL is the page as its sandboxed frame shows it: highlight points at
// the elements a change touched, scroll keeps the reader's place.
export function viewURL(pageId: string, version: number, options: { highlight?: number[]; scroll?: number } = {}) {
  const query = new URLSearchParams();
  if (options.highlight?.length) query.set("highlight", options.highlight.join(","));
  if (options.scroll) query.set("scroll", String(Math.round(options.scroll)));
  const rest = query.toString();
  return `${versionPath(pageId, version)}/view${rest ? `?${rest}` : ""}`;
}

// fileURL downloads a version as the file it was saved as.
export const fileURL = (pageId: string, version: number) => `${versionPath(pageId, version)}/file`;

export const reportViewing = (pageId: string, version: number) =>
  call(client.PUT("/api/v1/workspace/viewing", { body: { pageId, version } }));

export const stopViewing = (pageId: string) => call(client.DELETE("/api/v1/workspace/viewing/{pageId}", page(pageId)));

export const setSelection = (selection: WorkspaceSelection) =>
  call(client.PUT("/api/v1/workspace/selection", { body: selection }));

export const clearSelection = () => call(client.DELETE("/api/v1/workspace/selection"));

export async function listMembers(signal?: AbortSignal): Promise<WorkspaceMemberAccount[]> {
  return (await call(client.GET("/api/v1/workspace/admin/members", { signal }))).items;
}

const account = (accountId: string) => ({ params: { path: { accountId } } });
export const addMember = (accountId: string) =>
  call(client.PUT("/api/v1/workspace/admin/members/{accountId}", account(accountId)));
export const removeMember = (accountId: string) =>
  call(client.DELETE("/api/v1/workspace/admin/members/{accountId}", account(accountId)));

// The account's agent tokens are the Product Service's own (ADR 0040);
// 设置 → 我的账号 lists them too, through ./agentTokens.
export async function listAgentTokens(signal?: AbortSignal): Promise<AgentToken[]> {
  return (await call(client.GET("/api/v1/session/agent-tokens", { signal }))).items;
}
export const createAgentToken = (name: string, kind: AgentKind) =>
  call(client.POST("/api/v1/session/agent-tokens", { body: { name, kind } }));
export const revokeAgentToken = (tokenId: string) =>
  call(client.DELETE("/api/v1/session/agent-tokens/{tokenId}", { params: { path: { tokenId } } }));
