import { ArrowDown, CircleAlert, Copy, Magnet, RotateCw, Search, X } from "lucide-react";
import { type FormEvent, type ReactNode, useEffect, useRef, useState } from "react";

import { APIError } from "../api/client";
import { errorText } from "../api/errorMessages";
import { formatBytes } from "../lib/bytes";
import { copyText } from "../lib/clipboard";
import { calendarDate } from "../lib/time";
import { useToasts } from "../ui/feedback";
import {
  categoryLabels,
  searchWeb,
  type WebSearchCategory,
  type WebSearchPage,
  type WebSearchQuery,
  type WebSearchResult,
} from "./api";
import { useWebSearch } from "./status";
import { WebSearchGlyph } from "./WebSearchGlyph";
import styles from "./websearch.module.css";

type TimeRange = NonNullable<WebSearchQuery["timeRange"]>;

const timeRanges: Array<{ value: TimeRange | ""; label: string }> = [
  { value: "", label: "不限时间" },
  { value: "day", label: "一天内" },
  { value: "week", label: "一周内" },
  { value: "month", label: "一月内" },
  { value: "year", label: "一年内" },
];

const maxPage = 20;

// What the window shows for one search of one tab.
interface Shown {
  query: string;
  category: WebSearchCategory;
  timeRange: TimeRange | "";
  pages: WebSearchPage[];
  // loading is the page being fetched; error is why the last fetch failed.
  loading?: number;
  error?: APIError | Error;
}

// WebSearchPanel is the 网页搜索 window (docs/specs/web-search.md): a search
// box, the tabs an administrator left on, and SearXNG's results in A-NAS's own
// layout. Nothing typed here is kept once the window closes.
export function WebSearchPanel() {
  const status = useWebSearch();
  const [text, setText] = useState("");
  const [chosen, setCategory] = useState<WebSearchCategory>("web");
  const [timeRange, setTimeRange] = useState<TimeRange | "">("");
  const [searched, setShown] = useState<Shown>();
  // A tab an administrator turns off while the window is open gives way to
  // the first one, and its results go; until the switches are read, any tab is.
  const categories = status?.categories ?? [];
  const allowed = (tab: WebSearchCategory) => !status || categories.includes(tab);
  const category = allowed(chosen) ? chosen : (categories[0] ?? "web");
  const shown = searched && allowed(searched.category) ? searched : undefined;
  const pending = useRef<AbortController | undefined>(undefined);
  const input = useRef<HTMLInputElement>(null);
  const { toast, element: toasts } = useToasts();

  useEffect(() => () => pending.current?.abort(), []);

  const run = (query: string, nextCategory: WebSearchCategory, nextRange: TimeRange | "", page: number) => {
    pending.current?.abort();
    const controller = new AbortController();
    pending.current = controller;
    const same = shown && shown.query === query && shown.category === nextCategory && shown.timeRange === nextRange;
    const pages = page > 1 && same ? shown.pages : [];
    setShown({ query, category: nextCategory, timeRange: nextRange, pages, loading: page });
    searchWeb(
      {
        q: query,
        category: nextCategory,
        page,
        ...(nextRange && nextCategory === "news" ? { timeRange: nextRange } : {}),
      },
      controller.signal,
    )
      .then((result) => {
        if (controller.signal.aborted) return;
        setShown({ query, category: nextCategory, timeRange: nextRange, pages: [...pages, result] });
      })
      .catch((error: unknown) => {
        if (controller.signal.aborted) return;
        setShown({
          query,
          category: nextCategory,
          timeRange: nextRange,
          pages,
          error: error instanceof Error ? error : new Error(String(error)),
        });
      });
  };

  const submit = (event?: FormEvent) => {
    event?.preventDefault();
    const query = text.trim();
    if (query) run(query, category, timeRange, 1);
  };
  // Another tab or time range searches what the box holds now, typed or not.
  const current = () => text.trim() || shown?.query;
  const chooseCategory = (next: WebSearchCategory) => {
    setCategory(next);
    const query = current();
    if (shown && query) run(query, next, timeRange, 1);
  };
  const chooseTimeRange = (next: TimeRange | "") => {
    setTimeRange(next);
    const query = current();
    if (shown && query) run(query, category, next, 1);
  };
  const searchFor = (query: string) => {
    setText(query);
    run(query, category, timeRange, 1);
  };

  if (status && !status.enabled) {
    return (
      <div className={styles.ws}>
        <div className={styles.wsOff} role="status">
          <span className={styles.wsOffMark} aria-hidden>
            <WebSearchGlyph size={30} />
          </span>
          <strong>管理员已关闭网页搜索</strong>
        </div>
      </div>
    );
  }

  const box = (
    <form className={styles.wsBox} role="search" onSubmit={submit}>
      <Search className={styles.wsBoxIcon} aria-hidden />
      <input
        ref={input}
        type="search"
        value={text}
        onChange={(event) => setText(event.target.value)}
        placeholder="搜索网页、图片、视频"
        aria-label="搜索内容"
        maxLength={400}
        // biome-ignore lint/a11y/noAutofocus: the window opens to type a search.
        autoFocus
        enterKeyHint="search"
      />
      {text && (
        <button
          type="button"
          className={styles.wsClear}
          aria-label="清除"
          onClick={() => {
            setText("");
            input.current?.focus();
          }}
        >
          <X />
        </button>
      )}
      <button type="submit" className={styles.wsGo} disabled={!text.trim()}>
        搜索
      </button>
    </form>
  );

  const tabs = (
    <div className={styles.wsTabs}>
      <div role="tablist" aria-label="搜索分类">
        {categories.map((item) => (
          <button
            key={item}
            type="button"
            role="tab"
            aria-selected={item === category}
            className={item === category ? styles.active : undefined}
            onClick={() => chooseCategory(item)}
          >
            {categoryLabels[item]}
          </button>
        ))}
      </div>
      {category === "news" && (
        <select
          className={styles.wsRange}
          aria-label="发布时间"
          value={timeRange}
          onChange={(event) => chooseTimeRange(event.target.value as TimeRange | "")}
        >
          {timeRanges.map((range) => (
            <option key={range.value} value={range.value}>
              {range.label}
            </option>
          ))}
        </select>
      )}
    </div>
  );

  if (!shown) {
    return (
      <div className={styles.ws}>
        <div className={styles.wsHome}>
          <span className={styles.wsHomeMark} aria-hidden>
            <WebSearchGlyph size={34} />
          </span>
          {box}
          {tabs}
        </div>
        {toasts}
      </div>
    );
  }

  return (
    <div className={styles.ws}>
      <header className={styles.wsBar}>
        {box}
        {tabs}
      </header>
      <div className={styles.wsBody} aria-busy={shown.loading !== undefined || undefined}>
        <Results
          shown={shown}
          onMore={() => run(shown.query, shown.category, shown.timeRange, shown.pages.length + 1)}
          onRetry={() => run(shown.query, shown.category, shown.timeRange, shown.pages.length + 1)}
          onSearch={searchFor}
          onCopied={(ok) =>
            toast(ok ? { text: "已复制磁力链接", tone: "done" } : { text: "无法复制，请手动复制", tone: "error" })
          }
        />
      </div>
      {toasts}
    </div>
  );
}

function Results({
  shown,
  onMore,
  onRetry,
  onSearch,
  onCopied,
}: {
  shown: Shown;
  onMore: () => void;
  onRetry: () => void;
  onSearch: (query: string) => void;
  onCopied: (ok: boolean) => void;
}) {
  const { category, pages, loading, error } = shown;
  const results = pages.flatMap((page) => page.results);
  const first = pages[0];
  const last = pages.at(-1);
  const unresponsive = last?.unresponsive ?? [];
  const engines = useWebSearch()?.engines[category] ?? [];
  const allSilent =
    last !== undefined && last.results.length === 0 && engines.length > 0 && unresponsive.length >= engines.length;

  if (loading === 1) return <Skeleton category={category} />;
  if (error && pages.length === 0) return <Failure error={error} onRetry={onRetry} />;
  if (first && results.length === 0) {
    return allSilent ? (
      <Failure message="搜索引擎都没有响应，请稍后再试" onRetry={onRetry} />
    ) : (
      <div className={styles.wsEmpty} role="status">
        没有找到与“{shown.query}”相关的结果
      </div>
    );
  }
  return (
    <>
      {unresponsive.length > 0 && (
        <p className={styles.wsSilent}>
          <CircleAlert aria-hidden />
          {unresponsive.join("、")}未响应
        </p>
      )}
      {first?.infobox && <Infobox infobox={first.infobox} />}
      {first && first.answers.length > 0 && (
        <div className={styles.wsAnswers}>
          {first.answers.map((answer) => (
            <p key={answer.text}>
              {answer.url ? <ExternalLink href={answer.url}>{answer.text}</ExternalLink> : answer.text}
            </p>
          ))}
        </div>
      )}
      <ResultList category={category} results={results} onCopied={onCopied} />
      {loading !== undefined && <div className={styles.wsMoreLoading}>正在加载…</div>}
      {error && pages.length > 0 && <Failure error={error} onRetry={onRetry} inline />}
      {loading === undefined && !error && last && last.results.length > 0 && pages.length < maxPage && (
        <button type="button" className={styles.wsMore} onClick={onMore}>
          <ArrowDown />
          更多结果
        </button>
      )}
      {first && first.suggestions.length > 0 && (
        <nav className={styles.wsSuggestions} aria-label="相关搜索">
          <span>相关搜索</span>
          {first.suggestions.map((suggestion) => (
            <button key={suggestion} type="button" onClick={() => onSearch(suggestion)}>
              {suggestion}
            </button>
          ))}
        </nav>
      )}
    </>
  );
}

function ResultList({
  category,
  results,
  onCopied,
}: {
  category: WebSearchCategory;
  results: WebSearchResult[];
  onCopied: (ok: boolean) => void;
}) {
  switch (category) {
    case "images":
      return (
        <ul className={styles.wsImages} aria-label="图片结果">
          {results.map((result, index) => (
            <li key={`${result.url}#${index}`}>
              <a href={result.url} target="_blank" rel="noopener noreferrer" title={result.title}>
                <Thumbnail src={result.thumbnail} />
                <span className={styles.wsImageCaption}>
                  <strong>{result.title}</strong>
                  <small>
                    {[
                      result.width && result.height ? `${result.width} × ${result.height}` : "",
                      result.source || result.domain,
                    ]
                      .filter(Boolean)
                      .join(" · ")}
                  </small>
                </span>
              </a>
            </li>
          ))}
        </ul>
      );
    case "videos":
      return (
        <ul className={styles.wsVideos} aria-label="视频结果">
          {results.map((result, index) => (
            <li key={`${result.url}#${index}`}>
              <a href={result.url} target="_blank" rel="noopener noreferrer">
                <span className={styles.wsVideoCover}>
                  <Thumbnail src={result.thumbnail} />
                  {result.durationSeconds !== undefined && (
                    <span className={styles.wsDuration}>{durationText(result.durationSeconds)}</span>
                  )}
                </span>
                <strong>{result.title}</strong>
              </a>
              <small>{[result.platform, result.author, published(result)].filter(Boolean).join(" · ")}</small>
            </li>
          ))}
        </ul>
      );
    case "torrents":
      return (
        <ul className={styles.wsTorrents} aria-label="种子结果">
          {results.map((result, index) => (
            <li key={`${result.url}#${index}`}>
              <span className={styles.wsTorrentIcon} aria-hidden>
                <Magnet />
              </span>
              <div className={styles.wsTorrentMain}>
                {result.magnet ? (
                  <a href={result.magnet} className={styles.wsTitle}>
                    {result.title}
                  </a>
                ) : (
                  <span className={styles.wsTitle}>{result.title}</span>
                )}
                <small>
                  {result.sizeBytes !== undefined && <span>{formatBytes(result.sizeBytes)}</span>}
                  {result.seeders !== undefined && (
                    <span className={result.seeders > 0 ? styles.wsSeeders : undefined}>做种 {result.seeders}</span>
                  )}
                  {result.leechers !== undefined && <span>下载 {result.leechers}</span>}
                  {published(result) && <span>{published(result)}</span>}
                  <ExternalLink href={result.url}>{result.engines.join("、") || result.domain}</ExternalLink>
                </small>
              </div>
              {result.magnet && (
                <button
                  type="button"
                  className={styles.wsCopy}
                  onClick={() => onCopied(copyText(result.magnet ?? ""))}
                  aria-label={`复制“${result.title}”的磁力链接`}
                >
                  <Copy />
                  复制磁力链接
                </button>
              )}
            </li>
          ))}
        </ul>
      );
    default:
      return (
        <ol className={category === "news" ? `${styles.wsList} ${styles.wsNews}` : styles.wsList} aria-label="搜索结果">
          {results.map((result, index) => (
            <li key={`${result.url}#${index}`}>
              <div className={styles.wsListText}>
                <small className={styles.wsSource}>
                  <span>{result.domain}</span>
                  <span className={styles.wsEngines}>{result.engines.join("、")}</span>
                </small>
                <ExternalLink href={result.url} className={styles.wsTitle}>
                  {result.title}
                </ExternalLink>
                {(result.content || result.publishedAt) && (
                  <p>
                    {published(result) && <time dateTime={result.publishedAt}>{published(result)}</time>}
                    {result.content}
                  </p>
                )}
              </div>
              {result.thumbnail && (
                <span className={styles.wsListThumb}>
                  <Thumbnail src={result.thumbnail} />
                </span>
              )}
            </li>
          ))}
        </ol>
      );
  }
}

function Infobox({ infobox }: { infobox: NonNullable<WebSearchPage["infobox"]> }) {
  return (
    <aside className={styles.wsInfobox} aria-label={infobox.title}>
      {infobox.thumbnail && (
        <span className={styles.wsInfoboxThumb}>
          <Thumbnail src={infobox.thumbnail} />
        </span>
      )}
      <div>
        <strong>{infobox.title}</strong>
        {infobox.content && <p>{infobox.content}</p>}
        {infobox.url && <ExternalLink href={infobox.url}>查看详情</ExternalLink>}
      </div>
    </aside>
  );
}

// Thumbnail loads through A-NAS; one that fails leaves the tile's own color.
function Thumbnail({ src }: { src?: string }) {
  const [failed, setFailed] = useState(false);
  if (!src || failed) return <span className={styles.wsThumbEmpty} aria-hidden />;
  return <img src={src} alt="" loading="lazy" decoding="async" onError={() => setFailed(true)} />;
}

function ExternalLink({ href, className, children }: { href: string; className?: string; children: ReactNode }) {
  return (
    <a href={href} className={className} target="_blank" rel="noopener noreferrer">
      {children}
    </a>
  );
}

function Failure({
  error,
  message,
  onRetry,
  inline,
}: {
  error?: Error;
  message?: string;
  onRetry: () => void;
  inline?: boolean;
}) {
  const code = error instanceof APIError ? error.code : "";
  // Turned off is no failure the user can retry.
  const final = code === "web_search_disabled" || code === "torrent_search_disabled";
  return (
    <div className={inline ? `${styles.wsFailure} ${styles.inline}` : styles.wsFailure} role="alert">
      <span>{message ?? errorText(error, "webSearch")}</span>
      {!final && (
        <button type="button" onClick={onRetry}>
          <RotateCw />
          重试
        </button>
      )}
    </div>
  );
}

function Skeleton({ category }: { category: WebSearchCategory }) {
  const tiles = category === "images" || category === "videos";
  return (
    <div className={tiles ? styles.wsSkeletonTiles : styles.wsSkeletonList} aria-label="正在搜索">
      {Array.from({ length: tiles ? 8 : 5 }, (_, index) => (
        <span key={index} />
      ))}
    </div>
  );
}

function published(result: WebSearchResult): string {
  return result.publishedAt ? calendarDate(result.publishedAt) : "";
}

// durationText reads 233 as 3:53 and 8189 as 2:16:29.
export function durationText(seconds: number): string {
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  const rest = String(seconds % 60).padStart(2, "0");
  return hours > 0 ? `${hours}:${String(minutes).padStart(2, "0")}:${rest}` : `${minutes}:${rest}`;
}
