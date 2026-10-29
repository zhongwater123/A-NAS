import { Check, CircleDashed, Clock3, ListChecks, Plus, Syringe } from "lucide-react";
import { useEffect, useMemo, useState } from "react";

import { errorText } from "../api/errorMessages";
import {
  type FamilyBaby,
  type FamilyScheduledDose,
  type FamilyVaccineSchedule,
  getVaccines,
  setVaccinePlan,
} from "./api";
import type { Draft } from "./Composer";
import { useFamily } from "./context";
import styles from "./family.module.css";
import { daysUntil, shortDate } from "./format";
import { Modal } from "./parts";

// Vaccines is 疫苗接种: the national programme's doses and the planned
// non-programme ones, by recommended age, each with where it stands.
export function Vaccines({
  baby,
  version,
  onRecord,
  onChanged,
}: {
  baby: FamilyBaby;
  version: number;
  onRecord: (draft: Draft) => void;
  onChanged: () => void;
}) {
  const family = useFamily();
  const [schedule, setSchedule] = useState<FamilyVaccineSchedule>();
  const [failure, setFailure] = useState("");
  const [planning, setPlanning] = useState(false);

  useEffect(() => {
    let live = true;
    getVaccines(baby.id).then(
      (next) => {
        if (live) setSchedule(next);
      },
      (caught: unknown) => {
        if (live) setFailure(errorText(caught, "family"));
      },
    );
    return () => {
      live = false;
    };
  }, [baby.id, version]);

  const groups = useMemo(() => {
    const result: Array<{ due: string; label: string; doses: FamilyScheduledDose[] }> = [];
    for (const dose of schedule?.doses ?? []) {
      const last = result.at(-1);
      if (last && last.due === dose.dueDate) last.doses.push(dose);
      else result.push({ due: dose.dueDate, label: dose.ageLabel, doses: [dose] });
    }
    return result;
  }, [schedule]);

  if (failure) return <p className={styles.fcFailure}>{failure}</p>;
  if (!schedule) return <span className="loader" aria-label="正在读取疫苗程序" />;
  const count = (...statuses: FamilyScheduledDose["status"][]) =>
    schedule.doses.filter((dose) => statuses.includes(dose.status)).length;
  const scheduled = new Set(schedule.doses.map((dose) => dose.recordId).filter(Boolean));
  const others = schedule.records.filter((record) => !scheduled.has(record.id));
  return (
    <div className={styles.fcVaccines}>
      <div className={styles.fcVaccineSummary}>
        <Summary tone="done" value={count("given", "replaced")} label="已完成" />
        <Summary tone="due" value={count("available")} label="可以接种" />
        <Summary tone="soon" value={count("soon")} label="30 天内到期" />
        <Summary tone="later" value={count("upcoming")} label="未到时间" />
        <span className={styles.fcGrow} />
        <button type="button" className={styles.fcSecondary} onClick={() => setPlanning(true)}>
          <ListChecks size={14} /> 自费疫苗计划
        </button>
        <button type="button" className={styles.fcPrimarySmall} onClick={() => onRecord({ kind: "vaccine" })}>
          <Plus size={14} /> 记录接种
        </button>
      </div>
      <ol className={styles.fcSchedule}>
        {groups.map((group) => (
          <li key={group.due} className={styles.fcScheduleGroup}>
            <div className={styles.fcScheduleAge}>
              <strong>{group.label}</strong>
              <small>{shortDate(group.due)}</small>
            </div>
            <ul>
              {group.doses.map((dose) => (
                <DoseRow
                  key={`${dose.code}-${dose.dose}`}
                  dose={dose}
                  onRecord={() => onRecord({ kind: "vaccine", vaccine: { code: dose.code, dose: dose.dose } })}
                />
              ))}
            </ul>
          </li>
        ))}
      </ol>
      {others.length > 0 && (
        <section className={styles.fcSection}>
          <h3>其他接种记录</h3>
          <ul className={styles.fcOtherDoses}>
            {others.map((record) => (
              <li key={record.id}>
                <Syringe size={14} />
                {record.vaccine?.name ||
                  family.catalog.vaccines.vaccines.find((vaccine) => vaccine.code === record.vaccine?.code)?.name}{" "}
                第 {record.vaccine?.dose} 剂<small>{shortDate(record.occurredOn)}</small>
              </li>
            ))}
          </ul>
        </section>
      )}
      <p className={styles.fcSource}>
        依据：{schedule.title}。{schedule.source}
      </p>
      {planning && (
        <PlanDialog
          plan={schedule.plan}
          onClose={() => setPlanning(false)}
          onSave={async (codes) => {
            try {
              setSchedule(await setVaccinePlan(baby.id, codes));
              setPlanning(false);
              onChanged();
            } catch (caught) {
              family.toast({ text: errorText(caught, "family"), tone: "error" });
            }
          }}
        />
      )}
    </div>
  );
}

function Summary({ tone, value, label }: { tone: string; value: number; label: string }) {
  return (
    <div className={`${styles.fcSummary} ${styles[tone] ?? ""}`}>
      <strong>{value}</strong>
      <span>{label}</span>
    </div>
  );
}

function DoseRow({ dose, onRecord }: { dose: FamilyScheduledDose; onRecord: () => void }) {
  const done = dose.status === "given" || dose.status === "replaced";
  const days = daysUntil(dose.dueDate);
  const status = {
    given: `${dose.givenOn ? shortDate(dose.givenOn) : ""}已接种`,
    replaced: `已由${dose.replacedBy}完成`,
    available: "可以接种",
    soon: days === 0 ? "今天到月龄" : `${days} 天后`,
    upcoming: "未到时间",
  }[dose.status];
  return (
    <li className={`${styles.fcDose} ${styles[dose.status] ?? ""}`}>
      <span className={styles.fcDoseIcon} aria-hidden="true">
        {done ? <Check size={13} /> : dose.status === "upcoming" ? <CircleDashed size={13} /> : <Clock3 size={13} />}
      </span>
      <span className={styles.fcDoseName}>
        {dose.name}
        <small>
          第 {dose.dose} 剂{dose.programme ? "" : " · 自费"}
        </small>
      </span>
      <span className={styles.fcDoseStatus}>{status}</span>
      {!done && (
        <button
          type="button"
          className={styles.fcDoseRecord}
          onClick={onRecord}
          aria-label={`记录${dose.name}第 ${dose.dose} 剂`}
        >
          记录
        </button>
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
      icon={<ListChecks size={17} />}
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
      <p className={styles.fcFootnote}>
        选中的疫苗会按常见程序列进接种表并提醒。不同产品程序不同，以说明书和接种门诊为准。
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
