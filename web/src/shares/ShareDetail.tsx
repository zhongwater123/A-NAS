import {
  ArrowLeft,
  Check,
  ChevronDown,
  ChevronRight,
  CircleAlert,
  Clock3,
  Download,
  LoaderCircle,
  MessageCircle,
  Search,
  Undo2,
  X,
} from "lucide-react";
import { type ReactNode, useEffect, useEffectEvent, useRef, useState } from "react";
import { createPortal } from "react-dom";

import { formatBytes } from "../lib/bytes";
import { usePeople } from "../people/people";
import { getShare, type Job, listJobs, revokeShare, type Share, type ShareEntry, viewShare } from "./api";
import { Avatar, nameOf, names } from "./avatars";
import { Conversation, type Thread, useThread } from "./Conversation";
import {
  errorText,
  fileBytes,
  jobDone,
  jobLabel,
  leaf,
  moment,
  type Phase,
  parentPath,
  phaseLabel,
  phaseOf,
  reasonText,
  receipt,
  remaining,
  shareTitle,
  topLevel,
  when,
} from "./format";
import palette from "./palette.module.css";
import { EntryTile, kindOf } from "./parts";
import { SaveDialog } from "./SaveDialog";
import { SharePreview } from "./SharePreview";
import type { Layout } from "./SharesPanel";
import { useShares } from "./SharesProvider";
import styles from "./shares.module.css";

const byName = (a: ShareEntry, b: ShareEntry) =>
  Number(b.isDir) - Number(a.isDir) || leaf(a.path).localeCompare(leaf(b.path), "zh-CN");

export function ShareDetail({
  id,
  layout,
  layerHost,
  preferredRecipient,
  onBack,
  onDirection,
}: {
  id: string;
  layout: Layout;
  // Dialogs and the viewer cover the whole window, the list included.
  layerHost?: HTMLElement | null;
  preferredRecipient?: string;
  onBack: () => void;
  onDirection: (direction: "received" | "sent") => void;
}) {
  const shares = useShares();
  const people = usePeople();
  const [share, setShare] = useState<Share>();
  const [jobs, setJobs] = useState<Job[]>([]);
  const [recipient, setRecipient] = useState(preferredRecipient ?? "");
  const [directory, setDirectory] = useState("");
  const [selected, setSelected] = useState<string[]>([]);
  const [preview, setPreview] = useState<string>();
  const [saving, setSaving] = useState<string[]>();
  const [chatOpen, setChatOpen] = useState(layout === "wide" || !!preferredRecipient);
  const [query, setQuery] = useState("");
  const [confirmRevoke, setConfirmRevoke] = useState(false);
  const [revoking, setRevoking] = useState(false);
  const [error, setError] = useState("");
  // Where each save of this window went; the job itself does not say.
  const [places, setPlaces] = useState<Record<string, string>>({});
  const [reload, setReload] = useState(0);
  const isActive = useEffectEvent(() => shares.active);
  const direct = useEffectEvent(onDirection);
  const refreshShares = shares.refresh;
  const viewed = useRef(false);
  const directed = useRef(false);
  const chatToggle = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (!shares.windowVisible) return;
    let stopped = false,
      busy = false;
    const load = async () => {
      if (busy) return;
      busy = true;
      try {
        const value = await getShare(id);
        if (stopped) return;
        setShare(value);
        setRecipient((current) =>
          value.recipients.some((item) => item.userId === current) ? current : (value.recipients[0]?.userId ?? ""),
        );
        if (!directed.current) {
          directed.current = true;
          direct(value.sender.id === shares.userId ? "sent" : "received");
        }
        let warning = "";
        if (value.sender.id !== shares.userId) {
          const shouldView = isActive() && document.visibilityState !== "hidden" && !viewed.current;
          // Neither an unavailable job list nor a failed view receipt blocks the other.
          const [savedJobs, receipt] = await Promise.allSettled([
            listJobs(id),
            shouldView ? viewShare(id) : Promise.resolve(undefined),
          ]);
          if (stopped) return;
          if (savedJobs.status === "fulfilled") setJobs(savedJobs.value);
          else warning = errorText(savedJobs.reason);
          if (receipt.status === "fulfilled" && receipt.value) {
            viewed.current = true;
            setShare(receipt.value);
            refreshShares();
          } else if (receipt.status === "rejected")
            warning = [warning, errorText(receipt.reason)].filter(Boolean).join("；");
        }
        if (!stopped) setError(warning);
      } catch (error) {
        if (!stopped) setError(errorText(error));
      } finally {
        busy = false;
      }
    };
    void load();
    const timer = setInterval(() => void load(), 3000);
    return () => {
      stopped = true;
      clearInterval(timer);
    };
  }, [id, reload, shares.userId, refreshShares, shares.windowVisible]);
  // Each request to open the share with a recipient opens that conversation.
  const [askedBefore, setAskedBefore] = useState({ preferredRecipient, focus: shares.focus });
  if (askedBefore.preferredRecipient !== preferredRecipient || askedBefore.focus !== shares.focus) {
    setAskedBefore({ preferredRecipient, focus: shares.focus });
    if (preferredRecipient) {
      setRecipient(preferredRecipient);
      setChatOpen(true);
    }
  }
  const thread = useThread(id, recipient, shares.windowVisible);
  const phase = share ? phaseOf(share) : undefined;
  const available = phase === "active";
  // A share that is no longer available closes its viewer and save dialog.
  if (!available && (preview !== undefined || saving !== undefined)) {
    setPreview(undefined);
    setSaving(undefined);
  }
  const sender = share?.sender.id === shares.userId;
  const revoke = async () => {
    setRevoking(true);
    try {
      await revokeShare(id);
      setConfirmRevoke(false);
      setReload((value) => value + 1);
      shares.refresh();
    } catch (error) {
      setError(errorText(error));
    } finally {
      setRevoking(false);
    }
  };
  const openChat = (person?: string) => {
    if (person) setRecipient(person);
    setChatOpen(true);
  };
  const closeChat = () => {
    setChatOpen(false);
    chatToggle.current?.focus();
  };
  const docked = layout === "wide";

  if (!share)
    return (
      <section className={styles.shDetail}>
        <div className={styles.shDetailLoading}>
          {error ? (
            <p role="alert" className={styles.shAlert}>
              {error}
            </p>
          ) : (
            <>
              <LoaderCircle className={styles.shSpin} />
              正在读取分享…
            </>
          )}
        </div>
      </section>
    );

  const top = topLevel(share);
  const needle = query.trim().toLocaleLowerCase();
  const visibleEntries = share.entries
    .filter((entry) => parentPath(entry.path) === directory && leaf(entry.path).toLocaleLowerCase().includes(needle))
    .sort(byName);
  const files = visibleEntries.filter((entry) => !entry.isDir);
  const allSelected = visibleEntries.length > 0 && visibleEntries.every((entry) => selected.includes(entry.id));
  const owner = nameOf(people, share.sender.id, share.sender.username);
  const me = share.recipients.find((item) => item.userId === shares.userId);
  const ownUnread = thread.messages.filter(
    (message) =>
      message.sender.id !== shares.userId &&
      message.id >
        (sender
          ? (share.recipients.find((item) => item.userId === recipient)?.senderReadThrough ?? 0)
          : (me?.readThrough ?? 0)),
  ).length;
  const otherThreadsUnread =
    sender && share.recipients.some((item) => item.userId !== recipient && item.lastMessage > item.senderReadThrough);
  const navigate = (path: string) => {
    setDirectory(path);
    setQuery("");
  };
  const startSave = (ids: string[]) => setSaving(ids);
  const left = remaining(share);
  const layer = (node: ReactNode) => (layerHost ? createPortal(node, layerHost) : node);
  return (
    <section className={`${styles.shDetail}${chatOpen ? " chat-open" : ""}`}>
      <div className={styles.shDetailMain}>
        <header className={styles.shHead}>
          {layout === "single" && (
            <button className={`${styles.shIconBtn} ${styles.shBack}`} aria-label="返回分享列表" onClick={onBack}>
              <ArrowLeft />
            </button>
          )}
          {top[0] ? (
            <EntryTile name={top[0].path} isDir={top[0].isDir} size="lg" />
          ) : (
            <span className={`${styles.shTile} ${styles.shTileLg} ${styles.toneOther}`}>
              <LoaderCircle className={phase === "preparing" ? styles.shSpin : ""} />
            </span>
          )}
          <div className={styles.shHeadText}>
            <h2 title={top.map((entry) => leaf(entry.path)).join("、")}>{shareTitle(share)}</h2>
            <p className={styles.shByline}>
              {sender ? (
                <span>
                  分享给{" "}
                  <b>
                    {names(
                      people,
                      share.recipients.map((item) => item.userId),
                      3,
                    )}
                  </b>
                </span>
              ) : (
                <span className={styles.shBylinePerson}>
                  <Avatar people={people} id={share.sender.id} username={share.sender.username} size="xs" />
                  <b>{owner}</b> 分享给你
                </span>
              )}
              <span className={styles.shSep} aria-hidden="true">
                ·
              </span>
              <time dateTime={share.publishedAt ?? share.createdAt}>
                {moment(share.publishedAt ?? share.createdAt)}
              </time>
            </p>
            <p className={styles.shFacts}>
              {top.length > 0 && (
                <span>
                  {top.length} 项 · {formatBytes(fileBytes(share.entries))}
                </span>
              )}
              {phase === "active" ? (
                <span className={left.urgent ? styles.urgent : ""} title={`${moment(share.expiresAt)} 到期`}>
                  <Clock3 />
                  {left.text}
                </span>
              ) : (
                <span className={`${styles.shPhase} ${styles[phase!] ?? ""}`}>{phaseLabel[phase!]}</span>
              )}
            </p>
          </div>
        </header>
        {error && (
          <p role="alert" className={`${styles.shAlert} ${styles.shDetailAlert}`}>
            {error}
          </p>
        )}
        <StateBanner share={share} phase={phase!} sender={sender} owner={owner} />
        <div className={styles.shActionbar}>
          <div className={styles.shActionbarStart}>
            {sender ? (
              <Recipients share={share} people={people} current={chatOpen ? recipient : undefined} onOpen={openChat} />
            ) : (
              !chatOpen && (
                <Peek
                  thread={thread}
                  people={people}
                  userId={shares.userId}
                  unread={ownUnread}
                  onOpen={() => openChat()}
                />
              )
            )}
          </div>
          <div className={styles.shHeadActions}>
            <button
              ref={chatToggle}
              className={`${styles.shBtn} ${styles.shChatToggle}${chatOpen ? ` ${styles.on}` : ""}`}
              aria-label="私聊"
              aria-expanded={chatOpen}
              onClick={() => (chatOpen ? closeChat() : openChat())}
            >
              <MessageCircle />
              <span>私聊</span>
              {ownUnread > 0 ? (
                <b className={styles.shCount}>{ownUnread}</b>
              ) : (
                otherThreadsUnread && (
                  <span className={styles.shDot}>
                    <span className={styles.shSr}>其他接收者有新消息</span>
                  </span>
                )
              )}
            </button>
            {!sender && (
              <button
                className={`${styles.shBtn} primary`}
                disabled={!available || !top.length}
                onClick={() => startSave(selected.length ? selected : top.map((entry) => entry.id))}
              >
                <Download />
                {selected.length ? `保存所选 ${selected.length} 项` : "保存全部"}
              </button>
            )}
            {sender && phase !== "revoked" && phase !== "failed" && (
              <button
                className={`${styles.shBtn} ${styles.quietDanger}`}
                disabled={revoking}
                onClick={() => setConfirmRevoke(true)}
              >
                <Undo2 />
                撤回
              </button>
            )}
          </div>
        </div>
        <section className={styles.shFiles} aria-label="分享内容">
          <div className={styles.shFilesBar}>
            {!sender && (
              <input
                type="checkbox"
                className={styles.shCheck}
                aria-label="全选当前文件夹"
                disabled={!available || !visibleEntries.length}
                checked={allSelected}
                onChange={(event) =>
                  setSelected((old) =>
                    event.target.checked
                      ? [...new Set([...old, ...visibleEntries.map((entry) => entry.id)])]
                      : old.filter((item) => !visibleEntries.some((entry) => entry.id === item)),
                  )
                }
              />
            )}
            <nav className={styles.shCrumbs} aria-label="分享内路径">
              <button disabled={!directory} onClick={() => navigate("")}>
                全部文件
              </button>
              {directory
                .split("/")
                .filter(Boolean)
                .map((name, index, parts) => (
                  <span key={index}>
                    <ChevronRight />
                    <button
                      disabled={index === parts.length - 1}
                      onClick={() => navigate(parts.slice(0, index + 1).join("/"))}
                    >
                      {name}
                    </button>
                  </span>
                ))}
            </nav>
            {selected.length > 0 && (
              <span className={styles.shSelection}>
                已选 {selected.length} 项
                <button aria-label="清除选择" title="清除选择" onClick={() => setSelected([])}>
                  <X />
                </button>
              </span>
            )}
            <label className={`${styles.shSearch} ${styles.compact}`}>
              <Search />
              <input
                type="search"
                aria-label="搜索分享文件"
                placeholder="搜索当前文件夹"
                value={query}
                onChange={(event) => setQuery(event.target.value)}
              />
            </label>
          </div>
          <div className={styles.shTable} role="list">
            {visibleEntries.length > 0 && (
              <div className={styles.shTableHead} aria-hidden="true">
                {!sender && <span className={styles.c} />}
                <span className={styles.n}>名称</span>
                <span className={styles.k}>类型</span>
                <span className={styles.s}>大小</span>
                {!sender && available && <span className={styles.a} />}
              </div>
            )}
            {visibleEntries.map((entry) => {
              const name = leaf(entry.path),
                checked = selected.includes(entry.id);
              return (
                <div
                  role="listitem"
                  className={`${styles.shFile}${checked ? ` ${styles.selected}` : ""}${!entry.isDir && !available ? ` ${styles.locked}` : ""}`}
                  key={entry.id}
                >
                  {!sender && (
                    <input
                      type="checkbox"
                      className={styles.shCheck}
                      aria-label={`选择 ${name}`}
                      disabled={!available}
                      checked={checked}
                      onChange={(event) =>
                        setSelected((previous) =>
                          event.target.checked ? [...previous, entry.id] : previous.filter((item) => item !== entry.id),
                        )
                      }
                    />
                  )}
                  <button
                    className={styles.shFileOpen}
                    title={entry.isDir ? `打开 ${name}` : available ? `预览 ${name}` : name}
                    onClick={() => (entry.isDir ? navigate(entry.path) : setPreview(entry.id))}
                    disabled={!entry.isDir && !available}
                  >
                    <EntryTile name={name} isDir={entry.isDir} />
                    <span className={styles.shFileName}>{name}</span>
                  </button>
                  <span className={styles.shFileKind}>{kindOf(name, entry.isDir).label}</span>
                  <span className={styles.shFileSize}>{entry.isDir ? "—" : formatBytes(entry.size)}</span>
                  {!sender && available && (
                    <button
                      className={styles.shFileSave}
                      aria-label={`保存 ${name}`}
                      title="保存"
                      onClick={() => startSave([entry.id])}
                    >
                      <Download />
                    </button>
                  )}
                </div>
              );
            })}
            {!visibleEntries.length && (
              <div className={styles.shFilesEmpty}>
                {phase === "preparing" ? (
                  <>
                    <LoaderCircle className={styles.shSpin} />
                    <p>副本准备好后就能看到文件</p>
                  </>
                ) : query ? (
                  <>
                    <Search />
                    <p>没有匹配“{query.trim()}”的文件</p>
                    <button className={`${styles.shBtn} ${styles.small}`} onClick={() => setQuery("")}>
                      清除搜索
                    </button>
                  </>
                ) : (
                  <p>{phase === "failed" ? "没有可显示的文件" : "文件夹为空"}</p>
                )}
              </div>
            )}
          </div>
          {!sender && <SaveJobs jobs={jobs} places={places} />}
        </section>
      </div>
      {chatOpen && !docked && <div className={styles.shScrim} onClick={closeChat} />}
      {recipient && (
        <Conversation
          share={share}
          recipientId={recipient}
          thread={thread}
          shown={chatOpen}
          docked={docked}
          visible={chatOpen && !preview && !saving && !confirmRevoke}
          onRecipient={(value) => setRecipient(value)}
          onClose={closeChat}
        />
      )}
      {preview &&
        available &&
        files.some((entry) => entry.id === preview) &&
        layer(
          <SharePreview
            shareId={id}
            entries={files}
            index={files.findIndex((entry) => entry.id === preview)}
            onIndex={(index) => setPreview(files[index].id)}
            onClose={() => setPreview(undefined)}
            onSave={
              sender
                ? undefined
                : (entry) => {
                    setPreview(undefined);
                    startSave([entry.id]);
                  }
            }
          />,
        )}
      {saving &&
        layer(
          <SaveDialog
            share={share}
            entryIds={saving}
            onClose={() => setSaving(undefined)}
            onSaved={(job, place) => {
              setJobs((previous) => [job, ...previous.filter((item) => item.id !== job.id)]);
              setPlaces((previous) => ({ ...previous, [job.id]: place }));
              setSaving(undefined);
              setSelected([]);
              shares.refresh();
            }}
          />,
        )}
      {confirmRevoke &&
        layer(
          <div className={`${palette.shPalette} ${styles.shLayer}`}>
            <section
              className={`${styles.shDialog} ${styles.shConfirm}`}
              role="alertdialog"
              aria-modal="true"
              aria-labelledby="sh-revoke-title"
              aria-describedby="sh-revoke-text"
            >
              <span className={styles.shConfirmMark}>
                <Undo2 />
              </span>
              <h2 id="sh-revoke-title">撤回这次分享？</h2>
              <p id="sh-revoke-text">
                {names(
                  people,
                  share.recipients.map((item) => item.userId),
                  3,
                )}
                将不能继续预览、保存或回复，正在进行的保存会停止。已经保存的文件无法收回。
              </p>
              <footer className={styles.shDialogFoot}>
                <button className={styles.shBtn} disabled={revoking} onClick={() => setConfirmRevoke(false)}>
                  取消
                </button>
                <button
                  className={`${styles.shBtn} ${styles.danger}`}
                  autoFocus
                  disabled={revoking}
                  onClick={() => void revoke()}
                >
                  {revoking ? "正在撤回…" : "确认撤回"}
                </button>
              </footer>
            </section>
          </div>,
        )}
    </section>
  );
}

function StateBanner({ share, phase, sender, owner }: { share: Share; phase: Phase; sender: boolean; owner: string }) {
  const reason = reasonText(share.error);
  if (phase === "active") return null;
  if (phase === "preparing")
    return (
      <div className={`${styles.shBanner} ${styles.info}`}>
        <LoaderCircle className={styles.shSpin} />
        <p>
          <b>正在准备文件副本{share.preparedBytes > 0 && ` · 已复制 ${formatBytes(share.preparedBytes)}`}</b>
          <span>准备好后会自动发给接收者，可以先关闭窗口。</span>
        </p>
      </div>
    );
  if (phase === "failed")
    return (
      <div className={`${styles.shBanner} ${styles.danger}`}>
        <CircleAlert />
        <p>
          <b>{reason || "副本准备失败"}</b>
          <span>没有发给任何人。可以重新选择文件再分享一次。</span>
        </p>
      </div>
    );
  if (phase === "revoked")
    return (
      <div className={`${styles.shBanner} ${styles.muted}`}>
        <Undo2 />
        <p>
          <b>{reason || (sender ? "你已撤回这次分享" : `${owner} 已撤回这次分享`)}</b>
          <span>文件清单和消息仍然可以查看。</span>
        </p>
      </div>
    );
  return (
    <div className={`${styles.shBanner} ${styles.muted}`}>
      <Clock3 />
      <p>
        <b>已于 {moment(share.expiresAt)} 到期</b>
        <span>文件清单和消息仍然可以查看；到期前开始的保存会继续完成。</span>
      </p>
    </div>
  );
}

// Recipients is the sender's view of who has opened and saved the share; a
// person opens their own conversation.
function Recipients({
  share,
  people,
  current,
  onOpen,
}: {
  share: Share;
  people: ReturnType<typeof usePeople>;
  current?: string;
  onOpen: (id: string) => void;
}) {
  return (
    <div className={styles.shRecipients} role="group" aria-label="接收者">
      {share.recipients.map((item) => {
        const state = receipt(item);
        const unread = item.lastMessage > item.senderReadThrough;
        return (
          <button
            key={item.userId}
            className={`${styles.shRecipient}${current === item.userId ? ` ${styles.on}` : ""}`}
            aria-label={`与 ${nameOf(people, item.userId)} 私聊`}
            onClick={() => onOpen(item.userId)}
          >
            <span className={styles.shRecipientFace}>
              <Avatar people={people} id={item.userId} size="md" />
              {unread && (
                <span className={styles.shDot}>
                  <span className={styles.shSr}>有新消息</span>
                </span>
              )}
            </span>
            <span className={styles.shRecipientText}>
              <strong>{nameOf(people, item.userId)}</strong>
              <small className={`${styles[`tone-${state.tone}`] ?? ""}`}>
                {state.tone === "saved" && <Check />}
                {state.text}
              </small>
            </span>
          </button>
        );
      })}
    </div>
  );
}

// Peek keeps the newest line of a collapsed conversation in view, so the
// sender's note is not hidden behind the 私聊 button.
function Peek({
  thread,
  people,
  userId,
  unread,
  onOpen,
}: {
  thread: Thread;
  people: ReturnType<typeof usePeople>;
  userId: string;
  unread: number;
  onOpen: () => void;
}) {
  const last = thread.messages.at(-1);
  if (!last) return null;
  const own = last.sender.id === userId;
  return (
    <button className={`${styles.shPeek}${unread ? ` ${styles.unread}` : ""}`} onClick={onOpen} aria-label="打开私聊">
      <Avatar people={people} id={last.sender.id} username={last.sender.username} size="sm" />
      <span className={styles.shPeekText}>
        <span>
          {own ? "你：" : ""}
          {last.text}
        </span>
      </span>
      <time dateTime={last.createdAt}>{when(last.createdAt)}</time>
      {unread > 0 ? (
        <b className={styles.shCount}>{unread}</b>
      ) : (
        <span className={styles.shPeekReply}>
          回复
          <ChevronRight />
        </span>
      )}
    </button>
  );
}

function SaveJobs({ jobs, places }: { jobs: Job[]; places: Record<string, string> }) {
  const [open, setOpen] = useState(false);
  if (!jobs.length) return null;
  const job = jobs[0];
  const total = job.entryIds?.length ?? 0,
    done = jobDone(job);
  const reason = reasonText(job.error);
  const place = places[job.id];
  const icon =
    job.status === "running" ? (
      <LoaderCircle className={styles.shSpin} />
    ) : job.status === "completed" ? (
      <Check />
    ) : (
      <CircleAlert />
    );
  return (
    <div className={`${styles.shJob} ${styles[job.status] ?? ""}`} aria-label="保存任务">
      <div className={styles.shJobMain}>
        <span className={styles.shJobIcon}>{icon}</span>
        <div className={styles.shJobText}>
          <strong>
            {jobLabel[job.status] ?? job.status}
            {place && <span> 到 {place}</span>}
          </strong>
          <small>
            {job.status === "completed"
              ? `${done} 项 · ${formatBytes(job.bytesTotal)}`
              : `${formatBytes(job.bytesDone)} / ${formatBytes(job.bytesTotal)}${total ? ` · ${done}/${total} 项` : ""}`}
            {reason && <em> · {reason}</em>}
          </small>
          {job.status === "running" && (
            <progress value={job.bytesDone} max={Math.max(job.bytesTotal, 1)} aria-label="保存进度" />
          )}
        </div>
        {jobs.length > 1 && (
          <button className={styles.shJobHistory} aria-expanded={open} onClick={() => setOpen(!open)}>
            保存记录 {jobs.length}
            <ChevronDown />
          </button>
        )}
      </div>
      {open && (
        <ul className={styles.shJobList}>
          {jobs.map((item) => (
            <li key={item.id}>
              <span className={`${styles.shJobState} ${styles[item.status] ?? ""}`}>
                {jobLabel[item.status] ?? item.status}
              </span>
              <span>{places[item.id] ?? ""}</span>
              <small>
                {jobDone(item)} 项{item.error && ` · ${reasonText(item.error)}`}
              </small>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
