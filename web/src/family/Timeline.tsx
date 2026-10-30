import { Flag, Pause, Play, RotateCcw, Syringe } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { relativeTime } from "../lib/time";
import {
  addComment,
  audioURL,
  deleteComment,
  deleteRecord,
  type FamilyBaby,
  type FamilyGrowth,
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
import { assess, bandShort, decimal, kindLabels, monthLabel, monthRange, sealText, shortDate, verbs } from "./format";
import { ItemMenu, MemberFace, Modal, MonthTitle, Mosaic, useMemberName } from "./parts";

// Timeline is the channel's 时间线: every record by the day it happened,
// grouped by the baby's month of life, the content first.
export function Timeline({
  baby,
  version,
  growth,
  onChanged,
  onEdit,
  onRecord,
}: {
  baby: FamilyBaby;
  version: number;
  growth?: FamilyGrowth | null;
  onChanged: () => void;
  onEdit: (draft: Draft) => void;
  onRecord: (kind: FamilyKind) => void;
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
        text: `已删除这条${kindLabels[record.kind]}`,
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

  const months = useMemo(() => {
    const groups: Array<{ month: number; records: FamilyRecord[] }> = [];
    for (const record of records ?? []) {
      const month = record.age?.totalMonths ?? -1;
      const last = groups.at(-1);
      if (last && last.month === month) last.records.push(record);
      else groups.push({ month, records: [record] });
    }
    return groups;
  }, [records]);

  if (failure) return <p className={styles.fcFailure}>{failure}</p>;
  if (!records) return <span className="loader" aria-label="正在读取时间线" />;
  if (records.length === 0)
    return (
      <div className={styles.fcBlank}>
        <h2>还没有记录</h2>
        <button type="button" className={styles.fcPrimary} onClick={() => onRecord("photos")}>
          上传照片视频
        </button>
      </div>
    );
  return (
    <div className={styles.fcTimeline}>
      {months.map((group) => (
        <section
          key={`${group.month}-${group.records[0].id}`}
          className={styles.fcMonth}
          aria-label={monthLabel(group.month)}
        >
          <header className={styles.fcMonthHead}>
            <h2>
              <MonthTitle month={group.month} />
            </h2>
            {group.month >= 0 && <DayRange days={monthRange(baby.birthDate, group.month)} />}
          </header>
          <div className={styles.fcEntries}>
            {group.records.map((record) => (
              <Entry
                key={record.id}
                record={record}
                growth={growth}
                onChanged={replace}
                onEdit={() => onEdit({ kind: record.kind, record })}
                onDelete={() => void remove(record)}
              />
            ))}
          </div>
        </section>
      ))}
      <footer className={styles.fcTimelineEnd}>
        {next && (
          <button type="button" className={styles.fcSecondary} onClick={() => void more()} disabled={loading}>
            {loading ? "正在读取…" : "更早的记录"}
          </button>
        )}
        <button type="button" className={styles.fcLink} onClick={() => setTrashOpen(true)}>
          最近删除
        </button>
      </footer>
      {trashOpen && <TrashDialog baby={baby} onClose={() => setTrashOpen(false)} onRestored={onChanged} />}
    </div>
  );
}

// DayRange says a month's first and last day, wrapping between them.
function DayRange({ days: [start, end] }: { days: [string, string] }) {
  return (
    <small>
      <span>{start} —</span> <span>{end}</span>
    </small>
  );
}

// Entry is one record in the timeline: who and when in a quiet line, then
// what they recorded, then the comments folded into one line.
function Entry({
  record,
  growth,
  onChanged,
  onEdit,
  onDelete,
}: {
  record: FamilyRecord;
  growth?: FamilyGrowth | null;
  onChanged: (record: FamilyRecord) => void;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const family = useFamily();
  const nameOf = useMemberName();
  const [talking, setTalking] = useState(false);
  const author = nameOf(record.authorId, record.authorName);
  const items = [
    ...(record.authorId === family.userId ? [{ label: "编辑", run: onEdit }] : []),
    ...(canDelete(family, record.authorId) ? [{ label: "删除", danger: true, run: onDelete }] : []),
  ];
  const photos = record.photos.filter((photo) => photo.role === "album");
  const verb =
    record.kind === "photos"
      ? `添加了 ${photos.length} ${photos.every((photo) => photo.video) ? "段视频" : "张照片"}`
      : verbs[record.kind];
  return (
    <article className={styles.fcEntry}>
      <div className={styles.fcBy}>
        <MemberFace accountId={record.authorId} username={record.authorName} className={styles.fcFaceTiny} />
        <b>{author.role || author.name}</b>
        <span>{verb}</span>
        <span className={styles.fcDot}>·</span>
        <span>{shortDate(record.occurredOn)}</span>
        <button type="button" className={styles.fcByAction} onClick={() => setTalking(true)}>
          留言
        </button>
        <ItemMenu label="更多操作" items={items} />
      </div>
      <Body record={record} growth={growth} />
      <Comments record={record} open={talking} onOpen={() => setTalking(true)} onChanged={onChanged} />
    </article>
  );
}

function Body({ record, growth }: { record: FamilyRecord; growth?: FamilyGrowth | null }) {
  const album = record.photos.filter((photo) => photo.role === "album");
  switch (record.kind) {
    case "journal":
      return (
        <>
          <blockquote className={styles.fcQuote}>{record.text}</blockquote>
          <Mosaic photos={album} />
        </>
      );
    case "photos":
      return (
        <>
          <Mosaic photos={album} />
          {record.text && <p className={styles.fcCaption}>{record.text}</p>}
        </>
      );
    case "recording":
      return <Voice record={record} />;
    case "measurement":
      return <Facts record={record} growth={growth} />;
    case "vaccine":
      return <VaccineLine record={record} />;
    case "milestone":
      return (
        <>
          <div className={styles.fcMilestoneLine}>
            <span className={styles.fcSeal} aria-hidden="true">
              {record.milestone?.autoKey ? sealText(record.milestone.title) : <Flag size={16} strokeWidth={1.6} />}
            </span>
            <div>
              <h3>{record.milestone?.title}</h3>
              {record.text && <p>{record.text}</p>}
            </div>
          </div>
          <Mosaic photos={album} />
        </>
      );
  }
}

function Facts({ record, growth }: { record: FamilyRecord; growth?: FamilyGrowth | null }) {
  const m = record.measurement ?? {};
  const months = record.age ? record.age.totalMonths + record.age.days / 30.44 : undefined;
  const band = (curve: keyof FamilyGrowth["curves"], value?: number) => {
    if (!growth || value === undefined || months === undefined) return undefined;
    const placed = assess(growth.curves[curve], growth.percentiles, months, value);
    return placed && bandShort(placed);
  };
  const cells = [
    {
      label: months !== undefined && months >= 24 ? "身高" : "身长",
      value: m.heightCm,
      unit: "cm",
      digits: 1,
      band: band("height", m.heightCm),
    },
    { label: "体重", value: m.weightKg, unit: "kg", digits: 2, band: band("weight", m.weightKg) },
    { label: "头围", value: m.headCm, unit: "cm", digits: 1, band: band("head", m.headCm) },
  ].filter((cell) => cell.value !== undefined);
  return (
    <>
      <div className={styles.fcFacts}>
        {cells.map((cell) => (
          <div key={cell.label}>
            <span>{cell.label}</span>
            <strong>
              {decimal(cell.value!, cell.digits)}
              <small>{cell.unit}</small>
            </strong>
            {cell.band && <em>{cell.band}</em>}
          </div>
        ))}
      </div>
      {record.text && <p className={styles.fcCaption}>{record.text}</p>}
    </>
  );
}

function VaccineLine({ record }: { record: FamilyRecord }) {
  const { catalog } = useFamily();
  const dose = record.vaccine!;
  const name = dose.name || catalog.vaccines.vaccines.find((vaccine) => vaccine.code === dose.code)?.name || dose.code;
  const certificates = record.photos.filter((photo) => photo.role === "certificate");
  return (
    <>
      <div className={styles.fcVaccineLine}>
        <span className={styles.fcSealSmall} aria-hidden="true">
          <Syringe size={14} strokeWidth={1.6} />
        </span>
        <div>
          <strong>
            {name} 第 {dose.dose} 剂
          </strong>
          {(dose.clinic || dose.batch) && (
            <small>{[dose.clinic, dose.batch && `批号 ${dose.batch}`].filter(Boolean).join(" · ")}</small>
          )}
        </div>
      </div>
      {record.text && <p className={styles.fcCaption}>{record.text}</p>}
      <Mosaic photos={certificates} limit={3} />
    </>
  );
}

// Voice plays a recording from a quiet pill with a play button and a
// waveform drawn from the record, which only decorates.
function Voice({ record }: { record: FamilyRecord }) {
  const audio = useRef<HTMLAudioElement>(null);
  const [playing, setPlaying] = useState(false);
  const [duration, setDuration] = useState<number>();
  const [progress, setProgress] = useState(0);
  const bars = useMemo(() => waveform(record.id), [record.id]);
  const toggle = () => {
    const element = audio.current;
    if (!element) return;
    if (element.paused) void element.play().catch(() => setPlaying(false));
    else element.pause();
  };
  const time = (seconds?: number) =>
    seconds === undefined || !Number.isFinite(seconds)
      ? ""
      : `${Math.floor(seconds / 60)}:${String(Math.round(seconds % 60)).padStart(2, "0")}`;
  return (
    <div className={styles.fcVoice}>
      <button type="button" className={styles.fcPlay} onClick={toggle} aria-label={playing ? "暂停" : "播放录音"}>
        {playing ? <Pause size={14} fill="currentColor" /> : <Play size={14} fill="currentColor" />}
      </button>
      <span className={styles.fcWave} aria-hidden="true">
        {bars.map((height, index) => (
          <i
            // biome-ignore lint/suspicious/noArrayIndexKey: the bars are a fixed decoration
            key={index}
            style={{ height: `${height}%` }}
            className={index / bars.length < progress ? styles.played : undefined}
          />
        ))}
      </span>
      <span className={styles.fcVoiceTitle}>
        <b>{record.text || record.recording?.fileName || "录音"}</b>
        <small>{time(duration)}</small>
      </span>
      {/* oxlint-disable-next-line jsx-a11y/media-has-caption -- a family's own voice recording has no captions */}
      <audio
        ref={audio}
        preload="metadata"
        src={audioURL(record.id)}
        onPlay={() => setPlaying(true)}
        onPause={() => setPlaying(false)}
        onEnded={() => {
          setPlaying(false);
          setProgress(0);
        }}
        onLoadedMetadata={(event) => setDuration(event.currentTarget.duration)}
        onTimeUpdate={(event) => {
          const element = event.currentTarget;
          if (element.duration) setProgress(element.currentTime / element.duration);
        }}
      />
    </div>
  );
}

// waveform draws a recording's bars from its ID, the same each time; they
// decorate and do not follow the sound.
function waveform(id: string): number[] {
  let seed = [...id].reduce((sum, character) => (sum * 31 + character.charCodeAt(0)) >>> 0, 7);
  const bars: number[] = [];
  for (let index = 0; index < 28; index++) {
    seed = (seed * 1103515245 + 12345) >>> 0;
    bars.push(25 + (seed % 70));
  }
  return bars;
}

// Comments shows the latest two comments in one line, or nothing when there
// are none; opened, it lists all of them and a field to add one.
function Comments({
  record,
  open,
  onOpen,
  onChanged,
}: {
  record: FamilyRecord;
  open: boolean;
  onOpen: () => void;
  onChanged: (record: FamilyRecord) => void;
}) {
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
  if (!open) {
    if (record.comments.length === 0) return null;
    return (
      <button type="button" className={styles.fcTalk} onClick={onOpen}>
        {record.comments.slice(-2).map((comment) => (
          <span key={comment.id}>
            <b>{nameOf(comment.authorId, comment.authorName).name}</b> {comment.text}
          </span>
        ))}
        {record.comments.length > 2 && <span className={styles.fcTalkMore}>共 {record.comments.length} 条</span>}
      </button>
    );
  }
  return (
    <div className={styles.fcThread}>
      {record.comments.map((comment) => {
        const who = nameOf(comment.authorId, comment.authorName);
        return (
          <div key={comment.id} className={styles.fcComment}>
            <MemberFace accountId={comment.authorId} username={comment.authorName} className={styles.fcFaceTiny} />
            <p>
              <b>{who.name}</b>
              {comment.text}
              <small>{relativeTime(comment.createdAt)}</small>
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
          placeholder="写一句留言"
          maxLength={1000}
          value={text}
          onChange={(event) => setText(event.target.value)}
          aria-label="留言"
          // oxlint-disable-next-line jsx-a11y/no-autofocus -- the field opens because the member chose to comment
          autoFocus
        />
        <button type="submit" className={styles.fcSecondary} disabled={busy || !text.trim()}>
          发送
        </button>
      </form>
    </div>
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
      toast({ text: familyError(caught), tone: "error" });
    }
  };
  return (
    <Modal title="最近删除" onClose={onClose}>
      <p className={styles.fcNote}>
        删除的记录保留 15 天，之后永久删除。
        {family.overview.me.guardian ? "家长可以恢复任何人的记录。" : "你可以恢复自己添加的记录。"}
      </p>
      {records === undefined && <span className="loader small" aria-label="正在读取" />}
      {records?.length === 0 && <p className={styles.fcNote}>没有最近删除的记录。</p>}
      <ul className={styles.fcRows}>
        {records?.map((record) => (
          <li key={record.id}>
            <span className={styles.fcRowMain}>
              {kindLabels[record.kind]}：{record.text || record.milestone?.title || shortDate(record.occurredOn)}
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
