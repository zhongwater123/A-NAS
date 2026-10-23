import { CircleAlert, Download, RefreshCw, Search, X } from "lucide-react";
import { ReactNode, useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";

import { ContainerLogLine, ContainerSummary, readContainerLogs } from "../containersApi";
import { formatClock } from "./model";
import { IconAction } from "./parts";

export const logFollowInterval = 3_000;
const tailOptions = [200, 500];

export function ContainerLogs({ item }: { item: ContainerSummary }) {
  const [lines, setLines] = useState<ContainerLogLine[]>();
  const [failure, setFailure] = useState<string>();
  const [loading, setLoading] = useState(true);
  const [tail, setTail] = useState(tailOptions[0]);
  // A stopped container writes nothing new, so following starts off for it.
  const [follow, setFollow] = useState(item.state === "running");
  const [query, setQuery] = useState("");
  const [updatedAt, setUpdatedAt] = useState<number>();
  const body = useRef<HTMLDivElement>(null);
  // Whether the view sticks to the newest line; scrolling up to read releases it.
  const pinned = useRef(true);
  const active = useRef<AbortController | undefined>(undefined);

  const fetchLogs = useCallback(() => {
    active.current?.abort();
    const controller = new AbortController();
    active.current = controller;
    return readContainerLogs(item.id, tail, controller.signal)
      .then((read) => {
        setLines(read);
        setFailure(undefined);
        setUpdatedAt(Date.now());
      })
      .catch((error: unknown) => {
        if (!(error instanceof DOMException && error.name === "AbortError")) setFailure("无法读取日志");
      })
      .finally(() => {
        if (active.current === controller) setLoading(false);
      });
  }, [item.id, tail]);
  const load = useCallback(() => {
    setLoading(true);
    return fetchLogs();
  }, [fetchLogs]);

  // Another container or tail length loads at once.
  const logsFor = JSON.stringify([item.id, tail]);
  const [logsBefore, setLogsBefore] = useState(logsFor);
  if (logsBefore !== logsFor) {
    setLogsBefore(logsFor);
    setLoading(true);
  }
  useEffect(() => {
    void fetchLogs();
    return () => active.current?.abort();
  }, [fetchLogs]);
  useEffect(() => {
    if (!follow) return;
    const timer = window.setInterval(() => {
      if (!document.hidden) void load();
    }, logFollowInterval);
    return () => window.clearInterval(timer);
  }, [follow, load]);
  // Set scrollTop rather than call scrollIntoView: recent Chromium returns a
  // Promise from it, and the log view once crashed the desktop on unmount.
  useLayoutEffect(() => {
    if (pinned.current && body.current) body.current.scrollTop = body.current.scrollHeight;
  }, [lines, query]);

  const needle = query.trim().toLowerCase();
  const visible = lines && needle ? lines.filter((line) => line.text.toLowerCase().includes(needle)) : (lines ?? []);
  const onScroll = () => {
    const element = body.current;
    if (element) pinned.current = element.scrollHeight - element.scrollTop - element.clientHeight < 24;
  };
  const download = () => {
    const text = visible.map((line) => `${line.time ?? ""}\t${line.stream}\t${line.text}`).join("\n");
    const url = URL.createObjectURL(new Blob([`${text}\n`], { type: "text/plain;charset=utf-8" }));
    const link = document.createElement("a");
    link.href = url;
    link.download = `${item.name}-${fileStamp(new Date())}.log`;
    link.click();
    window.setTimeout(() => URL.revokeObjectURL(url), 1_000);
  };

  return (
    <div className="dk-logs" role="tabpanel" aria-label="日志">
      <div className="dk-log-bar">
        <label className="dk-search dk-log-search">
          <Search />
          <input
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder="筛选日志"
            aria-label="筛选日志"
          />
          {query && (
            <button className="dk-search-clear" aria-label="清除筛选" onClick={() => setQuery("")}>
              <X />
            </button>
          )}
        </label>
        <select
          className="dk-select"
          aria-label="读取行数"
          value={tail}
          onChange={(event) => setTail(Number(event.target.value))}
        >
          {tailOptions.map((option) => (
            <option key={option} value={option}>
              最近 {option} 行
            </option>
          ))}
        </select>
        <button className={`dk-toggle ${follow ? "on" : ""}`} aria-pressed={follow} onClick={() => setFollow(!follow)}>
          <i aria-hidden="true" />
          自动刷新
        </button>
        <IconAction label="下载日志" icon={<Download />} disabled={visible.length === 0} onClick={download} />
        <IconAction
          label="刷新日志"
          icon={<RefreshCw className={loading ? "dk-spin" : ""} />}
          disabled={loading}
          onClick={() => void load()}
        />
      </div>
      {failure && (
        <div className="dk-note error" role="alert">
          <CircleAlert />
          {failure}
        </div>
      )}
      <div className="dk-log" role="log" aria-label={`${item.name} 日志`} ref={body} onScroll={onScroll}>
        {lines === undefined && !failure && <p className="dk-log-empty">正在读取日志…</p>}
        {lines?.length === 0 && <p className="dk-log-empty">暂无日志输出</p>}
        {lines && lines.length > 0 && visible.length === 0 && (
          <p className="dk-log-empty">没有包含“{query.trim()}”的行</p>
        )}
        {visible.map((line, index) => (
          <div className={`dk-log-line ${line.stream}`} key={index}>
            <time>{line.time ? formatClock(line.time) : ""}</time>
            <span>{highlight(line.text, needle)}</span>
          </div>
        ))}
      </div>
      <footer className="dk-log-foot">
        <span>{lines ? (needle ? `${visible.length} / ${lines.length} 行匹配` : `共 ${lines.length} 行`) : ""}</span>
        {updatedAt !== undefined && (
          <span>
            {follow ? <i className="dk-live" aria-hidden="true" /> : null}更新于 {formatClock(updatedAt)}
          </span>
        )}
      </footer>
    </div>
  );
}

// Marks each case-insensitive occurrence of needle; split keeps the matches at odd indexes.
function highlight(text: string, needle: string): ReactNode {
  if (!needle) return text;
  const pattern = new RegExp(`(${needle.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")})`, "gi");
  return text.split(pattern).map((part, index) => (index % 2 === 1 ? <mark key={index}>{part}</mark> : part));
}

function fileStamp(date: Date): string {
  const pad = (value: number) => String(value).padStart(2, "0");
  return `${date.getFullYear()}${pad(date.getMonth() + 1)}${pad(date.getDate())}-${pad(date.getHours())}${pad(date.getMinutes())}${pad(date.getSeconds())}`;
}
