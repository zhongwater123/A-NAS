import { Camera, Flag, Mic, NotebookPen, RotateCcw, Ruler, Send, Syringe, Trash2 } from "lucide-react";
import { type ReactNode, useCallback, useEffect, useState } from "react";

import { relativeTime } from "../lib/time";
import {
  addComment,
  audioURL,
  deleteComment,
  deleteRecord,
  type FamilyBaby,
  type FamilyKind,
  type FamilyRecord,
  familyError,
  listRecords,
  listTrash,
  requestKey,
  restoreRecord,
} from "./api";
import type { Draft } from "./Composer";
import { canDelete, useFamily } from "./context";
import styles from "./family.module.css";
import { ageText, decimal, kindLabels, shortDate } from "./format";
import { ItemMenu, MemberFace, Modal, PhotoGrid, useMemberName } from "./parts";

const kindIcons: Record<FamilyKind, ReactNode> = {
  photos: <Camera size={13} />,
  recording: <Mic size={13} />,
  journal: <NotebookPen size={13} />,
  measurement: <Ruler size={13} />,
  vaccine: <Syringe size={13} />,
  milestone: <Flag size={13} />,
};

// Feed is 动态: everything added to the channel, newest first.
export function Feed({
  baby,
  version,
  onChanged,
  onEdit,
}: {
  baby: FamilyBaby;
  version: number;
  onChanged: () => void;
  onEdit: (draft: Draft) => void;
}) {
  const family = useFamily();
  const [records, setRecords] = useState<FamilyRecord[]>();
  const [next, setNext] = useState("");
  const [loading, setLoading] = useState(false);
  const [failure, setFailure] = useState("");
  const [trashOpen, setTrashOpen] = useState(false);

  useEffect(() => {
    let live = true;
    listRecords(baby.id).then(
      (page) => {
        if (!live) return;
        setRecords(page.records);
        setNext(page.next ?? "");
        setFailure("");
      },
      (caught: unknown) => {
        if (live) setFailure(familyError(caught));
      },
    );
    return () => {
      live = false;
    };
  }, [baby.id, version]);

  const more = async () => {
    if (!next || loading) return;
    setLoading(true);
    try {
      const page = await listRecords(baby.id, next);
      setRecords((current) => [...(current ?? []), ...page.records]);
      setNext(page.next ?? "");
    } catch (caught) {
      family.toast({ text: familyError(caught), tone: "error" });
    }
    setLoading(false);
  };

  const replace = useCallback((record: FamilyRecord) => {
    setRecords((current) => current?.map((item) => (item.id === record.id ? record : item)));
  }, []);

  const remove = async (record: FamilyRecord) => {
    try {
      await deleteRecord(record.id);
      setRecords((current) => current?.filter((item) => item.id !== record.id));
      family.toast({
        text: `已删除这条${kindLabels[record.kind]}，15 天内可以在“最近删除”中恢复`,
        action: {
          label: "撤销",
          run: () =>
            void restoreRecord(record.id).then(onChanged, (caught: unknown) =>
              family.toast({ text: familyError(caught), tone: "error" }),
            ),
        },
      });
      onChanged();
    } catch (caught) {
      family.toast({ text: familyError(caught), tone: "error" });
    }
  };

  return (
    <div className={styles.fcFeed}>
      <div className={styles.fcBoardBar}>
        <span>{records ? (records.length ? "最近添加的放在最前面" : "") : ""}</span>
        <button type="button" className={styles.fcLink} onClick={() => setTrashOpen(true)}>
          <Trash2 size={13} /> 最近删除
        </button>
      </div>
      {failure && <p className={styles.fcFailure}>{failure}</p>}
      {records === undefined && !failure && <span className="loader" aria-label="正在读取动态" />}
      {records?.length === 0 && (
        <div className={styles.fcEmptyBoard}>
          <p>还没有任何记录</p>
          <span>点右上角的“记一笔”，上传照片视频、录音，写日志，记身高体重。</span>
        </div>
      )}
      {records?.map((record) => (
        <RecordCard
          key={record.id}
          record={record}
          onChanged={replace}
          onEdit={() => onEdit({ kind: record.kind, record })}
          onDelete={() => void remove(record)}
        />
      ))}
      {next && (
        <button type="button" className={styles.fcMoreButton} onClick={() => void more()} disabled={loading}>
          {loading ? "正在读取…" : "更早的记录"}
        </button>
      )}
      {trashOpen && <TrashDialog baby={baby} onClose={() => setTrashOpen(false)} onRestored={onChanged} />}
    </div>
  );
}

// RecordCard is one record in 动态, with its comments.
export function RecordCard({
  record,
  onChanged,
  onEdit,
  onDelete,
}: {
  record: FamilyRecord;
  onChanged: (record: FamilyRecord) => void;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const family = useFamily();
  const nameOf = useMemberName();
  const author = nameOf(record.authorId, record.authorName);
  const mine = record.authorId === family.userId;
  const items = [
    ...(mine ? [{ label: "编辑", run: onEdit }] : []),
    ...(canDelete(family, record.authorId) ? [{ label: "删除", danger: true, run: onDelete }] : []),
  ];
  return (
    <article className={styles.fcCard}>
      <header className={styles.fcCardHead}>
        <MemberFace accountId={record.authorId} username={record.authorName} className={styles.fcFace} />
        <div className={styles.fcCardWho}>
          <strong>
            {author.name}
            {author.role && <span className={styles.fcRoleTag}>{author.role}</span>}
          </strong>
          <small>
            {relativeTime(record.createdAt)}
            {record.age
              ? ` · 宝宝${ageText(record.age)}`
              : record.occurredOn
                ? ` · ${shortDate(record.occurredOn)}`
                : ""}
          </small>
        </div>
        <span className={`${styles.fcKindTag} ${styles[`kind-${record.kind}`] ?? ""}`}>
          {kindIcons[record.kind]}
          {kindLabels[record.kind]}
        </span>
        <ItemMenu label="更多操作" items={items} />
      </header>
      <RecordBody record={record} />
      <Comments record={record} onChanged={onChanged} />
    </article>
  );
}

// RecordBody shows what a record holds.
export function RecordBody({ record }: { record: FamilyRecord }) {
  switch (record.kind) {
    case "measurement": {
      const m = record.measurement ?? {};
      return (
        <div className={styles.fcCardBody}>
          <div className={styles.fcStats}>
            {m.heightCm !== undefined && (
              <Stat
                label={record.age && record.age.totalMonths >= 24 ? "身高" : "身长"}
                value={decimal(m.heightCm, 1)}
                unit="cm"
              />
            )}
            {m.weightKg !== undefined && <Stat label="体重" value={decimal(m.weightKg, 2)} unit="kg" />}
            {m.headCm !== undefined && <Stat label="头围" value={decimal(m.headCm, 1)} unit="cm" />}
          </div>
          {record.text && <p className={styles.fcText}>{record.text}</p>}
        </div>
      );
    }
    case "vaccine":
      return <VaccineBody record={record} />;
    case "recording":
      return (
        <div className={styles.fcCardBody}>
          <div className={styles.fcRecording}>
            <span className={styles.fcRecordingIcon} aria-hidden="true">
              <Mic size={16} />
            </span>
            <div>
              <strong>{record.text || record.recording?.fileName || "录音"}</strong>
              {/* oxlint-disable-next-line jsx-a11y/media-has-caption -- a family's own voice recording has no captions */}
              <audio controls preload="none" src={audioURL(record.id)} className={styles.fcAudio} />
            </div>
          </div>
        </div>
      );
    case "milestone":
      return (
        <div className={styles.fcCardBody}>
          <p className={styles.fcMilestoneTitle}>
            <Flag size={15} /> {record.milestone?.title}
            <small>{shortDate(record.occurredOn)}</small>
          </p>
          {record.text && <p className={styles.fcText}>{record.text}</p>}
          <PhotoGrid photos={record.photos} />
        </div>
      );
    default:
      return (
        <div className={styles.fcCardBody}>
          {record.text && <p className={styles.fcText}>{record.text}</p>}
          <PhotoGrid photos={record.photos} />
        </div>
      );
  }
}

function VaccineBody({ record }: { record: FamilyRecord }) {
  const { catalog } = useFamily();
  const dose = record.vaccine!;
  const name = dose.name || catalog.vaccines.vaccines.find((vaccine) => vaccine.code === dose.code)?.name || dose.code;
  return (
    <div className={styles.fcCardBody}>
      <p className={styles.fcVaccineLine}>
        <strong>{name}</strong> 第 {dose.dose} 剂
        <small>
          {shortDate(record.occurredOn)}
          {dose.clinic && ` · ${dose.clinic}`}
          {dose.batch && ` · 批号 ${dose.batch}`}
        </small>
      </p>
      {record.text && <p className={styles.fcText}>{record.text}</p>}
      <PhotoGrid photos={record.photos} limit={3} />
    </div>
  );
}

function Stat({ label, value, unit }: { label: string; value: string; unit: string }) {
  return (
    <div className={styles.fcStat}>
      <span>{label}</span>
      <strong>
        {value}
        <small>{unit}</small>
      </strong>
    </div>
  );
}

function Comments({ record, onChanged }: { record: FamilyRecord; onChanged: (record: FamilyRecord) => void }) {
  const family = useFamily();
  const nameOf = useMemberName();
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const send = async () => {
    const value = text.trim();
    if (!value || busy) return;
    setBusy(true);
    try {
      const comment = await addComment(record.id, value, requestKey());
      onChanged({ ...record, comments: [...record.comments, comment] });
      setText("");
    } catch (caught) {
      family.toast({ text: familyError(caught), tone: "error" });
    }
    setBusy(false);
  };
  const remove = async (commentId: string) => {
    try {
      await deleteComment(commentId);
      onChanged({ ...record, comments: record.comments.filter((comment) => comment.id !== commentId) });
    } catch (caught) {
      family.toast({ text: familyError(caught), tone: "error" });
    }
  };
  return (
    <footer className={styles.fcComments}>
      {record.comments.map((comment) => {
        const who = nameOf(comment.authorId, comment.authorName);
        return (
          <div key={comment.id} className={styles.fcComment}>
            <MemberFace accountId={comment.authorId} username={comment.authorName} className={styles.fcFaceSmall} />
            <p>
              <strong>{who.name}</strong>
              {comment.text}
            </p>
            <ItemMenu
              label="留言操作"
              items={
                canDelete(family, comment.authorId)
                  ? [{ label: "删除留言", danger: true, run: () => void remove(comment.id) }]
                  : []
              }
            />
          </div>
        );
      })}
      <form
        className={styles.fcCommentForm}
        onSubmit={(event) => {
          event.preventDefault();
          void send();
        }}
      >
        <input
          className={styles.fcCommentInput}
          placeholder="留言…"
          maxLength={1000}
          value={text}
          onChange={(event) => setText(event.target.value)}
          aria-label="留言"
        />
        {text.trim() && (
          <button type="submit" className={styles.fcIconButton} aria-label="发送留言" disabled={busy}>
            <Send size={15} />
          </button>
        )}
      </form>
    </footer>
  );
}

function TrashDialog({ baby, onClose, onRestored }: { baby: FamilyBaby; onClose: () => void; onRestored: () => void }) {
  const family = useFamily();
  const { toast } = family;
  const [records, setRecords] = useState<FamilyRecord[]>();
  const nameOf = useMemberName();
  useEffect(() => {
    let live = true;
    listTrash(baby.id).then(
      (items) => {
        if (live) setRecords(items);
      },
      (caught: unknown) => {
        if (live) toast({ text: familyError(caught), tone: "error" });
      },
    );
    return () => {
      live = false;
    };
  }, [baby.id, toast]);
  const restore = async (record: FamilyRecord) => {
    try {
      await restoreRecord(record.id);
      setRecords((current) => current?.filter((item) => item.id !== record.id));
      onRestored();
    } catch (caught) {
      family.toast({ text: familyError(caught), tone: "error" });
    }
  };
  return (
    <Modal title="最近删除" icon={<Trash2 size={17} />} onClose={onClose}>
      <p className={styles.fcFootnote}>
        删除的记录保留 15 天，之后永久删除。
        {family.overview.me.guardian ? "家长可以恢复任何人的记录。" : "你可以恢复自己添加的记录。"}
      </p>
      {records === undefined && <span className="loader small" aria-label="正在读取" />}
      {records?.length === 0 && <p className={styles.fcEmptyLine}>没有最近删除的记录</p>}
      <ul className={styles.fcTrashList}>
        {records?.map((record) => (
          <li key={record.id}>
            <span className={`${styles.fcKindTag} ${styles[`kind-${record.kind}`] ?? ""}`}>
              {kindIcons[record.kind]}
              {kindLabels[record.kind]}
            </span>
            <span className={styles.fcTrashText}>
              {record.text || record.milestone?.title || shortDate(record.occurredOn)}
              <small>
                {nameOf(record.authorId, record.authorName).name} · 删除于{" "}
                {record.deletedAt ? relativeTime(record.deletedAt) : ""}
              </small>
            </span>
            <button type="button" className={styles.fcSecondary} onClick={() => void restore(record)}>
              <RotateCcw size={13} /> 恢复
            </button>
          </li>
        ))}
      </ul>
    </Modal>
  );
}
