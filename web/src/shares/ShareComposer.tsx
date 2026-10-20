import { useEffect, useRef, useState } from "react";
import { Check, Clock3, LoaderCircle, Search, Send, X } from "lucide-react";
import type { FileEntry, Profile } from "../api";
import { formatBytes } from "../bytes";
import { PersonAvatar } from "../PersonAvatar";
import { displayName } from "../people";
import { createShare, listMembers, requestKey } from "./api";
import { EmojiInput } from "./EmojiInput";
import { errorText } from "./format";
import { EntryTile } from "./parts";

export function ShareComposer({ entries, onClose, onCreated }: {
  entries: FileEntry[];
  onClose: () => void;
  onCreated: (id: string) => void;
}) {
  const [members, setMembers] = useState<Profile[]>([]);
  const [recipientIds, setRecipients] = useState<string[]>([]);
  const [query, setQuery] = useState("");
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const [memberError, setMemberError] = useState("");
  const [loading, setLoading] = useState(true);
  const [reload, setReload] = useState(0);
  const [busy, setBusy] = useState(false);
  const mounted = useRef(true);
  const form = useRef<HTMLFormElement>(null);
  const submitting = useRef(false);
  const attempt = useRef<{ signature: string; key: string } | undefined>(undefined);

  useEffect(() => {
    mounted.current = true;
    const previous = document.activeElement as HTMLElement | null;
    form.current?.querySelector<HTMLInputElement>('input[type="search"]')?.focus();
    return () => { mounted.current = false; previous?.focus(); };
  }, []);
  useEffect(() => {
    let stopped = false;
    setLoading(true); setMemberError("");
    void listMembers().then((items) => {
      if (!stopped) setMembers(items);
    }).catch((error) => {
      if (!stopped) setMemberError(errorText(error));
    }).finally(() => { if (!stopped) setLoading(false); });
    return () => { stopped = true; };
  }, [reload]);

  const submit = async () => {
    if (submitting.current || loading || !recipientIds.length) return;
    submitting.current = true;
    setBusy(true); setError("");
    try {
      const sourceIds = entries.map((entry) => entry.id);
      const signature = JSON.stringify({ sourceIds, recipientIds, message });
      if (attempt.current?.signature !== signature) attempt.current = { signature, key: requestKey() };
      const share = await createShare({ requestId: attempt.current.key, sourceIds, recipientIds, message });
      if (mounted.current) onCreated(share.id);
    } catch (error) {
      if (mounted.current) setError(errorText(error));
    } finally {
      submitting.current = false;
      if (mounted.current) setBusy(false);
    }
  };
  const needle = query.trim().toLocaleLowerCase();
  const filtered = members.filter((member) => `${displayName(member)} ${member.username}`.toLocaleLowerCase().includes(needle));
  const folders = entries.filter((entry) => entry.kind === "directory").length;
  const bytes = entries.filter((entry) => entry.kind === "file").reduce((sum, entry) => sum + entry.sizeBytes, 0);
  const everyone = members.length > 0 && members.every((member) => recipientIds.includes(member.id));
  const first = entries[0];

  return <div className="sh-backdrop">
    <form ref={form} className="sh-compose" role="dialog" aria-modal="true" aria-label="分享给成员"
      onSubmit={(event) => { event.preventDefault(); void submit(); }}
      onKeyDown={(event) => {
        if (event.key === "Escape") { event.stopPropagation(); if (!busy) onClose(); }
        if (event.key !== "Tab") return;
        const controls = Array.from(form.current?.querySelectorAll<HTMLElement>('button:not(:disabled), input:not(:disabled), textarea:not(:disabled)') ?? []);
        const head = controls[0], tail = controls.at(-1);
        if (event.shiftKey && document.activeElement === head) { event.preventDefault(); tail?.focus(); }
        else if (!event.shiftKey && document.activeElement === tail) { event.preventDefault(); head?.focus(); }
      }}>
      <header className="sh-compose-head">
        <h2>分享给成员</h2>
        <button type="button" className="sh-icon-btn" aria-label="关闭分享" disabled={busy} onClick={onClose}><X /></button>
      </header>
      <div className="sh-compose-body">
        <section className="sh-compose-items" aria-label="分享内容">
          {first && <EntryTile name={first.name} isDir={first.kind === "directory"} size="lg" />}
          <div>
            <strong title={entries.map((entry) => entry.name).join("、")}>{entries.length > 1 ? `${first?.name} 等 ${entries.length} 项` : first?.name}</strong>
            <small>{[folders && `${folders} 个文件夹`, entries.length - folders && `${entries.length - folders} 个文件`, !folders && bytes > 0 && formatBytes(bytes)].filter(Boolean).join(" · ")}</small>
          </div>
          {entries.length > 1 && <ul className="sh-compose-names">{entries.slice(0, 6).map((entry) => <li key={entry.id} title={entry.name}><EntryTile name={entry.name} isDir={entry.kind === "directory"} size="sm" /><span>{entry.name}</span></li>)}{entries.length > 6 && <li className="more">还有 {entries.length - 6} 项</li>}</ul>}
        </section>
        <fieldset className="sh-compose-people" disabled={busy}>
          <legend>发给<span>{recipientIds.length ? `已选 ${recipientIds.length} 人` : ""}</span></legend>
          <div className="sh-compose-tools">
            <label className="sh-search"><Search /><input type="search" aria-label="搜索成员" placeholder="搜索昵称或账号" value={query} onChange={(event) => setQuery(event.target.value)} /></label>
            {members.length > 1 && <button type="button" className="sh-btn small ghost" onClick={() => setRecipients(everyone ? [] : members.slice(0, 100).map((member) => member.id))}>{everyone ? "取消全选" : "全选"}</button>}
          </div>
          <div className="sh-people" aria-busy={loading}>
            {loading ? <p className="sh-people-note"><LoaderCircle className="sh-spin" />正在读取成员…</p>
              : memberError ? <div className="sh-people-note"><p role="alert">{memberError}</p><button type="button" className="sh-btn small" onClick={() => setReload((value) => value + 1)}>重新加载</button></div>
                : !filtered.length ? <p className="sh-people-note">{members.length ? "没有找到匹配的成员" : "这台 NAS 还没有其他成员"}</p>
                  : filtered.map((member) => {
                    const chosen = recipientIds.includes(member.id);
                    return <label className={`sh-person${chosen ? " chosen" : ""}`} key={member.id} title={member.nickname ? `${member.nickname}（${member.username}）` : member.username}>
                      <input type="checkbox" aria-label={displayName(member)} checked={chosen} disabled={!chosen && recipientIds.length >= 100}
                        onChange={(event) => setRecipients((ids) => event.target.checked ? [...ids, member.id] : ids.filter((id) => id !== member.id))} />
                      <span className="sh-person-face"><PersonAvatar who={member} className="sh-avatar sh-avatar-xl" /><span className="sh-person-check" aria-hidden="true"><Check /></span></span>
                      <strong>{displayName(member)}</strong>
                      {member.nickname && <small>{member.username}</small>}
                    </label>;
                  })}
          </div>
        </fieldset>
        <EmojiInput label="留言" placeholder="说点什么…" value={message} onChange={setMessage} disabled={busy} />
        {error && <p role="alert" className="sh-alert">{error}</p>}
      </div>
      <footer className="sh-compose-foot">
        <span className="sh-foot-note"><Clock3 />7 天内有效 · 内容固定，不随原文件变化</span>
        <button type="button" className="sh-btn" disabled={busy} onClick={onClose}>取消</button>
        <button type="submit" className="sh-btn primary" disabled={busy || loading || !recipientIds.length}>{busy ? <LoaderCircle className="sh-spin" /> : <Send />}{busy ? "正在发送…" : "发送分享"}</button>
      </footer>
    </form>
  </div>;
}
