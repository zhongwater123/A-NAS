import { X } from "lucide-react";
import { useEffect, useRef, useState } from "react";

import { Spinner } from "../ui/workspace";
import { anchorImageURL, listAnchors, type ScoringAnchor, type ScoringRaterBatch, scoringError } from "./api";
import styles from "./scoring.module.css";

// GuideContent is what raters see before scoring a batch and whenever they
// look again: the administrator's scoring guide, the batch's 打分项, how to
// score, and the reference photos, lowest score first, each with its score.
export function GuideContent({ batch }: { batch: Pick<ScoringRaterBatch, "id" | "rubric" | "dimensions"> }) {
  const several = batch.dimensions.length > 1;
  const [anchors, setAnchors] = useState<ScoringAnchor[]>();
  const [failure, setFailure] = useState("");
  useEffect(() => {
    let live = true;
    listAnchors(batch.id)
      .then((items) => live && setAnchors(items))
      .catch((caught: unknown) => live && setFailure(scoringError(caught)));
    return () => {
      live = false;
    };
  }, [batch.id]);
  return (
    <div className={styles.scGuide}>
      <section>
        <h3>打分标准</h3>
        {batch.rubric ? (
          <p className={styles.scRubric}>{batch.rubric}</p>
        ) : (
          <p className={styles.scMuted}>管理员没有写打分标准。</p>
        )}
      </section>
      {several && (
        <section>
          <h3>打分项</h3>
          <p className={styles.scRubric}>
            每张照片依次打 {batch.dimensions.length} 项：
            {batch.dimensions.map((dimension) => dimension.name).join("、")}。
          </p>
        </section>
      )}
      <section>
        <h3>怎样打分</h3>
        <ul className={styles.scKeys}>
          <li>在下方的数轴上拖动或点选 0 到 5 分，也可以直接输入，最多两位小数</li>
          <li>
            <kbd>←</kbd> <kbd>→</kbd> 每次 0.1，按住 <kbd>Shift</kbd> 每次 0.01，<kbd>0</kbd>–<kbd>5</kbd> 跳到整数分
          </li>
          {several ? (
            <>
              <li>
                在数轴上点选或拖动后松开，自动进入下一项，最后一项松开后进入下一张；用键盘时按 <kbd>Enter</kbd>
              </li>
              <li>
                <kbd>↑</kbd> <kbd>↓</kbd> 或点上方的项回到已打的项
              </li>
            </>
          ) : (
            <li>
              在数轴上点选或拖动后松开，或按 <kbd>Enter</kbd>，提交并进入下一张
            </li>
          )}
          <li>每张照片开始时没有分数；数轴上淡黄色的点是上一张的分数</li>
          <li>
            单击照片看原图 100%，移动鼠标换位置，右下角小窗显示看的是哪一块，再单击返回；按住 <kbd>Z</kbd> 也可以
          </li>
          <li>
            照片两侧的箭头或 <kbd>[</kbd> <kbd>]</kbd> 切换上一张、下一张；这一张打完才能到下一张，只能回到上一张
          </li>
          <li>数轴上方是你最近打过分的 12 张照片；鼠标停在一张上，它会并排放到正在打分的照片旁边</li>
          <li>
            <kbd>S</kbd> 无法判断，<kbd>X</kbd> 图片有问题，都不计分
          </li>
          <li>
            <kbd>⌫</kbd> 撤销一步：回到上一项，在第一项时回到上一张的最后一项
          </li>
          <li>
            <kbd>F</kbd> 全屏，<kbd>?</kbd> 再看打分标准
          </li>
        </ul>
      </section>
      <section>
        <h3>参考照片</h3>
        {failure && <p className={styles.scMuted}>{failure}</p>}
        {!anchors && !failure && <Spinner small />}
        {anchors?.length === 0 && <p className={styles.scMuted}>这个批次没有参考照片。</p>}
        {anchors && anchors.length > 0 && (
          <ul className={styles.scAnchors}>
            {anchors.map((anchor) => (
              <li key={anchor.id}>
                <img src={anchorImageURL(batch.id, anchor.id)} alt={`${anchor.score} 分的参考照片`} loading="lazy" />
                <span>{anchor.score.toFixed(2)} 分</span>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}

// Guide shows GuideContent over the stage; Escape or the button closes it.
export function Guide({ batch, onClose }: { batch: ScoringRaterBatch; onClose: () => void }) {
  const close = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    close.current?.focus();
  }, []);
  return (
    <div className={styles.scOverlay} role="dialog" aria-modal="true" aria-label="打分标准">
      <div className={styles.scGuideSheet}>
        <header>
          <h2>{batch.name}</h2>
          <button ref={close} type="button" className={styles.scIconButton} aria-label="关闭" onClick={onClose}>
            <X />
          </button>
        </header>
        <GuideContent batch={batch} />
      </div>
    </div>
  );
}
