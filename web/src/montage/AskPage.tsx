import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, ChevronLeft, Scissors } from "lucide-react";
import { useRef, useState } from "react";

import { errorText } from "../api/errorMessages";
import { Spinner } from "../ui/workspace";
import { type MontageProjectDetail, type ProjectChange, updateProject } from "./api";
import { Failed } from "./Failed";
import styles from "./montage.module.css";
import { projectKey, projectQuery } from "./queries";
import { limitsOf } from "./words";

const MAX_ASK = 200;
// The prototype's ideas, a click away.
const ideas = ["山海与冒险", "围绕一路上的人", "慢下来的旅行", "多放海边和日落，结尾安静一点"];

// AskPage is the prototype's 剪辑要求 page: the order of the shots, and one
// sentence to the planner. Both are optional.
export function AskPage({ id, onBack }: { id: string; onBack: () => void }) {
  const project = useQuery(projectQuery(id));
  if (project.data) return <Ask detail={project.data} onBack={onBack} />;
  return (
    <main className={styles.mtSub}>
      <button type="button" className={`${styles.mtBtn} ${styles.small} ${styles.mtBack}`} onClick={onBack}>
        <ChevronLeft aria-hidden="true" />
        返回
      </button>
      {project.error ? <Failed error={project.error} onRetry={() => void project.refetch()} /> : <Spinner />}
    </main>
  );
}

function Ask({ detail, onBack }: { detail: MontageProjectDetail; onBack: () => void }) {
  const client = useQueryClient();
  const [ask, setAsk] = useState(detail.ask);
  const save = useMutation({
    mutationFn: (change: ProjectChange) => updateProject(detail.id, change),
    onSuccess: (updated) => client.setQueryData(projectKey(detail.id), updated),
  });
  // The sentence is saved once per change: as the box loses the focus, when
  // an idea is clicked, and on the way out by either button. sent is the
  // text last handed to the server and saving that request, so leaving right
  // after the box lost the focus waits for the one request and sends no
  // second.
  const sent = useRef<string | null>(detail.ask);
  const saving = useRef<Promise<void>>(Promise.resolve());
  const keep = (text: string): Promise<void> => {
    if (text !== sent.current) {
      sent.current = text;
      saving.current = save.mutateAsync({ ask: text }).then(
        () => undefined,
        (error: unknown) => {
          // Not saved: the next try sends it again. The error shows below.
          sent.current = null;
          throw error;
        },
      );
      saving.current.catch(() => undefined);
    }
    return saving.current;
  };
  const leave = () => void keep(ask).then(onBack, () => undefined);
  const limits = limitsOf(ask);
  return (
    <main className={styles.mtSub}>
      <button type="button" className={`${styles.mtBtn} ${styles.small} ${styles.mtBack}`} onClick={leave}>
        <ChevronLeft aria-hidden="true" />
        返回
      </button>
      <section className={`${styles.mtPanel} ${styles.mtStep} ${styles.cut}`} aria-label="剪辑要求">
        <div className={styles.mtStepH}>
          <span className={styles.mtBadge} aria-hidden="true">
            <Scissors />
          </span>
          <div>
            <h2>剪辑要求</h2>
          </div>
          <small>可选</small>
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
                由 VNAS 安排
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
              placeholder="例如：山海与冒险，从海边一路升到高空"
              onChange={(event) => setAsk(event.target.value)}
              onBlur={() => void keep(ask)}
            />
            <div className={styles.mtIdeas}>
              {ideas.map((idea) => (
                <button
                  key={idea}
                  type="button"
                  onClick={() => {
                    setAsk(idea);
                    void keep(idea);
                  }}
                >
                  {idea}
                </button>
              ))}
            </div>
            <div className={styles.mtCount}>
              {ask.length} / {MAX_ASK}
            </div>
            {limits.length > 0 && (
              <p className={`${styles.mtNote} ${styles.mtHint}`} role="note">
                首版的限制：{limits.join("；")}
              </p>
            )}
          </div>
        </div>
        {save.error && <p className={`${styles.mtError} ${styles.mtHint}`}>{errorText(save.error, "montage")}</p>}
      </section>
      <div className={styles.mtFoot}>
        <button type="button" className={`${styles.mtBtn} ${styles.primary}`} onClick={leave}>
          <Check aria-hidden="true" />
          完成
        </button>
      </div>
    </main>
  );
}
