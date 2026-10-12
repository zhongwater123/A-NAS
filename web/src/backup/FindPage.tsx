import { ArchiveRestore, Download, FileSearch, Search, X } from "lucide-react";
import { ReactNode, useEffect, useState } from "react";

import { useBackup } from "./BackupCenter";
import { messageOf, searchSnapshots, snapshotDownloadURL, type SnapshotMatch, type SnapshotVersion } from "./backupApi";
import { formatBytes, formatDateTime, formatDay, spaceLabel } from "./format";
import { EmptyState, EntryIcon, Notice, SpaceSwitcher, Spinner } from "./parts";
import { snapshotTitle } from "./SnapshotsPage";

const collapsedVersions = 3;

export function FindPage() {
  const { spaces, data, spaceId, setSpaceId, restored } = useBackup();
  const space = spaces.find((item) => item.id === spaceId) ?? spaces[0];
  const [query, setQuery] = useState("");
  const [result, setResult] = useState<{ query: string; items: SnapshotMatch[]; truncated: boolean }>();
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const snapshotCount = space ? data[space.id]?.snapshots?.length : undefined;

  useEffect(() => {
    const text = query.trim();
    setError("");
    if (!text || !space) { setResult(undefined); setLoading(false); return; }
    const controller = new AbortController();
    setLoading(true);
    // A restore can bring a path back into the space; restored reruns the search.
    const timer = window.setTimeout(() => {
      searchSnapshots(space.id, text, controller.signal)
        .then((found) => { setResult({ query: text, ...found }); setLoading(false); })
        .catch((caught) => { if (!controller.signal.aborted) { setError(messageOf(caught)); setLoading(false); } });
    }, 280);
    return () => { window.clearTimeout(timer); controller.abort(); };
  }, [query, space, restored]);

  if (!space) return null;
  return (
    <div className="bk-page bk-find-page">
      <header className="bk-page-head">
        <div><h2>找回文件</h2><p>按名称在{spaceLabel(space)}的所有快照里查找文件和文件夹。</p></div>
        <div className="bk-head-actions"><SpaceSwitcher spaces={spaces} value={space.id} onChange={setSpaceId} /></div>
      </header>

      <label className="bk-search">
        <Search />
        <input autoFocus value={query} maxLength={100} placeholder="输入文件名的一部分，例如“合同”或“.pdf”" aria-label="在快照中查找" onChange={(event) => setQuery(event.target.value)} />
        {loading && <Spinner small />}
        {query && <button type="button" className="bk-icon-button" aria-label="清除" onClick={() => setQuery("")}><X /></button>}
      </label>

      {error ? <Notice tone="danger">{error}</Notice>
        : !query.trim() ? (
          <EmptyState icon={<FileSearch />} title={snapshotCount === 0 ? `${spaceLabel(space)}还没有快照` : "误删或改坏了文件？"}>
            {snapshotCount === 0 ? "有了快照之后，就可以在这里找回过去的文件。" : `输入名称后，会列出每个匹配项在快照里的不同版本。${snapshotCount ? `目前共有 ${snapshotCount} 份快照可供查找。` : ""}`}
          </EmptyState>
        ) : result && result.query === query.trim() ? (
          result.items.length === 0 ? (
            <EmptyState icon={<FileSearch />} title={`没有找到“${result.query}”`}>换一个更短的关键词试试，或者切换到另一个空间。</EmptyState>
          ) : (
            <>
              <p className="bk-result-count">找到 {result.items.length} 项{result.truncated ? "，结果较多，只显示前一部分；请输入更完整的名称" : ""}</p>
              <ul className="bk-matches">
                {result.items.map((match) => <MatchCard key={match.path} match={match} query={result.query} />)}
              </ul>
            </>
          )
        ) : <div className="bk-loading inline"><Spinner />正在查找…</div>}
    </div>
  );
}

function MatchCard({ match, query }: { match: SnapshotMatch; query: string }) {
  const { spaces, spaceId, restore } = useBackup();
  const space = spaces.find((item) => item.id === spaceId)!;
  const [expanded, setExpanded] = useState(false);
  const folders = match.path.split("/").slice(0, -1);
  const versions = expanded ? match.versions : match.versions.slice(0, collapsedVersions);
  const restoreVersion = (version: SnapshotVersion) => restore({
    space,
    snapshot: { id: version.snapshotId, name: snapshotTitle({ name: version.snapshotName, origin: version.origin }), createdAt: version.createdAt },
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
                {match.kind === "directory" ? "文件夹" : version.modifiedAt ? `修改于 ${formatDateTime(version.modifiedAt)}` : "版本"}
                {match.kind === "file" && <span> · {formatBytes(version.sizeBytes)}</span>}
                {index === 0 && match.versions.length > 1 && <em>最新</em>}
              </strong>
              <small>
                {version.snapshots > 1
                  ? `保存在 ${version.snapshots} 份快照中 · ${formatDay(version.oldestAt)} 至 ${formatDay(version.createdAt)}`
                  : `保存在${snapshotTitle({ name: version.snapshotName, origin: version.origin }) === "定时快照" ? "定时快照" : `快照“${version.snapshotName}”`} · ${formatDateTime(version.createdAt)}`}
              </small>
            </div>
            <div className="bk-version-actions">
              {match.kind === "file" && <a className="bk-icon-button" href={snapshotDownloadURL(version.snapshotId, version.entryId)} download={match.name} aria-label={`下载这个版本的 ${match.name}`} title="下载"><Download /></a>}
              <button type="button" className="bk-restore-button" onClick={() => restoreVersion(version)}><ArchiveRestore />恢复</button>
            </div>
          </li>
        ))}
      </ol>
      {match.versions.length > collapsedVersions && (
        <button type="button" className="bk-link bk-more" onClick={() => setExpanded((value) => !value)}>{expanded ? "收起" : `显示全部 ${match.versions.length} 个版本`}</button>
      )}
    </li>
  );
}

// highlight marks the first case-insensitive occurrence of query in text.
function highlight(text: string, query: string): ReactNode {
  const index = text.toLowerCase().indexOf(query.toLowerCase());
  if (index < 0 || !query) return text;
  return <>{text.slice(0, index)}<mark>{text.slice(index, index + query.length)}</mark>{text.slice(index + query.length)}</>;
}
