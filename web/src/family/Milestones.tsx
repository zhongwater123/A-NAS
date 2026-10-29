import { Cake, Flag, ImagePlus, Plus, Sparkles } from "lucide-react";
import { useEffect, useState } from "react";

import { type FamilyBaby, type FamilyTimelineEntry, familyError, listMilestones } from "./api";
import type { Draft } from "./Composer";
import { useFamily } from "./context";
import styles from "./family.module.css";
import { ageText, daysUntil, longDate } from "./format";
import { PhotoGrid } from "./parts";

// Milestones is 大事记: the days every baby has (birth, 满月, 百天, each
// birthday) among the family's own milestones, oldest first, ending with the
// next one to come.
export function Milestones({
  baby,
  version,
  onAdd,
}: {
  baby: FamilyBaby;
  version: number;
  onAdd: (draft: Draft) => void;
}) {
  const family = useFamily();
  const [entries, setEntries] = useState<FamilyTimelineEntry[]>();
  const [failure, setFailure] = useState("");

  useEffect(() => {
    let live = true;
    listMilestones(baby.id).then(
      (items) => {
        if (live) setEntries(items);
      },
      (caught: unknown) => {
        if (live) setFailure(familyError(caught));
      },
    );
    return () => {
      live = false;
    };
  }, [baby.id, version]);

  if (failure) return <p className={styles.fcFailure}>{failure}</p>;
  if (!entries) return <span className="loader" aria-label="正在读取大事记" />;
  return (
    <div className={styles.fcTimelineWrap}>
      <div className={styles.fcBoardBar}>
        <span>出生、满月、百天和生日会自动出现，可以给它们配上照片和说明。</span>
        <button type="button" className={styles.fcPrimarySmall} onClick={() => onAdd({ kind: "milestone" })}>
          <Plus size={14} /> 添加大事记
        </button>
      </div>
      <ol className={styles.fcTimeline}>
        {entries.map((entry) => {
          const key = `${entry.date}-${entry.auto ?? entry.record?.id}`;
          const mine = entry.record && entry.record.authorId === family.userId;
          return (
            <li
              key={key}
              className={`${styles.fcMoment} ${entry.auto ? styles.auto : ""} ${entry.upcoming ? styles.upcoming : ""}`}
            >
              <span className={styles.fcMomentDot} aria-hidden="true">
                {entry.auto?.startsWith("birthday") || entry.auto === "year-1" ? (
                  <Cake size={13} />
                ) : entry.auto ? (
                  <Sparkles size={13} />
                ) : (
                  <Flag size={13} />
                )}
              </span>
              <div className={styles.fcMomentBody}>
                <p className={styles.fcMomentDate}>
                  {longDate(entry.date)}
                  {entry.upcoming
                    ? ` · 还有 ${daysUntil(entry.date)} 天`
                    : entry.auto === "birth"
                      ? ""
                      : ` · ${ageText(entry.age)}`}
                </p>
                <h3>{entry.title}</h3>
                {entry.record?.text && <p className={styles.fcText}>{entry.record.text}</p>}
                {entry.record && <PhotoGrid photos={entry.record.photos} limit={6} />}
                {entry.auto && !entry.upcoming && !entry.record && (
                  <button
                    type="button"
                    className={styles.fcLink}
                    onClick={() =>
                      onAdd({
                        kind: "milestone",
                        milestone: { autoKey: entry.auto!, title: entry.title, date: entry.date },
                      })
                    }
                  >
                    <ImagePlus size={13} /> 配上照片和说明
                  </button>
                )}
                {entry.record && mine && (
                  <button
                    type="button"
                    className={styles.fcLink}
                    onClick={() => onAdd({ kind: "milestone", record: entry.record })}
                  >
                    编辑
                  </button>
                )}
              </div>
            </li>
          );
        })}
      </ol>
    </div>
  );
}
