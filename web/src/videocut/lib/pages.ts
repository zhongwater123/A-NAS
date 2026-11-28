// Which page a project opens on, and how the film page shows it, by the
// project's state on the server (docs/specs/venus.md#作品状态机). This is the
// one place the mapping is written; pages never move themselves.
import type { VideoCutProjectState } from "../../api.generated";

export type Page = "prepare" | "cut";
export type CutMode = "generating" | "failed" | "ready" | "exporting";

// Each state's view on the film page; null opens the prepare page instead.
const modes: Record<VideoCutProjectState, CutMode | null> = {
  importing: null,
  pending: null,
  upload_interrupted: null,
  uploading: "generating",
  queued: "generating",
  generating: "generating",
  // Uploading, analysis and generating all happen on the film page's
  // 生成中 view, so a failure of any of them shows there, with its retry.
  failed: "failed",
  ready: "ready",
  exporting: "exporting",
  exported: "ready",
};

export const pageOf = (state: VideoCutProjectState): Page => (modes[state] ? "cut" : "prepare");

// cutModeOf is the film page's view of a project the film page shows.
export const cutModeOf = (state: VideoCutProjectState): CutMode => modes[state] ?? "generating";

// isMaking is whether the film is still being made, or failed to be: the
// tracks come from the song and there is no timeline yet.
export const isMaking = (mode: CutMode) => mode === "generating" || mode === "failed";

// hasFilm is whether the project has a made film, exported or not.
export const hasFilm = (state: VideoCutProjectState) => modes[state] === "ready" || modes[state] === "exporting";

// The film page's status label (顶栏的状态标签); none while the film can be edited.
export function statusLabel(mode: CutMode): string | undefined {
  return { generating: "生成中", failed: "生成失败", exporting: "导出中", ready: undefined }[mode];
}
