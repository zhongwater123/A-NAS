import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, ChevronLeft, Scissors } from "lucide-react";
import { useState } from "react";

import { errorText } from "../api/errorMessages";
import { Spinner } from "../ui/workspace";
import { type MontageProjectDetail, updateProject } from "./api";
import styles from "./montage.module.css";
import { projectQuery } from "./queries";
import { limitsOf } from "./words";

const MAX_ASK = 200;
// The prototype's ideas, a click away.
const ideas = ["山海与冒险", "围绕一路上的人", "慢下来的旅行", "多放海边和日落，结尾安静一点"];

// AskPage is the prototype's 剪辑要求 page: the order of the shots, and one
// sentence to the planner. Both are optional.
export function AskPage({ id, onBack }: { id: string; onBack: () => void }) {
  const project = useQuery(projectQuery(id));
  return (
    <main className={styles.mtSub}>
      <button type="button" className={`${styles.mtBtn} ${styles.small} ${styles.mtBack}`} onClick={onBack}>
        <ChevronLeft aria-hidden="true" />
        返回项目
      </button>
      {project.error && <p className={styles.mtError}>{errorText(project.error)}</p>}
      {project.data ? <Ask detail={project.data} onBack={onBack} /> : !project.error && <Spinner />}
    </main>
  );
}

function Ask({ detail, onBack }: { detail: MontageProjectDetail; onBack: () => void }) {
  const client = useQueryClient();
  const [ask, setAsk] = useState(detail.ask);
  const save = useMutation({
    mutationFn: (change: { ask?: string; chronological?: boolean }) => updateProject(detail.id, change),
    onSuccess: (updated) => client.setQueryData(["montage", "project", detail.id], updated),
  });
  const limits = limitsOf(ask);
  const keep = () => {
    if (ask !== detail.ask) save.mutate({ ask });
  };
  return (
    <>
      <section className={`${styles.mtPanel} ${styles.mtStep} ${styles.cut}`} aria-label="剪辑要求">
        <div className={styles.mtStepH}>
          <span className={styles.mtBadge} aria-hidden="true">
            <Scissors />
          </span>
          <div>
            <div className={styles.mtStepN}>可选 · 不改就用默认</div>
            <h2>剪辑要求</h2>
          </div>
          <small>都可以不改</small>
        </div>
        <div className={styles.mtCutopts}>
          <div>
            <div className={styles.mtLabel}>剪辑顺序</div>
            <div className={styles.mtSeg} role="group" aria-label="剪辑顺序">
              <button
                type="button"
                aria-pressed={detail.chronological}
                onClick={() => save.mutate({ chronological: true })}
              >
                按拍摄时间
              </button>
              <button
                type="button"
                aria-pressed={!detail.chronological}
                onClick={() => save.mutate({ chronological: false })}
              >
                交给 VNAS 安排
              </button>
            </div>
          </div>
          <div>
            <div className={styles.mtLabel}>一句话要求</div>
            <textarea
              className={styles.mtText}
              aria-label="一句话要求"
              maxLength={MAX_ASK}
              value={ask}
              placeholder="比如：山海与冒险，从海边一路升到高空"
              onChange={(event) => setAsk(event.target.value)}
              onBlur={keep}
            />
            <div className={styles.mtIdeas}>
              {ideas.map((idea) => (
                <button key={idea} type="button" onClick={() => setAsk(idea)}>
                  {idea}
                </button>
              ))}
            </div>
            <div className={styles.mtCount}>
              {ask.length} / {MAX_ASK}
            </div>
            {limits.length > 0 && (
              <p className={`${styles.mtNote} ${styles.mtHint}`} role="note">
                首版的限制：{limits.join("；")}。可以照样开始。
              </p>
            )}
          </div>
        </div>
        {save.error && <p className={`${styles.mtError} ${styles.mtHint}`}>{errorText(save.error)}</p>}
      </section>
      <div className={styles.mtFoot}>
        <button
          type="button"
          className={`${styles.mtBtn} ${styles.primary}`}
          onClick={() => {
            if (ask === detail.ask) return onBack();
            save.mutate({ ask }, { onSuccess: onBack });
          }}
        >
          <Check aria-hidden="true" />
          好了，回去开始剪辑
        </button>
      </div>
    </>
  );
}
