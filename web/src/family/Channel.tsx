import { Camera, Flag, ImagePlus, Mic, NotebookPen, Plus, Ruler, Syringe } from "lucide-react";
import { AnimatePresence, motion } from "motion/react";
import { DropdownMenu, Tabs } from "radix-ui";
import { useEffect, useEffectEvent, useRef, useState } from "react";

import { Album } from "./Album";
import {
  type FamilyBaby,
  type FamilyGrowth,
  type FamilyKind,
  type FamilyRecord,
  type FamilyScheduledDose,
  type FamilyVaccineSchedule,
  getGrowth,
  getVaccines,
} from "./api";
import { BabyAvatar, BabyDialog } from "./BabyDialog";
import { Composer, type Draft } from "./Composer";
import { useFamily } from "./context";
import styles from "./family.module.css";
import { ageText, daysUntil, longDate } from "./format";
import { Growth } from "./Growth";
import { Members } from "./Members";
import { Milestones } from "./Milestones";
import { MemberFace } from "./parts";
import { Timeline } from "./Timeline";
import { Vaccines } from "./Vaccines";

export type Board = "timeline" | "album" | "growth" | "vaccines" | "milestones" | "family";

const boards: Array<{ id: Board; label: string }> = [
  { id: "timeline", label: "时间线" },
  { id: "album", label: "相册" },
  { id: "growth", label: "成长" },
  { id: "vaccines", label: "疫苗" },
  { id: "milestones", label: "大事记" },
  { id: "family", label: "家人" },
];

// shows says which kinds of record each board shows.
const shows: Record<Board, FamilyKind[]> = {
  timeline: ["photos", "recording", "journal", "measurement", "vaccine", "milestone"],
  album: ["photos", "journal", "milestone"],
  growth: ["measurement"],
  vaccines: ["vaccine"],
  milestones: ["milestone"],
  family: [],
};

const quickKinds: Array<{ kind: FamilyKind; label: string; icon: typeof Camera }> = [
  { kind: "photos", label: "照片视频", icon: Camera },
  { kind: "journal", label: "日志", icon: NotebookPen },
  { kind: "recording", label: "录音", icon: Mic },
  { kind: "measurement", label: "身高体重", icon: Ruler },
  { kind: "vaccine", label: "疫苗", icon: Syringe },
  { kind: "milestone", label: "大事记", icon: Flag },
];

// backlogDays is how long ago a due dose stops being news: one due earlier
// was most likely given and not recorded, as for a child added at three. The
// desktop's hints stop at the same age (internal/family/hints.go).
const backlogDays = 60;

// Channel is a baby's channel: the boards in the middle, and beside them
// who the baby is, how old, what to record and what is due. Photos, videos
// or a recording dropped anywhere on it, or pasted, start a record.
export function Channel({ baby }: { baby: FamilyBaby }) {
  const family = useFamily();
  const [board, setBoard] = useState<Board>("timeline");
  const [draft, setDraft] = useState<Draft>();
  const [editing, setEditing] = useState(false);
  // Every change to the channel bumps the version, so each board reads again.
  const [version, setVersion] = useState(0);
  // Growth and the schedule feed the side panel and two boards; null is a
  // failed read.
  const [growth, setGrowth] = useState<FamilyGrowth | null>();
  const [schedule, setSchedule] = useState<FamilyVaccineSchedule | null>();
  // fresh is the record just saved, which the timeline scrolls to.
  const [fresh, setFresh] = useState<string>();
  const [dropping, setDropping] = useState(false);
  const depth = useRef(0);
  const root = useRef<HTMLDivElement>(null);
  const changed = () => setVersion((value) => value + 1);
  // A record saved shows where it was added from when that board holds its
  // kind, and in the timeline otherwise.
  const saved = (record: FamilyRecord) => {
    setDraft(undefined);
    setFresh(record.id);
    if (!shows[board].includes(record.kind)) setBoard("timeline");
    changed();
  };

  // start opens the composer with files the member brought: photos and
  // videos for a photo record, else a recording.
  const start = (files: File[]) => {
    const media = files.filter((file) => /^(image|video)\//.test(file.type) || /\.(heic|heif|mov)$/i.test(file.name));
    const audio = files.find((file) => file.type.startsWith("audio/") || /\.(m4a|amr|3gp|aac)$/i.test(file.name));
    if (media.length > 0) setDraft({ kind: "photos", files: media });
    else if (audio) setDraft({ kind: "recording", audio });
  };
  const carriesFiles = (event: React.DragEvent) => !draft && event.dataTransfer.types.includes("Files");

  // A paste goes to the window last pressed in, unless into a field.
  const inside = useRef(false);
  const pasted = useEffectEvent((event: ClipboardEvent) => {
    const target = event.target instanceof Element ? event.target : null;
    if (draft || !inside.current || target?.closest("input, textarea, [contenteditable]")) return;
    const files = [...(event.clipboardData?.files ?? [])];
    if (files.length === 0) return;
    event.preventDefault();
    start(files);
  });
  useEffect(() => {
    const pressed = (event: PointerEvent) => {
      inside.current = Boolean(root.current?.contains(event.target as Node));
    };
    const paste = (event: ClipboardEvent) => pasted(event);
    document.addEventListener("pointerdown", pressed, true);
    document.addEventListener("paste", paste);
    return () => {
      document.removeEventListener("pointerdown", pressed, true);
      document.removeEventListener("paste", paste);
    };
  }, []);

  useEffect(() => {
    let live = true;
    getGrowth(baby.id).then(
      (next) => {
        if (live) setGrowth(next);
      },
      () => {
        if (live) setGrowth(null);
      },
    );
    getVaccines(baby.id).then(
      (next) => {
        if (live) setSchedule(next);
      },
      () => {
        if (live) setSchedule(null);
      },
    );
    return () => {
      live = false;
    };
  }, [baby.id, version]);

  const record = (kind: FamilyKind) => setDraft({ kind });
  return (
    // biome-ignore lint/a11y/noStaticElementInteractions: dropping files is a shortcut; every record can be added with the buttons
    <div
      className={styles.fcChannel}
      ref={root}
      onDragEnter={(event) => {
        if (!carriesFiles(event)) return;
        depth.current += 1;
        setDropping(true);
      }}
      onDragOver={(event) => {
        if (carriesFiles(event)) event.preventDefault();
      }}
      onDragLeave={() => {
        depth.current = Math.max(0, depth.current - 1);
        if (depth.current === 0) setDropping(false);
      }}
      onDrop={(event) => {
        if (!carriesFiles(event)) return;
        event.preventDefault();
        depth.current = 0;
        setDropping(false);
        start([...event.dataTransfer.files]);
      }}
    >
      <Tabs.Root value={board} onValueChange={(value) => setBoard(value as Board)} className={styles.fcMain}>
        <Tabs.List className={styles.fcTabs} aria-label="板块">
          {boards.map(({ id, label }) => (
            <Tabs.Trigger key={id} value={id} className={styles.fcTab}>
              {label}
              {board === id && (
                <motion.span
                  layoutId="fc-tab-line"
                  className={styles.fcTabLine}
                  transition={{ type: "spring", stiffness: 480, damping: 38 }}
                />
              )}
            </Tabs.Trigger>
          ))}
        </Tabs.List>
        <div className={styles.fcScroll}>
          <Tabs.Content value="timeline" className={styles.fcBoard}>
            <Timeline
              baby={baby}
              version={version}
              growth={growth}
              fresh={fresh}
              onChanged={changed}
              onAdded={saved}
              onEdit={setDraft}
              onRecord={record}
            />
          </Tabs.Content>
          <Tabs.Content value="album" className={styles.fcBoard}>
            <Album baby={baby} version={version} onAdd={() => record("photos")} />
          </Tabs.Content>
          <Tabs.Content value="growth" className={styles.fcBoard}>
            <Growth
              baby={baby}
              growth={growth}
              onAdd={() => record("measurement")}
              onEdit={setDraft}
              onChanged={changed}
            />
          </Tabs.Content>
          <Tabs.Content value="vaccines" className={styles.fcBoard}>
            <Vaccines baby={baby} schedule={schedule} fresh={fresh} onPlanned={setSchedule} onRecord={setDraft} />
          </Tabs.Content>
          <Tabs.Content value="milestones" className={styles.fcBoard}>
            <Milestones baby={baby} version={version} onAdd={setDraft} />
          </Tabs.Content>
          <Tabs.Content value="family" className={styles.fcBoard}>
            <Members baby={baby} />
          </Tabs.Content>
        </div>
      </Tabs.Root>

      <aside className={styles.fcSide} aria-label={`${baby.name}的资料`}>
        <div className={styles.fcProfile}>
          {family.overview.me.guardian ? (
            <button
              type="button"
              className={styles.fcPortraitButton}
              onClick={() => setEditing(true)}
              aria-label="编辑宝宝资料"
            >
              <BabyAvatar baby={baby} className={styles.fcPortrait} />
            </button>
          ) : (
            <BabyAvatar baby={baby} className={styles.fcPortrait} />
          )}
          <h1>
            {baby.name}
            {baby.nickname && <small>{baby.nickname}</small>}
          </h1>
          <p className={styles.fcAge}>
            {ageText(baby.age, false)}
            {baby.age.days > 0 && baby.age.years < 3 && (baby.age.years || baby.age.months) ? (
              <small> {baby.age.days} 天</small>
            ) : null}
          </p>
          <p className={styles.fcBorn}>
            {longDate(baby.birthDate)}出生
            <NextDay baby={baby} />
          </p>
        </div>

        <section className={styles.fcSideBlock}>
          <h2>记一笔</h2>
          <div className={styles.fcQuick}>
            {quickKinds.map(({ kind, label, icon: Icon }) => (
              <button
                key={kind}
                type="button"
                className={kind === "photos" ? styles.primary : undefined}
                onClick={() => record(kind)}
              >
                <Icon size={18} strokeWidth={1.6} />
                {label}
              </button>
            ))}
          </div>
        </section>

        <DueDoses schedule={schedule} onOpen={() => setBoard("vaccines")} onRecord={setDraft} />

        <section className={styles.fcSideBlock}>
          <h2>家人 · {family.overview.members.length}</h2>
          <button
            type="button"
            className={styles.fcFamilyStack}
            onClick={() => setBoard("family")}
            aria-label="查看家人"
          >
            {family.overview.members.slice(0, 6).map((member) => (
              <MemberFace
                key={member.accountId}
                accountId={member.accountId}
                username={member.username}
                className={styles.fcStackFace}
              />
            ))}
            {family.overview.requests.length > 0 && (
              <span className={styles.fcStackNote}>{family.overview.requests.length} 人申请加入</span>
            )}
          </button>
        </section>
      </aside>

      <AnimatePresence>
        {dropping && (
          <motion.div
            className={styles.fcDropOverlay}
            initial={{ opacity: 0 }}
            animate={{ opacity: 1 }}
            exit={{ opacity: 0 }}
            transition={{ duration: 0.15 }}
          >
            <motion.div initial={{ scale: 0.92, y: 8 }} animate={{ scale: 1, y: 0 }} exit={{ scale: 0.96 }}>
              <ImagePlus size={28} strokeWidth={1.5} />
              松开以添加到{baby.nickname || baby.name}的时间线
            </motion.div>
          </motion.div>
        )}
      </AnimatePresence>

      {/* In a window too narrow for the side panel, its 记一笔 floats. */}
      <DropdownMenu.Root modal={false}>
        <DropdownMenu.Trigger className={styles.fcFab} aria-label="记一笔">
          <Plus size={20} />
        </DropdownMenu.Trigger>
        <DropdownMenu.Portal>
          <DropdownMenu.Content className={styles.fcMenu} side="top" align="end" sideOffset={8}>
            {quickKinds.map(({ kind, label, icon: Icon }) => (
              <DropdownMenu.Item key={kind} className={styles.fcMenuItem} onSelect={() => record(kind)}>
                <Icon size={15} strokeWidth={1.6} />
                {label}
              </DropdownMenu.Item>
            ))}
          </DropdownMenu.Content>
        </DropdownMenu.Portal>
      </DropdownMenu.Root>

      {draft && <Composer baby={baby} draft={draft} onClose={() => setDraft(undefined)} onSaved={saved} />}
      {editing && (
        <BabyDialog
          baby={baby}
          onClose={() => setEditing(false)}
          onSaved={async () => {
            setEditing(false);
            await family.reload();
          }}
        />
      )}
    </div>
  );
}

// DueDoses lists the doses that came due lately or come up within 30 days;
// older doses without a record are backlog to fill in, said in one line.
function DueDoses({
  schedule,
  onOpen,
  onRecord,
}: {
  schedule?: FamilyVaccineSchedule | null;
  onOpen: () => void;
  onRecord: (draft: Draft) => void;
}) {
  if (!schedule) return null;
  const due = schedule.doses.filter((dose) => dose.status === "available" || dose.status === "soon");
  const recent = due.filter((dose) => dose.status === "soon" || daysUntil(dose.dueDate) >= -backlogDays);
  const backlog = due.length - recent.length;
  if (due.length === 0) return null;
  return (
    <section className={styles.fcSideBlock}>
      <h2>该打的疫苗</h2>
      <AnimatePresence initial={false}>
        {recent.slice(0, 3).map((dose) => (
          <motion.div
            key={`${dose.code}-${dose.dose}`}
            className={styles.fcDueSlot}
            layout
            exit={{ opacity: 0, x: 40, height: 0 }}
            transition={{ duration: 0.3, ease: [0.2, 0.8, 0.2, 1] }}
          >
            <DueRow dose={dose} onRecord={onRecord} />
          </motion.div>
        ))}
      </AnimatePresence>
      {(recent.length > 3 || backlog > 0) && (
        <button type="button" className={styles.fcLink} onClick={onOpen}>
          {backlog > 0 ? `另有 ${backlog} 剂没有记录，打过的可以补记` : `还有 ${recent.length - 3} 剂，查看接种本`}
        </button>
      )}
    </section>
  );
}

function DueRow({ dose, onRecord }: { dose: FamilyScheduledDose; onRecord: (draft: Draft) => void }) {
  const days = daysUntil(dose.dueDate);
  const when = days > 0 ? `还有 ${days} 天` : days === 0 ? "今天到月龄" : "可以接种";
  return (
    <button
      type="button"
      className={styles.fcDue}
      onClick={() => onRecord({ kind: "vaccine", vaccine: { code: dose.code, dose: dose.dose } })}
      aria-label={`记录${dose.name}第 ${dose.dose} 剂`}
    >
      <strong>
        {dose.name} 第 {dose.dose} 剂
      </strong>
      <span>
        {dose.ageLabel} · {when}
      </span>
    </button>
  );
}

// NextDay counts down to the next 满月, 百天 or birthday.
function NextDay({ baby }: { baby: FamilyBaby }) {
  const [year, month, day] = baby.birthDate.split("-").map(Number);
  const birth = new Date(year, month - 1, day);
  const addMonths = (months: number) => {
    const first = new Date(birth.getFullYear(), birth.getMonth() + months, 1);
    const last = new Date(first.getFullYear(), first.getMonth() + 1, 0).getDate();
    return new Date(first.getFullYear(), first.getMonth(), Math.min(day, last));
  };
  const candidates: Array<[string, Date]> = [
    ["满月", addMonths(1)],
    ["百天", new Date(year, month - 1, day + 99)],
    ...Array.from({ length: 18 }, (_, index): [string, Date] => [
      index === 0 ? "周岁" : `${index + 1} 岁生日`,
      addMonths(12 * (index + 1)),
    ]),
  ];
  const now = new Date();
  const start = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime();
  for (const [title, date] of candidates) {
    const days = Math.round((date.getTime() - start) / 86_400_000);
    if (days > 0)
      return (
        <>
          <br />距{title}还有 {days} 天
        </>
      );
  }
  return null;
}
