import { ArchiveRestore, ChevronRight, CornerLeftUp, Folder, FolderOpen, MapPin, Trash2 } from "lucide-react";
import { FormEvent, useEffect, useEffectEvent, useRef, useState } from "react";

import { APIError, listEntries, type FileEntry, type Space } from "../api";

import { messageOf, restoreEntry, restoreSnapshot } from "./backupApi";
import { formatDateTime, restoredName, spaceLabel } from "./format";
import { Dialog, EntryIcon, Spinner } from "./parts";

export interface RestoreRequest {
  space: Space;
  snapshot: { id: string; name: string; createdAt: string };
  entry: { id: string; name: string; kind: "file" | "directory" };
  // Folder names from the snapshot's root down to the entry's own folder.
  folders: string[];
  // whole restores everything the snapshot holds into a new folder named
  // entry.name; entry.id is then empty.
  whole?: boolean;
  // name is the name the dialog starts with, when not the entry's own.
  name?: string;
  // inTrash says the recycle bin holds an item deleted from the same path,
  // with the content it had when it was deleted.
  inTrash?: boolean;
}

export interface Crumb {
  id: string;
  name: string;
}

// The folder's names in the way the server compares them: letter case aside,
// as SMB clients cannot tell such names apart.
const nameKey = (name: string) => name.trim().toLowerCase();

// After this long a restore offers to continue in the background.
const backgroundAfter = 1200;

export function RestoreDialog({
  request,
  onClose,
  onBackground,
  onRestored,
  onFailed,
  onOpenTrash,
}: {
  request: RestoreRequest;
  onClose: () => void;
  onBackground: () => void;
  onRestored: (entry: FileEntry, location: string, folders: Crumb[]) => void;
  onFailed: (message: string) => void;
  onOpenTrash?: () => void;
}) {
  const { space, snapshot, entry } = request;
  const initialName = request.name ?? entry.name;
  const [target, setTarget] = useState<Crumb[]>();
  const [missing, setMissing] = useState("");
  const [original, setOriginal] = useState(false);
  const [picking, setPicking] = useState(false);
  const [name, setName] = useState(initialName);
  // The names in the target folder, so a clash shows before restoring.
  const [taken, setTaken] = useState<Set<string>>();
  // The name the dialog put in place of a taken one, while it is unchanged.
  const [suggested, setSuggested] = useState("");
  const [busy, setBusy] = useState(false);
  const [slow, setSlow] = useState(false);
  const [hidden, setHidden] = useState(false);
  const [error, setError] = useState("");
  const [conflict, setConflict] = useState(false);
  const hiddenRef = useRef(false);

  // Start where the entry used to be: the deepest folder of its original
  // path that still exists in the space.
  useEffect(() => {
    let live = true;
    void (async () => {
      const trail: Crumb[] = [];
      let lost = "";
      try {
        for (const folder of request.folders) {
          const items = await listEntries(space.id, trail.at(-1)?.id ?? "");
          const match = items.find((item) => item.kind === "directory" && item.name === folder);
          if (!match) {
            lost = folder;
            break;
          }
          trail.push({ id: match.id, name: match.name });
        }
      } catch (caught) {
        if (live) setError(messageOf(caught));
      }
      if (!live) return;
      setTarget(trail);
      setMissing(lost);
      setOriginal(!lost);
    })();
    return () => {
      live = false;
    };
  }, [request.folders, space.id]);

  // When the target already holds the name, the dialog suggests a new one
  // instead of letting the restore fail; back where the name is free, it
  // returns to the original.
  const [listedFor, setListedFor] = useState({ target, spaceId: space.id, initialName, kind: entry.kind });
  if (
    listedFor.target !== target ||
    listedFor.spaceId !== space.id ||
    listedFor.initialName !== initialName ||
    listedFor.kind !== entry.kind
  ) {
    setListedFor({ target, spaceId: space.id, initialName, kind: entry.kind });
    if (target) setTaken(undefined);
  }
  // The name is read as it is when the listing returns.
  const suggestFor = useEffectEvent((names: Set<string>) => {
    if (name === initialName && names.has(nameKey(initialName))) {
      const next = restoredName(initialName, entry.kind);
      setName(next);
      setSuggested(next);
    } else if (suggested && name === suggested && !names.has(nameKey(initialName))) {
      setName(initialName);
      setSuggested("");
    }
  });
  useEffect(() => {
    if (!target) return;
    let live = true;
    listEntries(space.id, target.at(-1)?.id ?? "")
      .then((items) => {
        if (!live) return;
        const names = new Set(items.map((item) => nameKey(item.name)));
        setTaken(names);
        suggestFor(names);
      })
      .catch(() => {
        if (live) setTaken(new Set());
      });
    return () => {
      live = false;
    };
  }, [target, space.id, initialName, entry.kind]);

  const location = [spaceLabel(space), ...(target ?? []).map((crumb) => crumb.name)].join(" / ");
  const clash = !busy && name.trim() !== "" && Boolean(taken?.has(nameKey(name)));
  const alternative = restoredName(name.trim() || initialName, entry.kind);

  const submit = async (event?: FormEvent) => {
    event?.preventDefault();
    if (!target || busy) return;
    const finalName = name.trim();
    if (!finalName) return;
    setBusy(true);
    setError("");
    setConflict(false);
    const timer = window.setTimeout(() => setSlow(true), backgroundAfter);
    try {
      const parentId = target.at(-1)?.id ?? "";
      const restored = request.whole
        ? await restoreSnapshot(snapshot.id, parentId, finalName)
        : await restoreEntry(snapshot.id, entry.id, parentId, finalName);
      onRestored(restored, location, target);
    } catch (caught) {
      const message = messageOf(caught);
      if (hiddenRef.current) {
        onFailed(
          entry.kind === "directory"
            ? `恢复“${finalName}”未完成：${message}。已恢复的部分保留在目标位置。`
            : `恢复“${finalName}”失败：${message}`,
        );
        return;
      }
      const isConflict = caught instanceof APIError && caught.code === "name_conflict";
      setConflict(isConflict);
      setError(
        isConflict
          ? `“${location}”中已有名为“${finalName}”的项目。`
          : entry.kind === "directory"
            ? `${message}。已恢复的部分保留在目标位置。`
            : message,
      );
    } finally {
      window.clearTimeout(timer);
      setBusy(false);
      setSlow(false);
    }
  };

  const toBackground = () => {
    hiddenRef.current = true;
    setHidden(true);
    onBackground();
  };

  if (hidden) return null;
  return (
    <Dialog
      title={request.whole ? "恢复整份快照" : `恢复${entry.kind === "directory" ? "文件夹" : "文件"}`}
      icon={<ArchiveRestore />}
      busy={busy}
      onClose={onClose}
      footer={
        <>
          {busy && slow ? (
            <button type="button" className="bk-secondary" onClick={toBackground}>
              在后台继续
            </button>
          ) : (
            <button type="button" className="bk-secondary" disabled={busy} onClick={onClose}>
              取消
            </button>
          )}
          <button
            type="submit"
            form="bk-restore-form"
            className="bk-primary"
            disabled={!target || busy || !name.trim() || clash}
          >
            {busy ? (
              <>
                <Spinner small />
                正在恢复…
              </>
            ) : (
              "恢复"
            )}
          </button>
        </>
      }
    >
      <form id="bk-restore-form" className="bk-restore" onSubmit={(event) => void submit(event)}>
        {request.inTrash && (
          <div className="bk-trash-hint">
            <Trash2 />
            <span>
              回收站里还有它，那里是删除前最后的样子；快照里的是 {formatDateTime(snapshot.createdAt)} 的样子。
            </span>
            {onOpenTrash && (
              <button
                type="button"
                className="bk-link"
                onClick={() => {
                  onOpenTrash();
                  onClose();
                }}
              >
                去回收站还原
              </button>
            )}
          </div>
        )}
        <div className="bk-restore-source">
          <EntryIcon kind={entry.kind} />
          <div>
            <strong>{request.whole ? `快照“${snapshot.name}”的全部文件` : entry.name}</strong>
            <small>
              {request.whole ? (
                formatDateTime(snapshot.createdAt)
              ) : (
                <>
                  来自快照“{snapshot.name}” · {formatDateTime(snapshot.createdAt)}
                </>
              )}
            </small>
          </div>
        </div>

        <div className="bk-field">
          <span className="bk-field-label">恢复到</span>
          <div className={`bk-location${picking ? " open" : ""}`}>
            <MapPin />
            <span className="bk-location-path" title={location}>
              {target ? location : "正在查找原位置…"}
            </span>
            {original && target && !request.whole && <span className="bk-badge neutral">原位置</span>}
            <button
              type="button"
              className="bk-link"
              disabled={!target || busy}
              onClick={() => setPicking((value) => !value)}
            >
              {picking ? "收起" : "更改"}
            </button>
          </div>
          {missing && !picking && (
            <small className="bk-field-hint">原文件夹“{missing}”已不在空间中，将恢复到它还在的上一级。</small>
          )}
          {picking && target && (
            <FolderPicker
              space={space}
              start={target}
              onPick={(trail) => {
                setTarget(trail);
                setOriginal(false);
                setMissing("");
                setPicking(false);
              }}
            />
          )}
        </div>

        <label className="bk-field">
          <span className="bk-field-label">{request.whole ? "新文件夹名称" : "名称"}</span>
          <input
            data-autofocus
            value={name}
            disabled={busy}
            onChange={(event) => {
              setName(event.target.value);
              setSuggested("");
              setConflict(false);
              setError("");
            }}
            aria-label="恢复后的名称"
          />
          {suggested && name === suggested && (
            <small className="bk-field-note">
              {original ? "原位置" : "这里"}已有“{initialName}”，所以换了个名字另存一份，不会覆盖它。
              {original &&
                entry.kind === "file" &&
                !request.whole &&
                "想直接换回旧版本，可在文件管理中右键这个文件，选“历史版本”。"}
            </small>
          )}
        </label>

        {(clash || error) && (
          <div className="bk-inline-error" role="alert">
            <span>{clash ? `“${location}”中已有名为“${name.trim()}”的项目。` : error}</span>
            {(clash || conflict) && (
              <button
                type="button"
                className="bk-link"
                onClick={() => {
                  setName(alternative);
                  setSuggested("");
                  setConflict(false);
                  setError("");
                }}
              >
                改用“{alternative}”
              </button>
            )}
          </div>
        )}
        <p className="bk-fine">
          {request.whole
            ? "快照里的全部文件会复制到这个新文件夹中。"
            : entry.kind === "directory"
              ? "文件夹里的全部内容会一起恢复。"
              : ""}
          恢复会复制出新的项目，不会覆盖或改动空间中现有的文件。
        </p>
      </form>
    </Dialog>
  );
}

// FolderPicker browses the folders of the live space to choose a target.
function FolderPicker({ space, start, onPick }: { space: Space; start: Crumb[]; onPick: (trail: Crumb[]) => void }) {
  const [trail, setTrail] = useState<Crumb[]>(start);
  const [folders, setFolders] = useState<FileEntry[]>();
  const [error, setError] = useState("");
  const parentId = trail.at(-1)?.id ?? "";

  // Each folder shows nothing until its own folders arrive.
  const browsed = JSON.stringify([space.id, parentId]);
  const [browsedBefore, setBrowsedBefore] = useState(browsed);
  if (browsedBefore !== browsed) {
    setBrowsedBefore(browsed);
    setFolders(undefined);
    setError("");
  }
  useEffect(() => {
    let live = true;
    listEntries(space.id, parentId)
      .then((items) => {
        if (live) setFolders(items.filter((item) => item.kind === "directory"));
      })
      .catch((caught) => {
        if (live) setError(messageOf(caught));
      });
    return () => {
      live = false;
    };
  }, [space.id, parentId]);

  return (
    <div className="bk-picker" role="group" aria-label="选择恢复位置">
      <nav className="bk-picker-trail" aria-label="当前位置">
        <button type="button" onClick={() => setTrail([])}>
          {spaceLabel(space)}
        </button>
        {trail.map((crumb, index) => (
          <span key={crumb.id}>
            <ChevronRight />
            <button type="button" onClick={() => setTrail(trail.slice(0, index + 1))}>
              {crumb.name}
            </button>
          </span>
        ))}
      </nav>
      <div className="bk-picker-list">
        {trail.length > 0 && (
          <button type="button" className="bk-picker-row up" onClick={() => setTrail(trail.slice(0, -1))}>
            <CornerLeftUp />
            上一级
          </button>
        )}
        {error ? (
          <p className="bk-picker-note">{error}</p>
        ) : !folders ? (
          <p className="bk-picker-note">
            <Spinner small />
            正在读取文件夹…
          </p>
        ) : folders.length === 0 ? (
          <p className="bk-picker-note">这里没有子文件夹</p>
        ) : (
          folders.map((folder) => (
            <button
              key={folder.id}
              type="button"
              className="bk-picker-row"
              onClick={() => setTrail([...trail, { id: folder.id, name: folder.name }])}
            >
              <Folder />
              {folder.name}
              <ChevronRight className="trailing" />
            </button>
          ))
        )}
      </div>
      <div className="bk-picker-foot">
        <span>
          <FolderOpen />
          {[spaceLabel(space), ...trail.map((crumb) => crumb.name)].join(" / ")}
        </span>
        <button type="button" className="bk-primary small" onClick={() => onPick(trail)}>
          恢复到这里
        </button>
      </div>
    </div>
  );
}
