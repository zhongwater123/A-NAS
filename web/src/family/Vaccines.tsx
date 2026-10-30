import { ChevronDown, ListChecks, Plus } from "lucide-react";
import { motion } from "motion/react";
import { useMemo, useState } from "react";

import {
  type FamilyBaby,
  type FamilyScheduledDose,
  type FamilyVaccineSchedule,
  familyError,
  setVaccinePlan,
} from "./api";
import type { Draft } from "./Composer";
import { useFamily } from "./context";
import styles from "./family.module.css";
import { daysUntil, shortDate } from "./format";
import { Modal } from "./parts";

const done = (dose: FamilyScheduledDose) => dose.status === "given" || dose.status === "replaced";
const doseKey = (dose: FamilyScheduledDose) => `${dose.code}-${dose.dose}`;

// Vaccines is the channel's 接种本: the national programme's doses and the
// planned non-programme ones by recommended age, a stamp on each given dose.
// The ages already done fold into one line. The channel reads the schedule
// for this board and the side panel; null means it failed.
export function Vaccines({
  baby,
  schedule,
  fresh,
  onPlanned,
  onRecord,
}: {
  baby: FamilyBaby;
  schedule?: FamilyVaccineSchedule | null;
  // fresh is the record just saved: a dose it gave has its stamp pressed.
  fresh?: string;
  onPlanned: (schedule: FamilyVaccineSchedule) => void;
  onRecord: (draft: Draft) => void;
}) {
  const family = useFamily();
  const [planning, setPlanning] = useState(false);
  const [showDone, setShowDone] = useState(false);

  const groups = useMemo(() => {
    const result: Array<{ due: string; label: string; doses: FamilyScheduledDose[] }> = [];
    for (const dose of schedule?.doses ?? []) {
      const last = result.at(-1);
      if (last && last.due === dose.dueDate) last.doses.push(dose);
      else result.push({ due: dose.dueDate, label: dose.ageLabel, doses: [dose] });
    }
    return result;
  }, [schedule]);

  if (schedule === null) return <p className={styles.fcFailure}>暂时读不到接种本，请稍后再试。</p>;
  if (!schedule) return <span className="loader" aria-label="正在读取接种本" />;
  const count = (...statuses: FamilyScheduledDose["status"][]) =>
    schedule.doses.filter((dose) => statuses.includes(dose.status)).length;
  const scheduled = new Set(schedule.doses.map((dose) => dose.recordId).filter(Boolean));
  const others = schedule.records.filter((record) => !scheduled.has(record.id));
  // The leading ages whose doses are all done fold away, unless asked for.
  const folded = groups.findIndex((group) => !group.doses.every(done));
  const finished = folded === -1 ? groups.length : folded;
  const visible = showDone || finished < 2 ? groups : groups.slice(finished);
  const finishedDoses = groups.slice(0, finished).reduce((sum, group) => sum + group.doses.length, 0);
  return (
    <div className={styles.fcVaccines}>
      <header className={styles.fcBoardHead}>
        <div>
          <h2>接种本</h2>
        </div>
        <button type="button" className={styles.fcSecondary} onClick={() => setPlanning(true)}>
          <ListChecks size={14} /> 自费疫苗
        </button>
        <button type="button" className={styles.fcSecondary} onClick={() => onRecord({ kind: "vaccine" })}>
          <Plus size={14} /> 记录接种
        </button>
      </header>

      <div className={`${styles.fcFacts} ${styles.fcTally}`}>
        <Tally value={count("given", "replaced")} label="已完成" />
        <Tally value={count("available")} label="可以接种" tone={styles.due} />
        <Tally value={count("soon")} label="30 天内" tone={styles.soon} />
        <Tally value={count("upcoming")} label="还没到月龄" />
      </div>

      {finished >= 2 && (
        <button
          type="button"
          className={`${styles.fcFold} ${showDone ? styles.open : ""}`}
          aria-expanded={showDone}
          onClick={() => setShowDone((value) => !value)}
        >
          {groups[0].label} — {groups[finished - 1].label}的 {finishedDoses} 剂都已完成
          <ChevronDown size={14} />
        </button>
      )}
      <ol className={styles.fcBook}>
        {visible.map((group) => (
          <li key={group.due} className={styles.fcMonth}>
            <header className={styles.fcMonthHead}>
              <h2>{group.label}</h2>
              <small>{shortDate(group.due)}</small>
            </header>
            <ul className={styles.fcDoses}>
              {group.doses.map((dose) => (
                <DoseRow
                  key={doseKey(dose)}
                  dose={dose}
                  pressed={fresh !== undefined && dose.recordId === fresh}
                  onRecord={() => onRecord({ kind: "vaccine", vaccine: { code: dose.code, dose: dose.dose } })}
                />
              ))}
            </ul>
          </li>
        ))}
      </ol>

      {others.length > 0 && (
        <section className={styles.fcBoardSection}>
          <h3>其他接种记录</h3>
          <ul className={styles.fcDoses}>
            {others.map((record) => (
              <li key={record.id} className={`${styles.fcDose} ${styles.given}`}>
                <span className={styles.fcDoseName}>
                  {record.vaccine?.name ||
                    family.catalog.vaccines.vaccines.find((vaccine) => vaccine.code === record.vaccine?.code)?.name}
                  <small>第 {record.vaccine?.dose} 剂</small>
                </span>
                <Stamp date={record.occurredOn} />
              </li>
            ))}
          </ul>
        </section>
      )}
      <p className={styles.fcSource}>
        依据{schedule.title}（{schedule.source}），以当地接种门诊的安排为准。
      </p>

      {planning && (
        <PlanDialog
          plan={schedule.plan}
          onClose={() => setPlanning(false)}
          onSave={async (codes) => {
            try {
              onPlanned(await setVaccinePlan(baby.id, codes));
              setPlanning(false);
            } catch (caught) {
              family.toast({ text: familyError(caught), tone: "error" });
            }
          }}
        />
      )}
    </div>
  );
}

function Tally({ value, label, tone }: { value: number; label: string; tone?: string }) {
  return (
    <div className={value > 0 ? tone : undefined}>
      <span>{label}</span>
      <strong>
        {value}
        <small>剂</small>
      </strong>
    </div>
  );
}

// Stamp marks a given dose the way a clinic stamps the paper booklet; a
// pressed one comes down onto the page.
function Stamp({ date, by, pressed }: { date?: string; by?: string; pressed?: boolean }) {
  return (
    <span className={`${styles.fcStamp} ${by ? styles.replaced : ""}`}>
      <motion.span
        className={styles.fcStampSeal}
        aria-hidden="true"
        initial={pressed ? { scale: 2.4, opacity: 0, rotate: -40 } : false}
        animate={{ scale: 1, opacity: 0.85, rotate: -12 }}
        transition={{ type: "spring", stiffness: 380, damping: 16, delay: pressed ? 0.25 : 0 }}
      >
        {by ? "替代" : "已种"}
      </motion.span>
      <small>{by ? `已由${by}完成` : date ? `${shortDate(date)}接种` : "已接种"}</small>
    </span>
  );
}

function DoseRow({ dose, pressed, onRecord }: { dose: FamilyScheduledDose; pressed: boolean; onRecord: () => void }) {
  const days = daysUntil(dose.dueDate);
  return (
    <li className={`${styles.fcDose} ${styles[dose.status] ?? ""}`}>
      <span className={styles.fcDoseName}>
        {dose.name}
        <small>
          第 {dose.dose} 剂{dose.programme ? "" : " · 自费"}
        </small>
      </span>
      {dose.status === "given" ? (
        <Stamp date={dose.givenOn} pressed={pressed} />
      ) : dose.status === "replaced" ? (
        <Stamp by={dose.replacedBy} pressed={pressed} />
      ) : (
        <>
          <span className={styles.fcDoseStatus}>
            {dose.status === "available"
              ? "可以接种"
              : dose.status === "soon"
                ? days === 0
                  ? "今天到月龄"
                  : `还有 ${days} 天`
                : "还没到月龄"}
          </span>
          <button
            type="button"
            className={styles.fcDoseRecord}
            onClick={onRecord}
            aria-label={`记录${dose.name}第 ${dose.dose} 剂`}
          >
            记录
          </button>
        </>
      )}
    </li>
  );
}

// PlanDialog picks the non-programme vaccines the family plans and, where
// the region chooses between two programme schedules, which one.
function PlanDialog({
  plan,
  onClose,
  onSave,
}: {
  plan: string[];
  onClose: () => void;
  onSave: (codes: string[]) => Promise<void>;
}) {
  const { catalog } = useFamily();
  const vaccines = catalog.vaccines.vaccines;
  const [chosen, setChosen] = useState(new Set(plan));
  const [busy, setBusy] = useState(false);
  const groups = [...new Set(vaccines.filter((vaccine) => vaccine.group).map((vaccine) => vaccine.group!))];
  const toggle = (code: string, on: boolean) =>
    setChosen((current) => {
      const next = new Set(current);
      if (on) next.add(code);
      else next.delete(code);
      return next;
    });
  return (
    <Modal
      title="自费疫苗计划"
      busy={busy}
      wide
      onClose={onClose}
      footer={
        <>
          <span className={styles.fcGrow} />
          <button type="button" className={styles.fcSecondary} onClick={onClose} disabled={busy}>
            取消
          </button>
          <button
            type="button"
            className={styles.fcPrimary}
            disabled={busy}
            onClick={async () => {
              setBusy(true);
              await onSave([...chosen]);
              setBusy(false);
            }}
          >
            保存
          </button>
        </>
      }
    >
      <p className={styles.fcNote}>
        选中的疫苗会按常见程序排进接种本并提醒。不同产品程序不同，以说明书和接种门诊为准。
      </p>
      {groups.map((group) => {
        const options = vaccines.filter((vaccine) => vaccine.group === group);
        const current = options.find((vaccine) => chosen.has(vaccine.code))?.code ?? options[0].code;
        return (
          <fieldset key={group} className={styles.fcPlanGroup}>
            <legend>当地使用的程序</legend>
            {options.map((vaccine) => (
              <label key={vaccine.code} className={styles.fcPlanOption}>
                <input
                  type="radio"
                  name={`fc-group-${group}`}
                  checked={current === vaccine.code}
                  onChange={() =>
                    setChosen((value) => {
                      const next = new Set(value);
                      for (const option of options) next.delete(option.code);
                      if (vaccine.code !== options[0].code) next.add(vaccine.code);
                      return next;
                    })
                  }
                />
                <span>
                  {vaccine.name}（{vaccine.doses.length} 剂）
                  {vaccine.note && <small>{vaccine.note}</small>}
                </span>
              </label>
            ))}
          </fieldset>
        );
      })}
      <fieldset className={styles.fcPlanGroup}>
        <legend>非免疫规划疫苗</legend>
        {vaccines
          .filter((vaccine) => !vaccine.programme && vaccine.doses.length > 0)
          .map((vaccine) => (
            <label key={vaccine.code} className={styles.fcPlanOption}>
              <input
                type="checkbox"
                checked={chosen.has(vaccine.code)}
                onChange={(event) => toggle(vaccine.code, event.target.checked)}
              />
              <span>
                {vaccine.name}
                {vaccine.note && <small>{vaccine.note}</small>}
              </span>
            </label>
          ))}
      </fieldset>
    </Modal>
  );
}
