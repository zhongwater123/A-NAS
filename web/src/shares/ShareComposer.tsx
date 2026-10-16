import { useEffect, useRef, useState } from "react";
import { Check, Clock3, FileText, Folder, LoaderCircle, Search, Send, Share2, Users, X } from "lucide-react";
import type { FileEntry, Profile } from "../api";
import { formatBytes } from "../bytes";
import { PersonAvatar } from "../PersonAvatar";
import { displayName } from "../people";
import { createShare, errorText, listMembers, requestKey } from "./api";
import { EmojiInput } from "./EmojiInput";

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
  const filtered = members.filter((member) => `${displayName(member)} ${member.username}`.toLocaleLowerCase().includes(query.trim().toLocaleLowerCase()));

  return <div className="share-modal-backdrop">
    <form ref={form} className="share-modal share-compose" role="dialog" aria-modal="true" aria-label="分享给成员"
      onSubmit={(event) => { event.preventDefault(); void submit(); }}
      onKeyDown={(event) => {
        if (event.key === "Escape") { event.stopPropagation(); if (!busy) onClose(); }
        if (event.key !== "Tab") return;
        const controls = Array.from(form.current?.querySelectorAll<HTMLElement>('button:not(:disabled), input:not(:disabled), textarea:not(:disabled)') ?? []);
        const first = controls[0], last = controls.at(-1);
        if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last?.focus(); }
        else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus(); }
      }}>
      <header className="share-compose-header">
        <span className="share-compose-mark"><Share2 /></span>
        <div><h2>分享给成员</h2><p>把文件分享给 NAS 上的家人和伙伴</p></div>
        <button type="button" className="share-icon-button" aria-label="关闭分享" disabled={busy} onClick={onClose}><X /></button>
      </header>
      <div className="share-compose-body">
        <section className="share-source" aria-label="分享内容">
          <div className="share-section-label"><strong>分享内容</strong><span>{entries.length} 个项目</span></div>
          <div className="share-source-list">{entries.map((entry) => <div className="share-source-item" key={entry.id}>
            <span className={`share-source-icon ${entry.kind === "directory" ? "folder" : ""}`}>{entry.kind === "directory" ? <Folder /> : <FileText />}</span>
            <strong title={entry.name}>{entry.name}</strong><small>{entry.kind === "directory" ? "文件夹" : formatBytes(entry.sizeBytes)}</small>
          </div>)}</div>
          <div className="share-source-note"><Clock3 /><span>7 天有效 · 原文件后续修改不影响这次分享</span></div>
        </section>
        <fieldset className="share-recipient-picker" disabled={busy}>
          <legend>选择接收成员 <span>{recipientIds.length ? `已选 ${recipientIds.length} 人` : "可多选"}</span></legend>
          <div className="share-member-search"><Search /><input type="search" aria-label="搜索成员" placeholder="搜索昵称或账号" value={query} onChange={(event) => setQuery(event.target.value)} /></div>
          <div className="share-member-grid" aria-busy={loading}>
            {loading ? <p className="share-picker-empty"><LoaderCircle className="share-spinner" />正在读取成员…</p>
              : memberError ? <div className="share-picker-empty"><p role="alert">{memberError}</p><button type="button" onClick={() => setReload((value) => value + 1)}>重新加载</button></div>
                : !filtered.length ? <p className="share-picker-empty">{members.length ? "没有找到匹配的成员" : "暂无其他成员"}</p>
                  : filtered.map((member) => {
                    const selected = recipientIds.includes(member.id);
                    return <label className={`share-member-card ${selected ? "selected" : ""}`} key={member.id}>
                      <input type="checkbox" aria-label={displayName(member)} checked={selected} disabled={!selected && recipientIds.length >= 100}
                        onChange={(event) => setRecipients((ids) => event.target.checked ? [...ids, member.id] : ids.filter((id) => id !== member.id))} />
                      <PersonAvatar who={member} className="share-person-avatar" />
                      <span className="share-member-name"><strong>{displayName(member)}</strong><small>{member.nickname ? `@${member.username}` : "NAS 成员"}</small></span>
                      <span className="share-member-check" aria-hidden="true">{selected && <Check />}</span>
                    </label>;
                  })}
          </div>
          <p className="share-recipient-hint"><Users />每位成员与你单独对话，回复仅彼此可见</p>
        </fieldset>
        <EmojiInput label="留言" value={message} onChange={setMessage} disabled={busy} />
        {error && <p role="alert" className="share-error">{error}</p>}
      </div>
      <footer className="share-compose-footer">
        <span aria-live="polite">{busy ? "正在发送，请稍候…" : recipientIds.length ? `分享给 ${recipientIds.length} 位成员` : "选择成员后即可发送"}</span>
        <button type="button" disabled={busy} onClick={onClose}>取消</button>
        <button type="submit" className="primary" disabled={busy || loading || !recipientIds.length}>{busy ? <LoaderCircle className="share-spinner" /> : <Send />}{busy ? "正在提交…" : "发送分享"}</button>
      </footer>
    </form>
  </div>;
}
