import { call, client } from "../api/client";
import type {
  WebSearchCategory,
  WebSearchLicense,
  WebSearchPage,
  WebSearchQuery,
  WebSearchSettings,
  WebSearchStatus,
} from "../api.generated";

export type {
  WebSearchCategory,
  WebSearchLicense,
  WebSearchPage,
  WebSearchQuery,
  WebSearchResult,
  WebSearchStatus,
} from "../api.generated";

export const readWebSearch = (): Promise<WebSearchStatus> => call(client.GET("/api/v1/web-search"));

export const updateWebSearch = (settings: WebSearchSettings): Promise<WebSearchStatus> =>
  call(client.PUT("/api/v1/web-search", { body: settings }));

// The search text goes in the body, so no access log records it.
export const searchWeb = (query: WebSearchQuery, signal?: AbortSignal): Promise<WebSearchPage> =>
  call(client.POST("/api/v1/web-search/query", { body: query, signal }));

export const readWebSearchLicense = (): Promise<WebSearchLicense> => call(client.GET("/api/v1/web-search/license"));

export const licenseTextURL = "/api/v1/web-search/license/text";
export const sourceURL = "/api/v1/web-search/source";

export const categoryLabels: Record<WebSearchCategory, string> = {
  web: "网页",
  images: "图片",
  videos: "视频",
  news: "资讯",
  torrents: "种子",
};
