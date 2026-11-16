import { formatBytes } from "../lib/bytes";
import type { SystemUpdateProblem } from "./api";

// Words for the system update shared by its page, the overview and the
// activity log.

type ProblemCode = SystemUpdateProblem["code"];
const problemCodes: string[] = [
  "unreachable",
  "signature",
  "disk_space",
  "download",
  "install",
  "check",
  "rollback",
  "staging",
  "not_installed",
] satisfies ProblemCode[];
export const isProblemCode = (code: string): code is ProblemCode => problemCodes.includes(code);

export function problemText(problem: SystemUpdateProblem): string {
  switch (problem.code) {
    case "unreachable":
      return "连不上软件源";
    case "signature":
      return "软件源的签名不对，没有使用它";
    case "disk_space": {
      const missing = Number(problem.detail);
      return Number.isFinite(missing) && missing > 0
        ? `系统盘空间不够，还差 ${formatBytes(missing)}`
        : "系统盘空间不够";
    }
    case "download":
      return "下载没有完成";
    case "install":
      return "安装没有完成";
    case "check":
      return "安装后的检查没有通过";
    case "rollback":
      return "回到原版本时出错";
    case "staging":
      return "读取推送来的软件源时出错";
    case "not_installed":
      return "这台设备不是用 A-NAS 软件包安装的";
  }
}

export interface VersionParts {
  // release is the release, or the one a development build leads to.
  release: string;
  dev: boolean;
  // date is the day of a development build's commit, as 2026-11-13.
  date?: string;
  commit?: string;
}

// versionParts reads a Debian version of A-NAS (scripts/release-version.sh):
// 1.0.2 and 1.0.1~rc.7 are releases; 1.0.2~dev20261120.1+gabcdefabcdef and
// 1.0.1~rc.6+dev20261111.3+g083bb9db1234 are development builds.
export function versionParts(version: string): VersionParts {
  const dev = /[~+]dev(?:(\d{4})(\d{2})(\d{2})\.\d+)?(?:\+g([0-9a-f]{7,40}))?$/.exec(version);
  if (!dev) return { release: version.replace("~", "-"), dev: false };
  return {
    release: version.slice(0, dev.index).replace("~", "-"),
    dev: true,
    date: dev[1] ? `${dev[1]}-${dev[2]}-${dev[3]}` : undefined,
    commit: dev[4]?.slice(0, 8),
  };
}

// versionLabel is a version in a sentence: 1.0.2, or 1.0.2 开发版 4e842653.
export function versionLabel(version: string): string {
  const parts = versionParts(version);
  if (!parts.dev) return parts.release;
  return [parts.release, "开发版", parts.commit].filter(Boolean).join(" ");
}

// byText says who started an operation the service recorded.
export const byText = (by: string) => (by === "automatic" ? "自动" : by ? `由 ${by} 发起` : "");

export const waitingText: Record<string, string> = {
  storage: "数据卷正在转换或执行硬盘计划",
  apps: "有应用正在安装或卸载",
};
