// Which page a project opens on, and how the film page shows it, by the
// project's state on the server (docs/specs/venus.md#作品状态机). This is the
// one place the mapping is written; pages never move themselves.
import type { VideoCutProjectState } from "../../api.generated";

export type Page = "prepare" | "cut";
export type CutMode = "generating" | "failed" | "ready" | "exporting";

const pages: Record<VideoCutProjectState, Page> = {
  importing: "prepare",
  pending: "prepare",
  upload_interrupted: "prepare",
  uploading: "cut",
  queued: "cut",
  generating: "cut",
  // Uploading, analysis and generating all happen on the film page's
  // 生成中 view, so a failure of any of them shows there, with its retry.
  failed: "cut",
  ready: "cut",
  exporting: "cut",
  exported: "cut",
};

const modes: Partial<Record<VideoCutProjectState, CutMode>> = {
  uploading: "generating",
  queued: "generating",
  generating: "generating",
  failed: "failed",
  ready: "ready",
  exported: "ready",
  exporting: "exporting",
};

export const pageOf = (state: VideoCutProjectState): Page => pages[state];

// cutModeOf is the film page's view of a project the film page shows.
export const cutModeOf = (state: VideoCutProjectState): CutMode => modes[state] ?? "generating";

// The film page's status label (顶栏的状态标签); none while the film can be edited.
export function statusLabel(mode: CutMode): string | undefined {
  return { generating: "生成中", failed: "生成失败", exporting: "导出中", ready: undefined }[mode];
}
