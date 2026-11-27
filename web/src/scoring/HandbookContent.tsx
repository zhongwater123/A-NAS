import { useState } from "react";

import type { ScoringRaterBatch } from "./api";
import {
  type DimensionGuide,
  dimensionGuides,
  type Example,
  preparation,
  rules,
  skipping,
  specialCases,
  steps,
} from "./handbook";
import styles from "./scoring.module.css";

// Try asks the rater's level for an example photo before showing the
// handbook's, like a first round that counts for nothing. The levels are the
// batch's: 1, 3 and 5 on three levels, else 1 to 5. The answer takes the
// place of the button that reveals it, inside a live region that is there
// from the start, so that a screen reader reads it out.
function Try({ example, dimension, levels }: { example: Example; dimension: string; levels: readonly number[] }) {
  const [picked, setPicked] = useState<number>();
  const [shown, setShown] = useState(false);
  const answered = picked !== undefined || shown;
  const off = picked === undefined ? undefined : Math.abs(picked - example.level);
  return (
    <figure className={styles.scTry}>
      <img src={example.image} alt={example.alt} loading="lazy" />
      <figcaption>
        <div className={styles.scTryLevels} role="group" aria-label={`这张照片的${dimension}你打几分`}>
          {levels.map((level) => (
            <button
              key={level}
              type="button"
              aria-pressed={picked === level}
              className={[picked === level && styles.on, answered && level === example.level && styles.answer]
                .filter(Boolean)
                .join(" ")}
              onClick={() => setPicked(level)}
            >
              {level}
            </button>
          ))}
        </div>
        <div aria-live="polite">
          {answered ? (
            <p className={styles.scTryAnswer}>
              <b>手册 {example.level} 分</b>
              {example.why}
              {off !== undefined && <span>{off === 0 ? "和你一样" : `差 ${off} 分`}</span>}
            </p>
          ) : (
            <button type="button" className={styles.scTryReveal} onClick={() => setShown(true)}>
              先试着打一个分，或直接看答案
            </button>
          )}
        </div>
      </figcaption>
    </figure>
  );
}

// Dimension is one 打分项 as the handbook explains it, under the batch's name
// for it.
function Dimension({ guide, name, levels }: { guide: DimensionGuide; name: string; levels: readonly number[] }) {
  return (
    <section className={styles.scTutorialItem}>
      <h3>
        {name}
        {guide.note && <small>{guide.note}</small>}
      </h3>
      <p className={styles.scTutorialAsk}>{guide.question}</p>
      <p>{guide.about}</p>
      <p className={styles.scMuted}>
        <b>不看：</b>
        {guide.ignore}
      </p>
      <ul className={styles.scCues} aria-label="看这些">
        {guide.cues.map((cue) => (
          <li key={cue}>{cue}</li>
        ))}
      </ul>
      <ol className={styles.scLevelList} reversed>
        {guide.levels.map((text, index) => (
          <li key={text}>
            <b>{5 - index}</b>
            {text}
          </li>
        ))}
      </ol>
      <div className={styles.scTries}>
        {guide.examples.map((example) => (
          <Try key={example.image} example={example} dimension={name} levels={levels} />
        ))}
      </div>
      <div className={styles.scPitfalls}>
        <b>{guide.pitfallsTitle}</b>
        <ul>
          {guide.pitfalls.map((text) => (
            <li key={text}>{text}</li>
          ))}
        </ul>
      </div>
    </section>
  );
}

// HandbookContent is the scoring handbook as the scoring guide's sheet
// shows it, under the batch's own guide: the general rules, then each of the
// batch's 打分项 with photos to try.
export function HandbookContent({ batch }: { batch: Pick<ScoringRaterBatch, "dimensions" | "scale"> }) {
  const ids = batch.dimensions.map((dimension) => dimension.id);
  const three = batch.scale === "three";
  const levels = three ? [1, 3, 5] : [1, 2, 3, 4, 5];
  const shown = rules.filter((rule) => !rule.fiveOnly || batch.scale === "five");
  const explained = batch.dimensions.flatMap((dimension) => {
    const guide = dimensionGuides.find((candidate) => candidate.id === dimension.id);
    return guide ? [{ guide, name: dimension.name }] : [];
  });
  const others = batch.dimensions.filter((dimension) => !dimensionGuides.some((guide) => guide.id === dimension.id));
  return (
    <div className={`${styles.scGuide} ${styles.scTutorial}`}>
      <section>
        <h3>准备工作</h3>
        <ul className={styles.scKeys}>
          {preparation.map((text) => (
            <li key={text}>{text}</li>
          ))}
        </ul>
      </section>
      <section>
        <h3>每张照片</h3>
        <ol className={styles.scTutorialSteps}>
          {steps(ids).map((text) => (
            <li key={text}>{text}</li>
          ))}
        </ol>
      </section>
      <section>
        <h3>总原则</h3>
        <dl className={styles.scRules}>
          {shown.map((rule) => (
            <div key={rule.title}>
              <dt>{rule.title}</dt>
              <dd>{(three && rule.three) || rule.text}</dd>
            </div>
          ))}
        </dl>
      </section>
      {explained.map(({ guide, name }) => (
        <Dimension key={guide.id} guide={guide} name={name} levels={levels} />
      ))}
      {others.length > 0 && (
        <section>
          <h3>其他打分项</h3>
          <ul className={styles.scGuideItems}>
            {others.map((dimension) => (
              <li key={dimension.id}>
                <span />
                <b>{dimension.name}</b>
                <span>{dimension.description || "按批次的打分标准打"}</span>
              </li>
            ))}
          </ul>
        </section>
      )}
      <section>
        <h3>遇到这些怎么办</h3>
        <dl className={styles.scRules}>
          {specialCases.map(([situation, action]) => (
            <div key={situation}>
              <dt>{situation}</dt>
              <dd>{action}</dd>
            </div>
          ))}
        </dl>
        <p className={styles.scMuted}>{skipping}</p>
      </section>
    </div>
  );
}
