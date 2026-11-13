// The serif of the window's numbers, bundled: the NAS may have no way out.
import "@fontsource-variable/fraunces/opsz.css";

import { ChevronLeft, Images, Star, Users } from "lucide-react";
import { useCallback, useEffect, useState } from "react";

import { EmptyState, Spinner, useToasts } from "../ui/workspace";
import workspaceStyles from "../ui/workspace.module.css";
import { BatchesPage, RatersPage } from "./Admin";
import { claimThumbnailURL, listBatches, listRecent, type ScoringRaterBatch, scoringError } from "./api";
import { GuideContent } from "./Guide";
import { formatCount, formatScore } from "./model";
import { Stage } from "./Stage";
import styles from "./scoring.module.css";
import { type ScoringAccess, scoringAccess } from "./status";

type Page = "rate" | "batches" | "raters";

// A rater sees a batch's guide before scoring it the first time on this
// browser; afterwards it opens straight on the photos.
const seenKey = (userId: string, batchId: string) => `a-nas.scoring.guide.${userId}.${batchId}`;
function guideSeen(userId: string, batchId: string): boolean {
  try {
    return window.localStorage.getItem(seenKey(userId, batchId)) === "1";
  } catch {
    return false;
  }
}
function markGuideSeen(userId: string, batchId: string) {
  try {
    window.localStorage.setItem(seenKey(userId, batchId), "1");
  } catch {
    // Without storage the guide shows again next time.
  }
}

// ScoringPanel is 照片打分 (docs/specs/photo-scoring.md). Raters pick an open
// batch and score its photos; administrators also manage batches and choose
// the raters. Only the module's interface is here: who may do what is
// decided by the module.
export function ScoringPanel({ userId, access: initial }: { userId: string; access: ScoringAccess }) {
  // The window reads what the account may do again when it opens and after
  // an administrator changes the raters, so choosing oneself takes effect
  // without signing in again.
  const [access, setAccess] = useState(initial);
  const [page, setPage] = useState<Page>(initial.rater ? "rate" : "batches");
  const [scoring, setScoring] = useState<{ batch: ScoringRaterBatch; guide: boolean }>();
  const { toast, element: toasts } = useToasts(styles.scToasts);
  const refused = useCallback((text: string) => toast(text, "error"), [toast]);
  const readAccess = useCallback(() => {
    scoringAccess().then(setAccess, () => undefined);
  }, []);
  useEffect(() => {
    let live = true;
    scoringAccess().then(
      (next) => live && setAccess(next),
      () => undefined,
    );
    return () => {
      live = false;
    };
  }, []);

  if (scoring?.guide) {
    return (
      <div className={`${workspaceStyles.bkApp} ${styles.scApp}`}>
        <Calibration
          batch={scoring.batch}
          onBack={() => setScoring(undefined)}
          onStart={() => {
            markGuideSeen(userId, scoring.batch.id);
            setScoring({ batch: scoring.batch, guide: false });
          }}
        />
      </div>
    );
  }
  if (scoring) {
    return (
      <div className={`${workspaceStyles.bkApp} ${styles.scApp}`}>
        <Stage batch={scoring.batch} onBack={() => setScoring(undefined)} onRefused={refused} />
        {toasts}
      </div>
    );
  }

  const open = (batch: ScoringRaterBatch) => setScoring({ batch, guide: !guideSeen(userId, batch.id) });
  return (
    <div className={`${workspaceStyles.bkApp} ${styles.scApp}`}>
      {access.admin && (
        <nav className={styles.scSide} aria-label="照片打分">
          {access.rater && (
            <button type="button" aria-current={page === "rate" ? "page" : undefined} onClick={() => setPage("rate")}>
              <Star />
              打分
            </button>
          )}
          <button
            type="button"
            aria-current={page === "batches" ? "page" : undefined}
            onClick={() => setPage("batches")}
          >
            <Images />
            批次
          </button>
          <button type="button" aria-current={page === "raters" ? "page" : undefined} onClick={() => setPage("raters")}>
            <Users />
            打分人
          </button>
        </nav>
      )}
      <main className={styles.scMain}>
        {page === "rate" && <RaterHome onOpen={open} />}
        {page === "batches" && access.admin && <BatchesPage toast={toast} />}
        {page === "raters" && access.admin && <RatersPage toast={toast} onChanged={readAccess} />}
      </main>
      {toasts}
    </div>
  );
}

// RaterHome lists the open batches with the rater's own count in each. A
// batch's cover is the rater's own latest photos in it, with their scores:
// nothing of anyone else's.
function RaterHome({ onOpen }: { onOpen: (batch: ScoringRaterBatch) => void }) {
  const [batches, setBatches] = useState<ScoringRaterBatch[]>();
  const [failure, setFailure] = useState("");
  useEffect(() => {
    let live = true;
    listBatches()
      .then((items) => live && setBatches(items))
      .catch((caught: unknown) => live && setFailure(scoringError(caught)));
    return () => {
      live = false;
    };
  }, []);
  if (failure) {
    return (
      <EmptyState icon={<Star />} title="打不开要打分的批次">
        {failure}
      </EmptyState>
    );
  }
  if (!batches) {
    return (
      <div className={styles.scCenter}>
        <Spinner />
      </div>
    );
  }
  return (
    <div className={styles.scPage}>
      <header className={styles.scPageHead}>
        <h2>要打分的批次</h2>
      </header>
      {batches.length === 0 ? (
        <EmptyState icon={<Star />} title="现在没有要打分的批次">
          管理员开放批次后会显示在这里。
        </EmptyState>
      ) : (
        <ul className={styles.scCards}>
          {batches.map((batch) => (
            <li key={batch.id} className={styles.scCard}>
              <Cover batch={batch} />
              <div className={styles.scCardBody}>
                <div>
                  <strong>{batch.name}</strong>
                  <span>已打 {formatCount(batch.rated)} 张</span>
                </div>
                {batch.finished ? (
                  <span className={styles.scDone}>已打完</span>
                ) : (
                  <button type="button" className={styles.scPrimary} onClick={() => onOpen(batch)}>
                    {batch.rated > 0 ? "继续打分" : "开始打分"}
                  </button>
                )}
              </div>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

// coverShown is how many of the rater's latest photos a batch's cover holds.
const coverShown = 3;

function Cover({ batch }: { batch: ScoringRaterBatch }) {
  const [recent, setRecent] = useState<{ token: string; score: number }[]>();
  useEffect(() => {
    if (batch.rated === 0) return;
    let live = true;
    listRecent(batch.id).then(
      (items) => live && setRecent(items.slice(-coverShown).reverse()),
      () => live && setRecent([]),
    );
    return () => {
      live = false;
    };
  }, [batch.id, batch.rated]);
  if (!recent?.length) {
    return (
      <div className={styles.scCoverEmpty}>
        <Star aria-hidden="true" />
        {batch.rated === 0 ? "还没开始" : ""}
      </div>
    );
  }
  return (
    <div className={styles.scCover}>
      {recent.map((item) => {
        const score = formatScore(Math.round(item.score * 100));
        return (
          <figure key={item.token}>
            <img src={claimThumbnailURL(item.token)} alt={`你打了 ${score} 分的照片`} loading="lazy" />
            <figcaption>{score}</figcaption>
          </figure>
        );
      })}
    </div>
  );
}

// Calibration is the guide a rater reads before the first photo of a batch.
function Calibration({
  batch,
  onBack,
  onStart,
}: {
  batch: ScoringRaterBatch;
  onBack: () => void;
  onStart: () => void;
}) {
  return (
    <div className={styles.scCalibration}>
      <header className={styles.scPageHead}>
        <button type="button" className={styles.scBack} onClick={onBack}>
          <ChevronLeft />
          批次
        </button>
        <h2>{batch.name}</h2>
      </header>
      <GuideContent batch={batch} />
      <footer className={styles.scCalibrationFoot}>
        <button type="button" className={styles.scPrimary} onClick={onStart}>
          开始打分
        </button>
      </footer>
    </div>
  );
}
