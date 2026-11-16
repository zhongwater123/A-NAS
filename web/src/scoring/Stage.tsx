import { ChevronLeft } from "lucide-react";
import {
  type KeyboardEvent,
  type PointerEvent,
  useEffect,
  useEffectEvent,
  useLayoutEffect,
  useRef,
  useState,
} from "react";

import { requestKey } from "../api/requestKey";
import type { ScoringScores } from "../api.generated";
import { Spinner } from "../ui/workspace";
import {
  claimImageURL,
  claimPhotos,
  type FullView,
  fetchFull,
  listRecent,
  rate,
  retryable,
  type ScoringClaim,
  type ScoringRaterBatch,
  type ScoringRatingInput,
  scoringError,
} from "./api";
import { Guide } from "./Guide";
import { Loupe } from "./Loupe";
import {
  type Answer,
  clampScore,
  formatCount,
  formatScore,
  Outbox,
  type OutboxState,
  overall,
  parseScore,
  Stopwatch,
  sharpness,
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
// A value picked with the pointer moves on to the next 打分项 after this
// long, time to see where it landed; pressing again or a key keeps it.
const advanceAfter = 350;

// Scores of one photo in hundredths, by 打分项.
type Values = Record<string, number>;

// One of the rater's latest photos, with its score on every 打分项.
interface Scored {
  token: string;
  scores: Values;
  width: number;
  height: number;
}

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

// answerOf is what the page sends for an answer given now: the kind and, for
// a score, overall and the other 打分项 in points, how long the photo was
// looked at, and the sizes it was seen at.
function answerOf(
  kind: ScoringRatingInput["kind"],
  values: Values | undefined,
  watch: Stopwatch,
  stage: Element | null,
  image: Element | null,
): { body: ScoringRatingInput; now: number } {
  const now = performance.now();
  const body: ScoringRatingInput = {
    submission: requestKey(),
    kind,
    viewMs: Math.max(0, Math.round(watch.elapsed(now))),
    pixelRatio: Math.min(16, window.devicePixelRatio || 1),
  };
  if (kind === "score" && values) {
    body.score = values[overall] / 100;
    const others = Object.entries(values).filter(([id]) => id !== overall);
    if (others.length > 0) {
      body.scores = Object.fromEntries(others.map(([id, value]) => [id, value / 100])) as ScoringScores;
    }
  }
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
const keysHintItems = "← → 0.1　⇧ 0.01　↑ ↓ 换项";

// fitIn is the size a photo of width by height is shown at in a box: whole,
// as large as fits, never enlarged past its own size. With the box not
// measured yet (or in jsdom) the stylesheet alone keeps the photo inside.
function fitIn(width: number, height: number, boxWidth: number, boxHeight: number) {
  if (boxWidth <= 0 || boxHeight <= 0 || width <= 0 || height <= 0) return undefined;
  const scale = Math.min(boxWidth / width, boxHeight / height, 1);
  return { inlineSize: Math.round(width * scale), blockSize: Math.round(height * scale) };
}

const reducedMotion = () => window.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false;

// useHidden tells whether the page is in the background.
function useHidden() {
  const [hidden, setHidden] = useState(() => document.visibilityState === "hidden");
  useEffect(() => {
    const update = () => setHidden(document.visibilityState === "hidden");
    document.addEventListener("visibilitychange", update);
    return () => document.removeEventListener("visibilitychange", update);
  }, []);
  return hidden;
}

// Stage shows a rater one photo at a time, whole and never cropped, on a
// neutral wall with nothing else of it: no name, no file details, no one
// else's scores. Below it the ruler sets a score from 0 to 5 in hundredths
// against the rater's own latest scores, any of which can be set beside the
// photo. A batch with several 打分项 has them scored one after another on the
// same ruler: Enter, or releasing a pick of the pointer, moves on, and only
// the last one answers. The photo then glides down to its place on the
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
  const dimensions = batch.dimensions;
  const several = dimensions.length > 1;
  const last = dimensions.length - 1;
  const zoomable = dimensions.some((dimension) => dimension.id === sharpness);
  const [claims, setClaims] = useState<ScoringClaim[]>([]);
  const [revising, setRevising] = useState<ScoringClaim>();
  const [previous, setPrevious] = useState<{ claim: ScoringClaim; values?: Values }>();
  const [loading, setLoading] = useState(true);
  const [finished, setFinished] = useState(false);
  const [failure, setFailure] = useState("");
  const [rated, setRated] = useState(batch.rated);
  const [visible, setVisible] = useState<string>();
  const [guide, setGuide] = useState(false);
  const [rest, setRest] = useState(false);
  const [values, setValues] = useState<Values>(() =>
    Object.fromEntries(dimensions.map((dimension) => [dimension.id, startScore])),
  );
  const [item, setItem] = useState(0);
  const [reached, setReached] = useState(0);
  const [armed, setArmed] = useState(false);
  const [typed, setTyped] = useState<string>();
  const [marks, setMarks] = useState<Scored[]>([]);
  const [comparing, setComparing] = useState<Scored>();
  const [arriving, setArriving] = useState<string>();
  const [zoom, setZoom] = useState<{ token: string; view?: FullView; failure?: string }>();
  const [pointerAt, setPointerAt] = useState({ x: 0.5, y: 0.5 });
  const [delivery, setDelivery] = useState<OutboxState>(outbox.current());
  const hidden = useHidden();
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
  const watch = useRef(new Stopwatch());
  const scoringSince = useRef(0);
  const lastAnswerAt = useRef(0);
  const reading = useRef(0);
  // Where the last score ended: the next photo starts there on every 打分项.
  const lastValues = useRef<Values>(values);
  const fulls = useRef(new Map<string, Promise<FullView>>());
  const pointer = useRef<{ x: number; y: number }>(undefined);

  const dimension = dimensions[item];
  const value = values[dimension.id] ?? startScore;

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
      (items) =>
        live &&
        setMarks(
          items.map((recent) => {
            const scores: Values = { [overall]: Math.round(recent.score * 100) };
            for (const [id, score] of Object.entries(recent.scores)) {
              if (score !== undefined) scores[id] = Math.round(score * 100);
            }
            return { token: recent.token, scores, width: recent.width, height: recent.height };
          }),
        ),
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
      watch.current.start(performance.now(), true);
    });
    return () => {
      live = false;
    };
  }, [shownToken]);
  // The time on a photo stands still while the page is hidden or a sheet
  // covers the photo.
  useEffect(() => {
    const now = performance.now();
    if (guide || rest || hidden) watch.current.pause(now);
    else watch.current.resume(now);
  }, [guide, rest, hidden, visible]);

  // Full-size photos are big: only the shown photo's stays.
  useEffect(() => {
    for (const [token, loading] of fulls.current) {
      if (token === shownToken) continue;
      fulls.current.delete(token);
      loading.then(
        (view) => URL.revokeObjectURL(view.url),
        () => undefined,
      );
    }
  }, [shownToken]);
  useEffect(() => {
    const held = fulls.current;
    return () => {
      for (const loading of held.values()) {
        loading.then(
          (view) => URL.revokeObjectURL(view.url),
          () => undefined,
        );
      }
    };
  }, []);
  const zooming = zoom !== undefined;
  useEffect(() => {
    if (!zooming) return;
    const end = () => setZoom(undefined);
    window.addEventListener("blur", end);
    return () => window.removeEventListener("blur", end);
  }, [zooming]);

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
    const scored = mark && marks.find((candidate) => candidate.token === mark.token);
    compareTimer.current = window.setTimeout(
      () => setComparing(scored),
      scored ? (comparing ? 0 : compareDelay) : compareLinger,
    );
  };
  const endCompare = () => {
    window.clearTimeout(compareTimer.current);
    setComparing(undefined);
  };

  // Leaving the page with answers still on their way asks first.
  useEffect(() => {
    if (delivery.waiting === 0) return;
    const warn = (event: BeforeUnloadEvent) => event.preventDefault();
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [delivery.waiting]);

  const ready = Boolean(shown) && visible === shown?.token && !guide && !rest;

  const setValue = (hundredths: number) => {
    setValues((current) => ({ ...current, [dimension.id]: clampScore(hundredths) }));
    setTyped(undefined);
  };

  // go moves to another 打分项 of the photo, one already reached.
  const go = (to: number) => {
    if (to < 0 || to > reached || to === item) return;
    if (typed !== undefined && parseScore(typed) === undefined) return;
    setArmed(false);
    endCompare();
    setTyped(undefined);
    setItem(to);
  };

  // answer confirms the 打分项 being set and moves on to the next; the last
  // one, a skip or a mark answers the photo.
  const answer = (kind: ScoringRatingInput["kind"]) => {
    if (!shown || !ready) return;
    setArmed(false);
    let next = values;
    if (kind === "score") {
      const current = typed === undefined ? value : parseScore(typed);
      if (current === undefined) return;
      next = { ...values, [dimension.id]: current };
      if (item < last) {
        setValues(next);
        setTyped(undefined);
        endCompare();
        setItem(item + 1);
        setReached((furthest) => Math.max(furthest, item + 1));
        return;
      }
    }
    const { body, now } = answerOf(
      kind,
      kind === "score" ? next : undefined,
      watch.current,
      root.current,
      image.current,
    );
    outbox.push({ token: shown.token, body });
    answered.current.add(shown.token);
    endCompare();
    setZoom(undefined);
    if (kind === "score") {
      const from = image.current?.getBoundingClientRect();
      if (from && from.width > 0 && image.current && !reducedMotion()) {
        flight.current = { token: shown.token, from, src: image.current.currentSrc || image.current.src };
        setArriving(shown.token);
      }
      lastValues.current = next;
    }
    // The next photo starts on 总分 again, every 打分项 where the last score left it.
    setValues(lastValues.current);
    setItem(0);
    setReached(0);
    setTyped(undefined);
    setMarks((current) => {
      const others = current.filter((mark) => mark.token !== shown.token);
      if (kind !== "score") return others;
      return [...others, { token: shown.token, scores: next, width: shown.width, height: shown.height }].slice(
        -marksShown,
      );
    });
    if (revising) {
      setRevising(undefined);
    } else {
      setClaims((queue) => queue.filter((claim) => claim.token !== shown.token));
      setRated((count) => count + 1);
    }
    setPrevious({ claim: shown, values: kind === "score" ? next : undefined });
    if (lastAnswerAt.current === 0 || now - lastAnswerAt.current > pauseResets) scoringSince.current = now;
    lastAnswerAt.current = now;
    if (now - scoringSince.current >= breakAfter) setRest(true);
    void refill();
  };

  // A pick of the pointer, once released, moves on by itself after a moment.
  const advance = useEffectEvent(() => answer("score"));
  useEffect(() => {
    if (!armed) return;
    const timer = window.setTimeout(() => {
      setArmed(false);
      advance();
    }, advanceAfter);
    return () => window.clearTimeout(timer);
  }, [armed]);

  const undo = () => {
    if (!previous || revising || guide || rest) return;
    setArmed(false);
    setRevising(previous.claim);
    if (previous.values) setValues(previous.values);
    setItem(0);
    setReached(previous.values ? last : 0);
    setTyped(undefined);
  };

  const cancelRevising = () => {
    setRevising(undefined);
    setValues(lastValues.current);
    setItem(0);
    setReached(0);
    setTyped(undefined);
  };

  // startZoom shows the photo at 100% while Z is held, loading it the first time.
  const startZoom = () => {
    if (!zoomable || !shown || !ready) return;
    const token = shown.token;
    let loading = fulls.current.get(token);
    if (!loading) {
      loading = fetchFull(token);
      fulls.current.set(token, loading);
      // A failure is tried again on the next press.
      loading.catch(() => fulls.current.delete(token));
    }
    const box = image.current?.getBoundingClientRect();
    if (box && pointer.current && box.width > 0 && box.height > 0) {
      const inside = (from: number, size: number, at: number) => Math.min(1, Math.max(0, (at - from) / size));
      setPointerAt({
        x: inside(box.left, box.width, pointer.current.x),
        y: inside(box.top, box.height, pointer.current.y),
      });
    }
    setZoom({ token });
    loading.then(
      (view) => setZoom((current) => (current?.token === token ? { token, view } : current)),
      (caught: unknown) =>
        setZoom((current) =>
          current?.token === token
            ? { token, failure: caught instanceof Error ? caught.message : "现在看不了 100%" }
            : current,
        ),
    );
  };

  const track = (event: PointerEvent<HTMLDivElement>) => {
    pointer.current = { x: event.clientX, y: event.clientY };
    if (!zoom) return;
    const box = image.current?.getBoundingClientRect();
    if (!box || box.width === 0 || box.height === 0) return;
    setPointerAt({
      x: Math.min(1, Math.max(0, (event.clientX - box.left) / box.width)),
      y: Math.min(1, Math.max(0, (event.clientY - box.top) / box.height)),
    });
  };

  const act = (event: KeyboardEvent<HTMLDivElement>) => {
    const target = event.target as HTMLElement;
    if (target.closest("input, textarea, select")) return;
    const action = stageAction(event.nativeEvent);
    if (!action) return;
    event.preventDefault();
    event.stopPropagation();
    if (action.kind !== "zoom") setArmed(false);
    switch (action.kind) {
      case "set":
        setValue(action.score);
        break;
      case "nudge":
        setValues((current) => ({
          ...current,
          [dimension.id]: clampScore((current[dimension.id] ?? startScore) + action.by),
        }));
        setTyped(undefined);
        break;
      case "item":
        go(item + action.by);
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
      case "zoom":
        if (!event.repeat) startZoom();
        break;
      case "guide":
        setGuide((open) => !open);
        break;
      case "cancel":
        if (guide) setGuide(false);
        else if (revising) cancelRevising();
        break;
    }
  };
  const release = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key === "z" || event.key === "Z") setZoom(undefined);
  };

  const typedValid = typed === undefined || parseScore(typed) !== undefined;
  const side = comparing && shown && visible === shown.token ? comparing : undefined;
  const boxWidth = side ? (wallSize.width - compareGap) / 2 : wallSize.width;
  const boxHeight = side ? wallSize.height - captionRoom : wallSize.height;
  const rulerMarks: Mark[] = marks.map((mark) => ({
    token: mark.token,
    score: mark.scores[dimension.id] ?? mark.scores[overall] ?? 0,
    width: mark.width,
    height: mark.height,
  }));
  const label = several ? dimension.name : "分数";
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
          {previous && (
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
    const fitted = fitIn(shown.width, shown.height, boxWidth, boxHeight);
    const sideScore = side && (side.scores[dimension.id] ?? side.scores[overall] ?? 0);
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
            style={fitted}
          />
          {zoom?.token === shown.token && (
            <Loupe
              view={zoom.view}
              failure={zoom.failure}
              box={{ width: fitted?.inlineSize ?? 0, height: fitted?.blockSize ?? 0 }}
              at={pointerAt}
            />
          )}
          {side && <figcaption className={styles.scCaption}>这一张</figcaption>}
        </figure>
        {side && sideScore !== undefined && (
          <figure className={`${styles.scFrame} ${styles.scFrameRef}`}>
            <img
              key={side.token}
              src={claimImageURL(side.token)}
              alt={`你打过${several ? dimension.name : ""} ${formatScore(sideScore)} 分的照片`}
              draggable={false}
              className={styles.scPrint}
              style={fitIn(side.width, side.height, boxWidth, boxHeight)}
            />
            <figcaption className={styles.scCaption}>
              你打过
              {several && <em>{dimension.name}</em>}
              <b>{formatScore(sideScore)}</b>
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
      className={`${styles.scStage}${several ? ` ${styles.several}` : ""}`}
      role="application"
      aria-label={`给“${batch.name}”打分`}
      tabIndex={-1}
      onKeyDown={act}
      onKeyUp={release}
      onPointerMove={track}
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
        {zoomable && dimension.id === sharpness && (
          <span className={styles.scZoomHint}>
            按住<kbd>Z</kbd>看 100%
          </span>
        )}
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
          {!several && previous?.values && (
            <>
              {" · 原来 "}
              <b>{formatScore(previous.values[overall])}</b>
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
        <div className={styles.scScale}>
          {several && (
            <ol className={styles.scItems} aria-label="打分项">
              {dimensions.map((candidate, index) => (
                <li key={candidate.id}>
                  <button
                    type="button"
                    className={styles.scItem}
                    aria-current={index === item ? "step" : undefined}
                    disabled={index > reached}
                    onClick={() => go(index)}
                  >
                    <span>{candidate.name}</span>
                    <b>{index <= reached ? formatScore(values[candidate.id] ?? startScore) : "—"}</b>
                  </button>
                </li>
              ))}
            </ol>
          )}
          <Ruler
            value={value}
            label={label}
            marks={rulerMarks}
            latest={marks.at(-1)?.token}
            active={side?.token}
            arriving={arriving}
            onChange={(next) => {
              setArmed(false);
              setValue(next);
            }}
            onRelease={() => {
              if (several && item < last && ready) setArmed(true);
            }}
            onCompare={compare}
            rulerRef={ruler}
          />
        </div>
        <div className={styles.scReading}>
          {several && (
            <div className={styles.scWhich} aria-live="polite">
              <strong key={dimension.id}>{dimension.name}</strong>
              <small>
                {item + 1} / {dimensions.length}
              </small>
            </div>
          )}
          <div className={styles.scScoreLine}>
            <input
              className={styles.scScoreInput}
              value={typed ?? formatScore(value)}
              inputMode="decimal"
              aria-label={`${label}，0 到 5，最多两位小数`}
              aria-invalid={!typedValid}
              onChange={(event) => {
                setArmed(false);
                setTyped(event.target.value);
                const parsed = parseScore(event.target.value);
                if (parsed !== undefined) setValues((current) => ({ ...current, [dimension.id]: parsed }));
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
            {typedValid ? several ? keysHintItems : keysHint : <span role="alert">0 到 5 之间，最多两位小数</span>}
          </p>
          <button
            type="button"
            className={[styles.scConfirm, item < last && styles.next, armed && styles.armed].filter(Boolean).join(" ")}
            disabled={!ready || !typedValid}
            aria-keyshortcuts="Enter"
            onClick={() => answer("score")}
          >
            {item < last ? "下一项" : "确定"}
            <kbd aria-hidden="true">↵</kbd>
          </button>
          <div className={styles.scActs}>
            <button type="button" className={styles.scAct} disabled={!ready} onClick={() => answer("skip")}>
              无法判断 <kbd>S</kbd>
            </button>
            <button type="button" className={styles.scAct} disabled={!ready} onClick={() => answer("flag")}>
              图片有问题 <kbd>X</kbd>
            </button>
            <button type="button" className={styles.scAct} disabled={!previous || Boolean(revising)} onClick={undo}>
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
