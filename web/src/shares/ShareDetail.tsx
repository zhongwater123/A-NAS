import { useEffect, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
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
import { formatBytes } from "../bytes";
import { usePeople } from "../people";
import { getShare, listJobs, revokeShare, viewShare, type Job, type Share, type ShareEntry } from "./api";
import { Conversation, useThread, type Thread } from "./Conversation";
import {
  errorText,
  fileBytes,
  jobDone,
  jobLabel,
  leaf,
  moment,
  parentPath,
  phaseLabel,
  phaseOf,
  reasonText,
  receipt,
  remaining,
  shareTitle,
  topLevel,
  when,
  type Phase,
} from "./format";
import { Avatar, EntryTile, kindOf, nameOf, names } from "./parts";
import { SaveDialog } from "./SaveDialog";
import { SharePreview } from "./SharePreview";
import { useShares } from "./SharesProvider";
import type { Layout } from "./SharesPanel";

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
  const activeRef = useRef(shares.active);
  activeRef.current = shares.active;
  const viewed = useRef(false);
  const directed = useRef(false);
  const chatToggle = useRef<HTMLButtonElement>(null);
  useEffect(() => {
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
          onDirection(value.sender.id === shares.userId ? "sent" : "received");
        }
        let warning = "";
        if (value.sender.id !== shares.userId) {
          const shouldView = activeRef.current && document.visibilityState !== "hidden" && !viewed.current;
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
            shares.refresh();
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
  }, [id, reload, shares.userId]);
  useEffect(() => {
    if (preferredRecipient) {
      setRecipient(preferredRecipient);
      setChatOpen(true);
    }
  }, [preferredRecipient, shares.focus]);
  const thread = useThread(id, recipient);
  const phase = share ? phaseOf(share) : undefined;
  const available = phase === "active";
  useEffect(() => {
    if (!available) {
      setPreview(undefined);
      setSaving(undefined);
    }
  }, [available]);
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
      <section className="sh-detail">
        <div className="sh-detail-loading">
          {error ? (
            <p role="alert" className="sh-alert">
              {error}
            </p>
          ) : (
            <>
              <LoaderCircle className="sh-spin" />
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
    <section className={`sh-detail${chatOpen ? " chat-open" : ""}`}>
      <div className="sh-detail-main">
        <header className="sh-head">
          {layout === "single" && (
            <button className="sh-icon-btn sh-back" aria-label="返回分享列表" onClick={onBack}>
              <ArrowLeft />
            </button>
          )}
          {top[0] ? (
            <EntryTile name={top[0].path} isDir={top[0].isDir} size="lg" />
          ) : (
            <span className="sh-tile sh-tile-lg tone-other">
              <LoaderCircle className={phase === "preparing" ? "sh-spin" : ""} />
            </span>
          )}
          <div className="sh-head-text">
            <h2 title={top.map((entry) => leaf(entry.path)).join("、")}>{shareTitle(share)}</h2>
            <p className="sh-byline">
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
                <span className="sh-byline-person">
                  <Avatar people={people} id={share.sender.id} username={share.sender.username} size="xs" />
                  <b>{owner}</b> 分享给你
                </span>
              )}
              <span className="sh-sep" aria-hidden="true">
                ·
              </span>
              <time dateTime={share.publishedAt ?? share.createdAt}>
                {moment(share.publishedAt ?? share.createdAt)}
              </time>
            </p>
            <p className="sh-facts">
              {top.length > 0 && (
                <span>
                  {top.length} 项 · {formatBytes(fileBytes(share.entries))}
                </span>
              )}
              {phase === "active" ? (
                <span className={left.urgent ? "urgent" : ""} title={`${moment(share.expiresAt)} 到期`}>
                  <Clock3 />
                  {left.text}
                </span>
              ) : (
                <span className={`sh-phase ${phase}`}>{phaseLabel[phase!]}</span>
              )}
            </p>
          </div>
        </header>
        {error && (
          <p role="alert" className="sh-alert sh-detail-alert">
            {error}
          </p>
        )}
        <StateBanner share={share} phase={phase!} sender={sender} owner={owner} />
        <div className="sh-actionbar">
          <div className="sh-actionbar-start">
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
          <div className="sh-head-actions">
            <button
              ref={chatToggle}
              className={`sh-btn sh-chat-toggle${chatOpen ? " on" : ""}`}
              aria-label="私聊"
              aria-expanded={chatOpen}
              onClick={() => (chatOpen ? closeChat() : openChat())}
            >
              <MessageCircle />
              <span>私聊</span>
              {ownUnread > 0 ? (
                <b className="sh-count">{ownUnread}</b>
              ) : (
                otherThreadsUnread && (
                  <span className="sh-dot">
                    <span className="sh-sr">其他接收者有新消息</span>
                  </span>
                )
              )}
            </button>
            {!sender && (
              <button
                className="sh-btn primary"
                disabled={!available || !top.length}
                onClick={() => startSave(selected.length ? selected : top.map((entry) => entry.id))}
              >
                <Download />
                {selected.length ? `保存所选 ${selected.length} 项` : "保存全部"}
              </button>
            )}
            {sender && phase !== "revoked" && phase !== "failed" && (
              <button className="sh-btn quiet-danger" disabled={revoking} onClick={() => setConfirmRevoke(true)}>
                <Undo2 />
                撤回
              </button>
            )}
          </div>
        </div>
        <section className="sh-files" aria-label="分享内容">
          <div className="sh-files-bar">
            {!sender && (
              <input
                type="checkbox"
                className="sh-check"
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
            <nav className="sh-crumbs" aria-label="分享内路径">
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
              <span className="sh-selection">
                已选 {selected.length} 项
                <button aria-label="清除选择" title="清除选择" onClick={() => setSelected([])}>
                  <X />
                </button>
              </span>
            )}
            <label className="sh-search compact">
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
          <div className="sh-table" role="list">
            {visibleEntries.length > 0 && (
              <div className="sh-table-head" aria-hidden="true">
                {!sender && <span className="c" />}
                <span className="n">名称</span>
                <span className="k">类型</span>
                <span className="s">大小</span>
                {!sender && available && <span className="a" />}
              </div>
            )}
            {visibleEntries.map((entry) => {
              const name = leaf(entry.path),
                checked = selected.includes(entry.id);
              return (
                <div
                  role="listitem"
                  className={`sh-file${checked ? " selected" : ""}${!entry.isDir && !available ? " locked" : ""}`}
                  key={entry.id}
                >
                  {!sender && (
                    <input
                      type="checkbox"
                      className="sh-check"
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
                    className="sh-file-open"
                    title={entry.isDir ? `打开 ${name}` : available ? `预览 ${name}` : name}
                    onClick={() => (entry.isDir ? navigate(entry.path) : setPreview(entry.id))}
                    disabled={!entry.isDir && !available}
                  >
                    <EntryTile name={name} isDir={entry.isDir} />
                    <span className="sh-file-name">{name}</span>
                  </button>
                  <span className="sh-file-kind">{kindOf(name, entry.isDir).label}</span>
                  <span className="sh-file-size">{entry.isDir ? "—" : formatBytes(entry.size)}</span>
                  {!sender && available && (
                    <button
                      className="sh-file-save"
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
              <div className="sh-files-empty">
                {phase === "preparing" ? (
                  <>
                    <LoaderCircle className="sh-spin" />
                    <p>副本准备好后就能看到文件</p>
                  </>
                ) : query ? (
                  <>
                    <Search />
                    <p>没有匹配“{query.trim()}”的文件</p>
                    <button className="sh-btn small" onClick={() => setQuery("")}>
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
      {chatOpen && !docked && <div className="sh-scrim" onClick={closeChat} />}
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
          <div className="sh-layer">
            <section
              className="sh-dialog sh-confirm"
              role="alertdialog"
              aria-modal="true"
              aria-labelledby="sh-revoke-title"
              aria-describedby="sh-revoke-text"
            >
              <span className="sh-confirm-mark">
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
              <footer className="sh-dialog-foot">
                <button className="sh-btn" disabled={revoking} onClick={() => setConfirmRevoke(false)}>
                  取消
                </button>
                <button className="sh-btn danger" autoFocus disabled={revoking} onClick={() => void revoke()}>
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
      <div className="sh-banner info">
        <LoaderCircle className="sh-spin" />
        <p>
          <b>正在准备文件副本{share.preparedBytes > 0 && ` · 已复制 ${formatBytes(share.preparedBytes)}`}</b>
          <span>准备好后会自动发给接收者，可以先关闭窗口。</span>
        </p>
      </div>
    );
  if (phase === "failed")
    return (
      <div className="sh-banner danger">
        <CircleAlert />
        <p>
          <b>{reason || "副本准备失败"}</b>
          <span>没有发给任何人。可以重新选择文件再分享一次。</span>
        </p>
      </div>
    );
  if (phase === "revoked")
    return (
      <div className="sh-banner muted">
        <Undo2 />
        <p>
          <b>{reason || (sender ? "你已撤回这次分享" : `${owner} 已撤回这次分享`)}</b>
          <span>文件清单和消息仍然可以查看。</span>
        </p>
      </div>
    );
  return (
    <div className="sh-banner muted">
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
    <div className="sh-recipients" role="group" aria-label="接收者">
      {share.recipients.map((item) => {
        const state = receipt(item);
        const unread = item.lastMessage > item.senderReadThrough;
        return (
          <button
            key={item.userId}
            className={`sh-recipient${current === item.userId ? " on" : ""}`}
            aria-label={`与 ${nameOf(people, item.userId)} 私聊`}
            onClick={() => onOpen(item.userId)}
          >
            <span className="sh-recipient-face">
              <Avatar people={people} id={item.userId} size="md" />
              {unread && (
                <span className="sh-dot">
                  <span className="sh-sr">有新消息</span>
                </span>
              )}
            </span>
            <span className="sh-recipient-text">
              <strong>{nameOf(people, item.userId)}</strong>
              <small className={`tone-${state.tone}`}>
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
    <button className={`sh-peek${unread ? " unread" : ""}`} onClick={onOpen} aria-label="打开私聊">
      <Avatar people={people} id={last.sender.id} username={last.sender.username} size="sm" />
      <span className="sh-peek-text">
        <span>
          {own ? "你：" : ""}
          {last.text}
        </span>
      </span>
      <time dateTime={last.createdAt}>{when(last.createdAt)}</time>
      {unread > 0 ? (
        <b className="sh-count">{unread}</b>
      ) : (
        <span className="sh-peek-reply">
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
      <LoaderCircle className="sh-spin" />
    ) : job.status === "completed" ? (
      <Check />
    ) : (
      <CircleAlert />
    );
  return (
    <div className={`sh-job ${job.status}`} aria-label="保存任务">
      <div className="sh-job-main">
        <span className="sh-job-icon">{icon}</span>
        <div className="sh-job-text">
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
          <button className="sh-job-history" aria-expanded={open} onClick={() => setOpen(!open)}>
            保存记录 {jobs.length}
            <ChevronDown />
          </button>
        )}
      </div>
      {open && (
        <ul className="sh-job-list">
          {jobs.map((item) => (
            <li key={item.id}>
              <span className={`sh-job-state ${item.status}`}>{jobLabel[item.status] ?? item.status}</span>
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
