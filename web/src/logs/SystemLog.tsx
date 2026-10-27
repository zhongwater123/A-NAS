import { ChevronDown, Download, Radio, RefreshCw, ServerCog } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";

import { APIError } from "../api/client";
import { Banner, Pill, messageOf } from "../settings/parts";
import { SearchField, Select, downloadText, exportLimit, fileStamp, useDebounced, useRemembered } from "./common";
import { formatDateTime, fullTime, levelLabels, logTime } from "./describe";
import {
  type SystemLogEntry,
  type SystemLogFilters,
  type SystemLogService,
  type TimeRange,
  listSystemLogs,
  systemLogServices,
  timeRanges,
} from "./api";

const pageSize = 200;
const followInterval = 5000;

const serviceLabels = Object.fromEntries(systemLogServices.map((service) => [service.id, service.label])) as Record<
  SystemLogService,
  string
>;

const levelOptions: Array<{ id: SystemLogFilters["level"] | "all"; label: string }> = [
  { id: "all", label: "全部" },
  { id: "warning", label: "警告及以上" },
  { id: "error", label: "仅错误" },
];

interface LogState {
  items: SystemLogEntry[];
  next?: string;
  scannedThrough?: string;
  simulated: boolean;
  loading: boolean;
  loaded: boolean;
  error: string;
}

export function SystemLog() {
  const [filters, setFilters] = useRemembered<SystemLogFilters>("system-log", { range: "all", q: "" });
  const [text, setText] = useState(filters.q);
  const settledText = useDebounced(text);
  const [state, setState] = useState<LogState>({
    items: [],
    simulated: false,
    loading: true,
    loaded: false,
    error: "",
  });
  const [following, setFollowing] = useState(false);
  const [fresh, setFresh] = useState<Set<string>>(new Set());
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const [exporting, setExporting] = useState<{ count: number } | undefined>();
  const [notice, setNotice] = useState("");
  const controller = useRef<AbortController | undefined>(undefined);

  useEffect(() => {
    setFilters((current) => (current.q === settledText ? current : { ...current, q: settledText }));
  }, [settledText, setFilters]);

  const load = useCallback(
    async (before?: string) => {
      controller.current?.abort();
      const abort = new AbortController();
      controller.current = abort;
      setState((current) => ({ ...current, loading: true, error: "" }));
      try {
        const page = await listSystemLogs(filters, { before }, pageSize, abort.signal);
        setState((current) => ({
          items: before ? [...current.items, ...page.items] : page.items,
          next: page.next,
          scannedThrough: page.scannedThrough,
          simulated: page.dataSource === "simulated",
          loading: false,
          loaded: true,
          error: "",
        }));
        if (!before) {
          setExpanded(new Set());
          setFresh(new Set());
        }
      } catch (caught) {
        if (caught instanceof DOMException && caught.name === "AbortError") return;
        setState((current) => ({ ...current, loading: false, loaded: true, error: failure(caught) }));
      }
    },
    [filters],
  );
  useEffect(() => {
    void load();
    return () => controller.current?.abort();
  }, [load]);

  // Following asks for what arrived after the newest line every few seconds.
  const newest = state.items[0]?.cursor;
  const stateRef = useRef(state);
  stateRef.current = state;
  useEffect(() => {
    if (!following || !state.loaded) return;
    let stopped = false;
    const timer = window.setInterval(() => {
      if (stateRef.current.loading) return;
      void (async () => {
        try {
          const page = await listSystemLogs(filters, newest ? { after: newest } : {}, pageSize);
          if (stopped) return;
          if (page.overflow || !newest) {
            void load();
            return;
          }
          if (!page.items.length) return;
          setState((current) => ({ ...current, items: [...page.items, ...current.items], error: "" }));
          setFresh(new Set(page.items.map((entry) => entry.cursor)));
        } catch (caught) {
          if (!stopped) setState((current) => ({ ...current, error: failure(caught) }));
        }
      })();
    }, followInterval);
    return () => {
      stopped = true;
      window.clearInterval(timer);
    };
  }, [following, state.loaded, newest, filters, load]);

  const toggle = (cursor: string) =>
    setExpanded((current) => {
      const next = new Set(current);
      if (next.has(cursor)) next.delete(cursor);
      else next.add(cursor);
      return next;
    });
  const filtered = Boolean(filters.service || filters.level || filters.range !== "all" || filters.q);
  const clear = () => {
    setText("");
    setFilters({ range: "all", q: "" });
  };

  const exportText = async () => {
    setNotice("");
    setExporting({ count: 0 });
    try {
      const lines: SystemLogEntry[] = [];
      let before: string | undefined;
      do {
        const page = await listSystemLogs(filters, { before }, 500);
        lines.push(...page.items);
        before = page.next;
        setExporting({ count: lines.length });
      } while (before && lines.length < exportLimit);
      const kept = lines.slice(0, exportLimit);
      const body = kept
        .map((entry) => {
          const fields = Object.entries(entry.fields ?? {})
            .map(([key, value]) => `${key}=${JSON.stringify(value)}`)
            .join(" ");
          return `${entry.time} ${entry.level.toUpperCase().padEnd(7)} ${(entry.unit ?? entry.service).padEnd(28)} ${entry.message}${fields ? ` ${fields}` : ""}`;
        })
        .join("\n");
      downloadText(`a-nas-system-log-${fileStamp()}.log`, `${body}\n`, "text/plain;charset=utf-8");
      setNotice(
        before ? `只导出了最新的 ${exportLimit.toLocaleString("zh-CN")} 行日志。` : `已导出 ${kept.length} 行日志。`,
      );
    } catch (caught) {
      setNotice(`导出失败：${messageOf(caught)}`);
    } finally {
      setExporting(undefined);
    }
  };

  const serviceOptions: Array<{ id: SystemLogService | ""; label: string }> = [
    { id: "", label: "全部服务" },
    ...systemLogServices,
  ];
  const level = filters.level ?? "all";
  return (
    <div className="lg-panel">
      <div className="lg-toolbar">
        <div className="lg-toolbar-row">
          <div className="lg-chips" role="radiogroup" aria-label="日志级别">
            {levelOptions.map((option) => (
              <button
                key={option.id}
                role="radio"
                aria-checked={level === option.id}
                className={`lg-chip level-${option.id} ${level === option.id ? "active" : ""}`}
                onClick={() =>
                  setFilters((current) => ({ ...current, level: option.id === "all" ? undefined : option.id }))
                }
              >
                {option.id !== "all" && <i className="lg-chip-dot" />}
                {option.label}
              </button>
            ))}
          </div>
          <span className="lg-controls-gap" />
          <button
            role="switch"
            aria-checked={following}
            className={`lg-follow ${following ? "on" : ""}`}
            onClick={() => setFollowing((value) => !value)}
          >
            <Radio />
            实时
          </button>
          <button
            className="set-icon-btn bordered"
            aria-label="刷新系统日志"
            disabled={state.loading}
            onClick={() => void load()}
          >
            <RefreshCw className={state.loading ? "spinning" : ""} />
          </button>
          <button
            className="set-btn"
            disabled={Boolean(exporting) || !state.items.length}
            onClick={() => void exportText()}
          >
            <Download />
            {exporting ? `正在导出 ${exporting.count} 行…` : "导出"}
          </button>
        </div>
        <div className="lg-controls">
          <Select
            label="服务"
            value={filters.service ?? ""}
            options={serviceOptions}
            onChange={(service) => setFilters((current) => ({ ...current, service: service || undefined }))}
          />
          <Select<TimeRange>
            label="时间范围"
            value={filters.range}
            options={timeRanges}
            onChange={(range) => setFilters((current) => ({ ...current, range }))}
          />
          <SearchField label="搜索系统日志" placeholder="消息或字段" value={text} onChange={setText} />
        </div>
      </div>
      {notice && (
        <Banner tone={notice.startsWith("导出失败") ? "error" : "success"} onClose={() => setNotice("")}>
          {notice}
        </Banner>
      )}
      {state.error && (
        <div className="set-banner error lg-retry" role="alert">
          <span>无法读取系统日志：{state.error}</span>
          <button className="set-btn" onClick={() => void load()}>
            重试
          </button>
        </div>
      )}
      {!state.loaded ? (
        <div className="set-card lg-console-skeleton" role="status" aria-label="正在读取日志">
          <span className="loader" />
        </div>
      ) : state.items.length === 0 ? (
        !state.error && (
          <div className="set-card lg-empty">
            <span className="lg-empty-art system">
              <ServerCog />
            </span>
            <h3>{filtered ? "没有符合条件的日志" : "暂时没有日志"}</h3>
            <p>{filtered ? "换一个服务、级别或时间范围试试。" : "A-NAS 服务写下的运行日志会显示在这里。"}</p>
            {filtered && (
              <button className="set-btn" onClick={clear}>
                清除筛选
              </button>
            )}
          </div>
        )
      ) : (
        <>
          <div className="lg-console-head">
            <span>
              {state.items.length} 行{following && <em className="lg-live-dot">实时跟随中</em>}
            </span>
            {state.simulated && <Pill tone="warning">模拟数据</Pill>}
          </div>
          <ol className="set-card lg-console" role="log" aria-label="系统日志" aria-live={following ? "polite" : "off"}>
            {state.items.map((entry) => (
              <LogLine
                key={entry.cursor}
                entry={entry}
                fresh={fresh.has(entry.cursor)}
                open={expanded.has(entry.cursor)}
                onToggle={() => toggle(entry.cursor)}
              />
            ))}
          </ol>
        </>
      )}
      {state.loaded && state.items.length > 0 && (
        <div className="lg-foot">
          {state.scannedThrough && <span>已检查到 {formatDateTime(state.scannedThrough)}，更早的日志还没有检查。</span>}
          {state.next ? (
            <button className="set-btn" disabled={state.loading} onClick={() => void load(state.next)}>
              {state.loading ? "正在读取…" : "加载更早的日志"}
            </button>
          ) : (
            <span>已到最早的日志</span>
          )}
        </div>
      )}
    </div>
  );
}

// failure explains why the logs could not be read.
function failure(caught: unknown): string {
  if (caught instanceof APIError && caught.code === "logs_unavailable")
    return "Host Agent 暂时没有响应，请确认它正在运行后重试。";
  return messageOf(caught);
}

function LogLine({
  entry,
  fresh,
  open,
  onToggle,
}: {
  entry: SystemLogEntry;
  fresh: boolean;
  open: boolean;
  onToggle: () => void;
}) {
  const time = logTime(entry.time);
  const fields = Object.entries(entry.fields ?? {});
  return (
    <li className={`lg-line ${entry.level} ${fresh ? "fresh" : ""} ${open ? "open" : ""}`}>
      <button className="lg-line-main" aria-expanded={open} onClick={onToggle}>
        <time dateTime={entry.time} title={fullTime(entry.time)}>
          <span>{time.day}</span>
          {time.clock}
        </time>
        <span className={`lg-level ${entry.level}`}>{levelLabels[entry.level]}</span>
        <span className="lg-service" title={entry.unit}>
          {serviceLabels[entry.service] ?? entry.service}
        </span>
        <span className="lg-message">
          {entry.message}
          {fields.length > 0 && (
            <span className="lg-fields">
              {fields.map(([key, value]) => (
                <span key={key}>
                  <b>{key}</b>={value}
                </span>
              ))}
            </span>
          )}
        </span>
        <ChevronDown className="lg-chevron" aria-hidden="true" />
      </button>
      {open && (
        <dl className="lg-details console">
          <div>
            <dt>时间</dt>
            <dd>{fullTime(entry.time)}</dd>
          </div>
          <div>
            <dt>服务</dt>
            <dd>
              {serviceLabels[entry.service] ?? entry.service}
              {entry.unit && <code>{entry.unit}</code>}
            </dd>
          </div>
          {entry.pid ? (
            <div>
              <dt>进程</dt>
              <dd>
                <code>{entry.pid}</code>
              </dd>
            </div>
          ) : null}
          <div>
            <dt>消息</dt>
            <dd>
              <code className="wrap">{entry.message}</code>
              {entry.truncated && <small>消息超过 16 KiB，已截断</small>}
            </dd>
          </div>
          {fields.map(([key, value]) => (
            <div key={key}>
              <dt>{key}</dt>
              <dd>
                <code className="wrap">{value}</code>
              </dd>
            </div>
          ))}
          <div>
            <dt>日志游标</dt>
            <dd>
              <code className="wrap">{entry.cursor}</code>
            </dd>
          </div>
        </dl>
      )}
    </li>
  );
}
