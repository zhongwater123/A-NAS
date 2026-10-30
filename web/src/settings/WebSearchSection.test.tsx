import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { setSession } from "../api/client";
import { session } from "../test/fixtures";
import { type WebSearchOptions, webSearchHandlers, webSearchStatus } from "../test/handlers/websearch";
import { server } from "../test/server";
import { resetWebSearch } from "../websearch/status";
import { WebSearchSection } from "./WebSearchSection";

function serve(options: WebSearchOptions = {}) {
  const webSearch = webSearchHandlers(options);
  server.use(...webSearch.handlers);
  return webSearch.sent;
}

describe("Settings → 网页搜索", () => {
  beforeEach(() => setSession(session()));
  afterEach(() => {
    setSession();
    resetWebSearch();
  });

  it("turns web search and its torrent tab on and off", async () => {
    const sent = serve({ status: webSearchStatus(true, false) });
    const user = userEvent.setup();
    render(<WebSearchSection />);

    const webSearch = await screen.findByRole("switch", { name: "网页搜索" });
    const torrents = screen.getByRole("switch", { name: "种子搜索" });
    expect(webSearch).toBeChecked();
    expect(torrents).not.toBeChecked();
    expect(screen.getByText("运行中")).toBeInTheDocument();
    expect(screen.getByText("百度、必应、搜狗")).toBeInTheDocument();

    await user.click(torrents);
    expect(await screen.findByRole("switch", { name: "种子搜索" })).toBeChecked();
    await user.click(webSearch);
    expect(await screen.findByText("已关闭")).toBeInTheDocument();
    expect(screen.getByRole("switch", { name: "种子搜索" })).toBeDisabled();
    expect(sent.settings).toEqual([
      { enabled: true, torrents: true },
      { enabled: false, torrents: true },
    ]);
  });

  it("offers SearXNG's license and the source this release was built from", async () => {
    serve();
    render(<WebSearchSection />);
    expect(await screen.findByText("版本 2026.10.30+1cd13d98d")).toBeInTheDocument();
    expect(screen.getByText("AGPL-3.0-or-later")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "下载源码" })).toHaveAttribute("href", "/api/v1/web-search/source");
    expect(screen.getByRole("link", { name: "许可证" })).toHaveAttribute("target", "_blank");
    expect(screen.getByText("运行环境中的 1 个软件包")).toBeInTheDocument();
  });

  it("says when the release carries no web search runtime", async () => {
    serve({ license: null });
    render(<WebSearchSection />);
    expect(await screen.findByText("这个版本没有附带网页搜索的运行环境。")).toBeInTheDocument();
  });
});
