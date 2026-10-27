import {
  Check,
  CheckCheck,
  CircleCheckBig,
  Inbox,
  MessageCircle,
  MessageSquareText,
  Pencil,
  RotateCw,
  Trash2,
  X,
} from "lucide-react";
import {
  PointerEvent as ReactPointerEvent,
  RefObject,
  useCallback,
  useEffect,
  useEffectEvent,
  useId,
  useRef,
  useState,
} from "react";

import { APIError } from "../api/client";
import { PersonAvatar } from "../people/PersonAvatar";
import { nameOf, person, usePeople } from "../people/people";
import {
  createFeedback,
  deleteFeedback,
  describeFeedbackError,
  editFeedback,
  FeedbackItem,
  feedbackImageURL,
  listFeedback,
  resolveFeedback,
} from "./api";
import { FEEDBACK_UI } from "./capture";
import { Draft, emptyDraft, FeedbackEditor, releaseDraft } from "./FeedbackEditor";
import { FeedbackThread } from "./FeedbackThread";
import styles from "./feedback.module.css";
import { Lightbox } from "./Lightbox";
import { describeTime, fullTime } from "./time";

type Filter = "open" | "resolved" | "all";
type Preview = { urls: string[]; index: number };

const ui = { [FEEDBACK_UI]: "" };
// The list refreshes often while someone looks at it, and slowly for the badge.
const OPEN_REFRESH = 15_000;
const CLOSED_REFRESH = 60_000;

// FeedbackChannel is 问题反馈: a button on the right edge of the desktop that
// opens one shared checklist every account can add to, edit, tick off and
// discuss in comments.
export function FeedbackChannel() {
  const [open, setOpen] = useState(false);
  const [items, setItems] = useState<FeedbackItem[]>();
  const [loadError, setLoadError] = useState("");
  const [filter, setFilter] = useState<Filter>("open");
  // Items ticked off or reopened stay in the current view until it changes,
  // so they do not vanish under the pointer.
  const [lingering, setLingering] = useState<string[]>([]);
  const [draft, setDraft] = useState<Draft>(emptyDraft);
  const [sending, setSending] = useState(false);
  const [sendError, setSendError] = useState("");
  const [preview, setPreview] = useState<Preview>();
  const [flash, setFlash] = useState(0);
  const panelID = useId();
  const button = useRef<HTMLButtonElement>(null);
  const wasOpen = useRef(false);

  const refresh = useCallback(
    () =>
      listFeedback().then(
        (list) => {
          setItems(list);
          setLoadError("");
          return list;
        },
        (error: unknown) => {
          setLoadError(describeFeedbackError(error));
          return undefined;
        },
      ),
    [],
  );
  useEffect(() => {
    void refresh();
    const timer = window.setInterval(() => void refresh(), open ? OPEN_REFRESH : CLOSED_REFRESH);
    return () => window.clearInterval(timer);
  }, [open, refresh]);
  useEffect(() => {
    if (wasOpen.current && !open) button.current?.focus({ preventScroll: true });
    wasOpen.current = open;
  }, [open]);
  // A draft outlives closing the panel, but not leaving the desktop.
  const releaseOnLeave = useEffectEvent(() => releaseDraft(draft));
  useEffect(() => () => releaseOnLeave(), []);

  const close = () => {
    setOpen(false);
    setLingering([]);
  };
  const replace = (next: FeedbackItem) => setItems((list) => list?.map((item) => (item.id === next.id ? next : item)));
  const remove = (id: string) => setItems((list) => list?.filter((item) => item.id !== id));
  const choose = (next: Filter) => {
    setFilter(next);
    setLingering([]);
  };

  const send = async () => {
    setSending(true);
    setSendError("");
    try {
      const item = await createFeedback(
        draft.text,
        draft.added.map((image) => image.blob),
      );
      releaseDraft(draft);
      setDraft(emptyDraft);
      setItems((list) => [item, ...(list ?? []).filter((existing) => existing.id !== item.id)]);
      if (filter === "resolved") setFilter("open");
    } catch (error) {
      setSendError(describeFeedbackError(error));
    } finally {
      setSending(false);
    }
  };

  const all = items ?? [];
  const openCount = all.filter((item) => !item.resolved).length;
  const resolvedCount = all.length - openCount;
  const shown = all
    .filter(
      (item) => filter === "all" || lingering.includes(item.id) || (filter === "open" ? !item.resolved : item.resolved),
    )
    .sort((a, b) => order(a, b, filter));

  return (
    <>
      {!open && <FeedbackButton ref={button} openCount={openCount} panelID={panelID} onOpen={() => setOpen(true)} />}
      {open && (
        <section
          id={panelID}
          className={styles.fbPanel}
          {...ui}
          role="dialog"
          aria-label="问题反馈"
          onKeyDown={(event) => {
            if (event.key === "Escape") close();
          }}
        >
          <header className={styles.fbHead}>
            <span className={styles.fbHeadIcon} aria-hidden="true">
              <MessageSquareText />
            </span>
            <div className={styles.fbHeadText}>
              <h2>问题反馈</h2>
              <p>全员可见 · 任何人都可以编辑、标记已处理</p>
            </div>
            <button className={styles.fbIconButton} aria-label="关闭问题反馈" onClick={close}>
              <X />
            </button>
          </header>

          <FeedbackEditor
            label="新反馈"
            draft={draft}
            onChange={(next) => {
              setDraft(next);
              setSendError("");
            }}
            placeholder={"遇到了什么问题，或有什么建议？\n可以直接粘贴截图，或把图片拖进来"}
            submitLabel="提交"
            busy={sending}
            error={sendError}
            autoFocus
            onSubmit={() => void send()}
            onPreview={(urls, index) => setPreview({ urls, index })}
            onCaptured={() => setFlash((value) => value + 1)}
          />

          <div className={styles.fbFilter} role="tablist" aria-label="筛选反馈">
            {(
              [
                ["open", "待处理", openCount],
                ["resolved", "已处理", resolvedCount],
                ["all", "全部", all.length],
              ] as const
            ).map(([value, label, count]) => (
              <button key={value} role="tab" aria-selected={filter === value} onClick={() => choose(value)}>
                {label}
                <span>{count}</span>
              </button>
            ))}
          </div>

          <div className={styles.fbScroll}>
            {items === undefined && !loadError && (
              <div className={styles.fbState}>
                <span className={styles.fbLoader} />
                <p>正在读取反馈…</p>
              </div>
            )}
            {loadError && items === undefined && (
              <div className={styles.fbState}>
                <Inbox />
                <h3>无法读取反馈</h3>
                <p>{loadError}</p>
                <button className={styles.fbGhost} onClick={() => void refresh()}>
                  <RotateCw />
                  重试
                </button>
              </div>
            )}
            {items !== undefined && shown.length === 0 && <EmptyState filter={filter} />}
            {shown.length > 0 && (
              <ul className={styles.fbList} aria-label="反馈清单">
                {shown.map((item) => (
                  <FeedbackCard
                    key={item.id}
                    item={item}
                    onChange={replace}
                    onRemove={remove}
                    onToggled={(id) => setLingering((ids) => (ids.includes(id) ? ids : [...ids, id]))}
                    onRefresh={refresh}
                    onPreview={(urls, index) => setPreview({ urls, index })}
                    onCaptured={() => setFlash((value) => value + 1)}
                  />
                ))}
              </ul>
            )}
          </div>
        </section>
      )}
      {flash > 0 && <div key={flash} className={styles.fbFlash} {...ui} aria-hidden="true" />}
      {preview && (
        <Lightbox
          urls={preview.urls}
          index={preview.index}
          onIndex={(index) => setPreview({ ...preview, index })}
          onClose={() => setPreview(undefined)}
        />
      )}
    </>
  );
}

// Open items newest first; handled ones by when they were handled.
function order(a: FeedbackItem, b: FeedbackItem, filter: Filter) {
  if (filter === "all" && a.resolved !== b.resolved) return a.resolved ? 1 : -1;
  const at = (item: FeedbackItem) =>
    filter !== "open" && item.resolved && item.resolvedAt ? item.resolvedAt : item.createdAt;
  return at(b).localeCompare(at(a));
}

function EmptyState({ filter }: { filter: Filter }) {
  if (filter === "resolved")
    return (
      <div className={styles.fbState}>
        <CircleCheckBig />
        <h3>还没有已处理的反馈</h3>
        <p>处理完一条反馈后，勾选它就会划线归到这里。</p>
      </div>
    );
  return (
    <div className={styles.fbState}>
      <CircleCheckBig />
      <h3>{filter === "open" ? "没有待处理的反馈" : "还没有反馈"}</h3>
      <p>发现问题时，在上面写下来或贴一张截图。</p>
    </div>
  );
}

const POSITION_KEY = "a-nas.feedback.button-top";
const DEFAULT_POSITION = 0.58;

function readPosition() {
  try {
    const value = Number(localStorage.getItem(POSITION_KEY));
    return value > 0 && value < 1 ? value : DEFAULT_POSITION;
  } catch {
    return DEFAULT_POSITION;
  }
}

// FeedbackButton sits on the right edge; dragging it up or down moves it out
// of the way of a window, and the place is remembered.
function FeedbackButton({
  ref,
  openCount,
  panelID,
  onOpen,
}: {
  ref: RefObject<HTMLButtonElement | null>;
  openCount: number;
  panelID: string;
  onOpen: () => void;
}) {
  const [top, setTop] = useState(readPosition);
  const [dragging, setDragging] = useState(false);
  const dragged = useRef(false);

  const pointerDown = (event: ReactPointerEvent<HTMLButtonElement>) => {
    if (event.button !== 0) return;
    const shell = event.currentTarget.offsetParent?.getBoundingClientRect();
    if (!shell || shell.height === 0) return;
    const startY = event.clientY;
    dragged.current = false;
    let latest = top;
    const move = (moveEvent: PointerEvent) => {
      if (!dragged.current && Math.abs(moveEvent.clientY - startY) < 5) return;
      dragged.current = true;
      setDragging(true);
      latest = Math.min(1, Math.max(0, (moveEvent.clientY - shell.top) / shell.height));
      setTop(latest);
    };
    const stop = () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", stop);
      setDragging(false);
      if (dragged.current) {
        try {
          localStorage.setItem(POSITION_KEY, String(latest));
        } catch {
          /* the position is a convenience */
        }
      }
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", stop);
  };

  return (
    <button
      ref={ref}
      className={`${styles.fbFab} ${dragging ? styles.dragging : ""}`}
      {...ui}
      // Below the status bar and clear of the bottom edge on any screen.
      style={{ top: `clamp(160px, ${top * 100}%, calc(100% - 64px))` }}
      aria-label={openCount ? `问题反馈，${openCount} 条待处理` : "问题反馈"}
      aria-haspopup="dialog"
      aria-expanded={false}
      aria-controls={panelID}
      title="问题反馈（可上下拖动）"
      onPointerDown={pointerDown}
      onClick={() => {
        if (!dragged.current) onOpen();
        dragged.current = false;
      }}
    >
      <span className={styles.fbFabIcon}>
        <MessageSquareText />
      </span>
      <span className={styles.fbFabLabel}>反馈</span>
      {openCount > 0 && (
        <span className={styles.fbFabCount} aria-hidden="true">
          {openCount > 99 ? "99+" : openCount}
        </span>
      )}
    </button>
  );
}

interface FeedbackCardProps {
  item: FeedbackItem;
  onChange: (item: FeedbackItem) => void;
  onRemove: (id: string) => void;
  onToggled: (id: string) => void;
  onRefresh: () => Promise<FeedbackItem[] | undefined>;
  onPreview: (urls: string[], index: number) => void;
  onCaptured: () => void;
}

function FeedbackCard({ item, onChange, onRemove, onToggled, onRefresh, onPreview, onCaptured }: FeedbackCardProps) {
  const people = usePeople();
  const [mode, setMode] = useState<"view" | "edit" | "delete">("view");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [optimistic, setOptimistic] = useState<boolean>();
  const [expanded, setExpanded] = useState(false);
  // Edit state: the draft, the saved images to drop and the version it started from.
  const [draft, setDraft] = useState<Draft>(emptyDraft);
  const [detached, setDetached] = useState<string[]>([]);
  const [baseVersion, setBaseVersion] = useState(item.version);
  const [changedBy, setChangedBy] = useState("");
  // Comments stay folded until asked for; the draft survives folding them.
  const [threadOpen, setThreadOpen] = useState(false);
  const [focusComposer, setFocusComposer] = useState(false);
  const [commentDraft, setCommentDraft] = useState<Draft>(emptyDraft);
  const releaseOnLeave = useEffectEvent(() => releaseDraft(commentDraft));
  useEffect(() => () => releaseOnLeave(), []);
  const threadID = useId();
  const commentCount = item.comments.length;

  const resolved = optimistic ?? item.resolved;
  const imageURLs = item.images.map((image) => feedbackImageURL(item.id, image.id));
  const long = item.text.length > 220 || item.text.split("\n").length > 6;

  const fail = (caught: unknown) => {
    if (caught instanceof APIError && caught.code === "not_found") {
      onRemove(item.id);
      return;
    }
    setError(describeFeedbackError(caught));
  };

  const toggle = async () => {
    const next = !resolved;
    setOptimistic(next);
    setError("");
    onToggled(item.id);
    try {
      onChange(await resolveFeedback(item.id, next));
    } catch (caught) {
      fail(caught);
    } finally {
      setOptimistic(undefined);
    }
  };

  const startEdit = () => {
    setDraft({ text: item.text, added: [] });
    setDetached([]);
    setBaseVersion(item.version);
    setChangedBy("");
    setError("");
    setMode("edit");
  };
  const cancelEdit = () => {
    releaseDraft(draft);
    setDraft(emptyDraft);
    setMode("view");
    setError("");
  };

  const save = async () => {
    setBusy(true);
    setError("");
    try {
      const keep = item.images.filter((image) => !detached.includes(image.id)).map((image) => image.id);
      const next = await editFeedback(
        item.id,
        baseVersion,
        draft.text,
        keep,
        draft.added.map((image) => image.blob),
      );
      releaseDraft(draft);
      setDraft(emptyDraft);
      onChange(next);
      setMode("view");
    } catch (caught) {
      if (caught instanceof APIError && caught.code === "feedback_changed") {
        // Keep the draft; show what changed and save over it on the next try.
        const latest = (await onRefresh())?.find((candidate) => candidate.id === item.id);
        if (latest) {
          setBaseVersion(latest.version);
          setChangedBy(nameOf(people, latest.updatedBy ?? latest.createdBy));
        } else onRemove(item.id);
      } else fail(caught);
    } finally {
      setBusy(false);
    }
  };

  const confirmDelete = async () => {
    setBusy(true);
    try {
      await deleteFeedback(item.id);
      onRemove(item.id);
    } catch (caught) {
      fail(caught);
      setBusy(false);
    }
  };

  if (mode === "edit") {
    const attached = item.images
      .filter((image) => !detached.includes(image.id))
      .map((image) => ({ id: image.id, url: feedbackImageURL(item.id, image.id) }));
    return (
      <li className={`${styles.fbItem} ${styles.editing}`}>
        <FeedbackEditor
          label="编辑反馈"
          draft={draft}
          onChange={setDraft}
          attached={attached}
          onDetach={(id) => setDetached((ids) => [...ids, id])}
          placeholder="反馈内容"
          submitLabel="保存"
          busy={busy}
          error={error}
          autoFocus
          notice={
            changedBy && (
              <div className={styles.fbConflict} role="status">
                <strong>{changedBy} 刚刚修改了这条反馈</strong>
                {item.text && <blockquote>{item.text}</blockquote>}
                <span>你的内容还在下面，再次保存将以你的版本为准。</span>
              </div>
            )
          }
          onSubmit={() => void save()}
          onCancel={cancelEdit}
          onPreview={onPreview}
          onCaptured={onCaptured}
        />
      </li>
    );
  }

  return (
    <li className={`${styles.fbItem} ${resolved ? styles.resolved : ""}`}>
      <button
        className={styles.fbCheck}
        role="checkbox"
        aria-checked={resolved}
        aria-label={resolved ? "标记为待处理" : "标记为已处理"}
        title={resolved ? "标记为待处理" : "标记为已处理"}
        onClick={() => void toggle()}
      >
        <Check />
      </button>
      <div className={styles.fbItemBody}>
        {item.text && (
          <p className={`${styles.fbItemText} ${long && !expanded ? styles.clamped : ""}`}>
            <span className={styles.fbStrike}>{item.text}</span>
            {resolved && <span className="sr-only">（已处理）</span>}
          </p>
        )}
        {long && (
          <button className={styles.fbMore} onClick={() => setExpanded((value) => !value)}>
            {expanded ? "收起" : "展开全文"}
          </button>
        )}
        {item.images.length > 0 && (
          <ul className={`${styles.fbThumbs} ${item.images.length === 1 ? styles.single : ""}`} aria-label="附图">
            {item.images.map((image, index) => (
              <li key={image.id}>
                <button
                  className={styles.fbThumb}
                  aria-label={`查看附图 ${index + 1}`}
                  onClick={() => onPreview(imageURLs, index)}
                >
                  <img src={imageURLs[index]} alt="" loading="lazy" />
                </button>
              </li>
            ))}
          </ul>
        )}
        <div className={styles.fbMeta}>
          <PersonAvatar who={person(people, { username: item.createdBy })} className={styles.fbAvatar} />
          <span className={styles.fbAuthor}>{nameOf(people, item.createdBy)}</span>
          <time dateTime={item.createdAt} title={fullTime(item.createdAt)}>
            {describeTime(item.createdAt)}
          </time>
          {item.updatedBy && item.updatedAt && (
            <span
              className={styles.fbEdited}
              title={`${nameOf(people, item.updatedBy)} 于 ${fullTime(item.updatedAt)} 编辑`}
            >
              已编辑
            </span>
          )}
          <span className={styles.fbMetaEnd}>
            {resolved && item.resolvedBy && item.resolvedAt && (
              <span className={styles.fbResolved} title={fullTime(item.resolvedAt)}>
                <CheckCheck />
                {nameOf(people, item.resolvedBy)} 已处理
              </span>
            )}
            <button
              className={`${styles.fbCommentToggle} ${commentCount ? styles.has : ""}`}
              aria-expanded={threadOpen}
              aria-controls={threadOpen ? threadID : undefined}
              aria-label={commentCount ? `${commentCount} 条评论` : "写评论"}
              title={threadOpen ? "收起评论" : commentCount ? "查看评论" : "写评论"}
              onClick={() => {
                setFocusComposer(!threadOpen && commentCount === 0);
                setThreadOpen((open) => !open);
              }}
            >
              <MessageCircle />
              {commentCount > 0 && commentCount}
              {commentDraft.text.trim() || commentDraft.added.length ? (
                <span className={styles.fbDraftDot} aria-hidden="true" />
              ) : null}
            </button>
            {mode === "view" && (
              <span className={styles.fbItemActions}>
                <button aria-label="编辑反馈" title="编辑" onClick={startEdit}>
                  <Pencil />
                </button>
                <button
                  aria-label="删除反馈"
                  title="删除"
                  onClick={() => {
                    setError("");
                    setMode("delete");
                  }}
                >
                  <Trash2 />
                </button>
              </span>
            )}
          </span>
        </div>
        {error && (
          <p className={styles.fbItemError} role="alert">
            {error}
          </p>
        )}
        {mode === "delete" && (
          <div className={styles.fbConfirm} role="group" aria-label="确认删除">
            <span>删除后所有人都看不到这条反馈。</span>
            <button className={styles.fbGhost} disabled={busy} onClick={() => setMode("view")}>
              取消
            </button>
            <button className={styles.fbDanger} disabled={busy} onClick={() => void confirmDelete()}>
              删除
            </button>
          </div>
        )}
        {threadOpen && (
          <FeedbackThread
            id={threadID}
            item={item}
            focusComposer={focusComposer}
            draft={commentDraft}
            onDraft={setCommentDraft}
            onChange={onChange}
            onRemove={onRemove}
            onRefresh={onRefresh}
            onPreview={onPreview}
            onCaptured={onCaptured}
          />
        )}
      </div>
    </li>
  );
}
