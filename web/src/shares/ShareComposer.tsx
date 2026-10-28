import { Check, Clock3, LoaderCircle, Search, Send, X } from "lucide-react";
import { useEffect, useRef, useState } from "react";

import type { FileEntry, Profile } from "../api.generated";
import { formatBytes } from "../lib/bytes";
import { PersonAvatar } from "../people/PersonAvatar";
import { displayName } from "../people/people";
import { createShare, listMembers, requestKey } from "./api";
import avatars from "./avatars.module.css";
import { EmojiInput } from "./EmojiInput";
import { errorText } from "./format";
import palette from "./palette.module.css";
import { EntryTile } from "./parts";
import styles from "./shares.module.css";

export function ShareComposer({
  entries,
  onClose,
  onCreated,
}: {
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
  const search = useRef<HTMLInputElement>(null);
  const submitting = useRef(false);
  const attempt = useRef<{ signature: string; key: string } | undefined>(undefined);

  useEffect(() => {
    mounted.current = true;
    const previous = document.activeElement as HTMLElement | null;
    form.current?.querySelector<HTMLInputElement>('input[type="search"]')?.focus();
    return () => {
      mounted.current = false;
      previous?.focus();
    };
  }, []);
  useEffect(() => {
    let stopped = false;
    void listMembers()
      .then((items) => {
        if (!stopped) setMembers(items);
      })
      .catch((error) => {
        if (!stopped) setMemberError(errorText(error));
      })
      .finally(() => {
        if (!stopped) setLoading(false);
      });
    return () => {
      stopped = true;
    };
  }, [reload]);

  const submit = async () => {
    if (submitting.current || loading || !recipientIds.length) return;
    submitting.current = true;
    setBusy(true);
    setError("");
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
  const filtered = members.filter((member) =>
    `${displayName(member)} ${member.username}`.toLocaleLowerCase().includes(needle),
  );
  const folders = entries.filter((entry) => entry.kind === "directory").length;
  const bytes = entries.filter((entry) => entry.kind === "file").reduce((sum, entry) => sum + entry.sizeBytes, 0);
  const chosen = recipientIds.flatMap((id) => members.filter((member) => member.id === id));
  const everyone = members.length > 0 && members.every((member) => recipientIds.includes(member.id));
  const first = entries[0];

  return (
    <div className={`${palette.shPalette} ${styles.shBackdrop}`}>
      <form
        ref={form}
        className={styles.shCompose}
        role="dialog"
        aria-modal="true"
        aria-label="分享给成员"
        onSubmit={(event) => {
          event.preventDefault();
          void submit();
        }}
        onKeyDown={(event) => {
          if (event.key === "Escape") {
            event.stopPropagation();
            if (!busy) onClose();
          }
          if (event.key !== "Tab") return;
          const controls = Array.from(
            form.current?.querySelectorAll<HTMLElement>(
              "button:not(:disabled), input:not(:disabled), textarea:not(:disabled)",
            ) ?? [],
          );
          const head = controls[0],
            tail = controls.at(-1);
          if (event.shiftKey && document.activeElement === head) {
            event.preventDefault();
            tail?.focus();
          } else if (!event.shiftKey && document.activeElement === tail) {
            event.preventDefault();
            head?.focus();
          }
        }}
      >
        <header className={styles.shComposeHead}>
          <h2>分享给成员</h2>
          <button type="button" className={styles.shIconBtn} aria-label="关闭分享" disabled={busy} onClick={onClose}>
            <X />
          </button>
        </header>
        <div className={styles.shComposeBody}>
          <section className={styles.shComposeItems} aria-label="分享内容">
            {first && <EntryTile name={first.name} isDir={first.kind === "directory"} size="lg" />}
            <div>
              <strong title={entries.map((entry) => entry.name).join("、")}>
                {entries.length > 1 ? `${first?.name} 等 ${entries.length} 项` : first?.name}
              </strong>
              <small>
                {[
                  folders && `${folders} 个文件夹`,
                  entries.length - folders && `${entries.length - folders} 个文件`,
                  !folders && bytes > 0 && formatBytes(bytes),
                ]
                  .filter(Boolean)
                  .join(" · ")}
              </small>
            </div>
            {entries.length > 1 && (
              <ul className={styles.shComposeNames}>
                {entries.slice(0, 6).map((entry) => (
                  <li key={entry.id} title={entry.name}>
                    <EntryTile name={entry.name} isDir={entry.kind === "directory"} size="sm" />
                    <span>{entry.name}</span>
                  </li>
                ))}
                {entries.length > 6 && <li className={styles.more}>还有 {entries.length - 6} 项</li>}
              </ul>
            )}
          </section>
          <fieldset className={styles.shComposePeople} disabled={busy}>
            <legend className={styles.shSr}>发给</legend>
            {/* The chosen members sit between the heading and the count, in a
                row whose height does not change when the first one is chosen,
                so the list never moves under the pointer. */}
            <div className={styles.shComposePeopleHead}>
              <span aria-hidden="true">发给</span>
              {!chosen.length ? (
                <p>点击头像选择要分享的成员</p>
              ) : (
                <ul className={styles.shChosen} aria-label="已选成员">
                  {chosen.map((member, index) => (
                    <li key={member.id}>
                      <PersonAvatar who={member} className={`${avatars.shAvatar} ${avatars.shAvatarXs}`} />
                      <span>{displayName(member)}</span>
                      <button
                        type="button"
                        aria-label={`取消选择 ${displayName(member)}`}
                        onClick={(event) => {
                          // Keep focus in the row: the next tag, the previous
                          // one, or the member search once the row is empty.
                          const tags = event.currentTarget.closest("ul")?.querySelectorAll("button");
                          (tags?.[index + 1] ?? tags?.[index - 1] ?? search.current)?.focus();
                          setRecipients((ids) => ids.filter((id) => id !== member.id));
                        }}
                      >
                        <X />
                      </button>
                    </li>
                  ))}
                </ul>
              )}
              {recipientIds.length > 0 && <strong>已选 {recipientIds.length} 人</strong>}
            </div>
            <div className={styles.shComposeTools}>
              <label className={styles.shSearch}>
                <Search />
                <input
                  ref={search}
                  type="search"
                  aria-label="搜索成员"
                  placeholder="搜索昵称或账号"
                  value={query}
                  onChange={(event) => setQuery(event.target.value)}
                />
              </label>
              {members.length > 1 && (
                <button
                  type="button"
                  className={`${styles.shBtn} ${styles.small} ${styles.ghost}`}
                  onClick={() => setRecipients(everyone ? [] : members.slice(0, 100).map((member) => member.id))}
                >
                  {everyone ? "取消全选" : "全选"}
                </button>
              )}
            </div>
            <div className={styles.shPeople} aria-busy={loading}>
              {loading ? (
                <p className={styles.shPeopleNote}>
                  <LoaderCircle className={styles.shSpin} />
                  正在读取成员…
                </p>
              ) : memberError ? (
                <div className={styles.shPeopleNote}>
                  <p role="alert">{memberError}</p>
                  <button
                    type="button"
                    className={`${styles.shBtn} ${styles.small}`}
                    onClick={() => {
                      setLoading(true);
                      setMemberError("");
                      setReload((value) => value + 1);
                    }}
                  >
                    重新加载
                  </button>
                </div>
              ) : !filtered.length ? (
                <p className={styles.shPeopleNote}>
                  {members.length ? "没有找到匹配的成员" : "这台 NAS 还没有其他成员"}
                </p>
              ) : (
                filtered.map((member) => {
                  const chosen = recipientIds.includes(member.id);
                  return (
                    <label
                      className={`${styles.shPerson}${chosen ? ` ${styles.chosen}` : ""}`}
                      key={member.id}
                      title={member.nickname ? `${member.nickname}（${member.username}）` : member.username}
                    >
                      <input
                        type="checkbox"
                        aria-label={displayName(member)}
                        checked={chosen}
                        disabled={!chosen && recipientIds.length >= 100}
                        onChange={(event) =>
                          setRecipients((ids) =>
                            event.target.checked ? [...ids, member.id] : ids.filter((id) => id !== member.id),
                          )
                        }
                      />
                      <span className={styles.shPersonFace}>
                        <PersonAvatar who={member} className={`${avatars.shAvatar} ${avatars.shAvatarXl}`} />
                        <span className={styles.shPersonCheck} aria-hidden="true">
                          <Check />
                        </span>
                      </span>
                      <strong>{displayName(member)}</strong>
                      {member.nickname && <small>{member.username}</small>}
                    </label>
                  );
                })
              )}
            </div>
          </fieldset>
          <EmojiInput label="留言" placeholder="说点什么…" value={message} onChange={setMessage} disabled={busy} />
          {error && (
            <p role="alert" className={styles.shAlert}>
              {error}
            </p>
          )}
        </div>
        <footer className={styles.shComposeFoot}>
          <span className={styles.shFootNote}>
            <Clock3 />7 天内有效 · 内容固定，不随原文件变化
          </span>
          <button type="button" className={styles.shBtn} disabled={busy} onClick={onClose}>
            取消
          </button>
          <button
            type="submit"
            className={`${styles.shBtn} primary`}
            disabled={busy || loading || !recipientIds.length}
          >
            {busy ? <LoaderCircle className={styles.shSpin} /> : <Send />}
            {busy ? "正在发送…" : "发送分享"}
          </button>
        </footer>
      </form>
    </div>
  );
}
