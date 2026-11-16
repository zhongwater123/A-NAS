// What the 照片打分 window computes without the page: the keys of the
// rating stage, the outbox that delivers answers in order, and how numbers
// are shown.
import { APIError } from "../api/client";
import type { operations, ScoringBatch, ScoringExportField, ScoringRatingInput } from "../api.generated";

// Scores are whole hundredths in the page, 0 to maxScore, as in the module;
// only the API speaks points with two decimals.
export const maxScore = 500;

// overall is 总分, which every batch scores first.
export const overall = "overall";
// sharpness is the 打分项 judged on the photo at its full size.
export const sharpness = "sharpness";

export type StageAction =
  | { kind: "set"; score: number }
  | { kind: "nudge"; by: number }
  | { kind: "item"; by: -1 | 1 }
  | { kind: "confirm" }
  | { kind: "skip" }
  | { kind: "flag" }
  | { kind: "undo" }
  | { kind: "zoom" }
  | { kind: "guide" }
  | { kind: "cancel" };

// stageAction reads a key of the rating stage: 0–5 move to that whole
// score, ← and → by 0.1 and with Shift by 0.01, ↑ and ↓ to the 打分项 before
// and after, Enter confirms, S skips (cannot judge), X marks a problem,
// Backspace goes back to the last photo, Z shows the photo at 100% while
// held, ? shows the guide and Escape leaves what is open. A key with Ctrl,
// Alt or the command key is the browser's.
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

// formatScore shows hundredths as points with two decimals, as 3.47.
export const formatScore = (hundredths: number) => (hundredths / 100).toFixed(2);

// parseScore reads what the rater typed: 0 to 5 with at most two decimals,
// in hundredths, or undefined.
export function parseScore(text: string): number | undefined {
  const trimmed = text.trim();
  if (!/^\d(\.\d{1,2})?$/.test(trimmed)) return undefined;
  const hundredths = Math.round(Number(trimmed) * 100);
  return hundredths <= maxScore ? hundredths : undefined;
}

// Stopwatch counts how long a photo is looked at: running from when it
// appears, standing still while the page is hidden or a sheet covers it.
// Times are performance.now() milliseconds.
export class Stopwatch {
  private spent = 0;
  private since: number | undefined;

  start(now: number, running: boolean) {
    this.spent = 0;
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
          this.publish({ waiting: this.queue.length, retrying: false, rated });
        } catch (caught) {
          if (!this.retryable(caught)) {
            this.queue.shift();
            this.publish({ waiting: this.queue.length, retrying: false });
            this.onRefused(answer, caught);
            continue;
          }
          this.pause = Math.min(this.pause ? this.pause * 2 : firstRetry, longestRetry);
          this.publish({ retrying: true });
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
