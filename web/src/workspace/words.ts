import type { AgentKind, WorkspaceVersion } from "./api";

export const kindLabel: Record<AgentKind, string> = {
  "claude-code": "Claude Code",
  codex: "Codex",
  other: "其他 agent",
};

// savedText says who saved a version and how: “A 上传”, or “A 经 办公电脑”
// for a member's agent, named as its token was.
export function savedText(version: WorkspaceVersion): string {
  if (version.via.kind === "web") return `${version.savedBy.name} 上传`;
  return `${version.savedBy.name} 经 ${version.via.name || kindLabel[version.via.kind]}`;
}

// arrivalText announces another member's new version: “A 经 办公电脑 保存了
// v2：把按钮改小”, or “B 上传了 v3”.
export function arrivalText(version: WorkspaceVersion): string {
  const what =
    version.via.kind === "web"
      ? `${version.savedBy.name} 上传了 v${version.version}`
      : `${savedText(version)} 保存了 v${version.version}`;
  return version.note ? `${what}：${version.note}` : what;
}

// isHTML tells whether a file is one a page can be made of, by its name: a
// file dragged from the system often has no type.
export const isHTML = (file: File) => /\.html?$/i.test(file.name) || file.type === "text/html";
