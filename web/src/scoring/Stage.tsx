import { ChevronLeft } from "lucide-react";
import { type KeyboardEvent, useEffect, useEffectEvent, useLayoutEffect, useRef, useState } from "react";

import { requestKey } from "../api/requestKey";
import { Spinner } from "../ui/workspace";
import {
  claimImageURL,
  claimPhotos,
  listRecent,
  rate,
  retryable,
  type ScoringClaim,
  type ScoringRaterBatch,
  type ScoringRatingInput,
  scoringError,
} from "./api";
import { Guide } from "./Guide";
import {
  type Answer,
  clampScore,
  formatCount,
  formatScore,
  Outbox,
  type OutboxState,
  parseScore,
  stageAction,
} from "./model";
import { type Mark, Ruler } from "./Ruler";
import styles from "./scoring.module.css";
import { useSize } from "./useSize";

// Answers outlive the window: each batch's outbox keeps sending after the
// window closes, and a window opened again shows what is still on its way.
const outboxes = new Map<string, Outbox>();
let refusedListener: ((answer: Answer, caught: unknown) => void) | undefined;

function outboxFor(batchId: string): Outbox {
  let outbox = outboxes.get(batchId);
  if (!outbox) {
    outbox = new Outbox(
      async (answer) => (await rate(answer.token, answer.body)).rated,
      retryable,
      (answer, caught) => refusedListener?.(answer, caught),
    );
    outboxes.set(batchId, outbox);
  }
  return outbox;
}

// A photo that cannot be decoded within decodeLimit is shown as the browser
// manages, rather than holding up the rater.
const decodeLimit = 8_000;
// A rater who keeps scoring for breakAfter is asked to rest; a pause of
// pauseResets starts the count again.
const breakAfter = 40 * 60_000;
const pauseResets = 5 * 60_000;
// The ruler shows this many of the rater's latest scores.
const marksShown = 12;
const startScore = 250;

type Prepared = Map<string, Promise<void>>;

function prepare(prepared: Prepared, token: string): Promise<void> {
  let ready = prepared.get(token);
  if (!ready) {
    const image = new Image();
    image.src = claimImageURL(token);
    const decoded =
      typeof image.decode === "function"
        ? image.decode().catch(() => undefined)
        : new Promise<void>((resolve) => {
            image.onload = image.onerror = () => resolve();
          });
    ready = Promise.race([decoded, new Promise<void>((resolve) => window.setTimeout(resolve, decodeLimit))]);
    prepared.set(token, ready);
  }
  return ready;
}

function sizeOf(element: Element | null) {
  if (!element) return undefined;
  const { width, height } = element.getBoundingClientRect();
  return { width: Math.round(width), height: Math.round(height) };
}

// answerOf is what the page sends for an answer given now: the kind and
// score in points, how long the photo was shown, and the sizes it was seen at.
function answerOf(
  kind: ScoringRatingInput["kind"],
  hundredths: number | undefined,
  shownAt: number,
  stage: Element | null,
  image: Element | null,
): { body: ScoringRatingInput; now: number } {
  const now = performance.now();
  const body: ScoringRatingInput = {
    submission: requestKey(),
    kind,
    viewMs: Math.max(0, Math.round(now - shownAt)),
    pixelRatio: Math.min(16, window.devicePixelRatio || 1),
  };
  if (kind === "score" && hundredths !== undefined) body.score = hundredths / 100;
  const viewport = sizeOf(stage);
  const display = sizeOf(image);
  if (viewport) body.viewport = viewport;
  if (display) body.display = display;
  return { body, now };
}

// The gap between the photo and the one set beside it, and the room left
// under each for its caption.
const compareGap = 56;
const captionRoom = 40;
// Hovering a thumbnail sets it beside the photo after compareDelay; moving
// from one thumbnail to the next swaps at once, and leaving them all takes
// compareLinger, so a pointer crossing the gaps does not flicker.
const compareDelay = 110;
const compareLinger = 160;
const keysHint = "← → 0.1　⇧ 0.01　0–5 整数分";

// fitIn is the size a photo of width by height is shown at in a box: whole,
// as large as fits, never enlarged past its own size. With the box not
// measured yet (or in jsdom) the stylesheet alone keeps the photo inside.
function fitIn(width: number, height: number, boxWidth: number, boxHeight: number) {
  if (boxWidth <= 0 || boxHeight <= 0 || width <= 0 || height <= 0) return undefined;
  const scale = Math.min(boxWidth / width, boxHeight / height, 1);
  return { inlineSize: Math.round(width * scale), blockSize: Math.round(height * scale) };
}

const reducedMotion = () => window.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false;

// Stage shows a rater one photo at a time, whole and never cropped, on a
// neutral wall with nothing else of it: no name, no file details, no one
// else's scores. Below it the ruler sets a score from 0 to 5 in hundredths
// against the rater's own latest scores, any of which can be set beside the
// photo; Enter or 确定 answers, the photo glides down to its place on the
// ruler, and the next one, decoded beforehand, takes its place.
export function Stage({
  batch,
  onBack,
  onRefused,
}: {
  batch: ScoringRaterBatch;
  onBack: () => void;
  onRefused: (text: string) => void;
}) {
  const outbox = outboxFor(batch.id);
  const [claims, setClaims] = useState<ScoringClaim[]>([]);
  const [revising, setRevising] = useState<ScoringClaim>();
  const [last, setLast] = useState<{ claim: ScoringClaim; score?: number }>();
  const [loading, setLoading] = useState(true);
  const [finished, setFinished] = useState(false);
  const [failure, setFailure] = useState("");
  const [rated, setRated] = useState(batch.rated);
  const [visible, setVisible] = useState<string>();
  const [guide, setGuide] = useState(false);
  const [rest, setRest] = useState(false);
  const [value, setValue] = useState(startScore);
  const [typed, setTyped] = useState<string>();
  const [marks, setMarks] = useState<Mark[]>([]);
  const [comparing, setComparing] = useState<Mark>();
  const [arriving, setArriving] = useState<string>();
  const [delivery, setDelivery] = useState<OutboxState>(outbox.current());
  const root = useRef<HTMLDivElement>(null);
  const image = useRef<HTMLImageElement>(null);
  const ruler = useRef<HTMLDivElement>(null);
  const wall = useRef<HTMLDivElement>(null);
  const wallSize = useSize(wall);
  const compareTimer = useRef<number>(undefined);
  const flight = useRef<{ token: string; from: DOMRect; src: string }>(undefined);
  const resume = useRef<HTMLButtonElement>(null);
  const prepared = useRef<Prepared>(new Map());
  const answered = useRef(new Set<string>());
  const shownAt = useRef(0);
  const scoringSince = useRef(0);
  const lastAnswerAt = useRef(0);
  const reading = useRef(0);

  useEffect(() => outbox.subscribe(setDelivery), [outbox]);
  useEffect(() => {
    refusedListener = (_answer, caught) => onRefused(scoringError(caught));
    return () => {
      refusedListener = undefined;
    };
  }, [onRefused]);

  // refill asks for the photos held, topped up to three. Photos answered
  // here whose answers are still on their way are held at the server too and
  // stay out of the queue.
  const refill = async () => {
    const read = ++reading.current;
    try {
      const held = await claimPhotos(batch.id);
      if (read !== reading.current) return;
      const waiting = outbox.pending();
      const queue = held.items.filter((claim) => !answered.current.has(claim.token) && !waiting.has(claim.token));
      setClaims(queue);
      // Photos answered here but not delivered yet are still held at the
      // server: the batch is finished only when the server says so.
      setFinished(held.finished && queue.length === 0);
      setFailure("");
    } catch (caught) {
      if (read === reading.current) setFailure(scoringError(caught));
    } finally {
      if (read === reading.current) setLoading(false);
    }
  };

  // With every held photo answered and the answers delivered, ask again.
  const askAgain = useEffectEvent(() => {
    void refill();
  });
  const queueEmpty = claims.length === 0;
  useEffect(() => {
    if (!loading && queueEmpty && !finished && !revising && delivery.waiting === 0) askAgain();
  }, [loading, queueEmpty, finished, revising, delivery.waiting]);

  const start = useEffectEvent(() => {
    void refill();
    root.current?.focus({ preventScroll: true });
  });
  useEffect(() => start(), []);
  // The rater's own latest scores, from before this window too.
  useEffect(() => {
    let live = true;
    listRecent(batch.id).then(
      (items) => live && setMarks(items.map((item) => ({ ...item, score: Math.round(item.score * 100) }))),
      () => undefined,
    );
    return () => {
      live = false;
    };
  }, [batch.id]);

  // The photos in the queue are decoded ahead of their turn.
  useEffect(() => {
    for (const claim of claims) void prepare(prepared.current, claim.token);
  }, [claims]);

  const shown = revising ?? claims[0];
  const shownToken = shown?.token;
  useEffect(() => {
    if (!shownToken) return;
    let live = true;
    void prepare(prepared.current, shownToken).then(() => {
      if (!live) return;
      setVisible(shownToken);
      shownAt.current = performance.now();
    });
    return () => {
      live = false;
    };
  }, [shownToken]);

  useEffect(() => {
    if (rest) resume.current?.focus();
  }, [rest]);
  useEffect(() => () => window.clearTimeout(compareTimer.current), []);

  // The photo just scored glides from the wall to its thumbnail, which the
  // ruler has placed by now; the thumbnail shows when it lands. A rater who
  // asked the system for less motion sees the thumbnail at once.
  useLayoutEffect(() => {
    const trip = flight.current;
    if (!trip) return;
    flight.current = undefined;
    const landed = () => setArriving((current) => (current === trip.token ? undefined : current));
    const target = [...(ruler.current?.querySelectorAll<HTMLElement>("[data-token]") ?? [])]
      .find((element) => element.dataset.token === trip.token)
      ?.querySelector("img");
    const box = root.current?.getBoundingClientRect();
    const to = target?.getBoundingClientRect();
    if (!root.current || !box || !to || to.width === 0 || typeof HTMLElement.prototype.animate !== "function") {
      window.requestAnimationFrame(landed);
      return;
    }
    const copy = document.createElement("img");
    copy.src = trip.src;
    copy.alt = "";
    copy.className = styles.scFly;
    Object.assign(copy.style, {
      left: `${trip.from.left - box.left}px`,
      top: `${trip.from.top - box.top}px`,
      width: `${trip.from.width}px`,
      height: `${trip.from.height}px`,
    });
    root.current.appendChild(copy);
    const scale = to.width / trip.from.width;
    copy
      .animate(
        [
          { transform: "translate(0, 0) scale(1)" },
          { transform: `translate(${to.left - trip.from.left}px, ${to.top - trip.from.top}px) scale(${scale})` },
        ],
        { duration: 620, easing: "cubic-bezier(0.55, 0, 0.1, 1)" },
      )
      .finished.catch(() => undefined)
      .finally(() => {
        copy.remove();
        landed();
      });
  }, [marks]);

  const compare = (mark: Mark | undefined) => {
    window.clearTimeout(compareTimer.current);
    compareTimer.current = window.setTimeout(
      () => setComparing(mark),
      mark ? (comparing ? 0 : compareDelay) : compareLinger,
    );
  };

  // Leaving the page with answers still on their way asks first.
  useEffect(() => {
    if (delivery.waiting === 0) return;
    const warn = (event: BeforeUnloadEvent) => event.preventDefault();
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [delivery.waiting]);

  const ready = Boolean(shown) && visible === shown?.token && !guide && !rest;

  const answer = (kind: ScoringRatingInput["kind"]) => {
    if (!shown || !ready) return;
    let score: number | undefined;
    if (kind === "score") {
      score = typed === undefined ? value : parseScore(typed);
      if (score === undefined) return;
    }
    const { body, now } = answerOf(kind, score, shownAt.current, root.current, image.current);
    outbox.push({ token: shown.token, body });
    answered.current.add(shown.token);
    window.clearTimeout(compareTimer.current);
    setComparing(undefined);
    if (score !== undefined) {
      const from = image.current?.getBoundingClientRect();
      if (from && from.width > 0 && image.current && !reducedMotion()) {
        flight.current = { token: shown.token, from, src: image.current.currentSrc || image.current.src };
        setArriving(shown.token);
      }
      setValue(score);
    }
    setTyped(undefined);
    setMarks((current) => {
      const others = current.filter((mark) => mark.token !== shown.token);
      if (score === undefined) return others;
      return [...others, { token: shown.token, score, width: shown.width, height: shown.height }].slice(-marksShown);
    });
    if (revising) {
      setRevising(undefined);
    } else {
      setClaims((queue) => queue.filter((claim) => claim.token !== shown.token));
      setRated((count) => count + 1);
    }
    setLast({ claim: shown, score });
    if (lastAnswerAt.current === 0 || now - lastAnswerAt.current > pauseResets) scoringSince.current = now;
    lastAnswerAt.current = now;
    if (now - scoringSince.current >= breakAfter) setRest(true);
    void refill();
  };

  const undo = () => {
    if (!last || revising || guide || rest) return;
    setRevising(last.claim);
    if (last.score !== undefined) setValue(last.score);
    setTyped(undefined);
  };

  const act = (event: KeyboardEvent<HTMLDivElement>) => {
    const target = event.target as HTMLElement;
    if (target.closest("input, textarea, select")) return;
    const action = stageAction(event.nativeEvent);
    if (!action) return;
    event.preventDefault();
    event.stopPropagation();
    switch (action.kind) {
      case "set":
        setValue(clampScore(action.score));
        setTyped(undefined);
        break;
      case "nudge":
        setValue((current) => clampScore(current + action.by));
        setTyped(undefined);
        break;
      case "confirm":
        answer("score");
        break;
      case "skip":
      case "flag":
        answer(action.kind);
        break;
      case "undo":
        undo();
        break;
      case "guide":
        setGuide((open) => !open);
        break;
      case "cancel":
        if (guide) setGuide(false);
        else if (revising) setRevising(undefined);
        break;
    }
  };

  const typedValid = typed === undefined || parseScore(typed) !== undefined;
  const side = comparing && shown && visible === shown.token ? comparing : undefined;
  const boxWidth = side ? (wallSize.width - compareGap) / 2 : wallSize.width;
  const boxHeight = side ? wallSize.height - captionRoom : wallSize.height;
  let body;
  if (loading) {
    body = (
      <div className={styles.scStageMessage}>
        <Spinner />
      </div>
    );
  } else if (failure && !shown) {
    body = (
      <div className={styles.scStageMessage}>
        <strong>取不到要打分的照片</strong>
        <p>{failure}</p>
        <button type="button" className={styles.scPrimary} onClick={() => void refill()}>
          重试
        </button>
      </div>
    );
  } else if (!shown && finished) {
    body = (
      <div className={styles.scStageMessage}>
        <strong>这个批次你已经打完了</strong>
        <p>已打 {formatCount(rated)} 张。</p>
        <div className={styles.scStageMessageActions}>
          {last && (
            <button type="button" className={styles.scQuiet} onClick={undo}>
              改上一张的分数
            </button>
          )}
          <button type="button" className={styles.scPrimary} onClick={onBack}>
            返回批次
          </button>
        </div>
      </div>
    );
  } else if (shown && visible === shown.token) {
    body = (
      <>
        <figure className={styles.scFrame}>
          <img
            ref={image}
            key={shown.token}
            src={claimImageURL(shown.token)}
            alt="要打分的照片"
            draggable={false}
            className={styles.scPrint}
            style={fitIn(shown.width, shown.height, boxWidth, boxHeight)}
          />
          {side && <figcaption className={styles.scCaption}>这一张</figcaption>}
        </figure>
        {side && (
          <figure className={`${styles.scFrame} ${styles.scFrameRef}`}>
            <img
              key={side.token}
              src={claimImageURL(side.token)}
              alt={`你打过 ${formatScore(side.score)} 分的照片`}
              draggable={false}
              className={styles.scPrint}
              style={fitIn(side.width, side.height, boxWidth, boxHeight)}
            />
            <figcaption className={styles.scCaption}>
              你打过<b>{formatScore(side.score)}</b>
            </figcaption>
          </figure>
        )}
      </>
    );
  } else {
    body = <Spinner />;
  }

  return (
    // The stage takes the window's keys; it holds the focus while it is shown.
    // oxlint-disable-next-line jsx-a11y/no-noninteractive-element-interactions
    <div
      ref={root}
      className={styles.scStage}
      role="application"
      aria-label={`给“${batch.name}”打分`}
      tabIndex={-1}
      onKeyDown={act}
      data-vnas-private="照片打分只由成员本人打分"
    >
      <header className={styles.scTop}>
        <button type="button" className={styles.scStageBack} aria-label={`返回批次（${batch.name}）`} onClick={onBack}>
          <i>
            <ChevronLeft />
          </i>
          <span>{batch.name}</span>
        </button>
        <span className={styles.scGrow} />
        <span className={styles.scCount} aria-live="polite">
          已打 <b>{formatCount(rated)}</b> 张
        </span>
        <button type="button" className={styles.scTextLink} onClick={() => setGuide(true)}>
          打分标准
        </button>
        <button type="button" className={styles.scRound} aria-label="怎样打分" onClick={() => setGuide(true)}>
          ?
        </button>
      </header>
      {revising && (
        <div className={`${styles.scBanner} ${styles.scRevising}`} role="status">
          正在改上一张
          {last?.score !== undefined && (
            <>
              {" · 原来 "}
              <b>{formatScore(last.score)}</b>
            </>
          )}
          {" · Esc 取消"}
        </div>
      )}
      {delivery.waiting > 0 && delivery.retrying && (
        <div className={`${styles.scBanner} ${styles.scDelivery}`} role="status">
          有 {delivery.waiting} 个分数还没送达，连接恢复后会自动提交
        </div>
      )}
      <div ref={wall} className={`${styles.scWall}${side ? ` ${styles.comparing}` : ""}`}>
        {body}
      </div>
      <section className={styles.scInstrument} aria-label="打分">
        <Ruler
          value={value}
          marks={marks}
          latest={marks.at(-1)?.token}
          active={side?.token}
          arriving={arriving}
          onChange={(next) => {
            setValue(next);
            setTyped(undefined);
          }}
          onCompare={compare}
          rulerRef={ruler}
        />
        <div className={styles.scReading}>
          <div className={styles.scScoreLine}>
            <input
              className={styles.scScoreInput}
              value={typed ?? formatScore(value)}
              inputMode="decimal"
              aria-label="分数，0 到 5，最多两位小数"
              aria-invalid={!typedValid}
              onChange={(event) => {
                setTyped(event.target.value);
                const parsed = parseScore(event.target.value);
                if (parsed !== undefined) setValue(parsed);
              }}
              onBlur={() => typedValid && setTyped(undefined)}
              onKeyDown={(event) => {
                if (event.key === "Enter") {
                  event.preventDefault();
                  answer("score");
                } else if (event.key === "Escape") {
                  setTyped(undefined);
                  root.current?.focus({ preventScroll: true });
                }
              }}
            />
            <span className={styles.scOutOf}>/ 5</span>
          </div>
          <p className={`${styles.scSay}${typedValid ? "" : ` ${styles.error}`}`}>
            {typedValid ? keysHint : <span role="alert">0 到 5 之间，最多两位小数</span>}
          </p>
          <button
            type="button"
            className={styles.scConfirm}
            disabled={!ready || !typedValid}
            aria-keyshortcuts="Enter"
            onClick={() => answer("score")}
          >
            确定<kbd aria-hidden="true">↵</kbd>
          </button>
          <div className={styles.scActs}>
            <button type="button" className={styles.scAct} disabled={!ready} onClick={() => answer("skip")}>
              无法判断 <kbd>S</kbd>
            </button>
            <button type="button" className={styles.scAct} disabled={!ready} onClick={() => answer("flag")}>
              图片有问题 <kbd>X</kbd>
            </button>
            <button type="button" className={styles.scAct} disabled={!last || Boolean(revising)} onClick={undo}>
              撤销 <kbd>⌫</kbd>
            </button>
          </div>
        </div>
      </section>
      {guide && <Guide batch={batch} onClose={() => setGuide(false)} />}
      {rest && (
        <div className={styles.scOverlay} role="dialog" aria-modal="true" aria-label="休息一下">
          <div className={styles.scRest}>
            <strong>已连续打分 40 分钟</strong>
            <p>可以休息几分钟再继续。</p>
            <button
              ref={resume}
              type="button"
              className={styles.scPrimary}
              onClick={() => {
                scoringSince.current = performance.now();
                setRest(false);
                root.current?.focus({ preventScroll: true });
              }}
            >
              继续打分
            </button>
          </div>
        </div>
      )}
    </div>
  );
}
