import { Pencil, Trash2 } from "lucide-react";
import { useState } from "react";

import { APIError } from "../api";
import { nameOf, person, usePeople } from "../people";
import { PersonAvatar } from "../PersonAvatar";
import { Draft, FeedbackEditor, emptyDraft, releaseDraft } from "./FeedbackEditor";
import {
  FeedbackComment, FeedbackItem, MAX_COMMENT_IMAGES, MAX_COMMENT_TEXT, addComment, deleteComment, describeFeedbackError, editComment,
  feedbackImageURL,
} from "./feedbackApi";
import { describeTime, fullTime } from "./time";

interface ThreadProps {
  id: string;
  item: FeedbackItem;
  // Opening an empty thread is asking to write in it.
  focusComposer: boolean;
  // The card keeps the draft, so closing the thread does not lose it.
  draft: Draft;
  onDraft: (draft: Draft) => void;
  onChange: (item: FeedbackItem) => void;
  onRemove: (id: string) => void;
  onRefresh: () => Promise<FeedbackItem[] | undefined>;
  onPreview: (urls: string[], index: number) => void;
  onCaptured: () => void;
}

// FeedbackThread is the discussion under one item: its comments, oldest
// first, and a box to add one.
export function FeedbackThread({ id, item, focusComposer, draft, onDraft, onChange, onRemove, onRefresh, onPreview, onCaptured }: ThreadProps) {
  const [sending, setSending] = useState(false);
  const [error, setError] = useState("");

  const send = async () => {
    setSending(true);
    setError("");
    try {
      const next = await addComment(item.id, draft.text, draft.added.map((image) => image.blob));
      releaseDraft(draft);
      onDraft(emptyDraft);
      onChange(next);
    } catch (caught) {
      if (caught instanceof APIError && caught.code === "not_found") onRemove(item.id);
      else setError(describeFeedbackError(caught, "comment"));
    } finally {
      setSending(false);
    }
  };

  return (
    <section id={id} className="fb-thread" aria-label="评论">
      {item.comments.length > 0 && (
        <ol className="fb-comments">
          {item.comments.map((comment) => (
            <CommentRow
              key={comment.id}
              item={item}
              comment={comment}
              onChange={onChange}
              onRemove={onRemove}
              onRefresh={onRefresh}
              onPreview={onPreview}
              onCaptured={onCaptured}
            />
          ))}
        </ol>
      )}
      <FeedbackEditor
        compact
        label="写评论"
        draft={draft}
        onChange={(next) => { onDraft(next); setError(""); }}
        placeholder="写评论，可以粘贴截图…"
        submitLabel="评论"
        busy={sending}
        error={error}
        autoFocus={focusComposer}
        maxImages={MAX_COMMENT_IMAGES}
        maxText={MAX_COMMENT_TEXT}
        onSubmit={() => void send()}
        onPreview={onPreview}
        onCaptured={onCaptured}
      />
    </section>
  );
}

interface CommentRowProps {
  item: FeedbackItem;
  comment: FeedbackComment;
  onChange: (item: FeedbackItem) => void;
  onRemove: (id: string) => void;
  onRefresh: () => Promise<FeedbackItem[] | undefined>;
  onPreview: (urls: string[], index: number) => void;
  onCaptured: () => void;
}

// Like items, any account may edit or delete any comment.
function CommentRow({ item, comment, onChange, onRemove, onRefresh, onPreview, onCaptured }: CommentRowProps) {
  const people = usePeople();
  const [mode, setMode] = useState<"view" | "edit" | "delete">("view");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [draft, setDraft] = useState<Draft>(emptyDraft);
  const [detached, setDetached] = useState<string[]>([]);
  const [baseVersion, setBaseVersion] = useState(comment.version);
  const [changedBy, setChangedBy] = useState("");
  const imageURLs = comment.images.map((image) => feedbackImageURL(item.id, image.id));

  const fail = (caught: unknown) => {
    if (caught instanceof APIError && caught.code === "not_found") { onRemove(item.id); return; }
    // Someone deleted the comment: the refreshed list no longer shows it.
    if (caught instanceof APIError && caught.code === "comment_not_found") { void onRefresh(); return; }
    setError(describeFeedbackError(caught, "comment"));
  };

  const startEdit = () => {
    setDraft({ text: comment.text, added: [] });
    setDetached([]);
    setBaseVersion(comment.version);
    setChangedBy("");
    setError("");
    setMode("edit");
  };
  const cancelEdit = () => { releaseDraft(draft); setDraft(emptyDraft); setMode("view"); setError(""); };

  const save = async () => {
    setBusy(true);
    setError("");
    try {
      const keep = comment.images.filter((image) => !detached.includes(image.id)).map((image) => image.id);
      const next = await editComment(item.id, comment.id, baseVersion, draft.text, keep, draft.added.map((image) => image.blob));
      releaseDraft(draft);
      setDraft(emptyDraft);
      onChange(next);
      setMode("view");
    } catch (caught) {
      if (caught instanceof APIError && caught.code === "comment_changed") {
        // Keep the draft; show what changed and save over it on the next try.
        const latest = (await onRefresh())?.find((candidate) => candidate.id === item.id)?.comments.find((candidate) => candidate.id === comment.id);
        if (latest) { setBaseVersion(latest.version); setChangedBy(nameOf(people, latest.updatedBy ?? latest.createdBy)); }
      } else fail(caught);
    } finally {
      setBusy(false);
    }
  };

  const confirmDelete = async () => {
    setBusy(true);
    try { onChange(await deleteComment(item.id, comment.id)); } catch (caught) { fail(caught); setBusy(false); }
  };

  if (mode === "edit") {
    const attached = comment.images.filter((image) => !detached.includes(image.id)).map((image) => ({ id: image.id, url: feedbackImageURL(item.id, image.id) }));
    return (
      <li className="fb-comment editing">
        <FeedbackEditor
          compact
          label="编辑评论"
          draft={draft}
          onChange={setDraft}
          attached={attached}
          onDetach={(id) => setDetached((ids) => [...ids, id])}
          placeholder="评论内容"
          submitLabel="保存"
          busy={busy}
          error={error}
          autoFocus
          maxImages={MAX_COMMENT_IMAGES}
          maxText={MAX_COMMENT_TEXT}
          notice={changedBy && (
            <div className="fb-conflict" role="status">
              <strong>{changedBy} 刚刚修改了这条评论</strong>
              {comment.text && <blockquote>{comment.text}</blockquote>}
              <span>你的内容还在下面，再次保存将以你的版本为准。</span>
            </div>
          )}
          onSubmit={() => void save()}
          onCancel={cancelEdit}
          onPreview={onPreview}
          onCaptured={onCaptured}
        />
      </li>
    );
  }

  return (
    <li className="fb-comment">
      <PersonAvatar who={person(people, { username: comment.createdBy })} className="fb-avatar" />
      <div className="fb-comment-body">
        <div className="fb-comment-head">
          <span className="fb-author">{nameOf(people, comment.createdBy)}</span>
          <time dateTime={comment.createdAt} title={fullTime(comment.createdAt)}>{describeTime(comment.createdAt)}</time>
          {comment.updatedBy && comment.updatedAt && <span className="fb-edited" title={`${nameOf(people, comment.updatedBy)} 于 ${fullTime(comment.updatedAt)} 编辑`}>已编辑</span>}
          {mode === "view" && (
            <span className="fb-comment-actions">
              <button aria-label="编辑评论" title="编辑" onClick={startEdit}><Pencil /></button>
              <button aria-label="删除评论" title="删除" onClick={() => { setError(""); setMode("delete"); }}><Trash2 /></button>
            </span>
          )}
        </div>
        {comment.text && <p className="fb-comment-text">{comment.text}</p>}
        {comment.images.length > 0 && (
          <ul className="fb-thumbs small" aria-label="评论附图">
            {comment.images.map((image, index) => (
              <li key={image.id}>
                <button className="fb-thumb" aria-label={`查看评论附图 ${index + 1}`} onClick={() => onPreview(imageURLs, index)}>
                  <img src={imageURLs[index]} alt="" loading="lazy" />
                </button>
              </li>
            ))}
          </ul>
        )}
        {error && <p className="fb-item-error" role="alert">{error}</p>}
        {mode === "delete" && (
          <div className="fb-confirm" role="group" aria-label="确认删除评论">
            <span>删除这条评论？</span>
            <button className="fb-ghost" disabled={busy} onClick={() => setMode("view")}>取消</button>
            <button className="fb-danger" disabled={busy} onClick={() => void confirmDelete()}>删除</button>
          </div>
        )}
      </div>
    </li>
  );
}
