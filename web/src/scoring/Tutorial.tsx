import { ChevronLeft } from "lucide-react";
import { useState } from "react";

import type { ScoringRaterBatch } from "./api";
import {
  type DimensionGuide,
  dimensionGuides,
  type Example,
  order,
  preparation,
  rules,
  skipping,
  specialCases,
} from "./handbook";
import styles from "./scoring.module.css";

// Try asks the rater's level for an example photo before showing the
// handbook's, like a first round that counts for nothing.
function Try({ example, dimension }: { example: Example; dimension: string }) {
  const [picked, setPicked] = useState<number>();
  const [shown, setShown] = useState(false);
  const answered = picked !== undefined || shown;
  const off = picked === undefined ? undefined : Math.abs(picked - example.level);
  return (
    <figure className={styles.scTry}>
      <img src={example.image} alt={example.alt} loading="lazy" />
      <figcaption>
        <div className={styles.scTryLevels} role="group" aria-label={`这张照片的${dimension}你打几分`}>
          {[1, 2, 3, 4, 5].map((level) => (
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
        {answered ? (
          <p className={styles.scTryAnswer} aria-live="polite">
            <b>手册 {example.level} 分</b>
            {example.why}
            {off !== undefined && <span>{off === 0 ? "和你一样" : off === 1 ? "差 1 分" : `差 ${off} 分`}</span>}
          </p>
        ) : (
          <button type="button" className={styles.scTryReveal} onClick={() => setShown(true)}>
            先试着打一个分，或直接看答案
          </button>
        )}
      </figcaption>
    </figure>
  );
}

// Dimension is one 打分项 as the handbook explains it, under the batch's name
// for it.
function Dimension({ guide, name }: { guide: DimensionGuide; name: string }) {
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
          <Try key={example.image} example={example} dimension={name} />
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

// Tutorial is the scoring handbook, read through before every visit to a
// batch like terms to accept: the general rules, then each of the batch's
// 打分项 with photos to try. 完成 starts scoring at any point, so it also
// skips the rest.
export function Tutorial({
  batch,
  onBack,
  onDone,
}: {
  batch: Pick<ScoringRaterBatch, "name" | "dimensions" | "scale">;
  onBack: () => void;
  onDone: () => void;
}) {
  const ids = new Set(batch.dimensions.map((dimension) => dimension.id));
  const shown = rules.filter(
    (rule) => (!rule.wholeOnly || batch.scale === "five") && (!rule.dimension || ids.has(rule.dimension)),
  );
  const explained = batch.dimensions.flatMap((dimension) => {
    const guide = dimensionGuides.find((candidate) => candidate.id === dimension.id);
    return guide ? [{ guide, name: dimension.name }] : [];
  });
  const others = batch.dimensions.filter((dimension) => !dimensionGuides.some((guide) => guide.id === dimension.id));
  return (
    <div className={styles.scCalibration}>
      <header className={styles.scPageHead}>
        <button type="button" className={styles.scBack} onClick={onBack}>
          <ChevronLeft />
          批次
        </button>
        <h2>打分前看一遍</h2>
        <span className={styles.scMuted}>{batch.name}</span>
      </header>
      <div className={`${styles.scGuide} ${styles.scTutorial}`}>
        <p className={styles.scTutorialLead}>
          这是风景打分手册的要点。往下翻着看一遍，示例照片可以先试着打分，再看手册给的分；试打不计分。随时点“完成”开始打分。
        </p>
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
            {order.map((text) => (
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
                <dd>{rule.text}</dd>
              </div>
            ))}
          </dl>
        </section>
        {explained.map(({ guide, name }) => (
          <Dimension key={guide.id} guide={guide} name={name} />
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
        <p className={styles.scMuted}>示例照片取自 EVA 公开数据集，只用来示意 1、3、5 分大概的样子。</p>
      </div>
      <footer className={styles.scCalibrationFoot}>
        <button type="button" className={styles.scPrimary} onClick={onDone}>
          完成
        </button>
      </footer>
    </div>
  );
}
