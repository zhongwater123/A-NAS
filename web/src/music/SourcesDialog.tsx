import { ArrowLeft, Check, ChevronRight, Folder, FolderPlus, HardDrive, Loader2, Users, X } from "lucide-react";
import { useEffect, useState } from "react";

import { errorText } from "../api/errorMessages";
import type { FileEntry, Space } from "../api.generated";
import { listEntries, listSpaces } from "../files/api";
import feedbackStyles from "../ui/feedback.module.css";
import type { MusicSources } from "./api";
import styles from "./music.module.css";

interface Chosen {
  spaceId: string;
  spaceKind: "private" | "shared";
  folderId?: string;
  path: string;
}

interface Props {
  sources: MusicSources;
  onSave: (custom: boolean, items: Chosen[]) => Promise<void>;
  onClose: () => void;
}

const spaceLabel = (kind: string) => (kind === "shared" ? "共享空间" : "个人空间");

// SourcesDialog chooses the music folders: by default the whole personal
// space and the shared space, or only the folders the member picks.
export function SourcesDialog({ sources, onSave, onClose }: Props) {
  const [custom, setCustom] = useState(sources.custom);
  const [chosen, setChosen] = useState<Chosen[]>(sources.custom ? sources.items : []);
  const [picking, setPicking] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    const escape = (event: KeyboardEvent) => {
      if (event.key === "Escape" && !saving) onClose();
    };
    window.addEventListener("keydown", escape);
    return () => window.removeEventListener("keydown", escape);
  }, [onClose, saving]);
  const save = async () => {
    setSaving(true);
    setError("");
    try {
      await onSave(custom, custom ? chosen : []);
    } catch (caught) {
      setError(errorText(caught, "music"));
      setSaving(false);
    }
  };
  return (
    <div
      className={feedbackStyles.phDialogBackdrop}
      onPointerDown={(event) => {
        if (event.target === event.currentTarget && !saving) onClose();
      }}
    >
      <div
        className={`${feedbackStyles.phDialog} ${styles.muSources}`}
        role="dialog"
        aria-modal="true"
        aria-label="音乐文件夹"
      >
        <header>
          <h3>音乐文件夹</h3>
          <small>音乐库只整理这些位置里的音乐。文件留在原处，不会被复制或移动。</small>
          <button type="button" className={feedbackStyles.phDialogClose} aria-label="关闭" onClick={onClose}>
            <X />
          </button>
        </header>
        {picking ? (
          <FolderPicker
            onCancel={() => setPicking(false)}
            onPick={(item) => {
              setChosen((current) =>
                current.some((other) => other.spaceId === item.spaceId && other.path === item.path)
                  ? current
                  : [...current, item],
              );
              setPicking(false);
            }}
          />
        ) : (
          <>
            <div className={styles.muSourceModes} role="radiogroup" aria-label="音乐来源">
              <button
                type="button"
                role="radio"
                aria-checked={!custom}
                className={!custom ? styles.active : ""}
                onClick={() => setCustom(false)}
              >
                <span className={styles.muRadio} />
                <span>
                  <strong>个人空间和共享空间</strong>
                  <small>自动整理两个空间里的全部音乐（推荐）</small>
                </span>
              </button>
              <button
                type="button"
                role="radio"
                aria-checked={custom}
                className={custom ? styles.active : ""}
                onClick={() => setCustom(true)}
              >
                <span className={styles.muRadio} />
                <span>
                  <strong>只整理指定的文件夹</strong>
                  <small>适合把有声书、录音等和音乐分开</small>
                </span>
              </button>
            </div>
            {custom && (
              <div className={styles.muSourceList}>
                {chosen.map((item) => (
                  <div key={`${item.spaceId}:${item.path}`} className={styles.muSource}>
                    {item.spaceKind === "shared" ? <Users /> : <HardDrive />}
                    <span>
                      <strong>{item.path ? item.path.split("/").at(-1) : spaceLabel(item.spaceKind)}</strong>
                      <small>
                        {spaceLabel(item.spaceKind)}
                        {item.path ? ` / ${item.path}` : ""}
                      </small>
                    </span>
                    <button
                      type="button"
                      className={`${styles.muIconButton} ${styles.small}`}
                      aria-label="移除文件夹"
                      onClick={() => setChosen((current) => current.filter((other) => other !== item))}
                    >
                      <X />
                    </button>
                  </div>
                ))}
                {!chosen.length && <p className={styles.muSourceEmpty}>还没有选择文件夹</p>}
                <button type="button" className={styles.muSourceAdd} onClick={() => setPicking(true)}>
                  <FolderPlus />
                  添加文件夹
                </button>
              </div>
            )}
            {error && <p className={styles.muFormError}>{error}</p>}
            <div className={`${styles.phDialogActions} ${feedbackStyles.phDialogActions}`}>
              <button type="button" onClick={onClose}>
                取消
              </button>
              <button
                type="button"
                className="primary"
                disabled={saving || (custom && !chosen.length)}
                onClick={() => void save()}
              >
                {saving ? "正在保存…" : "保存并重新整理"}
              </button>
            </div>
          </>
        )}
      </div>
    </div>
  );
}

// FolderPicker browses the member's own spaces for a folder.
function FolderPicker({ onPick, onCancel }: { onPick: (item: Chosen) => void; onCancel: () => void }) {
  const [spaces, setSpaces] = useState<Space[]>();
  const [space, setSpace] = useState<Space>();
  const [trail, setTrail] = useState<FileEntry[]>([]);
  const [entries, setEntries] = useState<FileEntry[]>();
  const [error, setError] = useState("");
  useEffect(() => {
    listSpaces().then(
      (items) => setSpaces(items.filter((item) => !item.viewing)),
      () => setError("无法读取空间"),
    );
  }, []);
  const parent = trail.at(-1);
  const [browsedBefore, setBrowsedBefore] = useState({ space, parent });
  if (browsedBefore.space !== space || browsedBefore.parent !== parent) {
    setBrowsedBefore({ space, parent });
    if (space) setEntries(undefined);
  }
  useEffect(() => {
    if (!space) return;
    let live = true;
    listEntries(space.id, parent?.id ?? "").then(
      (items) => {
        if (live) setEntries(items.filter((item) => item.kind === "directory"));
      },
      () => {
        if (live) setError("无法读取文件夹");
      },
    );
    return () => {
      live = false;
    };
  }, [space, parent]);
  const path = trail.map((entry) => entry.name).join("/");
  return (
    <div className={styles.muPicker}>
      <div className={styles.muPickerBar}>
        <button
          type="button"
          className={`${styles.muIconButton} ${styles.small}`}
          aria-label="返回"
          onClick={() => {
            if (trail.length) setTrail(trail.slice(0, -1));
            else if (space) setSpace(undefined);
            else onCancel();
          }}
        >
          <ArrowLeft />
        </button>
        <span>{space ? `${spaceLabel(space.kind)}${path ? ` / ${path}` : ""}` : "选择位置"}</span>
      </div>
      <div className={styles.muPickerList}>
        {!space &&
          (spaces ?? []).map((item) => (
            <button key={item.id} type="button" onClick={() => setSpace(item)}>
              {item.kind === "shared" ? <Users /> : <HardDrive />}
              <span>{spaceLabel(item.kind)}</span>
              <ChevronRight />
            </button>
          ))}
        {space && !entries && (
          <p className={styles.muPickerNote}>
            <Loader2 className={styles.spin} />
            正在读取…
          </p>
        )}
        {space &&
          entries?.map((entry) => (
            <button key={entry.id} type="button" onClick={() => setTrail([...trail, entry])}>
              <Folder />
              <span>{entry.name}</span>
              <ChevronRight />
            </button>
          ))}
        {space && entries && !entries.length && <p className={styles.muPickerNote}>这里没有子文件夹</p>}
        {error && <p className={styles.muFormError}>{error}</p>}
      </div>
      <div className={`${styles.phDialogActions} ${feedbackStyles.phDialogActions}`}>
        <button type="button" onClick={onCancel}>
          取消
        </button>
        <button
          type="button"
          className="primary"
          disabled={!space}
          onClick={() => space && onPick({ spaceId: space.id, spaceKind: space.kind, folderId: parent?.id, path })}
        >
          <Check />
          {trail.length ? `选择“${parent!.name}”` : space ? `选择整个${spaceLabel(space.kind)}` : "选择"}
        </button>
      </div>
    </div>
  );
}
