import { ArrowDown, LoaderCircle, RotateCw, Search, X } from "lucide-react";
import { Tabs } from "radix-ui";
import { type CSSProperties, type FormEvent, Fragment, type ReactNode, useEffect, useRef, useState } from "react";

import { APIError } from "../api/client";
import { errorText } from "../api/errorMessages";
import { useToasts } from "../ui/feedback";
import {
  categoryLabels,
  searchWeb,
  type WebSearchAnswer,
  type WebSearchCategory,
  type WebSearchInfobox,
  type WebSearchPausedEngine,
  type WebSearchQuery,
  type WebSearchResult,
  type WebSearchUpdate,
} from "./api";
import { EngineStatus } from "./EngineStatus";
import { ExternalLink, Infobox, ResultList } from "./ResultViews";
import { useWebSearch } from "./status";
import { WebSearchGlyph } from "./WebSearchGlyph";
import styles from "./websearch.module.css";

export { durationText } from "./ResultViews";

type TimeRange = NonNullable<WebSearchQuery["timeRange"]>;

const timeRanges: Array<{ value: TimeRange | ""; label: string }> = [
  { value: "", label: "不限时间" },
  { value: "day", label: "一天内" },
  { value: "week", label: "一周内" },
  { value: "month", label: "一月内" },
  { value: "year", label: "一年内" },
];

const maxPage = 20;

// Tabs whose results are tiles across the window rather than a column.
const tiled = new Set<WebSearchCategory>(["images", "videos"]);

// A page of results as its updates have come: engines still searching add
// theirs below.
interface Page {
  results: WebSearchResult[];
  answers: WebSearchAnswer[];
  infobox?: WebSearchInfobox;
  suggestions: string[];
  unresponsive: string[];
  // Engines the search left out: their site blocked them, or they had just
  // been asked too often.
  paused: WebSearchPausedEngine[];
  busy: string[];
  pending: number;
}

const emptyPage: Page = {
  results: [],
  answers: [],
  suggestions: [],
  unresponsive: [],
  paused: [],
  busy: [],
  pending: 0,
};

// withUpdate adds an update to its page: new results after the others, and
// the engines of results that more engines have found since.
export function withUpdate(page: Page, update: WebSearchUpdate): Page {
  const found = new Map(update.found.map((item) => [item.url, item.engines]));
  const results = [...page.results, ...update.results].map((result) => {
    const engines = found.get(result.url);
    return engines ? { ...result, engines } : result;
  });
  return {
    results,
    answers: [...page.answers, ...update.answers],
    infobox: page.infobox ?? update.infobox,
    suggestions: [...page.suggestions, ...update.suggestions],
    unresponsive: [...page.unresponsive, ...update.unresponsive],
    paused: [...page.paused, ...update.paused],
    busy: [...page.busy, ...update.busy],
    pending: update.pending,
  };
}

// What the window shows for one search of one tab.
interface Shown {
  query: string;
  category: WebSearchCategory;
  timeRange: TimeRange | "";
  pages: Page[];
  // loading is the page asked for until its first update comes; error is why
  // the search of page failed, which keeps what came before the failure.
  loading?: number;
  error?: APIError | Error;
  failed?: number;
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
  const body = useRef<HTMLDivElement>(null);
  const { toast, element: toasts } = useToasts();

  useEffect(() => () => pending.current?.abort(), []);

  const run = (query: string, nextCategory: WebSearchCategory, nextRange: TimeRange | "", page: number) => {
    pending.current?.abort();
    const controller = new AbortController();
    pending.current = controller;
    const same = shown && shown.query === query && shown.category === nextCategory && shown.timeRange === nextRange;
    // A page asked for again replaces what came of it before.
    const pages = page > 1 && same ? shown.pages.slice(0, page - 1) : [];
    // Another search starts at the top.
    if (page === 1 && body.current) body.current.scrollTop = 0;
    setShown({ query, category: nextCategory, timeRange: nextRange, pages, loading: page });
    let current: Page | undefined;
    searchWeb(
      {
        q: query,
        category: nextCategory,
        page,
        ...(nextRange && nextCategory === "news" ? { timeRange: nextRange } : {}),
      },
      controller.signal,
      (update) => {
        if (controller.signal.aborted) return;
        current = withUpdate(current ?? emptyPage, update);
        setShown({ query, category: nextCategory, timeRange: nextRange, pages: [...pages, current] });
      },
    ).catch((error: unknown) => {
      if (controller.signal.aborted) return;
      // What came before the failure stays.
      setShown({
        query,
        category: nextCategory,
        timeRange: nextRange,
        pages: current ? [...pages, { ...current, pending: 0 }] : pages,
        error: error instanceof Error ? error : new Error(String(error)),
        failed: page,
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

  // Arrow keys move between the tabs; Enter or a click searches one, so
  // passing a tab asks no engine. Radix chooses a tab on mousedown; a click
  // with none before it, as assistive technology sends, chooses it too.
  const tabs = (
    <div className={styles.wsTabs}>
      <Tabs.List aria-label="搜索分类">
        {categories.map((item) => (
          <Tabs.Trigger key={item} value={item} onClick={() => item !== category && chooseCategory(item)}>
            {categoryLabels[item]}
          </Tabs.Trigger>
        ))}
      </Tabs.List>
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

  return (
    <Tabs.Root
      className={styles.ws}
      value={category}
      onValueChange={(value) => chooseCategory(value as WebSearchCategory)}
      activationMode="manual"
    >
      {shown ? (
        <>
          <header className={styles.wsBar}>
            {box}
            {tabs}
          </header>
          <Tabs.Content
            value={category}
            ref={body}
            className={styles.wsBody}
            aria-busy={shown.loading !== undefined || undefined}
          >
            <Results
              shown={shown}
              engines={status?.engines[shown.category] ?? []}
              onMore={() => run(shown.query, shown.category, shown.timeRange, shown.pages.length + 1)}
              // Again the page that failed, or the last when no engine answered.
              onRetry={() =>
                run(shown.query, shown.category, shown.timeRange, shown.failed ?? Math.max(shown.pages.length, 1))
              }
              onSearch={searchFor}
              onCopied={(ok) =>
                toast(ok ? { text: "已复制磁力链接", tone: "done" } : { text: "无法复制，请手动复制", tone: "error" })
              }
            />
          </Tabs.Content>
        </>
      ) : (
        <div className={styles.wsHome}>
          <span className={styles.wsHomeMark} aria-hidden>
            <WebSearchGlyph size={34} />
          </span>
          {box}
          {tabs}
        </div>
      )}
      {toasts}
    </Tabs.Root>
  );
}

function Results({
  shown,
  engines,
  onMore,
  onRetry,
  onSearch,
  onCopied,
}: {
  shown: Shown;
  // The tab's engines, to tell no results from no engine answering.
  engines: string[];
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
  const paused = last?.paused ?? [];
  const busy = last?.busy ?? [];
  const pending = last?.pending ?? 0;
  const silent = unresponsive.length + paused.length + busy.length;
  const left = paused.length + busy.length;
  const allSilent = last !== undefined && last.results.length === 0 && engines.length > 0 && silent >= engines.length;
  const engineStatus = <EngineStatus unresponsive={unresponsive} paused={paused} busy={busy} />;

  if (error && pages.length === 0) return <Failure error={error} onRetry={onRetry} />;
  // The first results take a moment: shapes stand where they will come.
  if (loading === 1 || (results.length === 0 && pending > 0)) {
    return (
      <div className={tiled.has(category) ? styles.wsPage : `${styles.wsPage} ${styles.wsColumns}`}>
        <StatusLine count={0} pending={pending} searching>
          {engineStatus}
        </StatusLine>
        <Skeleton category={category} />
      </div>
    );
  }
  if (first && results.length === 0) {
    return allSilent ? (
      <Failure
        message={left > 0 ? "搜索引擎暂时不可用，请稍后再试" : "搜索引擎都没有响应，请稍后再试"}
        onRetry={onRetry}
      >
        {engineStatus}
      </Failure>
    ) : (
      <div className={styles.wsEmpty} role="status">
        <p>没有找到与“{shown.query}”相关的结果</p>
        {silent > 0 && (
          <div className={styles.wsEmptyActions}>
            {engineStatus}
            <button type="button" className={styles.wsRetry} onClick={onRetry}>
              <RotateCw />
              重试
            </button>
          </div>
        )}
      </div>
    );
  }
  const infobox = first?.infobox;
  const answers = first?.answers ?? [];
  const suggestions = first?.suggestions ?? [];
  return (
    <div
      className={
        tiled.has(category) ? styles.wsPage : `${styles.wsPage} ${styles.wsColumns} ${infobox ? styles.withAside : ""}`
      }
    >
      <StatusLine count={results.length} pending={loading === undefined ? pending : 0} searching={pending > 0}>
        {engineStatus}
      </StatusLine>
      {infobox && !tiled.has(category) && (
        <div className={styles.wsAside}>
          <Infobox infobox={infobox} />
        </div>
      )}
      <div className={styles.wsMain}>
        {answers.length > 0 && (
          <div className={styles.wsAnswers}>
            {answers.map((answer) => (
              <p key={answer.text}>
                {answer.url ? <ExternalLink href={answer.url}>{answer.text}</ExternalLink> : answer.text}
              </p>
            ))}
          </div>
        )}
        {pages.map((page, index) => (
          <Fragment key={index}>
            {index > 0 && page.results.length > 0 && (
              <div className={styles.wsPageBreak} role="separator">
                第 {index + 1} 页
              </div>
            )}
            {page.results.length > 0 && <ResultList category={category} results={page.results} onCopied={onCopied} />}
          </Fragment>
        ))}
        {loading !== undefined && (
          <p className={styles.wsMoreLoading}>
            <LoaderCircle aria-hidden />
            正在加载第 {loading} 页
          </p>
        )}
        {error && pages.length > 0 && <Failure error={error} onRetry={onRetry} inline />}
        {loading === undefined &&
          pending === 0 &&
          !error &&
          last &&
          last.results.length > 0 &&
          pages.length < maxPage && (
            <button type="button" className={styles.wsMore} onClick={onMore}>
              <ArrowDown />
              更多结果
            </button>
          )}
        {suggestions.length > 0 && (
          <nav className={styles.wsSuggestions} aria-label="相关搜索">
            <h2>相关搜索</h2>
            <div>
              {suggestions.map((suggestion) => (
                <button key={suggestion} type="button" onClick={() => onSearch(suggestion)}>
                  <Search aria-hidden />
                  {suggestion}
                </button>
              ))}
            </div>
          </nav>
        )}
      </div>
    </div>
  );
}

// StatusLine counts the results so far and the engines still searching, with
// the engines that gave no answer beside them.
function StatusLine({
  count,
  pending,
  searching,
  children,
}: {
  count: number;
  pending: number;
  searching?: boolean;
  children: ReactNode;
}) {
  return (
    <div className={styles.wsStatus}>
      <span className={styles.wsCount}>
        {searching && <LoaderCircle aria-hidden />}
        {[count > 0 ? `${count} 条结果` : "正在搜索", pending > 0 ? `还有 ${pending} 个引擎在搜索` : ""]
          .filter(Boolean)
          .join(" · ")}
      </span>
      {children}
    </div>
  );
}

function Failure({
  error,
  message,
  onRetry,
  inline,
  children,
}: {
  error?: Error;
  message?: string;
  onRetry: () => void;
  inline?: boolean;
  children?: ReactNode;
}) {
  const code = error instanceof APIError ? error.code : "";
  // Turned off is no failure the user can retry.
  const final = code === "web_search_disabled" || code === "torrent_search_disabled";
  return (
    <div className={inline ? `${styles.wsFailure} ${styles.inline}` : styles.wsFailure} role="alert">
      <p>{message ?? errorText(error, "webSearch")}</p>
      {(!final || children) && (
        <div className={styles.wsEmptyActions}>
          {children}
          {!final && (
            <button type="button" className={styles.wsRetry} onClick={onRetry}>
              <RotateCw />
              重试
            </button>
          )}
        </div>
      )}
    </div>
  );
}

// Skeleton holds the place of the first results in their tab's own shape.
function Skeleton({ category }: { category: WebSearchCategory }) {
  if (category === "images") {
    return (
      <div className={styles.wsSkeletonImages} aria-label="正在搜索">
        {[1.5, 1, 1.33, 0.75, 1.6, 1.2, 1, 1.4, 0.8, 1.33, 1.5, 1].map((shape, index) => (
          <span key={index} style={{ "--shape": shape } as CSSProperties} />
        ))}
      </div>
    );
  }
  if (category === "videos") {
    return (
      <div className={styles.wsSkeletonVideos} aria-label="正在搜索">
        {Array.from({ length: 8 }, (_, index) => (
          <span key={index}>
            <i />
            <i />
            <i />
          </span>
        ))}
      </div>
    );
  }
  return (
    <div className={styles.wsSkeletonList} aria-label="正在搜索">
      {Array.from({ length: 5 }, (_, index) => (
        <span key={index}>
          <i />
          <i />
          <i />
          <i />
        </span>
      ))}
    </div>
  );
}
