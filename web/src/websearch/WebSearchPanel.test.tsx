import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setSession } from "../api/client";
import { session } from "../test/fixtures";
import { pageFor, type WebSearchOptions, webSearchHandlers, webSearchStatus } from "../test/handlers/websearch";
import { server } from "../test/server";
import { resetWebSearch } from "./status";
import { durationText, WebSearchPanel } from "./WebSearchPanel";

function serve(options: WebSearchOptions = {}) {
  const webSearch = webSearchHandlers(options);
  server.use(...webSearch.handlers);
  return webSearch.sent;
}

async function search(text: string) {
  const user = userEvent.setup();
  await user.type(await screen.findByRole("searchbox", { name: "搜索内容" }), text);
  await user.click(screen.getByRole("button", { name: "搜索" }));
  return user;
}

describe("网页搜索", () => {
  beforeEach(() => setSession(session()));
  afterEach(() => {
    setSession();
    resetWebSearch();
    vi.restoreAllMocks();
    // jsdom has no execCommand; the copy test puts one on the document.
    Reflect.deleteProperty(document, "execCommand");
  });

  it("searches the web with the text in the body and links out", async () => {
    const sent = serve();
    render(<WebSearchPanel />);
    await search("流浪地球");

    const results = await screen.findByRole("list", { name: "搜索结果" });
    const first = within(results).getByRole("link", { name: "流浪地球 第 1 条" });
    expect(first).toHaveAttribute("href", "https://example.org/web/1");
    expect(first).toHaveAttribute("target", "_blank");
    expect(first).toHaveAttribute("rel", "noopener noreferrer");
    expect(sent.queries).toEqual([{ q: "流浪地球", category: "web", page: 1 }]);
    expect(sent.urls[0]).not.toContain("%E6%B5%81");
    expect(screen.getByText("百度未响应")).toBeInTheDocument();
    expect(screen.getByRole("complementary", { name: "流浪地球" })).toHaveTextContent("百科摘要");

    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "更多结果" }));
    expect(await within(results).findByRole("link", { name: "流浪地球 第 3 条" })).toBeInTheDocument();
    expect(sent.queries.at(-1)).toEqual({ q: "流浪地球", category: "web", page: 2 });
  });

  it("searches the same text in another tab", async () => {
    const sent = serve();
    render(<WebSearchPanel />);
    const user = await search("周杰伦");
    await screen.findByRole("list", { name: "搜索结果" });

    await user.click(screen.getByRole("tab", { name: "视频" }));
    const videos = await screen.findByRole("list", { name: "视频结果" });
    expect(within(videos).getAllByText("3:53")).toHaveLength(2);
    expect(within(videos).getAllByText(/哔哩哔哩 · 示例作者/)).toHaveLength(2);
    expect(sent.queries.at(-1)).toEqual({ q: "周杰伦", category: "videos", page: 1 });

    // A tab searches what the box holds, even before Enter.
    const box = screen.getByRole("searchbox", { name: "搜索内容" });
    await user.clear(box);
    await user.type(box, "晴天");
    await user.click(screen.getByRole("tab", { name: "资讯" }));
    await user.selectOptions(await screen.findByRole("combobox", { name: "发布时间" }), "一周内");
    await screen.findByRole("list", { name: "搜索结果" });
    expect(sent.queries.at(-2)).toEqual({ q: "晴天", category: "news", page: 1 });
    expect(sent.queries.at(-1)).toEqual({ q: "晴天", category: "news", page: 1, timeRange: "week" });
  });

  it("shows the torrent tab only while it is on, and copies a magnet link", async () => {
    serve({ status: webSearchStatus(true, false) });
    const { unmount } = render(<WebSearchPanel />);
    await screen.findByRole("tab", { name: "网页" });
    expect(screen.queryByRole("tab", { name: "种子" })).not.toBeInTheDocument();
    unmount();
    resetWebSearch();

    serve({ status: webSearchStatus(true, true) });
    render(<WebSearchPanel />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("tab", { name: "种子" }));
    await search("big buck bunny");
    const torrents = await screen.findByRole("list", { name: "种子结果" });
    expect(within(torrents).getByRole("link", { name: "big buck bunny 第 1 条" })).toHaveAttribute(
      "href",
      "magnet:?xt=urn:btih:1",
    );
    expect(within(torrents).getAllByText("886 MiB")).toHaveLength(2);
    const copy = vi.fn(() => true);
    Object.defineProperty(document, "execCommand", { value: copy, configurable: true });
    await user.click(within(torrents).getByRole("button", { name: "复制“big buck bunny 第 1 条”的磁力链接" }));
    expect(copy).toHaveBeenCalledWith("copy");
    expect(await screen.findByText("已复制磁力链接")).toBeInTheDocument();
  });

  it("tells a refused search apart from engines that did not answer", async () => {
    let refuse = true;
    serve({
      answer: (query) =>
        refuse
          ? { status: 429, code: "too_many_searches" }
          : {
              results: [],
              found: [],
              answers: [],
              suggestions: [],
              unresponsive: query.category === "web" ? ["百度", "搜狗", "360 搜索"] : [],
              paused: [],
              busy: [],
              pending: 0,
            },
    });
    render(<WebSearchPanel />);
    const user = await search("天气");
    expect(await screen.findByRole("alert")).toHaveTextContent("搜索太频繁，请稍后再试");

    refuse = false;
    await user.click(screen.getByRole("button", { name: "重试" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("搜索引擎都没有响应，请稍后再试");

    await user.click(screen.getByRole("tab", { name: "图片" }));
    expect(await screen.findByText("没有找到与“天气”相关的结果")).toBeInTheDocument();
  });

  it("shows the engines that answered first and adds the others' results below", async () => {
    let release = () => {};
    const hold = new Promise<void>((resolve) => {
      release = resolve;
    });
    serve({
      hold,
      answer: (query) => {
        const page = pageFor(query);
        const [first, second] = page.results;
        return [
          { ...page, results: [first], unresponsive: [], pending: 2 },
          {
            ...page,
            results: [second],
            found: [{ url: first.url, engines: ["百度", "搜狗"] }],
            suggestions: [],
            pending: 0,
          },
        ];
      },
    });
    render(<WebSearchPanel />);
    await search("流浪地球");
    const results = await screen.findByRole("list", { name: "搜索结果" });
    expect(within(results).getAllByRole("listitem")).toHaveLength(1);
    expect(screen.getByText("还有 2 个引擎在搜索")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "更多结果" })).not.toBeInTheDocument();

    release();
    expect(await within(results).findByRole("link", { name: "流浪地球 第 2 条" })).toBeInTheDocument();
    expect(within(results).getAllByRole("listitem")[0]).toHaveTextContent("百度、搜狗");
    expect(screen.queryByText("还有 2 个引擎在搜索")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "更多结果" })).toBeInTheDocument();
  });

  it("keeps what came when a search breaks off, and searches that page again", async () => {
    let cut = true;
    const sent = serve({
      answer: (query) => (cut ? { ...pageFor(query), pending: 3 } : pageFor(query)),
    });
    render(<WebSearchPanel />);
    const user = await search("流浪地球");
    expect(await screen.findByRole("alert")).toHaveTextContent("搜索中断，请重试");
    expect(screen.getByRole("link", { name: "流浪地球 第 1 条" })).toBeInTheDocument();

    cut = false;
    await user.click(screen.getByRole("button", { name: "重试" }));
    expect(await screen.findByRole("button", { name: "更多结果" })).toBeInTheDocument();
    expect(sent.queries.at(-1)).toEqual({ q: "流浪地球", category: "web", page: 1 });
    expect(screen.getAllByRole("link", { name: "流浪地球 第 1 条" })).toHaveLength(1);
  });

  it("counts many engines that did not answer instead of naming them all", async () => {
    const silent = ["Google", "DuckDuckGo", "Brave", "Startpage", "Qwant"];
    serve({ answer: (query) => ({ ...pageFor(query), unresponsive: silent }) });
    render(<WebSearchPanel />);
    await search("天气");
    const notice = await screen.findByText("5 个引擎未响应");
    expect(notice.closest("details")).not.toHaveAttribute("open");
    await userEvent.setup().click(notice);
    expect(screen.getByText("Google、DuckDuckGo、Brave、Startpage、Qwant")).toBeVisible();
  });

  it("names the engines a search left out and why", async () => {
    const until = "2026-11-02T08:30:00Z";
    serve({
      answer: (query) =>
        query.q === "天气"
          ? {
              ...pageFor(query),
              unresponsive: [],
              paused: [{ engine: "百度", reason: "captcha", until }],
              busy: ["搜狗"],
            }
          : {
              ...pageFor(query),
              results: [],
              unresponsive: ["360 搜索"],
              paused: [{ engine: "百度", reason: "captcha", until }],
              busy: ["搜狗"],
            },
    });
    render(<WebSearchPanel />);
    const user = await search("天气");
    expect(await screen.findByText("百度被网站拦截，暂停中")).toBeInTheDocument();
    expect(screen.getByText("搜狗搜索太频繁，本次跳过")).toBeInTheDocument();

    const box = screen.getByRole("searchbox");
    await user.clear(box);
    await user.type(box, "台风{Enter}");
    expect(await screen.findByRole("alert")).toHaveTextContent("搜索引擎暂时不可用，请稍后再试");
  });

  it("says when an administrator has turned web search off", async () => {
    serve({ status: webSearchStatus(false) });
    render(<WebSearchPanel />);
    expect(await screen.findByText("管理员已关闭网页搜索")).toBeInTheDocument();
    expect(screen.queryByRole("searchbox")).not.toBeInTheDocument();
  });

  it("reads durations as a clock", () => {
    expect(durationText(5)).toBe("0:05");
    expect(durationText(233)).toBe("3:53");
    expect(durationText(8189)).toBe("2:16:29");
  });
});
