import { CopyPlus, Download, History, RotateCcw } from "lucide-react";
import { useEffect, useState } from "react";

import type { FileEntry, Space } from "../api.generated";
import { formatBytes } from "../lib/bytes";

import {
  listVersions,
  messageOf,
  restoreEntry,
  snapshotDownloadURL,
  type SnapshotMatch,
  type SnapshotVersion,
} from "./api";
import { formatDateTime, formatDay, restoredName, spaceLabel } from "./format";
import { Dialog, EmptyState, EntryIcon, Notice, Spinner } from "./parts";
import { RestoreDialog, type RestoreRequest } from "./RestoreDialog";
import { snapshotTitle } from "./SnapshotsPage";
import "./backup.css";

const versionTime = (version: SnapshotVersion) => formatDateTime(version.modifiedAt ?? version.createdAt);

// VersionsDialog is 历史版本 in the file manager: the versions of one file
// that the space's snapshots hold, to download, copy back beside it, or put
// back in its place while the current file goes to the recycle bin.
export function VersionsDialog({
  file,
  space,
  folders,
  onClose,
  onChanged,
}: {
  file: FileEntry;
  space: Space;
  // Folder names from the space root down to the file's folder.
  folders: string[];
  onClose: () => void;
  // onChanged reports that the folder holding the file changed.
  onChanged: () => void;
}) {
  const [entry, setEntry] = useState(file);
  const [match, setMatch] = useState<SnapshotMatch>();
  const [error, setError] = useState("");
  const [reload, setReload] = useState(0);
  const [replacing, setReplacing] = useState<SnapshotVersion>();
  const [replaceError, setReplaceError] = useState("");
  const [copying, setCopying] = useState<RestoreRequest>();
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<{ tone: "success" | "danger"; text: string }>();

  const listing = JSON.stringify([entry.id, reload]);
  const [listingBefore, setListingBefore] = useState(listing);
  if (listingBefore !== listing) {
    setListingBefore(listing);
    setError("");
  }
  useEffect(() => {
    let live = true;
    listVersions(entry.id)
      .then((found) => {
        if (live) setMatch(found);
      })
      .catch((caught) => {
        if (live) setError(messageOf(caught));
      });
    return () => {
      live = false;
    };
  }, [entry.id, reload]);

  const location = [spaceLabel(space), ...folders].join(" / ");
  const snapshotOf = (version: SnapshotVersion) => ({
    id: version.snapshotId,
    name: snapshotTitle({ name: version.snapshotName, origin: version.origin }),
    createdAt: version.createdAt,
  });
  const copy = (version: SnapshotVersion) =>
    setCopying({
      space,
      folders,
      snapshot: snapshotOf(version),
      entry: { id: version.entryId, name: entry.name, kind: "file" },
      name: restoredName(entry.name, "file"),
    });
  const replace = async () => {
    if (!replacing) return;
    setBusy(true);
    setReplaceError("");
    try {
      const restored = await restoreEntry(
        replacing.snapshotId,
        replacing.entryId,
        entry.parentId ?? "",
        entry.name,
        true,
      );
      setNotice({ tone: "success", text: `已还原到 ${versionTime(replacing)} 的版本，原来的文件已移到回收站。` });
      setReplacing(undefined);
      setEntry(restored);
      setReload((count) => count + 1);
      onChanged();
    } catch (caught) {
      setReplaceError(messageOf(caught));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="bk-layer" data-vnas-private="快照里的历史版本">
      <Dialog
        title="历史版本"
        icon={<History />}
        wide
        busy={busy}
        onClose={onClose}
        footer={
          <button type="button" className="bk-secondary" data-autofocus onClick={onClose}>
            完成
          </button>
        }
      >
        <div className="bk-history-file">
          <EntryIcon kind="file" />
          <div>
            <strong title={entry.name}>{entry.name}</strong>
            <small title={location}>
              {location} · 现在 {formatBytes(entry.sizeBytes)}，修改于 {formatDateTime(entry.modifiedAt)}
            </small>
          </div>
        </div>
        {notice &&
          (notice.tone === "success" ? (
            <div className="bk-notice success" role="status">
              {notice.text}
            </div>
          ) : (
            <Notice tone="danger">{notice.text}</Notice>
          ))}
        {error ? (
          <Notice tone="danger">{error}</Notice>
        ) : !match ? (
          <div className="bk-loading inline">
            <Spinner small />
            正在读取快照中的版本…
          </div>
        ) : match.versions.length === 0 ? (
          <EmptyState icon={<History />} title="快照里还没有这个文件">
            它是在最近一次快照之后才出现的。之后每次拍摄快照，都会留下它当时的样子。
          </EmptyState>
        ) : (
          <ol className="bk-versions bk-history-versions" aria-label="版本">
            {match.versions.map((version, index) => (
              <li key={`${version.snapshotId}:${version.entryId}`}>
                <span
                  className={`bk-version-dot${version.current || (index === 0 && !match.versions.some((item) => item.current)) ? " newest" : ""}`}
                  aria-hidden="true"
                />
                <div className="bk-version-copy">
                  <strong>
                    {version.modifiedAt ? `修改于 ${formatDateTime(version.modifiedAt)}` : "版本"}
                    <span> · {formatBytes(version.sizeBytes)}</span>
                    {version.current && <em className="current">当前版本</em>}
                  </strong>
                  <small>
                    {version.snapshots > 1
                      ? `保存在 ${version.snapshots} 份快照中 · ${formatDay(version.oldestAt)} 至 ${formatDay(version.createdAt)}`
                      : `保存在${snapshotOf(version).name === "定时快照" ? "定时快照" : `快照“${version.snapshotName}”`} · ${formatDateTime(version.createdAt)}`}
                  </small>
                </div>
                <div className="bk-version-actions">
                  <a
                    className="bk-icon-button"
                    href={snapshotDownloadURL(version.snapshotId, version.entryId)}
                    download={entry.name}
                    aria-label={`下载 ${versionTime(version)} 的版本`}
                    title="下载"
                  >
                    <Download />
                  </a>
                  <button
                    type="button"
                    className="bk-text-button"
                    aria-label={`把 ${versionTime(version)} 的版本另存一份`}
                    title="在原文件夹另存一份，不动现在的文件"
                    onClick={() => copy(version)}
                  >
                    <CopyPlus />
                    另存一份
                  </button>
                  {!version.current && (
                    <button
                      type="button"
                      className="bk-restore-button"
                      aria-label={`还原到 ${versionTime(version)} 的版本`}
                      title="换回这个版本，现在的文件移到回收站"
                      onClick={() => {
                        setReplaceError("");
                        setReplacing(version);
                      }}
                    >
                      <RotateCcw />
                      还原
                    </button>
                  )}
                </div>
              </li>
            ))}
          </ol>
        )}
        {match && match.versions.some((version) => !version.current) && (
          <p className="bk-fine">
            “还原”把文件换回那个版本，现在的文件移到回收站；“另存一份”在旁边多存一个文件，不动现在的文件。
          </p>
        )}
      </Dialog>
      {replacing && (
        <Dialog
          title="还原到这个版本？"
          icon={<RotateCcw />}
          busy={busy}
          onClose={() => setReplacing(undefined)}
          footer={
            <>
              <button type="button" className="bk-secondary" disabled={busy} onClick={() => setReplacing(undefined)}>
                取消
              </button>
              <button type="button" className="bk-primary" disabled={busy} onClick={() => void replace()}>
                {busy ? (
                  <>
                    <Spinner small />
                    正在还原…
                  </>
                ) : (
                  "还原"
                )}
              </button>
            </>
          }
        >
          <p className="bk-dialog-text">
            “{entry.name}”会换成 {versionTime(replacing)} 的版本（{formatBytes(replacing.sizeBytes)}
            ）。现在的文件会移到回收站，30 天内可以恢复。
          </p>
          {replaceError && (
            <div className="bk-inline-error" role="alert">
              <span>{replaceError}</span>
            </div>
          )}
        </Dialog>
      )}
      {copying && (
        <RestoreDialog
          request={copying}
          onClose={() => setCopying(undefined)}
          onBackground={() => undefined}
          onRestored={(restored) => {
            setCopying(undefined);
            setNotice({ tone: "success", text: `已另存为“${restored.name}”。` });
            onChanged();
          }}
          onFailed={(message) => {
            setCopying(undefined);
            setNotice({ tone: "danger", text: message });
          }}
        />
      )}
    </div>
  );
}
