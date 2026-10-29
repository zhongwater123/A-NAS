import {
  Camera,
  Flag,
  Images,
  Mic,
  Newspaper,
  NotebookPen,
  Pencil,
  Plus,
  Ruler,
  Syringe,
  TrendingUp,
  Users,
} from "lucide-react";
import { DropdownMenu, Tabs } from "radix-ui";
import { useEffect, useState } from "react";

import { Album } from "./Album";
import { type FamilyBaby, type FamilyKind, type FamilyScheduledDose, getVaccines } from "./api";
import { BabyAvatar, BabyDialog } from "./BabyDialog";
import { Composer, type Draft } from "./Composer";
import { useFamily } from "./context";
import { Feed } from "./Feed";
import styles from "./family.module.css";
import { ageText, daysUntil, longDate } from "./format";
import { Growth } from "./Growth";
import { Members } from "./Members";
import { Milestones } from "./Milestones";
import { Vaccines } from "./Vaccines";

export type Board = "feed" | "album" | "growth" | "vaccines" | "milestones" | "family";

const boards: Array<{ id: Board; label: string; icon: typeof Newspaper }> = [
  { id: "feed", label: "动态", icon: Newspaper },
  { id: "album", label: "云相册", icon: Images },
  { id: "growth", label: "成长记录", icon: TrendingUp },
  { id: "vaccines", label: "疫苗接种", icon: Syringe },
  { id: "milestones", label: "大事记", icon: Flag },
  { id: "family", label: "亲友团", icon: Users },
];

const quickKinds: Array<{ kind: Exclude<FamilyKind, "recording"> | "recording"; label: string; icon: typeof Camera }> =
  [
    { kind: "photos", label: "照片视频", icon: Camera },
    { kind: "recording", label: "录音", icon: Mic },
    { kind: "journal", label: "日志", icon: NotebookPen },
    { kind: "measurement", label: "身高体重", icon: Ruler },
    { kind: "vaccine", label: "疫苗接种", icon: Syringe },
    { kind: "milestone", label: "大事记", icon: Flag },
  ];

// Channel is a baby's channel: who the baby is, how old, what is due, and
// the boards. Every record added anywhere shows in 动态 and its board.
export function Channel({ baby }: { baby: FamilyBaby }) {
  const family = useFamily();
  const [board, setBoard] = useState<Board>("feed");
  const [draft, setDraft] = useState<Draft>();
  const [editing, setEditing] = useState(false);
  // Every change to the channel bumps the version, so each board reads again.
  const [version, setVersion] = useState(0);
  const [due, setDue] = useState<FamilyScheduledDose[]>([]);
  const changed = () => setVersion((value) => value + 1);

  useEffect(() => {
    let live = true;
    getVaccines(baby.id).then(
      (schedule) => {
        if (live) setDue(schedule.doses.filter((dose) => dose.status === "available" || dose.status === "soon"));
      },
      () => undefined,
    );
    return () => {
      live = false;
    };
  }, [baby.id, version]);

  const next = nextAutoMilestone(baby);
  return (
    <div className={styles.fcChannel}>
      <section className={styles.fcHero}>
        <div className={styles.fcHeroGlow} aria-hidden="true" />
        <BabyAvatar baby={baby} className={styles.fcHeroFace} />
        <div className={styles.fcHeroText}>
          <h1>
            {baby.name}
            {baby.nickname && <span className={styles.fcNickname}>{baby.nickname}</span>}
          </h1>
          <p className={styles.fcHeroAge}>{ageText(baby.age)}</p>
          <p className={styles.fcHeroMeta}>
            {longDate(baby.birthDate)}出生{next && ` · 距${next.title}还有 ${next.days} 天`}
          </p>
        </div>
        <div className={styles.fcHeroActions}>
          {family.overview.me.guardian && (
            <button type="button" className={styles.fcGhost} onClick={() => setEditing(true)} aria-label="编辑宝宝资料">
              <Pencil size={15} />
            </button>
          )}
          <DropdownMenu.Root modal={false}>
            <DropdownMenu.Trigger className={styles.fcRecordButton}>
              <Plus size={17} /> 记一笔
            </DropdownMenu.Trigger>
            <DropdownMenu.Portal>
              <DropdownMenu.Content className={`${styles.fcMenu} ${styles.fcQuickMenu}`} align="end" sideOffset={6}>
                {quickKinds.map(({ kind, label, icon: Icon }) => (
                  <DropdownMenu.Item key={kind} className={styles.fcMenuItem} onSelect={() => setDraft({ kind })}>
                    <span className={`${styles.fcKindIcon} ${styles[`kind-${kind}`] ?? ""}`} aria-hidden="true">
                      <Icon size={15} />
                    </span>
                    {label}
                  </DropdownMenu.Item>
                ))}
              </DropdownMenu.Content>
            </DropdownMenu.Portal>
          </DropdownMenu.Root>
        </div>
        {due.length > 0 && (
          <button type="button" className={styles.fcDueBanner} onClick={() => setBoard("vaccines")}>
            <Syringe size={15} />
            <span>
              {dueText(due)}
              <small>以当地接种门诊的安排为准</small>
            </span>
          </button>
        )}
      </section>
      <Tabs.Root value={board} onValueChange={(value) => setBoard(value as Board)} className={styles.fcBoards}>
        <Tabs.List className={styles.fcTabs} aria-label="板块">
          {boards.map(({ id, label, icon: Icon }) => (
            <Tabs.Trigger key={id} value={id} className={styles.fcTab}>
              <Icon size={15} />
              {label}
            </Tabs.Trigger>
          ))}
        </Tabs.List>
        <Tabs.Content value="feed" className={styles.fcBoard}>
          <Feed baby={baby} version={version} onChanged={changed} onEdit={setDraft} />
        </Tabs.Content>
        <Tabs.Content value="album" className={styles.fcBoard}>
          <Album baby={baby} version={version} onAdd={() => setDraft({ kind: "photos" })} />
        </Tabs.Content>
        <Tabs.Content value="growth" className={styles.fcBoard}>
          <Growth
            baby={baby}
            version={version}
            onAdd={() => setDraft({ kind: "measurement" })}
            onEdit={setDraft}
            onChanged={changed}
          />
        </Tabs.Content>
        <Tabs.Content value="vaccines" className={styles.fcBoard}>
          <Vaccines baby={baby} version={version} onRecord={setDraft} onChanged={changed} />
        </Tabs.Content>
        <Tabs.Content value="milestones" className={styles.fcBoard}>
          <Milestones baby={baby} version={version} onAdd={setDraft} />
        </Tabs.Content>
        <Tabs.Content value="family" className={styles.fcBoard}>
          <Members baby={baby} />
        </Tabs.Content>
      </Tabs.Root>
      {draft && (
        <Composer
          baby={baby}
          draft={draft}
          onClose={() => setDraft(undefined)}
          onSaved={() => {
            setDraft(undefined);
            changed();
          }}
        />
      )}
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

// backlogDays is how long ago a due dose stops being news: one due earlier
// was most likely given and not recorded, as for a child added at three. The
// desktop's hints stop at the same age (internal/family/hints.go).
const backlogDays = 60;

// dueText says what is due: the doses that came due lately or come up soon,
// or else how many older doses have no record yet.
function dueText(due: FamilyScheduledDose[]): string {
  const recent = due.filter((dose) => dose.status === "soon" || daysUntil(dose.dueDate) >= -backlogDays);
  if (recent.length === 0) return `有 ${due.length} 剂疫苗到了接种月龄还没有记录，打过的可以补记`;
  const available = recent.filter((dose) => dose.status === "available");
  const first = (available[0] ?? recent[0])!;
  const name = `${first.name}第 ${first.dose} 剂`;
  if (available.length > 0)
    return available.length === 1 ? `${name}可以接种了` : `${name}等 ${available.length} 剂疫苗可以接种`;
  const days = daysUntil(first.dueDate);
  return `${name}${days === 0 ? "今天" : ` ${days} 天后`}到接种月龄`;
}

// nextAutoMilestone is the next 满月, 百天 or birthday, counted in days.
function nextAutoMilestone(baby: FamilyBaby): { title: string; days: number } | undefined {
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
    if (days > 0) return { title, days };
  }
  return undefined;
}
