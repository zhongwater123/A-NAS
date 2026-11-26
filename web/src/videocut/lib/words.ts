// What the montage window says about jobs (docs/specs/montage-center.md).
import type { MontageJob } from "../api/client";

export const stepNames: Record<MontageJob["steps"][number]["name"], string> = {
  analysis: "分析素材",
  catalog: "整理画面",
  song: "读取配乐",
  pick: "规划方案一的大纲",
  shots: "挑方案一的镜头",
  fill: "填入方案一的镜头",
  render: "渲染方案一",
  save: "保存方案一",
  "pick-2": "规划方案二的大纲",
  "shots-2": "挑方案二的镜头",
  "fill-2": "填入方案二的镜头",
  "render-2": "渲染方案二",
  "save-2": "保存方案二",
  // An edit job's first step (#596). No page starts an edit yet: its interface is being redone (#632).
  source: "读取修改稿",
};

type StepName = keyof typeof stepNames;
const shared: ReadonlySet<string> = new Set(["analysis", "catalog", "song", "source"]);
// The steps as the page groups them: what every 方案 shares (0), then each 方案's own.
export const stageOf = (name: StepName): number => (shared.has(name) ? 0 : name.endsWith("-2") ? 2 : 1);
export const stageName = (stage: number) => (stage === 0 ? "准备" : planName(stage));
// A step as its group lists it, the 方案 named by the group.
const ownSteps: Record<string, string> = {
  pick: "规划大纲",
  shots: "挑镜头",
  fill: "填入镜头",
  render: "渲染",
  save: "存进相册",
};
export const stepLabel = (name: StepName) => ownSteps[name.replace(/-\d+$/, "")] ?? stepNames[name];

// The 方案 a job makes, by number.
export const planNames: Record<number, string> = { 1: "方案一", 2: "方案二" };
export const planName = (plan: number) => planNames[plan] ?? `方案 ${plan}`;

export type JobAction = "drop" | "retryAnalysis" | "retry" | "replan";
export const actionLabels: Record<JobAction, string> = {
  drop: "去掉这些继续",
  retryAnalysis: "重试分析",
  retry: "重试",
  replan: "重新规划",
};

type Reason = NonNullable<MontageJob["waitReason"]>;

// Why a job waits or stopped, and the member's ways out of it besides
// cancelling: every reason the contract names has its line here. videos
// counts the videos concerned (missingAssetIds), when the job names them.
// A job that stopped for footage or for its song goes on with neither: it
// keeps what it started with, so the member cancels and starts again.
const reasons: Record<Reason, { text: (videos: number) => string; actions?: JobAction[] }> = {
  analysis: { text: () => "相册正在分析视频" },
  asset_trashed: { text: () => "有视频在回收站，恢复后继续" },
  vnas: { text: () => "等待 VNAS 规划" },
  photos_unavailable: { text: () => "相册暂时不可用，会自动重试" },
  asset_missing: { text: (n) => `${n || "有"} 个视频已经不在相册里`, actions: ["drop"] },
  analysis_failed: { text: (n) => `${n || "有"} 个视频分析失败`, actions: ["retryAnalysis", "drop"] },
  video_too_long: { text: (n) => `${n || "有"} 个视频超过 6 小时，不能用于剪辑`, actions: ["drop"] },
  model_unavailable: { text: () => "这台设备还没有安装描述画面用的模型，装好后点重试", actions: ["retry"] },
  no_usable_footage: { text: () => "视频里没有可用的画面。取消后添加视频，再开始剪辑" },
  music_invalid: { text: () => "这首歌不能用。取消后换一首，再开始剪辑" },
  // Jobs stopped before 2026-11-25 for these; VNAS plans again on its own since, and a job whose planning still
  // does not finish fails (plan_failed).
  vnas_unknown: { text: () => "VNAS 没有交回规划，重新规划？", actions: ["replan"] },
  plan_invalid: { text: () => "规划连续 3 次不合格", actions: ["replan"] },
  plan_error: { text: () => "规划出错", actions: ["replan"] },
  storage_full: { text: () => "空间不足，腾出空间后重试", actions: ["retry"] },
  plan_failed: { text: () => "规划没有完成", actions: ["replan"] },
};

// actionsOf are the ways out of a job that needs the member, besides cancelling, and of a failed job whose
// planning the member can start again.
export const actionsOf = (job: MontageJob): JobAction[] =>
  ((job.state === "needs_input" || (job.state === "failed" && job.waitReason === "plan_failed")) &&
    job.waitReason &&
    reasons[job.waitReason].actions) ||
  [];

export const active = (job?: MontageJob) =>
  Boolean(job && (job.state === "queued" || job.state === "running" || job.state === "waiting"));

// stateText says where a job stands in a line.
export function stateText(job: MontageJob): string {
  const reason = job.waitReason ? reasons[job.waitReason] : undefined;
  const videos = job.missingAssetIds?.length ?? 0;
  switch (job.state) {
    case "queued":
      return "排队中";
    case "running":
      return `正在${(job.step && stepNames[job.step as keyof typeof stepNames]) || "处理"}`;
    case "waiting":
      // The detail says what it waits for and when it tries again (VNAS 正在规划、VNAS 暂时连不上，2 分钟后再试…).
      return job.waitReason === "vnas" && job.error
        ? `等待 VNAS 规划：${job.error}`
        : (reason?.text(videos) ?? "等待中");
    case "needs_input":
      // The round handed the first 方案 back and ended: only the second is asked about.
      if (job.waitReason === "vnas_unknown" && job.step?.endsWith("-2")) return "VNAS 没有交回方案二的规划，重新规划？";
      // What failed, as the planner reported it.
      if (job.waitReason === "plan_error" && job.error) return `规划出错：${job.error}`;
      return reason?.text(videos) ?? "需要你处理";
    case "done":
      return "成片已存进相册";
    case "failed":
      return job.waitReason === "plan_failed" ? "规划没有完成，没有剪成" : "没有剪成";
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

// failureText is what the page says of a job that failed, beyond that it
// did. The montage center writes some failures for the member, in Chinese,
// as the one that asks to check the photo library for a film it could not
// confirm; the rest are its own diagnostics, which stay in its log.
export function failureText(job: MontageJob): string | undefined {
  return forMember(job.error);
}

// forMember is a failure as the montage center wrote it for the member, or nothing for its own diagnostics.
export function forMember(error?: string): string | undefined {
  return error && /[\u4e00-\u9fff]/.test(error) ? error : undefined;
}
