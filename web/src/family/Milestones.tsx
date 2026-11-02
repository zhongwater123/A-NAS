import { Flag, ImagePlus, Plus } from "lucide-react";
import { useEffect, useState } from "react";

import { type FamilyBaby, type FamilyTimelineEntry, familyError, listMilestones } from "./api";
import type { Draft } from "./Composer";
import { useFamily } from "./context";
import styles from "./family.module.css";
import { ageText, daysUntil, longDate, sealText } from "./format";
import { Mosaic } from "./parts";

// Milestones is 大事记: the days every baby has (birth, 满月, 百天, each
// birthday) among the family's own milestones, oldest first, each on a seal,
// ending with the next one to come.
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
    <div className={styles.fcMilestones}>
      <header className={styles.fcBoardHead}>
        <div>
          <h2>大事记</h2>
        </div>
        <button type="button" className={styles.fcSecondary} onClick={() => onAdd({ kind: "milestone" })}>
          <Plus size={14} /> 添加大事记
        </button>
      </header>
      <ol className={styles.fcSeals}>
        {entries.map((entry) => {
          const mine = entry.record && entry.record.authorId === family.userId;
          const photos = entry.record?.photos.filter((photo) => photo.role === "album") ?? [];
          return (
            <li
              key={`${entry.date}-${entry.auto ?? entry.record?.id}`}
              className={`${styles.fcSealEntry} ${entry.upcoming ? styles.upcoming : ""}`}
            >
              <span className={`${styles.fcSeal} ${entry.auto ? "" : styles.own}`} aria-hidden="true">
                {entry.auto ? sealText(entry.title) : <Flag size={16} strokeWidth={1.6} />}
              </span>
              <div className={styles.fcSealBody}>
                <small>
                  {longDate(entry.date)}
                  {entry.upcoming
                    ? ` · 还有 ${daysUntil(entry.date)} 天`
                    : entry.auto === "birth"
                      ? ""
                      : ` · ${ageText(entry.age, false)}`}
                </small>
                <h3>{entry.title}</h3>
                {entry.record?.text && <p>{entry.record.text}</p>}
                <Mosaic photos={photos} scope={`milestone-${entry.record?.id ?? entry.auto}`} />
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
