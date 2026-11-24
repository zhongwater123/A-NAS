import { ChevronLeft, ChevronRight, Maximize2, Minimize2 } from "lucide-react";
import {
  type KeyboardEvent,
  type MouseEvent,
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
  formatCount,
  formatScore,
  hiddenKeys,
  nudgeScore,
  Outbox,
  type OutboxState,
  overall,
  parseScore,
  pickScore,
  Stopwatch,
  scaleKeys,
  snapScore,
  stageAction,
  typeable,
  typingRule,
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
// A value picked with the pointer moves on after this long, time to see
// where it landed; pressing again or a key keeps it.
const advanceAfter = 350;

// Scores of one photo in hundredths, by 打分项; a new photo has none.
type Values = Partial<Record<string, number>>;

// One of the rater's latest photos, with its score on every 打分项.
interface Scored {
  token: string;
  scores: Record<string, number>;
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

// timeOn is how long the photo the stopwatch runs for has been looked at. It
// reads the clock outside the component, which the React purity lint asks.
function timeOn(watch: Stopwatch): number {
  return watch.elapsed(performance.now());
}

// answerOf is what the page sends for an answer given now: the kind and, for
// a score, the batch's first 打分项 and the others in points, how long the
// photo was looked at, and the sizes it was seen at.
function answerOf(
  kind: ScoringRatingInput["kind"],
  first: string,
  values: Record<string, number> | undefined,
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
    body.score = values[first] / 100;
    const others = Object.entries(values).filter(([id]) => id !== first);
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

// useFullscreen tells whether the window fills the screen, through the
// browser's full screen like the media center's player.
function useFullscreen() {
  const [full, setFull] = useState(() => Boolean(document.fullscreenElement));
  useEffect(() => {
    const update = () => setFull(Boolean(document.fullscreenElement));
    document.addEventListener("fullscreenchange", update);
    return () => document.removeEventListener("fullscreenchange", update);
  }, []);
  return full;
}

// Stage shows a rater one photo at a time, whole and never cropped, on a
// neutral wall with nothing else of it: no name, no file details, no one
// else's scores. Below it the ruler sets a score from 0 to 5 in hundredths
// against the rater's own latest scores, any of which can be set beside the
// photo. A new photo starts with no score. A batch with several 打分项 has
// them scored one after another on the same ruler: Enter, or releasing a pick
// of the pointer, moves on, and after the last item the photo is answered.
// The photo then glides down to its place on the ruler, and the next one,
// decoded beforehand, takes its place. Arrows at the sides go back to the
// last photo, and on once every item has a score.
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
  // The batch's scale; scoresHidden keeps every score from the rater, who sees
  // the place on the number line alone.
  const scale = batch.scale;
  const scoresHidden = batch.hideDecimals;
  const typing = typeable(scale, scoresHidden);
  // A batch of 总分 alone shows no item names, as it always did; any other
  // names what is being scored.
  const named = several || dimensions[0].id !== overall;
  // Answers keep the first item in score, and the ruler lands a scored photo
  // at it.
  const first = dimensions[0].id;
  const last = dimensions.length - 1;
  const [claims, setClaims] = useState<ScoringClaim[]>([]);
  const [revising, setRevising] = useState<ScoringClaim>();
  const [previous, setPrevious] = useState<{ claim: ScoringClaim; values?: Record<string, number> }>();
  const [loading, setLoading] = useState(true);
  const [finished, setFinished] = useState(false);
  const [failure, setFailure] = useState("");
  const [rated, setRated] = useState(batch.rated);
  const [visible, setVisible] = useState<string>();
  const [guide, setGuide] = useState(false);
  const [rest, setRest] = useState(false);
  const [values, setValues] = useState<Values>({});
  const [item, setItem] = useState(0);
  const [reached, setReached] = useState(0);
  const [armed, setArmed] = useState(false);
  const [typed, setTyped] = useState<string>();
  const [marks, setMarks] = useState<Scored[]>([]);
  const [comparing, setComparing] = useState<Scored>();
  const [arriving, setArriving] = useState<string>();
  const [zoom, setZoom] = useState<{ token: string; by: "key" | "click"; view?: FullView; failure?: string }>();
  const [pointerAt, setPointerAt] = useState({ x: 0.5, y: 0.5 });
  const [delivery, setDelivery] = useState<OutboxState>(outbox.current());
  const hidden = useHidden();
  const fullscreen = useFullscreen();
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
  // What was set on the photo being scored while the rater went back to the
  // last one; it is there again on the way back.
  const stash = useRef<{ values: Values; item: number; reached: number }>(undefined);
  // The photo left to change the last one, with the time it had been looked at.
  const left = useRef<{ token: string; spent: number }>(undefined);
  const fulls = useRef(new Map<string, Promise<FullView>>());
  const pointer = useRef<{ x: number; y: number }>(undefined);

  const dimension = dimensions[item];
  const value = values[dimension.id];
  const complete = (scores: Values): scores is Record<string, number> =>
    dimensions.every((candidate) => scores[candidate.id] !== undefined);

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
            const scores: Record<string, number> = { [first]: Math.round(recent.score * 100) };
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
  }, [batch.id, first]);

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
      // A photo the rater comes back to after changing the last one goes on
      // from the time it had: its time is from when it appeared until it is
      // answered, not from the return.
      const back = left.current?.token === shownToken ? left.current : undefined;
      if (back) left.current = undefined;
      watch.current.start(performance.now(), true, back?.spent);
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
  const typedValue = typed === undefined ? value : parseScore(typed, scale);
  const typedValid = typed === undefined || typedValue !== undefined;
  // What the photo has with what is typed now.
  const withTyped: Values = typedValue === undefined ? values : { ...values, [dimension.id]: typedValue };
  // Back to the last photo also from a finished batch, where none is shown.
  const canPrevious = previous !== undefined && !revising && !guide && !rest;
  const canNext = ready && typedValid && complete(withTyped);

  // A photo starts without a score: none is set before it is shown or while
  // a sheet covers it.
  const setValue = (hundredths: number) => {
    if (!ready) return;
    setValues((current) => ({ ...current, [dimension.id]: snapScore(hundredths, scale) }));
    setTyped(undefined);
  };

  // go moves to another 打分项 of the photo, one already reached.
  const go = (to: number) => {
    if (!ready || to < 0 || to > reached || to === item || !typedValid) return;
    setArmed(false);
    endCompare();
    setTyped(undefined);
    setValues(withTyped);
    setItem(to);
  };

  // answer sends the photo's answer, scores or a skip or a mark, and moves
  // to the next photo, or back to the one the rater left to change this one.
  const answer = (kind: ScoringRatingInput["kind"], scores?: Record<string, number>) => {
    if (!shown || !ready) return;
    setArmed(false);
    const { body, now } = answerOf(kind, first, scores, watch.current, root.current, image.current);
    outbox.push({ token: shown.token, body });
    answered.current.add(shown.token);
    endCompare();
    setZoom(undefined);
    if (scores) {
      const from = image.current?.getBoundingClientRect();
      if (from && from.width > 0 && image.current && !reducedMotion()) {
        flight.current = { token: shown.token, from, src: image.current.currentSrc || image.current.src };
        setArriving(shown.token);
      }
    }
    setMarks((current) => {
      const others = current.filter((mark) => mark.token !== shown.token);
      if (!scores) return others;
      return [...others, { token: shown.token, scores, width: shown.width, height: shown.height }].slice(-marksShown);
    });
    setPrevious({ claim: shown, values: scores });
    setTyped(undefined);
    if (revising) {
      setRevising(undefined);
      const kept = stash.current;
      stash.current = undefined;
      setValues(kept?.values ?? {});
      setItem(kept?.item ?? 0);
      setReached(kept?.reached ?? 0);
    } else {
      setValues({});
      setItem(0);
      setReached(0);
      setClaims((queue) => queue.filter((claim) => claim.token !== shown.token));
      setRated((count) => count + 1);
    }
    if (lastAnswerAt.current === 0 || now - lastAnswerAt.current > pauseResets) scoringSince.current = now;
    lastAnswerAt.current = now;
    if (now - scoringSince.current >= breakAfter) setRest(true);
    void refill();
  };

  // confirm takes the item being set and moves on to the next; after the
  // last one the photo is answered.
  const confirm = () => {
    if (!shown || !ready || typedValue === undefined) return;
    setArmed(false);
    if (item < last) {
      setValues(withTyped);
      setTyped(undefined);
      endCompare();
      setItem(item + 1);
      setReached((furthest) => Math.max(furthest, item + 1));
      return;
    }
    if (complete(withTyped)) answer("score", withTyped);
  };

  // A pick of the pointer, once released, moves on by itself after a moment.
  const advance = useEffectEvent(() => confirm());
  useEffect(() => {
    if (!armed) return;
    const timer = window.setTimeout(() => {
      setArmed(false);
      advance();
    }, advanceAfter);
    return () => window.clearTimeout(timer);
  }, [armed]);

  // revise goes back to the last photo at item at, keeping what was set on
  // this one; the server lets the rater change only the latest answer.
  const revise = (at: number) => {
    if (!previous || !canPrevious) return;
    setArmed(false);
    endCompare();
    setZoom(undefined);
    stash.current = { values: withTyped, item, reached };
    left.current = ready && shown ? { token: shown.token, spent: timeOn(watch.current) } : undefined;
    setRevising(previous.claim);
    setValues(previous.values ?? {});
    setItem(previous.values ? at : 0);
    setReached(previous.values ? last : 0);
    setTyped(undefined);
  };

  const cancelRevising = () => {
    setRevising(undefined);
    const kept = stash.current;
    stash.current = undefined;
    setValues(kept?.values ?? {});
    setItem(kept?.item ?? 0);
    setReached(kept?.reached ?? 0);
    setTyped(undefined);
  };

  // 撤销一步 takes back the last step: the item before, or from a photo's
  // first item the last photo's last item. A new photo's later items lose
  // what they had; a photo being changed keeps all its scores.
  const undo = () => {
    if (guide || rest) return;
    setArmed(false);
    setTyped(undefined);
    if (item > 0) {
      endCompare();
      if (!revising) {
        const kept = { ...values };
        for (const later of dimensions.slice(item)) delete kept[later.id];
        setValues(kept);
        setReached(item - 1);
      }
      setItem(item - 1);
      return;
    }
    revise(last);
  };

  // nextPhoto answers a photo that has every item, as it stands.
  const nextPhoto = () => {
    if (canNext && complete(withTyped)) answer("score", withTyped);
  };

  // startZoom shows the photo at 100%, loading it the first time, while Z
  // is held or until the next click.
  const startZoom = (by: "key" | "click", at?: { x: number; y: number }) => {
    if (!shown || !ready) return;
    const token = shown.token;
    let loading = fulls.current.get(token);
    if (!loading) {
      loading = fetchFull(token);
      fulls.current.set(token, loading);
      // A failure is tried again on the next look.
      loading.catch(() => fulls.current.delete(token));
    }
    const box = image.current?.getBoundingClientRect();
    const from = at ?? pointer.current;
    if (box && from && box.width > 0 && box.height > 0) {
      const inside = (start: number, size: number, point: number) => Math.min(1, Math.max(0, (point - start) / size));
      setPointerAt({ x: inside(box.left, box.width, from.x), y: inside(box.top, box.height, from.y) });
    }
    setZoom({ token, by });
    loading.then(
      (view) => setZoom((current) => (current?.token === token ? { ...current, view } : current)),
      (caught: unknown) =>
        setZoom((current) =>
          current?.token === token
            ? { ...current, failure: caught instanceof Error ? caught.message : "现在看不了 100%" }
            : current,
        ),
    );
  };
  const clickPhoto = (event: MouseEvent<HTMLElement>) => {
    if (zoom) setZoom(undefined);
    else startZoom("click", { x: event.clientX, y: event.clientY });
  };

  const toggleFullscreen = () => {
    if (document.fullscreenElement) {
      void document.exitFullscreen?.().catch(() => undefined);
      return;
    }
    // The whole window fills the screen, so that its notices show there too.
    const target = root.current?.closest<HTMLElement>("[data-scoring-window]") ?? root.current;
    void target?.requestFullscreen?.().catch(() => undefined);
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
    // With no photo to confirm (a sheet covers it, it is not shown yet, or
    // the batch is finished), Enter belongs to the button that has the
    // focus: 继续打分, the guide's close, 返回批次.
    if (action.kind === "confirm" && !ready && target.closest("button")) return;
    event.preventDefault();
    event.stopPropagation();
    if (action.kind !== "zoom") setArmed(false);
    switch (action.kind) {
      case "set": {
        const picked = pickScore(action.score, scale);
        if (picked !== undefined) setValue(picked);
        break;
      }
      case "nudge":
        setValue(nudgeScore(value, action.by, scale));
        break;
      case "item":
        go(item + action.by);
        break;
      case "confirm":
        confirm();
        break;
      case "skip":
      case "flag":
        if (!revising) answer(action.kind);
        break;
      case "undo":
        undo();
        break;
      case "previous":
        revise(0);
        break;
      case "next":
        nextPhoto();
        break;
      case "zoom":
        if (!event.repeat && !zoom) startZoom("key");
        break;
      case "fullscreen":
        toggleFullscreen();
        break;
      case "guide":
        setGuide((open) => !open);
        break;
      case "cancel":
        if (zoom) setZoom(undefined);
        else if (guide) setGuide(false);
        else if (revising) cancelRevising();
        break;
    }
  };
  const release = (event: KeyboardEvent<HTMLDivElement>) => {
    if ((event.key === "z" || event.key === "Z") && zoom?.by === "key") setZoom(undefined);
  };

  const side = comparing && shown && visible === shown.token ? comparing : undefined;
  const boxWidth = side ? (wallSize.width - compareGap) / 2 : wallSize.width;
  const boxHeight = side ? wallSize.height - captionRoom : wallSize.height;
  const rulerMarks: Mark[] = marks.map((mark) => ({
    token: mark.token,
    score: mark.scores[dimension.id] ?? mark.scores[first] ?? 0,
    width: mark.width,
    height: mark.height,
  }));
  // The last photo's score on this item, as a pale dot; not while changing it.
  const lastScore = revising ? undefined : marks.at(-1)?.scores[dimension.id];
  const label = named ? dimension.name : "分数";
  // The keys that move a set score, under the reading.
  const fine = scale === "hundredths" || scale === "tenths";
  const keysHint =
    (scoresHidden && fine ? hiddenKeys[scale] : scaleKeys[scale]) +
    (several ? "　↑ ↓ 换项" : fine && !scoresHidden ? "　0–5 整数分" : scale === "whole" ? "　0–5" : "");
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
            <button type="button" className={styles.scQuiet} onClick={() => revise(0)}>
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
    const sideScore = side && (side.scores[dimension.id] ?? side.scores[first] ?? 0);
    body = (
      <>
        {/* A click looks at the photo at 100% and another comes back; Z held does the same from the keyboard. */}
        {/* oxlint-disable-next-line jsx-a11y/click-events-have-key-events, jsx-a11y/no-noninteractive-element-interactions */}
        <figure className={`${styles.scFrame} ${styles.scFrameMain}`} title="单击看 100%" onClick={clickPhoto}>
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
              preview={claimImageURL(shown.token)}
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
              alt={
                scoresHidden
                  ? `你打过${named ? dimension.name : ""}的照片`
                  : `你打过${named ? dimension.name : ""} ${formatScore(sideScore, scale)} 分的照片`
              }
              draggable={false}
              className={styles.scPrint}
              style={fitIn(side.width, side.height, boxWidth, boxHeight)}
            />
            <figcaption className={styles.scCaption}>
              你打过
              {named && <em>{dimension.name}</em>}
              {!scoresHidden && <b>{formatScore(sideScore, scale)}</b>}
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
      className={`${styles.scStage}${several ? ` ${styles.several}` : ""}${fullscreen ? ` ${styles.full}` : ""}`}
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
        <span className={styles.scCount} aria-live="polite">
          已打 <b>{formatCount(rated)}</b> 张
        </span>
        <button type="button" className={styles.scTextLink} onClick={() => setGuide(true)}>
          打分标准
        </button>
        <button
          type="button"
          className={`${styles.scTextLink} ${styles.scFullscreen}`}
          aria-keyshortcuts="F"
          onClick={toggleFullscreen}
        >
          {fullscreen ? <Minimize2 aria-hidden="true" /> : <Maximize2 aria-hidden="true" />}
          {fullscreen ? "退出全屏" : "全屏"}
          <kbd aria-hidden="true">F</kbd>
        </button>
        <button type="button" className={styles.scRound} aria-label="怎样打分" onClick={() => setGuide(true)}>
          ?
        </button>
      </header>
      {revising && (
        <div className={`${styles.scBanner} ${styles.scRevising}`} role="status">
          正在改上一张 · Esc 不改
        </div>
      )}
      {delivery.waiting > 0 && delivery.retrying && (
        <div className={`${styles.scBanner} ${styles.scDelivery}`} role="status">
          {delivery.noSpace
            ? `有 ${delivery.waiting} 个分数还没送达：数据卷剩余空间不足，腾出空间后会自动提交`
            : `有 ${delivery.waiting} 个分数还没送达，连接恢复后会自动提交`}
        </div>
      )}
      <button
        type="button"
        className={`${styles.scTurn} ${styles.scTurnBack}`}
        aria-label="上一张"
        aria-keyshortcuts="["
        title={canPrevious ? "上一张  [" : "只能回到上一张"}
        disabled={!canPrevious}
        onClick={() => revise(0)}
      >
        <ChevronLeft aria-hidden="true" />
      </button>
      <button
        type="button"
        className={`${styles.scTurn} ${styles.scTurnNext}`}
        aria-label="下一张"
        aria-keyshortcuts="]"
        title={canNext ? "下一张  ]" : "这一张打完才能到下一张"}
        disabled={!canNext}
        onClick={nextPhoto}
      >
        <ChevronRight aria-hidden="true" />
      </button>
      <div ref={wall} className={`${styles.scWall}${side ? ` ${styles.comparing}` : ""}`}>
        {body}
      </div>
      <section className={styles.scInstrument} aria-label="打分">
        <div className={styles.scScale}>
          {several && (
            <ol className={styles.scItems} aria-label="打分项">
              {dimensions.map((candidate, index) => {
                const score = values[candidate.id];
                return (
                  <li key={candidate.id}>
                    <button
                      type="button"
                      className={styles.scItem}
                      aria-current={index === item ? "step" : undefined}
                      disabled={index > reached}
                      onClick={() => go(index)}
                    >
                      <span>{candidate.name}</span>
                      {score === undefined ? (
                        <b>—</b>
                      ) : scoresHidden ? (
                        <b className={styles.scItemSet} aria-label="已选">
                          ●
                        </b>
                      ) : (
                        <b>{formatScore(score, scale)}</b>
                      )}
                    </button>
                  </li>
                );
              })}
            </ol>
          )}
          <Ruler
            value={value}
            label={label}
            marks={rulerMarks}
            last={lastScore}
            active={side?.token}
            arriving={arriving}
            scale={scale}
            hidden={scoresHidden}
            onChange={(next) => {
              setArmed(false);
              setValue(next);
            }}
            onRelease={() => {
              if (ready) setArmed(true);
            }}
            onCompare={compare}
            rulerRef={ruler}
          />
        </div>
        <div className={styles.scReading}>
          {named && (
            <div className={styles.scWhich} aria-live="polite">
              <strong key={dimension.id}>{dimension.name}</strong>
              {several && (
                <small>
                  {item + 1} / {dimensions.length}
                </small>
              )}
            </div>
          )}
          {named && dimension.description && <p className={styles.scAbout}>{dimension.description}</p>}
          {!typing && (
            <div className={styles.scScoreLine} aria-live="polite">
              {value === undefined ? (
                <span className={styles.scNoScore}>—</span>
              ) : scoresHidden ? (
                <span className={styles.scPlaced}>
                  <i aria-hidden="true" />
                  已选
                </span>
              ) : (
                <>
                  <span className={styles.scLevel}>{formatScore(value, scale)}</span>
                  <span className={styles.scOutOf}>{value / 100} 分</span>
                </>
              )}
            </div>
          )}
          {typing && (
            <div className={styles.scScoreLine}>
              <input
                className={styles.scScoreInput}
                value={typed ?? (value === undefined ? "" : formatScore(value, scale))}
                placeholder="—"
                inputMode="decimal"
                readOnly={!ready}
                aria-label={`${label}，${typingRule[scale]}`}
                aria-invalid={!typedValid}
                onChange={(event) => {
                  setArmed(false);
                  setTyped(event.target.value);
                  const parsed = parseScore(event.target.value, scale);
                  if (parsed !== undefined) setValues((current) => ({ ...current, [dimension.id]: parsed }));
                }}
                onBlur={() => typedValid && setTyped(undefined)}
                onKeyDown={(event) => {
                  if (event.key === "Enter") {
                    event.preventDefault();
                    confirm();
                  } else if (event.key === "Escape") {
                    setTyped(undefined);
                    root.current?.focus({ preventScroll: true });
                  }
                }}
              />
              <span className={styles.scOutOf}>/ 5</span>
            </div>
          )}
          <p className={`${styles.scSay}${typedValid ? "" : ` ${styles.error}`}`}>
            {typedValid ? (
              typedValue === undefined ? (
                scale === "three" ? (
                  "点选一档，或按 1、3、5"
                ) : (
                  "在数轴上点选，或按 0–5"
                )
              ) : (
                keysHint
              )
            ) : (
              <span role="alert">{typingRule[scale]}</span>
            )}
          </p>
          <button
            type="button"
            className={[styles.scConfirm, item < last && styles.next, armed && styles.armed].filter(Boolean).join(" ")}
            disabled={!ready || typedValue === undefined}
            aria-keyshortcuts="Enter"
            onClick={confirm}
          >
            {item < last ? "下一项" : "确定"}
            <kbd aria-hidden="true">↵</kbd>
          </button>
          <div className={styles.scActs}>
            <button
              type="button"
              className={styles.scAct}
              disabled={!ready || Boolean(revising)}
              onClick={() => answer("skip")}
            >
              无法判断 <kbd>S</kbd>
            </button>
            <button
              type="button"
              className={styles.scAct}
              disabled={!ready || Boolean(revising)}
              onClick={() => answer("flag")}
            >
              图片有问题 <kbd>X</kbd>
            </button>
            <button
              type="button"
              className={styles.scAct}
              disabled={!ready || (item === 0 && !canPrevious)}
              onClick={undo}
            >
              撤销一步 <kbd>⌫</kbd>
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
