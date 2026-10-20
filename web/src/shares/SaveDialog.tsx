import { useEffect, useRef, useState } from "react";
import { ChevronRight, Folder, FolderPlus, HardDrive, LoaderCircle, Users, X } from "lucide-react";
import { createDirectory, listEntries, listSpaces, type FileEntry, type Space } from "../api";
import { formatBytes } from "../bytes";
import { requestKey, saveShare, type Job, type Share } from "./api";
import { errorText, leaf } from "./format";
import { EntryTile } from "./parts";

const spaceName = (space: Space) => space.kind === "private" ? "个人空间" : space.name;

// SaveDialog picks a folder the reader may write to and starts a save job on
// the NAS; it does not wait for the copy.
export function SaveDialog({ share, entryIds, onClose, onSaved }: { share: Share; entryIds: string[]; onClose: () => void; onSaved: (job: Job, place: string) => void }) {
  const [spaces, setSpaces] = useState<Space[]>([]);
  const [spaceId, setSpaceId] = useState("");
  const [trail, setTrail] = useState<FileEntry[]>([]);
  const [folders, setFolders] = useState<FileEntry[]>([]);
  const [error, setError] = useState("");
  const [working, setWorking] = useState<"" | "save" | "create">("");
  const busy = working !== "";
  const [loading, setLoading] = useState(true);
  const [naming, setNaming] = useState<string>();
  const attempt = useRef<{ signature: string; key: string } | undefined>(undefined);
  const layer = useRef<HTMLDivElement>(null);
  const parentId = trail.at(-1)?.id ?? "";
  useEffect(() => {
    let stopped = false;
    void listSpaces().then((items) => {
      if (stopped) return;
      const allowed = items.filter((space) => !space.viewing && space.writable !== false);
      setSpaces(allowed); setSpaceId(allowed[0]?.id ?? "");
      if (!allowed.length) setLoading(false);
    }).catch((error) => { if (!stopped) { setError(errorText(error)); setLoading(false); } });
    return () => { stopped = true; };
  }, []);
  useEffect(() => {
    if (!spaceId) return;
    let stopped = false;
    setLoading(true); setFolders([]);
    void listEntries(spaceId, parentId).then((items) => {
      if (stopped) return;
      setFolders(items.filter((entry) => entry.kind === "directory" && (entry as FileEntry & { writable?: boolean }).writable !== false).sort((a, b) => a.name.localeCompare(b.name, "zh-CN")));
      setError("");
    }).catch((error) => { if (!stopped) setError(errorText(error)); }).finally(() => { if (!stopped) setLoading(false); });
    return () => { stopped = true; };
  }, [spaceId, parentId]);
  useEffect(() => { layer.current?.querySelector<HTMLElement>(".sh-save-spaces button")?.focus(); }, [spaces.length]);

  const chosen = share.entries.filter((entry) => entryIds.includes(entry.id));
  const bytes = share.entries.filter((entry) => !entry.isDir && chosen.some((item) => item.id === entry.id || entry.path.startsWith(`${item.path}/`))).reduce((sum, entry) => sum + entry.size, 0);
  const summary = chosen.length === 1 ? leaf(chosen[0].path) : `${leaf(chosen[0]?.path ?? "")} 等 ${chosen.length} 项`;
  const space = spaces.find((item) => item.id === spaceId);
  const place = [space ? spaceName(space) : "", ...trail.map((entry) => entry.name)].filter(Boolean).join(" / ");
  const save = async () => {
    if (busy || loading || !spaceId) return;
    const target = { rootId: spaceId, parentId };
    const signature = JSON.stringify({ entryIds, target });
    setWorking("save"); setError("");
    try {
      if (attempt.current?.signature !== signature) attempt.current = { signature, key: requestKey() };
      onSaved(await saveShare(share.id, { requestId: attempt.current.key, entryIds, target }), place);
    } catch (error) { setError(errorText(error)); } finally { setWorking(""); }
  };
  const create = async () => {
    const name = naming?.trim();
    if (!name || busy) return;
    setWorking("create"); setError("");
    try { const folder = await createDirectory(spaceId, parentId, name); setNaming(undefined); setTrail((previous) => [...previous, folder]); }
    catch (error) { setError(errorText(error)); } finally { setWorking(""); }
  };
  return <div ref={layer} className="sh-layer" onKeyDown={(event) => { if (event.key === "Escape" && naming === undefined && !busy) { event.stopPropagation(); onClose(); } }}>
    <section className="sh-dialog sh-save" role="dialog" aria-modal="true" aria-labelledby="sh-save-title">
      <header className="sh-dialog-head">
        <div><h2 id="sh-save-title">保存到我的空间</h2><p className="sh-save-what">{chosen[0] && <EntryTile name={chosen[0].path} isDir={chosen[0].isDir} size="sm" />}<span>{summary}</span>{bytes > 0 && <small>{formatBytes(bytes)}</small>}</p></div>
        <button className="sh-icon-btn" aria-label="关闭保存位置" disabled={busy} onClick={onClose}><X /></button>
      </header>
      <div className="sh-save-body">
        <nav className="sh-save-spaces" aria-label="目标空间">
          {spaces.map((item) => <button key={item.id} aria-pressed={item.id === spaceId} disabled={busy} onClick={() => { setSpaceId(item.id); setTrail([]); setNaming(undefined); }}>{item.kind === "private" ? <HardDrive /> : <Users />}<span>{spaceName(item)}</span></button>)}
        </nav>
        <div className="sh-save-browser">
          <nav className="sh-crumbs" aria-label="保存位置">
            <button disabled={!trail.length || busy} onClick={() => setTrail([])}>{space ? spaceName(space) : "—"}</button>
            {trail.map((entry, index) => <span key={entry.id}><ChevronRight /><button disabled={index === trail.length - 1 || busy} onClick={() => setTrail((previous) => previous.slice(0, index + 1))}>{entry.name}</button></span>)}
          </nav>
          <div className="sh-folders" aria-busy={loading}>
            {naming !== undefined && <form className="sh-folder-new" onSubmit={(event) => { event.preventDefault(); void create(); }}>
              <Folder /><input autoFocus aria-label="新文件夹名称" value={naming} maxLength={255} placeholder="新文件夹" disabled={busy}
                onKeyDown={(event) => { if (event.key === "Escape") { event.stopPropagation(); setNaming(undefined); } }} onChange={(event) => setNaming(event.target.value)} />
              <button type="submit" className="sh-btn primary small" disabled={busy || !naming.trim()}>创建</button>
              <button type="button" className="sh-btn small" disabled={busy} onClick={() => setNaming(undefined)}>取消</button>
            </form>}
            {loading ? <p className="sh-folders-note"><LoaderCircle className="sh-spin" />正在读取文件夹…</p>
              : !spaces.length ? <p className="sh-folders-note">没有可以写入的个人空间或共享文件夹</p>
                : folders.map((entry) => <button key={entry.id} disabled={busy} onClick={() => { setNaming(undefined); setTrail((previous) => [...previous, entry]); }}><Folder />{entry.name}<ChevronRight /></button>)}
            {!loading && spaces.length > 0 && !folders.length && naming === undefined && <p className="sh-folders-note">这里没有子文件夹</p>}
          </div>
        </div>
      </div>
      {error && <p role="alert" className="sh-alert sh-dialog-alert">{error}</p>}
      <footer className="sh-dialog-foot">
        <button className="sh-btn ghost" disabled={!spaceId || busy || naming !== undefined} onClick={() => { setNaming(""); }}><FolderPlus />新建文件夹</button>
        <span className="sh-foot-note">同名文件会自动改名，不会覆盖</span>
        <button className="sh-btn" disabled={busy} onClick={onClose}>取消</button>
        <button className="sh-btn primary" disabled={busy || loading || !spaceId || naming !== undefined} onClick={() => void save()}>{working === "save" && <LoaderCircle className="sh-spin" />}{working === "save" ? "正在创建任务…" : "保存到此文件夹"}</button>
      </footer>
    </section>
  </div>;
}
