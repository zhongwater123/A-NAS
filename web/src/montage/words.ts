// What the montage window says about jobs (docs/specs/montage-center.md).
import type { MontageJob } from "./api";

export const stepNames: Record<string, string> = {
  analysis: "分析素材",
  catalog: "整理画面",
  song: "读取配乐",
  pick: "规划方案一的大纲",
  shots: "挑方案一的镜头",
  fill: "填入方案一的镜头",
  render: "渲染方案一",
  save: "方案一存进相册",
  "pick-2": "规划方案二的大纲",
  "shots-2": "挑方案二的镜头",
  "fill-2": "填入方案二的镜头",
  "render-2": "渲染方案二",
  "save-2": "方案二存进相册",
};

// The 方案 a job makes, by number.
export const planNames: Record<number, string> = { 1: "方案一", 2: "方案二" };

const waits: Record<string, string> = {
  analysis: "相册正在分析素材，或本地模型暂时不可用",
  asset_trashed: "有视频在回收站，恢复后继续",
  vnas: "等待 VNAS 规划",
  photos_unavailable: "等相册确认存回的成片，稍后自动重试",
};

// The member's way out of a job that stopped, by reason.
export const needs: Record<string, { text: string; actions: ("drop" | "retryAnalysis" | "retry" | "replan")[] }> = {
  asset_missing: { text: "有视频已经不在相册里了", actions: ["drop"] },
  analysis_failed: { text: "有视频分析失败", actions: ["retryAnalysis", "drop"] },
  video_too_long: { text: "有视频太长，相册分析不了", actions: ["drop"] },
  model_unavailable: { text: "相册缺少分析视频的本地模型：装好后重试", actions: ["retry"] },
  no_usable_footage: { text: "素材里没有可用的画面：追加素材后再生成", actions: [] },
  music_invalid: { text: "这首歌用不了：换一首后再生成", actions: [] },
  vnas_unknown: { text: "VNAS 没有交回规划，重新规划？", actions: ["replan"] },
  plan_invalid: { text: "规划连续 3 次不合格", actions: ["replan"] },
  storage_full: { text: "空间不足：腾出空间后重试", actions: ["retry"] },
};

export const active = (job?: MontageJob) =>
  Boolean(job && (job.state === "queued" || job.state === "running" || job.state === "waiting"));

// stateText says where a job stands in a line.
export function stateText(job: MontageJob): string {
  switch (job.state) {
    case "queued":
      return "排队中";
    case "running":
      return `正在${stepNames[job.step ?? ""] ?? "处理"}`;
    case "waiting": {
      const base = waits[job.waitReason ?? ""] ?? "等待中";
      // The detail says why VNAS has not taken it (VNAS 没有开启、还没有接手…).
      return job.waitReason === "vnas" && job.error ? `${base}：${job.error}` : base;
    }
    case "needs_input":
      return needs[job.waitReason ?? ""]?.text ?? "需要你处理";
    case "done":
      return "成片已存进相册";
    case "failed":
      return "没有剪成";
    case "cancelled":
      return "已取消";
  }
}

// The first version cannot do these (docs/architecture/montage-center.md, 不支持的要求): the page says so
// under the request, without stopping the member.
const unsupported: [RegExp, string][] = [
  [/竖屏|9[:：]16|方形/, "首版固定横屏 1920×1080"],
  [/\d+\s*(秒|分钟)|几秒|几分钟/, "成片时长跟着音乐走（约 90 秒）"],
  [/两首歌|换歌/, "首版只配一首歌"],
  [/字幕/, "首版不加字幕"],
  [/原声|配音/, "首版不保留原声"],
];

export function limitsOf(ask: string): string[] {
  return unsupported.filter(([pattern]) => pattern.test(ask)).map(([, said]) => said);
}
