import { ArchiveRestore, ChevronRight, Download, FileSearch, History, Layers, Search, X } from "lucide-react";
import { ReactNode, useEffect, useMemo, useState } from "react";

import type { Space } from "../api";
import { formatBytes } from "../bytes";
import { useBackup } from "./BackupCenter";
import {
  listRemovedEntries,
  messageOf,
  searchSnapshots,
  snapshotDownloadURL,
  type RemovedEntry,
  type SnapshotMatch,
  type SnapshotVersion,
} from "./backupApi";
import { formatCount, formatDateTime, formatDay, spaceLabel } from "./format";
import { EmptyState, EntryIcon, Notice, Spinner } from "./parts";
import { snapshotTitle } from "./SnapshotsPage";

const collapsedVersions = 3;
const allSpaces = "all";

interface Found<T> {
  space: Space;
  item: T;
}

// gather asks every space in scope and keeps what the reachable ones return:
// one space being unavailable should not hide the others' results.
async function gather<T>(spaces: Space[], load: (space: Space) => Promise<{ items: T[]; truncated: boolean }>) {
  const settled = await Promise.allSettled(spaces.map(async (space) => ({ space, ...(await load(space)) })));
  const reached = settled.flatMap((result) => (result.status === "fulfilled" ? [result.value] : []));
  const failed = settled.flatMap((result, index) =>
    result.status === "rejected" ? [{ space: spaces[index], reason: result.reason }] : [],
  );
  if (!reached.length && failed.length) throw failed[0].reason;
  return {
    items: reached.flatMap(({ space, items }) => items.map((item): Found<T> => ({ space, item }))),
    truncated: reached.some((result) => result.truncated),
    unreachable: failed.map((failure) => failure.space),
  };
}

export function FindPage() {
  const { spaces, data, restored } = useBackup();
  // People rarely remember which space a lost file lived in, so finding
  // starts across all of them.
  const [scope, setScope] = useState(allSpaces);
  const [query, setQuery] = useState("");
  const [result, setResult] = useState<{
    query: string;
    items: Found<SnapshotMatch>[];
    truncated: boolean;
    unreachable: Space[];
  }>();
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const targets = useMemo(
    () => (scope === allSpaces ? spaces : spaces.filter((space) => space.id === scope)),
    [scope, spaces],
  );
  const noSnapshots = targets.every((space) => data[space.id]?.snapshots?.length === 0);
  const where = targets.length === 1 ? spaceLabel(targets[0]) : "所有空间";

  const [searchedBefore, setSearchedBefore] = useState<{ query: string; targets: Space[]; restored: unknown }>();
  if (searchedBefore?.query !== query || searchedBefore.targets !== targets || searchedBefore.restored !== restored) {
    setSearchedBefore({ query, targets, restored });
    setError("");
    if (!query.trim() || !targets.length) {
      setResult(undefined);
      setLoading(false);
    } else setLoading(true);
  }
  useEffect(() => {
    const text = query.trim();
    if (!text || !targets.length) return;
    const controller = new AbortController();
    // A restore can bring a path back into a space; restored reruns the search.
    const timer = window.setTimeout(() => {
      gather(targets, (space) => searchSnapshots(space.id, text, controller.signal))
        .then((found) => {
          // Exact names first, as each space already orders its own results.
          const exact = (match: SnapshotMatch) => (match.name.toLowerCase() === text.toLowerCase() ? 0 : 1);
          found.items.sort((a, b) => exact(a.item) - exact(b.item));
          setResult({ query: text, ...found });
          setLoading(false);
        })
        .catch((caught) => {
          if (!controller.signal.aborted) {
            setError(messageOf(caught));
            setLoading(false);
          }
        });
    }, 280);
    return () => {
      window.clearTimeout(timer);
      controller.abort();
    };
  }, [query, targets, restored]);

  if (!spaces.length) return null;
  return (
    <div className="bk-page bk-find-page">
      <header className="bk-page-head">
        <div>
          <h2>找回文件</h2>
          <p>在{where}的快照里按名称查找；想不起名字，就看下面最近删除的项目。</p>
        </div>
        {spaces.length > 1 && (
          <div className="bk-head-actions">
            <div className="bk-segmented" role="radiogroup" aria-label="查找范围">
              <button
                type="button"
                role="radio"
                aria-checked={scope === allSpaces}
                className={scope === allSpaces ? "active" : ""}
                onClick={() => setScope(allSpaces)}
              >
                <Layers />
                全部空间
              </button>
              {spaces.map((space) => (
                <button
                  key={space.id}
                  type="button"
                  role="radio"
                  aria-checked={scope === space.id}
                  className={scope === space.id ? "active" : ""}
                  onClick={() => setScope(space.id)}
                >
                  {spaceLabel(space)}
                </button>
              ))}
            </div>
          </div>
        )}
      </header>

      <label className="bk-search">
        <Search />
        <input
          autoFocus
          value={query}
          maxLength={100}
          placeholder="输入文件名的一部分，例如“合同”或“.pdf”"
          aria-label="在快照中查找"
          onChange={(event) => setQuery(event.target.value)}
        />
        {loading && <Spinner small />}
        {query && (
          <button type="button" className="bk-icon-button" aria-label="清除" onClick={() => setQuery("")}>
            <X />
          </button>
        )}
      </label>

      {error ? (
        <Notice tone="danger">{error}</Notice>
      ) : !query.trim() ? (
        noSnapshots ? (
          <EmptyState icon={<FileSearch />} title={`${where}还没有快照`}>
            有了快照之后，就可以在这里找回过去的文件。
          </EmptyState>
        ) : (
          <RemovedList key={scope} spaces={targets} />
        )
      ) : result && result.query === query.trim() ? (
        result.items.length === 0 ? (
          <EmptyState icon={<FileSearch />} title={`没有找到“${result.query}”`}>
            {targets.length === 1 && spaces.length > 1
              ? "换一个更短的关键词，或者在全部空间中查找。"
              : "换一个更短的关键词试试，例如只输入名称里的一两个字。"}
          </EmptyState>
        ) : (
          <>
            <p className="bk-result-count">
              找到 {result.items.length} 项{result.truncated ? "，结果较多，只显示前一部分；请输入更完整的名称" : ""}
            </p>
            {result.unreachable.length > 0 && (
              <Notice tone="warning">
                {result.unreachable.map(spaceLabel).join("、")}暂时无法查找，结果中不含它的文件。
              </Notice>
            )}
            <ul className="bk-matches" data-vnas-private="快照里找到的文件">
              {result.items.map(({ space, item }) => (
                <MatchCard key={`${space.id}:${item.path}`} space={space} match={item} query={result.query} />
              ))}
            </ul>
          </>
        )
      ) : (
        <div className="bk-loading inline">
          <Spinner />
          正在查找…
        </div>
      )}
    </div>
  );
}

function MatchCard({ space, match, query }: { space: Space; match: SnapshotMatch; query: string }) {
  const { restore } = useBackup();
  const [expanded, setExpanded] = useState(false);
  const folders = match.path.split("/").slice(0, -1);
  const versions = expanded ? match.versions : match.versions.slice(0, collapsedVersions);
  const restoreVersion = (version: SnapshotVersion) =>
    restore({
      space,
      snapshot: {
        id: version.snapshotId,
        name: snapshotTitle({ name: version.snapshotName, origin: version.origin }),
        createdAt: version.createdAt,
      },
      entry: { id: version.entryId, name: match.name, kind: match.kind },
      folders,
    });
  return (
    <li className="bk-match">
      <header>
        <EntryIcon kind={match.kind} />
        <div className="bk-match-title">
          <strong>{highlight(match.name, query)}</strong>
          <small title={match.path}>{[spaceLabel(space), ...folders].join(" / ")}</small>
        </div>
        {!match.inSpace && <span className="bk-badge danger">已不在空间中</span>}
        {match.kind === "file" && <span className="bk-badge neutral">{match.versions.length} 个版本</span>}
      </header>
      <ol className="bk-versions">
        {versions.map((version, index) => (
          <li key={`${version.snapshotId}:${version.entryId}`}>
            <span className={`bk-version-dot${index === 0 ? " newest" : ""}`} aria-hidden="true" />
            <div className="bk-version-copy">
              <strong>
                {match.kind === "directory"
                  ? "文件夹"
                  : version.modifiedAt
                    ? `修改于 ${formatDateTime(version.modifiedAt)}`
                    : "版本"}
                {match.kind === "file" && <span> · {formatBytes(version.sizeBytes)}</span>}
                {version.current ? (
                  <em className="current">当前版本</em>
                ) : (
                  index === 0 && match.versions.length > 1 && <em>最新</em>
                )}
              </strong>
              <small>
                {version.snapshots > 1
                  ? `保存在 ${version.snapshots} 份快照中 · ${formatDay(version.oldestAt)} 至 ${formatDay(version.createdAt)}`
                  : `保存在${snapshotTitle({ name: version.snapshotName, origin: version.origin }) === "定时快照" ? "定时快照" : `快照“${version.snapshotName}”`} · ${formatDateTime(version.createdAt)}`}
              </small>
            </div>
            <div className="bk-version-actions">
              {match.kind === "file" && (
                <a
                  className="bk-icon-button"
                  href={snapshotDownloadURL(version.snapshotId, version.entryId)}
                  download={match.name}
                  aria-label={`下载这个版本的 ${match.name}`}
                  title="下载"
                >
                  <Download />
                </a>
              )}
              {/* The space already holds the current version; restoring it would only duplicate it. */}
              {!version.current && (
                <button type="button" className="bk-restore-button" onClick={() => restoreVersion(version)}>
                  <ArchiveRestore />
                  恢复
                </button>
              )}
            </div>
          </li>
        ))}
      </ol>
      {match.versions.length > collapsedVersions && (
        <button type="button" className="bk-link bk-more" onClick={() => setExpanded((value) => !value)}>
          {expanded ? "收起" : `显示全部 ${match.versions.length} 个版本`}
        </button>
      )}
    </li>
  );
}

// RemovedList shows what the snapshots hold that is no longer in the spaces,
// most recently gone first: what people look for when they cannot recall a name.
function RemovedList({ spaces }: { spaces: Space[] }) {
  const { restore, restored, openTrash } = useBackup();
  const [result, setResult] = useState<{ items: Found<RemovedEntry>[]; truncated: boolean; unreachable: Space[] }>();
  const [error, setError] = useState("");

  // A restore brings items back into the space, so the list reloads.
  const [listedBefore, setListedBefore] = useState({ spaces, restored });
  if (listedBefore.spaces !== spaces || listedBefore.restored !== restored) {
    setListedBefore({ spaces, restored });
    setError("");
  }
  useEffect(() => {
    const controller = new AbortController();
    gather(spaces, (space) => listRemovedEntries(space.id, controller.signal))
      .then((found) => {
        // Each space lists its newest losses first; together they keep that order.
        found.items.sort((a, b) => b.item.lastSeenAt.localeCompare(a.item.lastSeenAt));
        setResult(found);
      })
      .catch((caught) => {
        if (!controller.signal.aborted) setError(messageOf(caught));
      });
    return () => controller.abort();
  }, [spaces, restored]);

  if (error) return <Notice tone="danger">{error}</Notice>;
  if (!result)
    return (
      <div className="bk-loading inline">
        <Spinner />
        正在比对快照与现在的空间…
      </div>
    );
  if (!result.items.length) {
    return (
      <>
        {result.unreachable.length > 0 && (
          <Notice tone="warning">{result.unreachable.map(spaceLabel).join("、")}暂时无法比对。</Notice>
        )}
        <EmptyState icon={<FileSearch />} title="快照里的文件都还在">
          最近的快照中没有被删除或移走的项目。想找改坏之前的版本，可以在上面输入文件名。
        </EmptyState>
      </>
    );
  }
  return (
    <section className="bk-removed" aria-label="最近删除或移走" data-vnas-private="快照里最近删除或移走的文件">
      <header className="bk-section-head">
        <div>
          <h3>
            <History />
            最近删除或移走
          </h3>
          <p>快照里有、现在空间里找不到的文件和文件夹，最近不见的在前。</p>
        </div>
        <span>
          {formatCount(result.items.length)} 项{result.truncated ? "，只显示最近的一部分" : ""}
        </span>
      </header>
      {result.unreachable.length > 0 && (
        <Notice tone="warning">{result.unreachable.map(spaceLabel).join("、")}暂时无法比对，下面没有它的项目。</Notice>
      )}
      <ul className="bk-removed-list">
        {result.items.map(({ space, item }) => {
          const folders = item.path.split("/").slice(0, -1);
          const size =
            item.kind === "directory"
              ? `${formatCount(item.fileCount)} 个文件 · ${formatBytes(item.sizeBytes)}`
              : formatBytes(item.sizeBytes);
          const seen = `${formatDateTime(item.lastSeenAt)} 还在`;
          const between = item.goneBy
            ? `${formatDateTime(item.lastSeenAt)} 的快照里还有，${formatDateTime(item.goneBy)} 的快照里已没有：在这之间被删除、移走或改了名`
            : `最近一份快照（${formatDateTime(item.lastSeenAt)}）之后被删除、移走或改了名`;
          return (
            <li key={`${space.id}:${item.path}`}>
              <EntryIcon kind={item.kind} />
              <div className="bk-removed-copy">
                <strong title={item.name}>
                  {item.name}
                  {item.inTrash &&
                    (openTrash ? (
                      <button
                        type="button"
                        className="bk-badge neutral link"
                        title="回收站里还有它，从回收站还原可以拿回删除前最后的样子"
                        onClick={openTrash}
                      >
                        在回收站中
                        <ChevronRight />
                      </button>
                    ) : (
                      <span className="bk-badge neutral" title="回收站里还有它，从回收站还原可以拿回删除前最后的样子">
                        在回收站中
                      </span>
                    ))}
                </strong>
                <small title={item.path}>{[spaceLabel(space), ...folders].join(" / ")}</small>
                <small className="bk-removed-meta">
                  {size} · {seen}
                </small>
              </div>
              <span className="bk-removed-size">{size}</span>
              <span className="bk-removed-when" title={between}>
                {seen}
              </span>
              <div className="bk-version-actions">
                {item.kind === "file" && (
                  <a
                    className="bk-icon-button"
                    href={snapshotDownloadURL(item.snapshotId, item.entryId)}
                    download={item.name}
                    aria-label={`下载 ${item.name}`}
                    title="下载"
                  >
                    <Download />
                  </a>
                )}
                <button
                  type="button"
                  className="bk-restore-button"
                  aria-label={`恢复 ${item.name}`}
                  onClick={() =>
                    restore({
                      space,
                      folders,
                      inTrash: item.inTrash,
                      snapshot: {
                        id: item.snapshotId,
                        name: snapshotTitle({ name: item.snapshotName, origin: item.origin }),
                        createdAt: item.lastSeenAt,
                      },
                      entry: { id: item.entryId, name: item.name, kind: item.kind },
                    })
                  }
                >
                  <ArchiveRestore />
                  恢复
                </button>
              </div>
            </li>
          );
        })}
      </ul>
    </section>
  );
}

// highlight marks the first case-insensitive occurrence of query in text.
function highlight(text: string, query: string): ReactNode {
  const index = text.toLowerCase().indexOf(query.toLowerCase());
  if (index < 0 || !query) return text;
  return (
    <>
      {text.slice(0, index)}
      <mark>{text.slice(index, index + query.length)}</mark>
      {text.slice(index + query.length)}
    </>
  );
}
