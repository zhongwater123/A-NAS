import { APIError, call, client, failure, normalizeAPIError } from "../api/client";
import type {
  WebSearchCategory,
  WebSearchLicense,
  WebSearchQuery,
  WebSearchSettings,
  WebSearchStatus,
  WebSearchUpdate,
} from "../api.generated";

export type {
  WebSearchAnswer,
  WebSearchCategory,
  WebSearchInfobox,
  WebSearchLicense,
  WebSearchQuery,
  WebSearchResult,
  WebSearchStatus,
  WebSearchUpdate,
} from "../api.generated";

export const readWebSearch = (): Promise<WebSearchStatus> => call(client.GET("/api/v1/web-search"));

export const updateWebSearch = (settings: WebSearchSettings): Promise<WebSearchStatus> =>
  call(client.PUT("/api/v1/web-search", { body: settings }));

// searchWeb runs one search, handing onUpdate each part as the tab's engines
// answer (one JSON object a line); it ends with the last, which has no engine
// pending. The search text goes in the body, so no access log records it.
export async function searchWeb(
  query: WebSearchQuery,
  signal: AbortSignal,
  onUpdate: (update: WebSearchUpdate) => void,
): Promise<void> {
  const { data, error, response } = await client
    .POST("/api/v1/web-search/query", { body: query, signal, parseAs: "stream" })
    .catch((caught: unknown) => {
      throw normalizeAPIError(caught);
    });
  if (!response.ok || !data) throw failure(response.status, error);
  const reader = data.getReader();
  const decoder = new TextDecoder();
  let text = "";
  let last: WebSearchUpdate | undefined;
  const take = (line: string) => {
    if (!line.trim()) return;
    last = JSON.parse(line) as WebSearchUpdate;
    onUpdate(last);
  };
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      text += decoder.decode(value, { stream: true });
      const lines = text.split("\n");
      text = lines.pop() ?? "";
      lines.forEach(take);
    }
    take(text + decoder.decode());
  } catch (caught) {
    if (signal.aborted) throw caught;
    throw normalizeAPIError(caught);
  }
  // The connection ended before the engines all answered.
  if (!last || last.pending > 0) throw new APIError(0, "search_interrupted", "搜索中断，请重试");
}

export const readWebSearchLicense = (): Promise<WebSearchLicense> => call(client.GET("/api/v1/web-search/license"));

export const licenseTextURL = "/api/v1/web-search/license/text";
export const sourceURL = "/api/v1/web-search/source";

export const categoryLabels: Record<WebSearchCategory, string> = {
  web: "网页",
  images: "图片",
  videos: "视频",
  news: "资讯",
  music: "音乐",
  map: "地图",
  science: "学术",
  it: "IT",
  social: "社交",
  torrents: "种子",
};

// Every tab, in the window's order.
export const allCategories = Object.keys(categoryLabels) as WebSearchCategory[];
