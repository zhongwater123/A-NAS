// 网页搜索: the switches, a search of each tab, and SearXNG's license.
import type {
  WebSearchCategory,
  WebSearchLicense,
  WebSearchPage,
  WebSearchQuery,
  WebSearchSettings,
  WebSearchStatus,
} from "../../api.generated";
import { apiError, http } from "../server";

const engines: WebSearchStatus["engines"] = {
  web: ["百度", "必应", "搜狗"],
  images: ["百度图片"],
  videos: ["哔哩哔哩", "YouTube"],
  news: ["搜狗微信"],
  torrents: ["海盗湾"],
};

// webSearchStatus is the switches as an administrator left them.
export function webSearchStatus(enabled = true, torrents = false): WebSearchStatus {
  const categories: WebSearchCategory[] = [
    "web",
    "images",
    "videos",
    "news",
    ...(torrents ? ["torrents" as const] : []),
  ];
  return {
    enabled,
    running: enabled ? "running" : "stopped",
    torrents,
    categories: enabled ? categories : [],
    engines,
  };
}

// pageFor answers a query of a tab with two results, or a page an option names.
export function pageFor(query: WebSearchQuery): WebSearchPage {
  const page = query.page ?? 1;
  const base = { answers: [], suggestions: page === 1 ? [`${query.q} 是什么`] : [], unresponsive: ["百度"] };
  const results = [1, 2].map((n) => {
    const number = (page - 1) * 2 + n;
    const common = {
      url: `https://example.org/${query.category}/${number}`,
      title: `${query.q} 第 ${number} 条`,
      domain: "example.org",
      engines: [engines[query.category][0]],
    };
    switch (query.category) {
      case "videos":
        return {
          ...common,
          platform: "哔哩哔哩",
          author: "示例作者",
          durationSeconds: 233,
          thumbnail: `/api/v1/web-search/thumbnail?url=v${number}&h=x`,
        };
      case "images":
        return {
          ...common,
          width: 923,
          height: 880,
          source: "example.org",
          thumbnail: `/api/v1/web-search/thumbnail?url=i${number}&h=x`,
        };
      case "torrents":
        return { ...common, magnet: `magnet:?xt=urn:btih:${number}`, sizeBytes: 928702791, seeders: 3, leechers: 0 };
      default:
        return { ...common, content: "示例摘要", publishedAt: "2026-10-25T06:59:20Z" };
    }
  });
  return {
    ...base,
    results,
    ...(page === 1 && query.category === "web"
      ? { infobox: { title: query.q, content: "百科摘要", url: "https://example.org/wiki" } }
      : {}),
  };
}

export interface WebSearchOptions {
  status?: WebSearchStatus;
  // answer replaces pageFor, or returns an error code with its status.
  answer?: (query: WebSearchQuery) => WebSearchPage | { status: 403 | 429 | 503; code: string };
  // license is the release's notice; null is a release without the runtime.
  license?: WebSearchLicense | null;
}

export const searxngLicense: WebSearchLicense = {
  name: "SearXNG",
  version: "2026.10.30+1cd13d98d",
  commit: "1cd13d98d65a58f1126fa5bb5c77e42c0ea625f3",
  license: "AGPL-3.0-or-later",
  packages: [{ name: "flask", version: "3.1.3", license: "BSD-3-Clause" }],
  source: true,
};

// webSearchHandlers answers 网页搜索 and records what the window and the
// settings sent.
export function webSearchHandlers(options: WebSearchOptions = {}) {
  let status = options.status ?? webSearchStatus();
  const sent = { queries: [] as WebSearchQuery[], settings: [] as WebSearchSettings[], urls: [] as string[] };
  const handlers = [
    http.get("/api/v1/web-search", ({ response }) => response(200).json(status)),
    http.put("/api/v1/web-search", async ({ request, response }) => {
      const body = await request.json();
      sent.settings.push(body);
      status = webSearchStatus(body.enabled, body.torrents);
      return response(200).json(status);
    }),
    http.post("/api/v1/web-search/query", async ({ request, response }) => {
      const query = await request.json();
      sent.queries.push(query);
      sent.urls.push(request.url);
      const answer = options.answer?.(query) ?? pageFor(query);
      if ("code" in answer) return response(answer.status).json(apiError(answer.code));
      return response(200).json(answer);
    }),
    http.get("/api/v1/web-search/license", ({ response }) =>
      options.license === null
        ? response(404).json(apiError("not_found"))
        : response(200).json(options.license ?? searxngLicense),
    ),
  ];
  return { sent, handlers };
}
