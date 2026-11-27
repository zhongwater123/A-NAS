// What the 照片打分 window computes without the page: the keys of the
// rating stage, the outbox that delivers answers in order, and how numbers
// are shown.
import { APIError } from "../api/client";
import type { operations, ScoringBatch, ScoringExportField, ScoringRatingInput, ScoringScale } from "../api.generated";

// Scores are whole hundredths in the page, 0 to maxScore, as in the module;
// only the API speaks points with two decimals.
export const maxScore = 500;

// overall is 总体. A batch of it alone shows no item names.
export const overall = "overall";

export type StageAction =
  | { kind: "set"; score: number }
  | { kind: "nudge"; by: number }
  | { kind: "item"; by: -1 | 1 }
  | { kind: "confirm" }
  | { kind: "skip" }
  | { kind: "flag" }
  | { kind: "undo" }
  | { kind: "previous" }
  | { kind: "next" }
  | { kind: "zoom" }
  | { kind: "fullscreen" }
  | { kind: "guide" }
  | { kind: "cancel" };

// stageAction reads a key of the rating stage: 0–5 move to that whole
// score, ← and → by 0.1 and with Shift by 0.01 (the stage keeps both to the
// batch's scale: pickScore, nudgeScore), ↑ and ↓ to the 打分项 before and
// after, Enter confirms, S skips (cannot judge), X marks a problem,
// Backspace takes one step back, [ and ] go to the photo before and after,
// Z shows the photo at 100% while held, F fills the screen, ? shows the
// guide and Escape leaves what is open. A key with Ctrl, Alt or the command
// key is the browser's.
export function stageAction(
  event: Pick<KeyboardEvent, "key" | "ctrlKey" | "altKey" | "metaKey" | "shiftKey">,
): StageAction | undefined {
  if (event.ctrlKey || event.altKey || event.metaKey) return undefined;
  const { key } = event;
  if (/^[0-5]$/.test(key)) return { kind: "set", score: Number(key) * 100 };
  switch (key) {
    case "ArrowRight":
      return { kind: "nudge", by: event.shiftKey ? 1 : 10 };
    case "ArrowLeft":
      return { kind: "nudge", by: event.shiftKey ? -1 : -10 };
    case "ArrowUp":
      return { kind: "item", by: -1 };
    case "ArrowDown":
      return { kind: "item", by: 1 };
    case "Enter":
      return { kind: "confirm" };
    case "z":
    case "Z":
      return { kind: "zoom" };
    case "f":
    case "F":
      return { kind: "fullscreen" };
    case "[":
      return { kind: "previous" };
    case "]":
      return { kind: "next" };
    case "s":
    case "S":
      return { kind: "skip" };
    case "x":
    case "X":
      return { kind: "flag" };
    case "Backspace":
      return { kind: "undo" };
    case "?":
      return { kind: "guide" };
    case "Escape":
      return { kind: "cancel" };
  }
  return undefined;
}

export const clampScore = (hundredths: number) => Math.min(maxScore, Math.max(0, Math.round(hundredths)));

// A batch's scale: the number line from 0 to 5 with two decimals, with one,
// in whole points, or three or five levels. Every score stays in hundredths.
export type Scale = ScoringScale;

// The scales of levels: places to click, each with its word.
export type LevelScale = "three" | "five";
export interface Level {
  score: number;
  word: string;
}

// The levels of each: 差 1, 中 3 and 好 5; 很差 1 to 很好 5.
export const levelsOf: Record<LevelScale, readonly Level[]> = {
  three: [
    { score: 100, word: "差" },
    { score: 300, word: "中" },
    { score: 500, word: "好" },
  ],
  five: [
    { score: 100, word: "很差" },
    { score: 200, word: "较差" },
    { score: 300, word: "一般" },
    { score: 400, word: "好" },
    { score: 500, word: "很好" },
  ],
};

// 情绪 describes rather than judges, so its levels have words of their own,
// from calm to intense; every other 打分项, custom ones too, has the
// scale's. Go's describe (scales.go) writes the same words in the export.
const emotionLevels: Record<LevelScale, readonly Level[]> = {
  three: [
    { score: 100, word: "平静" },
    { score: 300, word: "中间" },
    { score: 500, word: "激烈" },
  ],
  five: [
    { score: 100, word: "很平静" },
    { score: 200, word: "安静" },
    { score: 300, word: "中间" },
    { score: 400, word: "有张力" },
    { score: 500, word: "激烈" },
  ],
};

// levelsFor is the levels of a scale with the words of a 打分项.
export const levelsFor = (scale: LevelScale, dimension?: string): readonly Level[] =>
  (dimension === "emotion" ? emotionLevels : levelsOf)[scale];

export const isLevels = (scale: Scale): scale is LevelScale => scale === "three" || scale === "five";

// The step between two scores of a number line, in hundredths.
const steps: Record<Exclude<Scale, LevelScale>, number> = { hundredths: 1, tenths: 10, whole: 100 };
const places: Record<Scale, number> = { hundredths: 2, tenths: 1, whole: 0, three: 0, five: 0 };

// The level nearest to hundredths, a tie going up; below the lowest is the
// lowest.
const levelOf = (hundredths: number, scale: LevelScale, dimension?: string) =>
  levelsFor(scale, dimension).reduce((best, level) =>
    Math.abs(level.score - hundredths) <= Math.abs(best.score - hundredths) ? level : best,
  );

// snapScore puts hundredths on the scale: the nearest step of the number
// line, or the nearest level.
export function snapScore(hundredths: number, scale: Scale, dimension?: string): number {
  if (isLevels(scale)) return levelOf(hundredths, scale).score;
  const step = steps[scale];
  return Math.max(lowestScore(dimension), clampScore(Math.round(hundredths / step) * step));
}

// lowestScore is the lowest score of a 打分项 on a number line: 情绪 goes
// from 1, calm, to 5, intense, as on its levels; every other one from 0.
export const lowestScore = (dimension?: string) => (dimension === "emotion" ? 100 : 0);

// formatScore shows hundredths as the scale writes them: 3.47, 3.5, 3, or
// the level's word for the 打分项, 中, 一般 or for 情绪 中间.
export const formatScore = (hundredths: number, scale: Scale = "hundredths", dimension?: string) =>
  isLevels(scale) ? levelOf(hundredths, scale, dimension).word : (hundredths / 100).toFixed(places[scale]);

// typeable tells the scales whose scores a rater can type: the number
// lines whose numbers the rater sees.
export const typeable = (scale: Scale, hidden: boolean) => !isLevels(scale) && !hidden;

// typingRule says what parseScore takes on a scale.
export const typingRule: Record<Scale, string> = {
  hundredths: "0 到 5 之间，最多两位小数",
  tenths: "0 到 5 之间，最多一位小数",
  whole: "0 到 5 之间的整数",
  three: "1、3 或 5",
  five: "1 到 5 之间的整数",
};

// typingRuleFor says it for a 打分项: 情绪 starts at 1 on a number line.
export const typingRuleFor = (scale: Scale, dimension?: string) =>
  lowestScore(dimension) > 0 && !isLevels(scale) ? typingRule[scale].replace("0 到 5", "1 到 5") : typingRule[scale];

const typingPatterns: Record<Scale, RegExp> = {
  hundredths: /^\d(\.\d{1,2})?$/,
  tenths: /^\d(\.\d)?$/,
  whole: /^\d$/,
  three: /^[135]$/,
  five: /^[1-5]$/,
};

// parseScore reads what the rater typed: 0 to 5, 1 to 5 for 情绪, with as
// many decimals as the scale has, in hundredths, or undefined.
export function parseScore(text: string, scale: Scale = "hundredths", dimension?: string): number | undefined {
  const trimmed = text.trim();
  if (!typingPatterns[scale].test(trimmed)) return undefined;
  const hundredths = Math.round(Number(trimmed) * 100);
  return hundredths >= lowestScore(dimension) && hundredths <= maxScore ? hundredths : undefined;
}

// pickScore is the score a key 0 to 5 picks on the scale, or undefined:
// three levels take 1, 3 and 5 only, five levels and 情绪 1 to 5.
export function pickScore(hundredths: number, scale: Scale, dimension?: string): number | undefined {
  if (isLevels(scale)) return levelsOf[scale].some((level) => level.score === hundredths) ? hundredths : undefined;
  return hundredths >= lowestScore(dimension) ? hundredths : undefined;
}

// nudgeScore moves a score by an arrow key: by is in hundredths for the
// line with two decimals (0.1, or 0.01 with Shift), else one step of the
// scale in by's direction, one level on a scale of levels. With no score
// yet it starts in the middle.
export function nudgeScore(now: number | undefined, by: number, scale: Scale): number {
  const up = by > 0;
  if (isLevels(scale)) {
    if (now === undefined) return 300;
    const ladder = levelsOf[scale];
    const at = ladder.indexOf(levelOf(now, scale));
    return ladder[Math.min(ladder.length - 1, Math.max(0, at + (up ? 1 : -1)))].score;
  }
  if (now === undefined) return scale === "whole" ? (up ? 300 : 200) : 250;
  return clampScore(now + (scale === "hundredths" ? by : up ? steps[scale] : -steps[scale]));
}

// The keys a set score moves by, shown under the reading.
export const scaleKeys: Record<Scale, string> = {
  hundredths: "← → 0.1　⇧ 0.01",
  tenths: "← → 0.1",
  whole: "← → 1 分",
  three: "1 差　3 中　5 好",
  five: "1–5 选档　← → 换一档",
};
// keysFor is scaleKeys for a 打分项: three levels name its own levels, so
// that 情绪 reads 1 平静 rather than 1 差.
export const keysFor = (scale: Scale, dimension?: string) =>
  scale === "three"
    ? levelsFor(scale, dimension)
        .map((level) => `${level.score / 100} ${level.word}`)
        .join("　")
    : scaleKeys[scale];
// The same with the decimals hidden from the rater.
export const hiddenKeys: Record<"hundredths" | "tenths", string> = {
  hundredths: "← → 移动　⇧ 细调",
  tenths: "← → 移动",
};

// Stopwatch counts how long a photo is looked at: running from when it
// appears, standing still while the page is hidden or a sheet covers it.
// Times are performance.now() milliseconds.
export class Stopwatch {
  private spent = 0;
  private since: number | undefined;

  // spent is the time the photo already had, when the rater comes back to it.
  start(now: number, running: boolean, spent = 0) {
    this.spent = spent;
    this.since = running ? now : undefined;
  }

  pause(now: number) {
    if (this.since === undefined) return;
    this.spent += now - this.since;
    this.since = undefined;
  }

  resume(now: number) {
    this.since ??= now;
  }

  elapsed(now: number): number {
    return this.spent + (this.since === undefined ? 0 : now - this.since);
  }
}

// orientationMatrix is the CSS matrix(a, b, c, d, e, f), with the transform
// origin at the top left, that turns a picture stored width by height in
// EXIF orientation 1 to 8 upright, as browsers do for <img>; the photo at
// 100% comes in its stored orientation.
export function orientationMatrix(
  orientation: number,
  width: number,
  height: number,
): [number, number, number, number, number, number] {
  switch (orientation) {
    case 2:
      return [-1, 0, 0, 1, width, 0];
    case 3:
      return [-1, 0, 0, -1, width, height];
    case 4:
      return [1, 0, 0, -1, 0, height];
    case 5:
      return [0, 1, 1, 0, 0, 0];
    case 6:
      return [0, 1, -1, 0, height, 0];
    case 7:
      return [0, -1, -1, 0, height, width];
    case 8:
      return [0, -1, 1, 0, 0, width];
    default:
      return [1, 0, 0, 1, 0, 0];
  }
}

// uprightSize is the size of a picture stored width by height once turned
// upright: orientations 5 to 8 swap the sides.
export const uprightSize = (orientation: number, width: number, height: number) =>
  orientation >= 5 ? { width: height, height: width } : { width, height };

// The export's columns beside image_id, image_path and rater, which every
// export has, in the groups the dialog shows them; the 打分项 are a group of
// their own, from the batch.
export type ExportField = ScoringExportField;
type ExportQuery = NonNullable<operations["exportScoringBatch"]["parameters"]["query"]>;
// Each field names the columns it adds, as the contract describes them.
export const exportRequired = ["image_id", "image_path", "rater"];
export const exportGroups: { label: string; fields: { field: ExportField; label: string; columns: string[] }[] }[] = [
  {
    label: "打分人",
    fields: [
      { field: "nickname", label: "昵称", columns: ["nickname"] },
      { field: "rater_id", label: "账号 ID", columns: ["rater_id"] },
    ],
  },
  { label: "图片", fields: [{ field: "image_size", label: "原图宽高", columns: ["image_width", "image_height"] }] },
  {
    label: "时间",
    fields: [
      { field: "duration", label: "单图耗时", columns: ["duration_s"] },
      { field: "submitted_at", label: "提交时间", columns: ["submitted_at"] },
    ],
  },
  {
    label: "记录",
    fields: [
      { field: "kind", label: "类型", columns: ["kind", "repeat", "superseded"] },
      { field: "links", label: "撤销与重复的关联", columns: ["rating_id", "replaces", "repeat_of"] },
    ],
  },
  {
    label: "显示",
    fields: [
      { field: "viewport", label: "打分区尺寸", columns: ["viewport_width", "viewport_height"] },
      { field: "display", label: "照片显示尺寸", columns: ["display_width", "display_height"] },
      { field: "pixel_ratio", label: "设备像素比", columns: ["pixel_ratio"] },
    ],
  },
];
// The export's order of fields: who, the photo, the 打分项, then the rest.
export function exportColumns(fields: ExportField[], dimensions: string[]): string[] {
  const columns = [...exportRequired];
  const named = exportGroups.flatMap((group) => group.fields);
  const add = (field: ExportField) => {
    if (fields.includes(field)) columns.push(...(named.find((entry) => entry.field === field)?.columns ?? []));
  };
  for (const field of ["nickname", "rater_id", "image_size"] as const) add(field);
  for (const dimension of dimensions) if (fields.includes(dimension as ExportField)) columns.push(dimension);
  for (const field of ["duration", "submitted_at", "kind", "links", "viewport", "display", "pixel_ratio"] as const)
    add(field);
  return columns;
}
// A new export starts with these, beside every 打分项.
export const exportDefaults: ExportField[] = ["nickname", "duration", "submitted_at"];
export type ExportInclude = NonNullable<ExportQuery["include"]>[number];
export const exportIncludes: { include: ExportInclude; label: string }[] = [
  { include: "skips", label: "跳过与标记" },
  { include: "superseded", label: "被撤销的旧分数" },
  { include: "repeats", label: "重复检查" },
];

export interface Placed {
  // Where the photo sits, from 0 to 1 along the ruler, and its width in px.
  at: number;
  width: number;
}

// lanes stacks the ruler's thumbnails so that none hides another: each goes
// to the lowest of laneCount lanes where it clears its neighbours, or, when
// every lane is taken there, to the lane with the most room. rulerWidth is
// the ruler's width in px.
export function lanes(items: Placed[], rulerWidth: number, laneCount = 3, gap = 6): number[] {
  const taken: { x: number; width: number }[][] = Array.from({ length: laneCount }, () => []);
  return items.map((item) => {
    const x = item.at * rulerWidth;
    const room = taken.map((lane) =>
      lane.length === 0 ? Infinity : Math.min(...lane.map((o) => Math.abs(o.x - x) - (o.width + item.width) / 2)),
    );
    let lane = room.findIndex((space) => space >= gap);
    if (lane < 0) lane = room.indexOf(Math.max(...room));
    taken[lane].push({ x, width: item.width });
    return lane;
  });
}

export interface Answer {
  token: string;
  body: ScoringRatingInput;
}

export interface OutboxState {
  // Answers not delivered yet, the one being sent included.
  waiting: number;
  // The last attempt failed and the outbox is waiting to try again.
  retrying: boolean;
  // It failed because the data volume is at its reserve: freeing space,
  // not the connection, lets the answers through.
  noSpace?: boolean;
  // The rater's count of answers in the batch, as the server last said.
  rated?: number;
}

type Send = (answer: Answer) => Promise<number>;
type Retryable = (caught: unknown) => boolean;

const firstRetry = 2_000;
const longestRetry = 30_000;

// Outbox sends a rater's answers one at a time and in order, so that a
// change of the last score never arrives before the score itself. An answer
// that fails for a reason that may pass (network, service down) is sent
// again with the same submission after a growing pause; the server takes
// the same submission only once. An answer the server refuses is dropped
// and reported. It is not tied to the window: closing the window does not
// lose answers still on their way.
export class Outbox {
  private queue: Answer[] = [];
  private sending = false;
  private pause = 0;
  private state: OutboxState = { waiting: 0, retrying: false };
  private readonly listeners = new Set<(state: OutboxState) => void>();

  constructor(
    private readonly send: Send,
    private readonly retryable: Retryable,
    private readonly onRefused: (answer: Answer, caught: unknown) => void,
  ) {}

  subscribe(listener: (state: OutboxState) => void): () => void {
    this.listeners.add(listener);
    listener(this.state);
    return () => this.listeners.delete(listener);
  }

  current(): OutboxState {
    return this.state;
  }

  push(answer: Answer) {
    this.queue.push(answer);
    this.publish({ waiting: this.queue.length });
    void this.drain();
  }

  // pending lists the tokens of answers not delivered yet.
  pending(): Set<string> {
    return new Set(this.queue.map((answer) => answer.token));
  }

  private publish(change: Partial<OutboxState>) {
    this.state = { ...this.state, ...change };
    for (const listener of this.listeners) listener(this.state);
  }

  private async drain() {
    if (this.sending) return;
    this.sending = true;
    try {
      while (this.queue.length > 0) {
        const answer = this.queue[0];
        try {
          const rated = await this.send(answer);
          this.queue.shift();
          this.pause = 0;
          this.publish({ waiting: this.queue.length, retrying: false, noSpace: false, rated });
        } catch (caught) {
          if (!this.retryable(caught)) {
            this.queue.shift();
            this.publish({ waiting: this.queue.length, retrying: false });
            this.onRefused(answer, caught);
            continue;
          }
          this.pause = Math.min(this.pause ? this.pause * 2 : firstRetry, longestRetry);
          this.publish({ retrying: true, noSpace: isCode(caught, "insufficient_storage") });
          await new Promise((resolve) => window.setTimeout(resolve, this.pause));
        }
      }
    } finally {
      this.sending = false;
    }
  }
}

export const isCode = (caught: unknown, code: string) => caught instanceof APIError && caught.code === code;

// The pages show numbers in the desktop's locale.
const decimal = new Intl.NumberFormat("zh-CN", { maximumFractionDigits: 1, minimumFractionDigits: 1 });
const twoPlaces = new Intl.NumberFormat("zh-CN", { maximumFractionDigits: 2, minimumFractionDigits: 2 });
const percent = new Intl.NumberFormat("zh-CN", { style: "percent", maximumFractionDigits: 0 });
const count = new Intl.NumberFormat("zh-CN");
const day = new Intl.DateTimeFormat("zh-CN", { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" });

export const formatCount = (value: number) => count.format(value);
// formatPoints writes a score, or a difference of scores, as raters see them.
export const formatPoints = (value: number) => twoPlaces.format(value);
export const formatCorrelation = (value: number | null) => (value === null ? "—" : twoPlaces.format(value));
export const formatPercent = (value: number) => percent.format(value);
export const formatSeconds = (milliseconds: number) => `${decimal.format(milliseconds / 1000)} 秒`;
export const formatWhen = (iso: string) => day.format(new Date(iso));

export type BatchState = ScoringBatch["state"];
export const stateLabel: Record<BatchState, string> = { importing: "导入中", open: "可打分", closed: "已结束" };

// What the batch page calls out about a rater. Agreement is a correlation
// with the others' mean; a repeat difference is in points on the 0 to 5 scale.
export const agreementGood = 0.7;
export const agreementPoor = 0.4;
export const repeatGood = 0.3;
export const repeatPoor = 0.6;
// A share of scores given within a second, and a run of the same score,
// that suggest a rater is not looking.
export const fastShareAlert = 0.1;
export const sameRunAlert = 15;

// share is how far a batch is: photos with all their raters among all.
export function share(done: number, total: number): number {
  return total > 0 ? Math.min(1, done / total) : 0;
}
